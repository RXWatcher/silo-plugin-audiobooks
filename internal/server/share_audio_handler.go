package server

import (
	"errors"
	"io"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/bookref"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/mediatoken"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
)

// Public audio access for share links. Slug + TTL gates the entire
// access — no per-request auth beyond an active share row.
// /share/{slug}/play returns the track URL list (recipient's
// audio player walks them). /share/{slug}/track/{idx} proxies
// bytes from the backend.

// handleSharePlay returns the share's playable shape: book id +
// track list. The recipient's <audio> element reads track URLs
// from here.
func (s *Server) handleSharePlay(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	link, err := s.d.Store.GetActiveShareLinkBySlug(r.Context(), slug)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "share link not found or expired", http.StatusNotFound)
		return
	}
	if err != nil {
		writeInternal(w, r, err)
		return
	}

	libID, backendBookID, _ := bookref.Decode(link.ItemID)
	if libID == 0 || backendBookID == "" {
		http.Error(w, "invalid share item id", http.StatusBadRequest)
		return
	}
	lib, err := s.d.Store.GetPortalLibrary(r.Context(), libID)
	if err != nil || lib.BackendPluginID == "" {
		http.Error(w, "library unavailable", http.StatusServiceUnavailable)
		return
	}
	bearer := "" // public route — backend client uses its service token
	detail, err := s.d.Backend.GetDetail(r.Context(), bearer, lib.BackendPluginID, backendBookID)
	if err != nil {
		http.Error(w, "book unavailable", http.StatusBadGateway)
		return
	}

	// Construct one track entry per file. Each track URL points
	// at /share/{slug}/track/{idx} on this listener — the slug
	// itself is the capability so no per-track token needed.
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	base := scheme + "://" + r.Host + "/share/" + neturl.PathEscape(slug)
	tracks := make([]map[string]any, 0, len(detail.Files))
	for i, f := range detail.Files {
		tracks = append(tracks, map[string]any{
			"index":            i,
			"duration_seconds": f.DurationSeconds,
			"size_bytes":       f.SizeBytes,
			"mime_type":        f.MimeType,
			"url":              base + "/track/" + strconv.Itoa(i),
		})
	}
	_ = s.d.Store.IncrementShareUse(r.Context(), link.ID)
	authors := detail.Authors
	if len(authors) == 0 {
		for _, a := range detail.AuthorRefs {
			if a.Name != "" {
				authors = append(authors, a.Name)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"slug":             link.Slug,
		"title":            detail.Title,
		"authors":          authors,
		"cover_url":        detail.CoverPath,
		"duration_seconds": detail.DurationSeconds,
		"tracks":           tracks,
	})
}

// handleShareTrack proxies audio bytes for one track of the
// shared book. Uses the same media-token + Range pass-through as
// the authenticated /abs/public/session route, with the share
// slug + active-row check as the capability.
func (s *Server) handleShareTrack(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	link, err := s.d.Store.GetActiveShareLinkBySlug(r.Context(), slug)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "share link not found or expired", http.StatusNotFound)
		return
	}
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	idx, err := strconv.Atoi(chi.URLParam(r, "idx"))
	if err != nil || idx < 0 {
		http.Error(w, "invalid track index", http.StatusBadRequest)
		return
	}
	libID, backendBookID, _ := bookref.Decode(link.ItemID)
	if libID == 0 || backendBookID == "" {
		http.Error(w, "invalid share item id", http.StatusBadRequest)
		return
	}
	lib, err := s.d.Store.GetPortalLibrary(r.Context(), libID)
	if err != nil || lib.BackendPluginID == "" {
		http.Error(w, "library unavailable", http.StatusServiceUnavailable)
		return
	}
	cfg, err := s.d.Store.GetBackendConfig(r.Context())
	if err != nil || cfg.MediaSigningSecret == "" {
		http.Error(w, "media signing not configured", http.StatusServiceUnavailable)
		return
	}
	// Mint a media token bound to the share's owner + book +
	// track. The token's user id is the owner (not the recipient)
	// — the backend doesn't know about share recipients; the
	// access decision is "the share row is active."
	tok, err := mediatoken.Mint(cfg.MediaSigningSecret, link.UserID, backendBookID, idx)
	if err != nil {
		http.Error(w, "mint token failed", http.StatusInternalServerError)
		return
	}
	backendPath := "/api/v1/stream/" + neturl.PathEscape(backendBookID) + "/" +
		strconv.Itoa(idx) + "?token=" + neturl.QueryEscape(tok)
	hdrs := map[string]string{}
	for _, h := range []string{"Range", "If-Match", "If-None-Match", "If-Modified-Since"} {
		if v := r.Header.Get(h); v != "" {
			hdrs[h] = v
		}
	}
	resp, err := s.d.Backend.HostClient().GetStream(r.Context(), "", lib.BackendPluginID, backendPath, hdrs)
	if err != nil {
		http.Error(w, "track unavailable", http.StatusBadGateway)
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
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// _ = strings placeholder for future header normalisation.
var _ = strings.ToLower
