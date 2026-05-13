package abs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/backend"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/cdn"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
)

// Handler wires the /abs/api/* and /abs/public/* surface.
type Handler struct {
	store      *store.Store
	backend    *backend.Client
	logger     Logger
	targetFn   func(ctx context.Context) (string, store.BackendConfig, error)
	hostBaseFn func() string
	installID  func() string // current plugin install ID for building public URLs
	cdnFn      func() (hostname, signingSecret string)
}

// Logger is a minimal interface to keep Handler decoupled from hclog.
type Logger interface {
	Warn(msg string, args ...any)
	Debug(msg string, args ...any)
}

// noopLogger is the default.
type noopLogger struct{}

func (noopLogger) Warn(string, ...any)  {}
func (noopLogger) Debug(string, ...any) {}

// Deps wires the handler's collaborators.
type Deps struct {
	Store      *store.Store
	Backend    *backend.Client
	Logger     Logger
	TargetFn   func(ctx context.Context) (string, store.BackendConfig, error)
	HostBaseFn func() string
	InstallID  func() string
	// CDNFn returns the CDN hostname and base64-encoded signing secret from
	// the plugin's global runtime config. When both are non-empty, handlePlay
	// and handleSessionSync emit presigned CDN URLs instead of portal-relative
	// session URLs. If CDNFn is nil the portal falls back to the non-CDN path.
	CDNFn func() (hostname, signingSecret string)
}

// NewHandler builds a handler.
func NewHandler(d Deps) *Handler {
	if d.Logger == nil {
		d.Logger = noopLogger{}
	}
	if d.HostBaseFn == nil {
		d.HostBaseFn = func() string { return "" }
	}
	if d.InstallID == nil {
		d.InstallID = func() string { return "continuum.audiobooks" }
	}
	if d.CDNFn == nil {
		d.CDNFn = func() (string, string) { return "", "" }
	}
	return &Handler{
		store: d.Store, backend: d.Backend, logger: d.Logger,
		targetFn: d.TargetFn, hostBaseFn: d.HostBaseFn, installID: d.InstallID,
		cdnFn: d.CDNFn,
	}
}

// Mount registers /abs/api/* + /abs/public/* on the parent router.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/abs/api/ping", h.handlePing)
	r.Get("/abs/api/healthcheck", h.handlePing)
	r.Get("/abs/api/init", h.handleInit)
	r.Get("/abs/api/status", h.handleStatus)
	r.Post("/abs/api/login", h.handleLogin)
	r.Post("/abs/api/auth/refresh", h.handleRefresh)
	r.Post("/abs/api/auth/logout", h.handleLogout)

	r.Group(func(r chi.Router) {
		r.Use(h.bearerAuth)
		r.Get("/abs/api/me", h.handleMe)
		r.Get("/abs/api/libraries", h.handleLibraries)
		r.Get("/abs/api/libraries/{id}", h.handleLibraryDetail)
		r.Get("/abs/api/libraries/{id}/items", h.handleLibraryItems)
		r.Get("/abs/api/libraries/{id}/authors", h.handleLibraryAuthors)
		r.Get("/abs/api/libraries/{id}/series", h.handleLibrarySeries)
		r.Get("/abs/api/libraries/{id}/personalized", h.handlePersonalized)
		r.Get("/abs/api/items/{id}", h.handleItem)
		r.Get("/abs/api/items/{id}/cover", h.handleItemCover)
		r.Post("/abs/api/items/{id}/play", h.handlePlay)
		r.Get("/abs/api/me/progress/{itemId}", h.handleGetProgress)
		r.Patch("/abs/api/me/progress/{itemId}", h.handlePatchProgress)
		r.Patch("/abs/api/session/{sid}", h.handleSessionSync)
		r.Post("/abs/api/session/{sid}/close", h.handleSessionClose)
	})

	// Public routes — session token in query is the capability.
	r.Get("/abs/public/session/{sid}/track/{idx}", h.handlePublicTrack)
}

// ctxKey is the ABS auth context key.
type ctxKey struct{}

type ctxAuth struct {
	UserID string
	JTI    string
	Token  string
}

func (h *Handler) bearerAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if raw == "" {
			raw = r.URL.Query().Get("token")
		}
		if raw == "" {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		_, cfg, err := h.targetFn(r.Context())
		if err != nil {
			http.Error(w, "config unavailable", http.StatusInternalServerError)
			return
		}
		claims, err := ParseToken(cfg.ABSJWTSecret, raw)
		if err != nil || claims.Type != "access" {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		// Look up jti; reject if revoked.
		row, err := h.store.GetABSTokenByJTI(r.Context(), claims.JTI)
		if err != nil || row.RevokedAt != nil {
			http.Error(w, "token revoked", http.StatusUnauthorized)
			return
		}
		_ = h.store.TouchABSToken(r.Context(), claims.JTI)
		ctx := context.WithValue(r.Context(), ctxKey{}, ctxAuth{
			UserID: claims.UserID,
			JTI:    claims.JTI,
			Token:  raw,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func absAuthFrom(r *http.Request) (ctxAuth, bool) {
	a, ok := r.Context().Value(ctxKey{}).(ctxAuth)
	return a, ok
}

// ---------- Handlers ----------

func (h *Handler) handlePing(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"server":  ServerSourceTag,
		"version": ServerVersion,
		"pong":    true,
	})
}

func (h *Handler) handleInit(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"isInit": true})
}

func (h *Handler) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"isInit":      true,
		"language":    "en-us",
		"app":         ServerSourceTag,
		"serverVersion": ServerVersion,
	})
}

// handleLogin mints ABS access + refresh JWTs for the caller. Identity is
// taken from the continuum-proxy-injected X-Continuum-User-Id header: the
// host validates the session cookie / API token before forwarding the
// request, so reaching this handler with that header set is proof of a
// valid continuum session.
//
// SECURITY: we deliberately do NOT fall back to a body-supplied username.
// On the standalone HTTP listener the host proxy never runs and the
// X-Continuum-User-* headers are explicitly stripped at the listener
// boundary (httproutes/server.go ServeHTTP). Accepting an arbitrary body
// username there would let any unauthenticated client mint a JWT for any
// user — a complete auth bypass. Operators issue tokens to mobile clients
// by logging into continuum via the host UI (which sets the headers when
// proxying), then handing the resulting token off to the client. POSTing
// /abs/api/login directly to the standalone port always 401s.
//
// Drift from the spec: the original ABS design has /login validate a
// username+password pair against a local user table. The continuum
// architecture puts auth at the host instead, so we delegate. If the SDK
// ever exposes a credential-validation RPC, we can lift that here without
// changing the standalone-port contract.
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-Continuum-User-Id")
	if userID == "" {
		http.Error(w, "login must be initiated via the continuum host; standalone /login is not accepted", http.StatusUnauthorized)
		return
	}
	_, cfg, err := h.targetFn(r.Context())
	if err != nil {
		http.Error(w, "config unavailable", http.StatusInternalServerError)
		return
	}
	accessTTL := time.Duration(cfg.ABSAccessTTLHours) * time.Hour
	if accessTTL == 0 {
		accessTTL = 24 * time.Hour
	}
	refreshTTL := time.Duration(cfg.ABSRefreshTTLDays) * 24 * time.Hour
	if refreshTTL == 0 {
		refreshTTL = 30 * 24 * time.Hour
	}
	accessJTI := ulid.Make().String()
	refreshJTI := ulid.Make().String()
	access, err := IssueAccessToken(cfg.ABSJWTSecret, userID, accessJTI, accessTTL)
	if err != nil {
		http.Error(w, "mint access: "+err.Error(), http.StatusInternalServerError)
		return
	}
	refresh, err := IssueRefreshToken(cfg.ABSJWTSecret, userID, refreshJTI, refreshTTL)
	if err != nil {
		http.Error(w, "mint refresh: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.store.InsertABSToken(r.Context(), store.ABSToken{
		ID:        accessJTI,
		UserID:    userID,
		JTI:       accessJTI,
		ExpiresAt: time.Now().Add(accessTTL),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Refresh-token insert must also succeed: a client that gets back a
	// refresh token whose JTI isn't in the DB will fail on its first use
	// (the validator looks up by JTI), forcing an interactive re-login. We
	// surface the failure here rather than silently mint a token the client
	// can't use.
	if err := h.store.InsertABSToken(r.Context(), store.ABSToken{
		ID:        refreshJTI,
		UserID:    userID,
		JTI:       refreshJTI,
		ExpiresAt: time.Now().Add(refreshTTL),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user": map[string]any{
			"id":               userID,
			"username":         userID,
			"defaultLibraryId": VirtualLibraryID,
		},
		"accessToken":  access,
		"refreshToken": refresh,
		"libraries":    []map[string]any{{"id": VirtualLibraryID, "name": VirtualLibraryName}},
	})
}

type refreshPayload struct {
	RefreshToken string `json:"refreshToken"`
}

func (h *Handler) handleRefresh(w http.ResponseWriter, r *http.Request) {
	var p refreshPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil || p.RefreshToken == "" {
		http.Error(w, "refreshToken required", http.StatusBadRequest)
		return
	}
	_, cfg, err := h.targetFn(r.Context())
	if err != nil {
		http.Error(w, "config unavailable", http.StatusInternalServerError)
		return
	}
	claims, err := ParseToken(cfg.ABSJWTSecret, p.RefreshToken)
	if err != nil || claims.Type != "refresh" {
		http.Error(w, "invalid refresh token", http.StatusUnauthorized)
		return
	}
	row, err := h.store.GetABSTokenByJTI(r.Context(), claims.JTI)
	if err != nil || row.RevokedAt != nil {
		http.Error(w, "refresh token revoked", http.StatusUnauthorized)
		return
	}
	// Rotate: mint new pair, revoke old refresh.
	accessTTL := time.Duration(cfg.ABSAccessTTLHours) * time.Hour
	refreshTTL := time.Duration(cfg.ABSRefreshTTLDays) * 24 * time.Hour
	newAccessJTI := ulid.Make().String()
	newRefreshJTI := ulid.Make().String()
	access, _ := IssueAccessToken(cfg.ABSJWTSecret, claims.UserID, newAccessJTI, accessTTL)
	refresh, _ := IssueRefreshToken(cfg.ABSJWTSecret, claims.UserID, newRefreshJTI, refreshTTL)
	_ = h.store.InsertABSToken(r.Context(), store.ABSToken{ID: newAccessJTI, UserID: claims.UserID, JTI: newAccessJTI, ExpiresAt: time.Now().Add(accessTTL)})
	_ = h.store.InsertABSToken(r.Context(), store.ABSToken{ID: newRefreshJTI, UserID: claims.UserID, JTI: newRefreshJTI, ExpiresAt: time.Now().Add(refreshTTL)})
	_ = h.store.RevokeABSTokenByJTI(r.Context(), claims.JTI)
	writeJSON(w, http.StatusOK, map[string]any{
		"accessToken":  access,
		"refreshToken": refresh,
	})
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if raw == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	_, cfg, err := h.targetFn(r.Context())
	if err != nil {
		http.Error(w, "config unavailable", http.StatusInternalServerError)
		return
	}
	claims, err := ParseToken(cfg.ABSJWTSecret, raw)
	if err == nil {
		_ = h.store.RevokeABSTokenByJTI(r.Context(), claims.JTI)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleMe(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"id":               a.UserID,
		"username":         a.UserID,
		"defaultLibraryId": VirtualLibraryID,
	})
}

func (h *Handler) handleLibraries(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"libraries": []map[string]any{
			{"id": VirtualLibraryID, "name": VirtualLibraryName, "mediaType": LibraryMediaType},
		},
	})
}

func (h *Handler) handleLibraryDetail(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{
		"library": map[string]any{
			"id":        VirtualLibraryID,
			"name":      VirtualLibraryName,
			"mediaType": LibraryMediaType,
		},
	}
	if includeHas(r.URL.Query().Get("include"), "filterdata") {
		resp["filterdata"] = h.collectFilterData(r)
		// Spec-shaped, optional extras: zero "issues" + zero playlists keep
		// the response stable for clients that read them.
		resp["issues"] = 0
		resp["numUserPlaylists"] = 0
	}
	writeJSON(w, http.StatusOK, resp)
}

// includeHas tests whether an "include" comma-separated query value contains
// the given key.
func includeHas(raw, want string) bool {
	if raw == "" {
		return false
	}
	for _, p := range strings.Split(raw, ",") {
		if strings.EqualFold(strings.TrimSpace(p), want) {
			return true
		}
	}
	return false
}

// collectFilterData walks the backend's browse endpoints to fill in the
// per-library filter pickers ABS clients render.
//
// Drift note: when the backend is unreachable we return empty arrays
// (never null) so the client doesn't crash on iteration. Same approach
// for the personalized shelves below.
func (h *Handler) collectFilterData(r *http.Request) map[string]any {
	a, _ := absAuthFrom(r)
	target, _, err := h.targetFn(r.Context())
	empty := map[string]any{
		"authors":    []AuthorObj{},
		"series":     []SeriesObj{},
		"narrators":  []string{},
		"genres":     []string{},
		"publishers": []string{},
		"languages":  []string{},
		"tags":       []string{},
	}
	if err != nil || target == "" {
		return empty
	}
	out := empty

	// Authors — IDs supplied by the backend (slug-based).
	if authors, err := h.backend.BrowseAuthors(r.Context(), a.Token, target, backend.ListParams{Limit: 500}); err == nil {
		refs := make([]AuthorObj, 0, len(authors.Items))
		for _, s := range authors.Items {
			refs = append(refs, AuthorObj{ID: s.ID, Name: s.Name})
		}
		out["authors"] = refs
	} else {
		h.logger.Warn("filterdata: browse authors", "err", err)
	}
	if series, err := h.backend.BrowseSeries(r.Context(), a.Token, target, backend.ListParams{Limit: 500}); err == nil {
		refs := make([]SeriesObj, 0, len(series.Items))
		for _, s := range series.Items {
			refs = append(refs, SeriesObj{ID: s.ID, Name: s.Name})
		}
		out["series"] = refs
	} else {
		h.logger.Warn("filterdata: browse series", "err", err)
	}
	if narrators, err := h.backend.BrowseNarrators(r.Context(), a.Token, target, backend.ListParams{Limit: 500}); err == nil {
		names := make([]string, 0, len(narrators.Items))
		for _, s := range narrators.Items {
			names = append(names, s.Name)
		}
		out["narrators"] = names
	} else {
		h.logger.Warn("filterdata: browse narrators", "err", err)
	}
	// genres / publishers / languages / tags: the v1 backend contract has no
	// dedicated browse endpoints for these. We return [] so the client UI
	// shows empty filter dropdowns rather than crashing; populating them
	// would require a contract extension (out of scope this round).
	return out
}

func (h *Handler) handleLibraryItems(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	target, _, err := h.targetFn(r.Context())
	if err != nil || target == "" {
		http.Error(w, "no backend configured", http.StatusPreconditionFailed)
		return
	}
	p := backend.ListParams{}
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, e := strconv.Atoi(l); e == nil {
			p.Limit = n
		}
	}
	out, err := h.backend.ListCatalog(r.Context(), a.Token, target, p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	results := make([]LibraryItem, len(out.Items))
	for i, s := range out.Items {
		results[i] = ToLibrarySummary(s)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results, "total": out.Total})
}

func (h *Handler) handleItem(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	target, _, err := h.targetFn(r.Context())
	if err != nil || target == "" {
		http.Error(w, "no backend configured", http.StatusPreconditionFailed)
		return
	}
	id := chi.URLParam(r, "id")
	d, err := h.backend.GetDetail(r.Context(), a.Token, target, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	// The contentUrl for each track is the bearer-protected web stream URL.
	// Drift from spec: spec wanted a session-scoped URL; we'd need to mint
	// a session JWT here. For now, use bearer-protected web stream.
	contentURLFn := func(idx int) string {
		return h.backend.StreamURL(target, id, idx)
	}
	writeJSON(w, http.StatusOK, ToLibraryItem(d, contentURLFn))
}

func (h *Handler) handleItemCover(w http.ResponseWriter, r *http.Request) {
	target, _, err := h.targetFn(r.Context())
	if err != nil || target == "" {
		http.Error(w, "no backend configured", http.StatusPreconditionFailed)
		return
	}
	id := chi.URLParam(r, "id")
	size := r.URL.Query().Get("size")
	http.Redirect(w, r, h.backend.CoverURL(target, id, size), http.StatusFound)
}

type playPayload struct {
	DeviceInfo  map[string]any `json:"deviceInfo"`
	MediaPlayer string         `json:"mediaPlayer"`
}

func (h *Handler) handlePlay(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	target, cfg, err := h.targetFn(r.Context())
	if err != nil || target == "" {
		http.Error(w, "no backend configured", http.StatusPreconditionFailed)
		return
	}
	bookID := chi.URLParam(r, "id")
	var p playPayload
	_ = json.NewDecoder(r.Body).Decode(&p)
	deviceID, _ := p.DeviceInfo["deviceId"].(string)
	if deviceID == "" {
		deviceID = "unknown"
	}
	d, err := h.backend.GetDetail(r.Context(), a.Token, target, bookID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	sessionID := ulid.Make().String()
	sess := store.ABSSession{
		ID:         sessionID,
		UserID:     a.UserID,
		BookID:     bookID,
		DeviceID:   deviceID,
		DeviceInfo: p.DeviceInfo,
		MediaPlayer: p.MediaPlayer,
	}
	if err := h.store.InsertABSSession(r.Context(), sess); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Build session-scoped contentURL per track.
	cdnHostname, cdnSecretB64 := h.cdnFn()
	useCDN := cdnHostname != "" && cdnSecretB64 != ""
	var cdnSecret []byte
	if useCDN {
		var decErr error
		cdnSecret, decErr = base64.StdEncoding.DecodeString(cdnSecretB64)
		if decErr != nil || len(cdnSecret) == 0 {
			// Misconfigured secret — fall back to portal proxy path.
			useCDN = false
		}
	}

	hostBase := h.hostBaseFn()
	installID := h.installID()
	tracks := make([]AudioTrack, len(d.Files))
	for i, f := range d.Files {
		var trackURL string
		if useCDN {
			tok, _ := cdn.MintStreamToken(cdnSecret, a.UserID, bookID, f.Index, 5*time.Minute)
			trackURL = cdn.PresignedURL(cdnHostname, bookID, f.Index, tok)
		} else {
			tok, _ := IssueSessionToken(cfg.ABSJWTSecret, a.UserID, sessionID, bookID, f.Index, 6*time.Hour)
			trackURL = hostBase + "/api/v1/plugins/" + installID +
				"/abs/public/session/" + sessionID + "/track/" + strconv.Itoa(f.Index) +
				"?token=" + tok
		}
		tracks[i] = AudioTrack{
			Index:      f.Index,
			ContentURL: trackURL,
			MimeType:   f.MimeType,
			Duration:   float64(f.DurationSeconds),
			Codec:      f.Format,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":             sessionID,
		"libraryItemId":  bookID,
		"audioTracks":    tracks,
		"mediaPlayer":    p.MediaPlayer,
	})
}

type syncPayload struct {
	CurrentTime  float64 `json:"currentTime"`
	TimeListened float64 `json:"timeListened"`
}

func (h *Handler) handleSessionSync(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	sid := chi.URLParam(r, "sid")
	var p syncPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if err := h.store.UpdateABSSession(r.Context(), sid, int(p.CurrentTime)); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Mirror to progress.
	sess, err := h.store.GetABSSession(r.Context(), sid)
	if err == nil {
		_ = h.store.UpsertProgress(r.Context(), store.Progress{
			UserID: a.UserID, BookID: sess.BookID,
			CurrentSeconds: int(p.CurrentTime),
		})
	}

	resp := map[string]any{"ok": true}

	// In CDN mode, return freshly-minted track URLs so the client can keep
	// streaming past the 5-minute presigned token window without re-calling
	// /play. We only do this when we can also fetch the book detail.
	cdnHostname, cdnSecretB64 := h.cdnFn()
	if cdnHostname != "" && cdnSecretB64 != "" && err == nil {
		cdnSecret, decErr := base64.StdEncoding.DecodeString(cdnSecretB64)
		if decErr == nil && len(cdnSecret) > 0 {
			target, _, targetErr := h.targetFn(r.Context())
			if targetErr == nil && target != "" {
				d, detailErr := h.backend.GetDetail(r.Context(), a.Token, target, sess.BookID)
				if detailErr == nil {
					tracks := make([]AudioTrack, len(d.Files))
					for i, f := range d.Files {
						tok, _ := cdn.MintStreamToken(cdnSecret, a.UserID, sess.BookID, f.Index, 5*time.Minute)
						tracks[i] = AudioTrack{
							Index:      f.Index,
							ContentURL: cdn.PresignedURL(cdnHostname, sess.BookID, f.Index, tok),
							MimeType:   f.MimeType,
							Duration:   float64(f.DurationSeconds),
							Codec:      f.Format,
						}
					}
					resp["audioTracks"] = tracks
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleSessionClose(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sid")
	_ = h.store.CloseABSSession(r.Context(), sid)
	w.WriteHeader(http.StatusNoContent)
}

// handlePublicTrack serves a session-scoped audio stream. JWT in the query
// string is the capability — verifies sid/bid/fidx + session row + not closed.
func (h *Handler) handlePublicTrack(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sid")
	idxStr := chi.URLParam(r, "idx")
	idx, err := strconv.Atoi(idxStr)
	if err != nil {
		http.Error(w, "idx must be int", http.StatusBadRequest)
		return
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Error(w, "token required", http.StatusUnauthorized)
		return
	}
	target, cfg, err := h.targetFn(r.Context())
	if err != nil {
		http.Error(w, "config unavailable", http.StatusInternalServerError)
		return
	}
	claims, err := ParseToken(cfg.ABSJWTSecret, token)
	if err != nil || claims.Type != "session" || claims.SessionID != sid || claims.FileIdx != idx {
		http.Error(w, "invalid session token", http.StatusUnauthorized)
		return
	}
	sess, err := h.store.GetABSSession(r.Context(), sid)
	if err != nil || sess.ClosedAt != nil {
		http.Error(w, "session closed or missing", http.StatusGone)
		return
	}
	// Redirect to the backend's stream URL. (The proxy will validate the
	// inbound bearer the audio client would otherwise need — here we let the
	// session-token-already-verified state stand in.)
	http.Redirect(w, r, h.backend.StreamURL(target, sess.BookID, idx), http.StatusFound)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ---------- Library authors / series ----------

// handleLibraryAuthors mirrors ABS GET /api/libraries/{id}/authors. The ABS
// reference server supports two response shapes (paginated vs non-paginated);
// we always return the paginated `{results, total, limit, page}` shape since
// mobile clients accept either.
func (h *Handler) handleLibraryAuthors(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	target, _, err := h.targetFn(r.Context())
	if err != nil || target == "" {
		http.Error(w, "no backend configured", http.StatusPreconditionFailed)
		return
	}
	limit, page := readPagedQuery(r, 50)
	out, err := h.backend.BrowseAuthors(r.Context(), a.Token, target, backend.ListParams{Limit: limit})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	results := make([]map[string]any, len(out.Items))
	for i, s := range out.Items {
		results[i] = map[string]any{
			"id":        s.ID,
			"name":      s.Name,
			"numBooks":  s.Count,
			"libraryId": VirtualLibraryID,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"results":  results,
		"total":    out.Total,
		"limit":    limit,
		"page":     page,
		"sortBy":   "name",
		"sortDesc": false,
		"minified": false,
	})
}

// handleLibrarySeries mirrors ABS GET /api/libraries/{id}/series.
func (h *Handler) handleLibrarySeries(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	target, _, err := h.targetFn(r.Context())
	if err != nil || target == "" {
		http.Error(w, "no backend configured", http.StatusPreconditionFailed)
		return
	}
	limit, page := readPagedQuery(r, 25)
	out, err := h.backend.BrowseSeries(r.Context(), a.Token, target, backend.ListParams{Limit: limit})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	results := make([]map[string]any, len(out.Items))
	for i, s := range out.Items {
		results[i] = map[string]any{
			"id":        s.ID,
			"name":      s.Name,
			"numBooks":  s.Count,
			"libraryId": VirtualLibraryID,
			// Drift: addedAt isn't supplied by the v1 backend's series
			// list. We emit 0 (rather than omit) so clients that do a
			// numeric sort on the field don't blow up.
			"addedAt": 0,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"results":  results,
		"total":    out.Total,
		"limit":    limit,
		"page":     page,
		"sortBy":   "name",
		"sortDesc": false,
		"minified": false,
	})
}

// readPagedQuery parses the ABS ?limit=&page= query, falling back to the
// supplied default limit.
func readPagedQuery(r *http.Request, defaultLimit int) (limit, page int) {
	limit = defaultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if v := r.URL.Query().Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			page = n
		}
	}
	return limit, page
}

// ---------- Personalized shelves ----------

// handlePersonalized returns the 6 standard ABS home-screen shelves. Each
// shelf carries up to `limit` (default 10) entities; if there's no data
// for a shelf (no progress yet, no backend) the shelf is emitted with an
// empty entities array — clients iterate the shelf list, so omitting a
// shelf breaks the layout.
func (h *Handler) handlePersonalized(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	shelves := []map[string]any{
		{"id": "continue-listening", "label": "Continue Listening", "labelStringKey": "LabelContinueListening", "type": "book", "entities": []any{}},
		{"id": "continue-series", "label": "Continue Series", "labelStringKey": "LabelContinueSeries", "type": "book", "entities": []any{}},
		{"id": "newest", "label": "Newest", "labelStringKey": "LabelNewest", "type": "book", "entities": []any{}},
		{"id": "recent-series", "label": "Recent Series", "labelStringKey": "LabelRecentSeries", "type": "series", "entities": []any{}},
		{"id": "discover", "label": "Discover", "labelStringKey": "LabelDiscover", "type": "book", "entities": []any{}},
		{"id": "listen-again", "label": "Listen Again", "labelStringKey": "LabelListenAgain", "type": "book", "entities": []any{}},
	}

	target, _, err := h.targetFn(r.Context())
	if err != nil || target == "" {
		// No backend configured — return empty shelves rather than 412 so
		// the home page renders an empty state.
		writeJSON(w, http.StatusOK, shelves)
		return
	}

	// Resolve progress rows; classify by is_finished + progress_pct.
	progRows, err := h.store.ListRecentProgress(r.Context(), a.UserID, 50)
	if err != nil {
		h.logger.Warn("personalized: list progress", "err", err)
		progRows = nil
	}
	// continue-listening: in-progress (not finished, progress > 0).
	// listen-again: finished items.
	contPaths := make([]LibraryItem, 0, limit)
	againPaths := make([]LibraryItem, 0, limit)
	for _, p := range progRows {
		if p.IsFinished {
			if len(againPaths) >= limit {
				continue
			}
			if d, derr := h.backend.GetDetail(r.Context(), a.Token, target, p.BookID); derr == nil {
				againPaths = append(againPaths, ToLibraryItem(d, func(int) string { return "" }))
			}
			continue
		}
		if p.ProgressPct <= 0 {
			continue
		}
		if len(contPaths) >= limit {
			continue
		}
		if d, derr := h.backend.GetDetail(r.Context(), a.Token, target, p.BookID); derr == nil {
			contPaths = append(contPaths, ToLibraryItem(d, func(int) string { return "" }))
		}
	}
	shelves[0]["entities"] = contPaths
	shelves[5]["entities"] = againPaths

	// newest + discover come from the catalog list. We sort by added_at
	// desc when the backend supports it. For "discover" we exclude items
	// the user already has progress on.
	progressBookIDs := map[string]bool{}
	for _, p := range progRows {
		progressBookIDs[p.BookID] = true
	}
	listOut, err := h.backend.ListCatalog(r.Context(), a.Token, target, backend.ListParams{Limit: limit * 3, Sort: "added", Order: "desc"})
	if err != nil {
		h.logger.Warn("personalized: list catalog", "err", err)
	} else {
		newest := make([]LibraryItem, 0, limit)
		discover := make([]LibraryItem, 0, limit)
		for _, s := range listOut.Items {
			if len(newest) < limit {
				newest = append(newest, ToLibrarySummary(s))
			}
			if !progressBookIDs[s.ID] && len(discover) < limit {
				discover = append(discover, ToLibrarySummary(s))
			}
		}
		shelves[2]["entities"] = newest
		shelves[4]["entities"] = discover
	}

	// recent-series: take the first N series the backend returns. Each
	// entity is a thin series object; clients render the name + cover.
	if seriesOut, err := h.backend.BrowseSeries(r.Context(), a.Token, target, backend.ListParams{Limit: limit}); err == nil {
		recent := make([]map[string]any, 0, len(seriesOut.Items))
		for _, s := range seriesOut.Items {
			recent = append(recent, map[string]any{
				"id":        s.ID,
				"name":      s.Name,
				"numBooks":  s.Count,
				"libraryId": VirtualLibraryID,
				"books":     []any{},
			})
		}
		shelves[3]["entities"] = recent
	}

	// continue-series: drift — the v1 backend has no "next book in series
	// I've started" query, so we leave it empty. Future work would join
	// the user's progress rows against the book.series_id to surface the
	// next sequence.

	writeJSON(w, http.StatusOK, shelves)
}

// ---------- Progress (ABS-shaped) ----------

// progressBody is the PATCH body shape; all fields optional.
type progressBody struct {
	CurrentTime *float64 `json:"currentTime"`
	Duration    *float64 `json:"duration"`
	IsFinished  *bool    `json:"isFinished"`
	Progress    *float64 `json:"progress"`
}

func (h *Handler) handleGetProgress(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	itemID := chi.URLParam(r, "itemId")
	p, err := h.store.GetProgress(r.Context(), a.UserID, itemID)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "progress not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, progressToABS(a.UserID, p))
}

func (h *Handler) handlePatchProgress(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	itemID := chi.URLParam(r, "itemId")
	var body progressBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	// Merge with existing row (if any) so the PATCH semantics hold: fields
	// not present in the body keep their current value.
	cur, err := h.store.GetProgress(r.Context(), a.UserID, itemID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	next := store.Progress{
		UserID:         a.UserID,
		BookID:         itemID,
		CurrentSeconds: cur.CurrentSeconds,
		ProgressPct:    cur.ProgressPct,
		IsFinished:     cur.IsFinished,
	}
	if body.CurrentTime != nil {
		next.CurrentSeconds = int(*body.CurrentTime)
	}
	if body.Progress != nil {
		next.ProgressPct = float32(*body.Progress)
	}
	if body.IsFinished != nil {
		next.IsFinished = *body.IsFinished
	}
	if err := h.store.UpsertProgress(r.Context(), next); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	updated, err := h.store.GetProgress(r.Context(), a.UserID, itemID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, progressToABS(a.UserID, updated))
}

// progressToABS shapes a store.Progress into the ABS /me/progress payload.
func progressToABS(userID string, p store.Progress) map[string]any {
	last := p.UpdatedAt.UnixMilli()
	out := map[string]any{
		"id":            userID + "-" + p.BookID,
		"libraryItemId": p.BookID,
		"mediaItemId":   p.BookID,
		"currentTime":   float64(p.CurrentSeconds),
		"duration":      0,
		"isFinished":    p.IsFinished,
		"progress":      float64(p.ProgressPct),
		"startedAt":     last,
		"finishedAt":    nil,
		"lastUpdate":    last,
	}
	if p.IsFinished {
		out["finishedAt"] = last
	}
	return out
}
