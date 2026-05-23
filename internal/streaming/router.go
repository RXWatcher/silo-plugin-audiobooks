// Package streaming redirects audio byte requests to the configured backend
// plugin's stream route. The portal mints a short-TTL signed JWT bound to
// (user, book, file_idx, exp) and embeds it as ?token= on the redirect
// target; the backend validates it without needing the host plugin proxy
// to authenticate the byte route at all (the backend declares the route
// public on its manifest).
//
// The cache and direct modes the package used to expose were removed: the
// cache mode silently failed on real audiobook sizes (the host-proxy client
// caps response bodies at 10 MiB), and the direct mode was a permanent stub.
// A single proxy redirect is the only thing the contract needs.
package streaming

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/backend"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/mediatoken"
)

// SecretProvider returns the current media signing secret. The ctx threads
// through from the inbound HTTP request so a slow DB lookup of the secret
// is cancellable by the client disconnecting — without it, a stalled
// Postgres during playback would pile up forever-blocked goroutines
// holding response writers open.
type SecretProvider func(ctx context.Context) string

// Router builds the backend stream URL with a signed media token and writes
// the redirect.
type Router struct {
	backend *backend.Client
	secret  SecretProvider
}

// NewRouter constructs a Router bound to the backend client. secret is
// called on every Stream() to read the latest persisted signing secret.
func NewRouter(b *backend.Client, secret SecretProvider) *Router {
	return &Router{backend: b, secret: secret}
}

// Stream issues a 302 to the backend's stream URL with a signed ?token= so
// the browser, following the redirect into <audio src>, carries auth to the
// second hop — browsers don't send Authorization headers on tag-issued
// requests. The token is bound to (userID, bookID, fileIdx, exp), so a
// leaked URL stops working after the TTL and can't be reused for other
// resources.
func (r *Router) Stream(w http.ResponseWriter, req *http.Request, userID, installID, bookID string, fileIdx int) {
	if r == nil || r.backend == nil {
		http.Error(w, "streaming not configured", http.StatusInternalServerError)
		return
	}
	if installID == "" {
		http.Error(w, "no backend configured", http.StatusPreconditionFailed)
		return
	}
	if r.secret == nil {
		slog.Error("streaming secret provider not wired")
		http.Error(w, "media signing not configured", http.StatusServiceUnavailable)
		return
	}
	secret := r.secret(req.Context())
	if secret == "" {
		slog.Warn("media_signing_secret is empty; refusing to issue a tokenless redirect")
		http.Error(w, "media signing not configured", http.StatusServiceUnavailable)
		return
	}
	token, err := mediatoken.Mint(secret, userID, bookID, fileIdx)
	if err != nil {
		if errors.Is(err, mediatoken.ErrSecretUnconfigured) {
			slog.Warn("media_signing_secret missing on mint", "book_id", bookID, "file_idx", fileIdx)
			http.Error(w, "media signing not configured", http.StatusServiceUnavailable)
			return
		}
		slog.Error("mint stream token failed", "book_id", bookID, "file_idx", fileIdx, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	target := r.backend.StreamURL(installID, bookID, fileIdx)
	sep := "?"
	if strings.Contains(target, "?") {
		sep = "&"
	}
	target = target + sep + "token=" + url.QueryEscape(token)
	http.Redirect(w, req, target, http.StatusFound)
}
