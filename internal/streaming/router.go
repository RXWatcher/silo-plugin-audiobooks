// Package streaming routes audio bytes to the client per the configured
// streaming_mode: proxy | cache | direct.
package streaming

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/backend"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
)

// Mode is the configured streaming mode.
type Mode string

const (
	ModeProxy  Mode = "proxy"
	ModeCache  Mode = "cache"
	ModeDirect Mode = "direct"
)

// Router resolves the mode per request and dispatches.
type Router struct {
	store   *store.Store
	backend *backend.Client
	cache   *Cache
}

// NewRouter builds a router; cache is optional (only used in cache mode).
func NewRouter(s *store.Store, b *backend.Client, cache *Cache) *Router {
	return &Router{store: s, backend: b, cache: cache}
}

// Stream resolves the mode and writes the response.
func (r *Router) Stream(w http.ResponseWriter, req *http.Request, bearer, installID, bookID string, fileIdx int) {
	if r.store == nil || r.backend == nil {
		http.Error(w, "streaming not configured", http.StatusInternalServerError)
		return
	}
	cfg, err := r.store.GetBackendConfig(req.Context())
	if err != nil {
		http.Error(w, "backend config unavailable", http.StatusInternalServerError)
		return
	}
	mode := Mode(cfg.StreamingMode)
	if mode == "" {
		mode = ModeProxy
	}
	switch mode {
	case ModeDirect:
		r.serveDirect(w, req, bearer, installID, bookID, fileIdx, cfg)
	case ModeCache:
		if r.cache == nil {
			http.Error(w, "cache mode not initialised", http.StatusInternalServerError)
			return
		}
		r.serveCache(w, req, bearer, installID, bookID, fileIdx)
	default:
		r.serveProxy(w, req, bearer, installID, bookID, fileIdx)
	}
}

func (r *Router) serveProxy(w http.ResponseWriter, req *http.Request, bearer, installID, bookID string, fileIdx int) {
	// Default proxy mode: just redirect the client to the public plugin URL.
	// The host's plugin proxy will validate the bearer and forward to the
	// backend, which 302s to the upstream stream URL.
	upstreamURL := r.backend.StreamURL(installID, bookID, fileIdx)
	http.Redirect(w, req, upstreamURL, http.StatusFound)
}

func (r *Router) serveDirect(w http.ResponseWriter, req *http.Request, bearer, installID, bookID string, fileIdx int, cfg store.BackendConfig) {
	// Look up the file's filename via the backend, then map to a local path.
	detail, err := r.backend.GetDetail(req.Context(), bearer, installID, bookID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if fileIdx < 0 || fileIdx >= len(detail.Files) {
		http.Error(w, "file index out of range", http.StatusNotFound)
		return
	}
	// The contract Files entries don't include filename — direct mode is
	// only supported when the backend exposes filesystem paths via a custom
	// out-of-band channel. For now this is a stub; future filesystem
	// backends will need a path-aware extension. Return 501 with detail.
	_ = detail
	_ = cfg
	http.Error(w, "direct mode requires filesystem-aware backend (not implemented yet)", http.StatusNotImplemented)
}

func (r *Router) serveCache(w http.ResponseWriter, req *http.Request, bearer, installID, bookID string, fileIdx int) {
	key := fmt.Sprintf("%s:%d", bookID, fileIdx)
	entry, err := r.cache.Get(req.Context(), key, func(ctx context.Context) (io.ReadCloser, string, int64, error) {
		// Miss: fetch from backend via the host plugin proxy.
		resp, err := r.backend.HostClient().Get(ctx, bearer, installID,
			fmt.Sprintf("/api/v1/stream/%s/%d", bookID, fileIdx))
		if err != nil {
			return nil, "", 0, err
		}
		// The backend's /stream route returns 302 to upstream; HostClient
		// follows redirects automatically and returns body bytes.
		return io.NopCloser(strings.NewReader(string(resp))), "audio/mpeg", int64(len(resp)), nil
	})
	if err != nil {
		// The host plugin-proxy client is buffered and capped at 10 MiB, so
		// caching real audiobook files (tens of MB to GBs) always fails
		// here. Rather than 502 the playback, degrade to proxy mode (a 302
		// to the backend stream) which streams correctly without buffering.
		r.serveProxy(w, req, bearer, installID, bookID, fileIdx)
		return
	}
	defer entry.Close()
	w.Header().Set("Content-Type", entry.MimeType)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", entry.Size))
	w.Header().Set("Accept-Ranges", "bytes")
	http.ServeContent(w, req, filepath.Base(entry.Path), entry.ModTime, entry.File)
	_ = os.Stat // keep import
}
