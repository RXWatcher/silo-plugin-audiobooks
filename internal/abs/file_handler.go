package abs

import (
	"io"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/mediatoken"
)

// handleItemFile serves the audio bytes for one file of one
// library item. Real ABS uses /api/items/{id}/file/{ino}/download as
// the offline-save URL and /api/items/{id}/file/{ino}?token=<jwt> as
// the iOS streaming URL — both routes hit this same handler.
//
// Auth is via the bearerAuth middleware, which already accepts both
// the Authorization header and a ?token= query fallback (the iOS
// streaming variant uses ?token= because AVPlayer doesn't send
// Authorization on its own requests).
//
// "ino" in real ABS is the file's filesystem inode (a large decimal
// integer). We synthesise an MD5-derived inode-shaped string in
// translate.go's trackInoFor — same value emitted by item-detail and
// /play — and accept it here. To turn the ino back into a backend
// file index we recompute trackInoFor for each file in the book's
// detail until one matches. As a fallback we also accept a bare 0-
// based integer in case any caller still threads the file index
// through directly (older snapshots of our own code did this).
//
// Behaviour:
//   - Mint a signed media token bound to (userID, bookID, fileIdx).
//   - Open a streaming GET to the backend's /api/v1/stream URL with
//     the token + the inbound Range header forwarded.
//   - Copy bytes back with Content-Type / Content-Length /
//     Content-Range / Accept-Ranges / Cache-Control passthrough.
//   - Set Content-Disposition: attachment on /download to encourage
//     the browser/save-to-disk behaviour mobile native code expects.
func (h *Handler) handleItemFile(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	encoded := chi.URLParam(r, "id")
	inoStr := chi.URLParam(r, "ino")
	lib, backendBookID, _, err := h.portalLibraryForBookRef(r.Context(), encoded)
	if err != nil || lib.BackendPluginID == "" {
		http.Error(w, "item not found", http.StatusNotFound)
		return
	}
	// Resolve the requested ino back to a backend file index by
	// recomputing trackInoFor for each known file. We need the detail
	// anyway to validate the file exists; doing the lookup here keeps
	// the contract symmetric with translate.buildAudioTracks (one
	// place mints inos, one place resolves them).
	detail, err := h.backend.GetDetail(r.Context(), "", lib.BackendPluginID, backendBookID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	fileIdx := -1
	for _, f := range detail.Files {
		if trackInoFor(backendBookID, f.Index) == inoStr {
			fileIdx = f.Index
			break
		}
	}
	if fileIdx < 0 {
		// Fallback: legacy callers sometimes pass the 0-based file
		// index directly. Accept it if it resolves to a real file.
		if n, err := strconv.Atoi(inoStr); err == nil && n >= 0 {
			for _, f := range detail.Files {
				if f.Index == n {
					fileIdx = n
					break
				}
			}
		}
	}
	if fileIdx < 0 {
		h.logger.Warn("abs file proxy: unknown ino",
			"book_id", backendBookID, "ino", inoStr, "file_count", len(detail.Files))
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	_, cfg, err := h.targetFn(r.Context())
	if err != nil {
		http.Error(w, "config unavailable", http.StatusInternalServerError)
		return
	}
	if cfg.MediaSigningSecret == "" {
		http.Error(w, "media signing not configured", http.StatusServiceUnavailable)
		return
	}
	mediaTok, err := mediatoken.Mint(cfg.MediaSigningSecret, a.UserID, backendBookID, fileIdx)
	if err != nil {
		http.Error(w, "mint media token", http.StatusInternalServerError)
		return
	}
	backendPath := "/api/v1/stream/" + neturl.PathEscape(backendBookID) + "/" + strconv.Itoa(fileIdx) +
		"?token=" + neturl.QueryEscape(mediaTok)

	hdrs := map[string]string{}
	for _, h := range []string{"Range", "If-Match", "If-None-Match", "If-Modified-Since"} {
		if v := r.Header.Get(h); v != "" {
			hdrs[h] = v
		}
	}
	resp, err := h.backend.HostClient().GetStream(r.Context(), "", lib.BackendPluginID, backendPath, hdrs)
	if err != nil {
		h.logger.Warn("abs file proxy: upstream error",
			"book_id", backendBookID, "ino", fileIdx, "err", err.Error())
		http.Error(w, "file unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for _, h := range []string{
		"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges",
		"ETag", "Last-Modified", "Cache-Control",
	} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	// Default Content-Type by file extension if the backend didn't
	// supply one. ABS clients pattern-match on mp3/m4b/mp4/epub/pdf/jpg
	// to decide how to render the bytes — wrong Content-Type sends
	// them down the wrong code path silently.
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", contentTypeForExt(r.URL.Path))
	}
	// /download variant: hint the client to save rather than stream.
	if strings.HasSuffix(r.URL.Path, "/download") {
		filename := strings.TrimSuffix(chi.URLParam(r, "ino"), "/") + extForContentType(w.Header().Get("Content-Type"))
		w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	}

	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		// Client disconnect mid-stream is the common case; debug only.
		h.logger.Debug("abs file proxy: copy ended",
			"book_id", backendBookID, "ino", fileIdx, "err", err.Error())
	}
}

// contentTypeForExt picks a default MIME for the file path suffix when
// the backend doesn't supply one. Covers the audio types the official
// ABS mobile + web clients route through their audio player + the
// ebook types the in-app reader handles.
func contentTypeForExt(path string) string {
	switch ext := strings.ToLower(extOf(path)); ext {
	case ".mp3":
		return "audio/mpeg"
	case ".m4b", ".m4a", ".mp4":
		return "audio/mp4"
	case ".flac":
		return "audio/flac"
	case ".ogg", ".opus":
		return "audio/ogg"
	case ".wav":
		return "audio/wav"
	case ".aac":
		return "audio/aac"
	case ".epub":
		return "application/epub+zip"
	case ".pdf":
		return "application/pdf"
	case ".cbz":
		return "application/vnd.comicbook+zip"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	}
	return "application/octet-stream"
}

// extOf returns the path's final extension (including the dot), or
// the empty string if there isn't one.
func extOf(path string) string {
	if i := strings.LastIndexByte(path, '.'); i >= 0 {
		// Trim a trailing /download suffix off the ext if present.
		ext := path[i:]
		if slash := strings.IndexByte(ext, '/'); slash >= 0 {
			return ext[:slash]
		}
		return ext
	}
	return ""
}

// extForContentType is the inverse of contentTypeForExt — picks a
// file extension for the Content-Disposition filename when the
// caller didn't supply one. Covers the audio + ebook types we
// actually serve.
func extForContentType(ct string) string {
	// Trim ;charset= and similar param suffixes.
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	switch strings.ToLower(ct) {
	case "audio/mpeg":
		return ".mp3"
	case "audio/mp4":
		return ".m4b"
	case "audio/flac":
		return ".flac"
	case "audio/ogg":
		return ".ogg"
	case "audio/wav":
		return ".wav"
	case "audio/aac":
		return ".aac"
	case "application/epub+zip":
		return ".epub"
	case "application/pdf":
		return ".pdf"
	}
	return ""
}
