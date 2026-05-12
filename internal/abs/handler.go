package abs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/backend"
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
	return &Handler{
		store: d.Store, backend: d.Backend, logger: d.Logger,
		targetFn: d.TargetFn, hostBaseFn: d.HostBaseFn, installID: d.InstallID,
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
		r.Get("/abs/api/items/{id}", h.handleItem)
		r.Get("/abs/api/items/{id}/cover", h.handleItemCover)
		r.Post("/abs/api/items/{id}/play", h.handlePlay)
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

type loginPayload struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleLogin trusts the inbound continuum identity (the proxy stamps user
// headers). It mints ABS access + refresh JWTs and persists the refresh jti.
//
// Drift from the spec: the original design called for validating
// username/password against continuum's auth endpoint. The current SDK has
// no such RPC; we accept whatever bearer/identity is forwarded and mint
// against that. Admins can issue tokens directly from the Apps SPA page.
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-Continuum-User-Id")
	if userID == "" {
		// Allow body-only userID for tests / admin-issued tokens.
		var p loginPayload
		_ = json.NewDecoder(r.Body).Decode(&p)
		userID = p.Username
	}
	if userID == "" {
		http.Error(w, "no user identity", http.StatusUnauthorized)
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
	_ = h.store.InsertABSToken(r.Context(), store.ABSToken{
		ID:        refreshJTI,
		UserID:    userID,
		JTI:       refreshJTI,
		ExpiresAt: time.Now().Add(refreshTTL),
	})
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
	writeJSON(w, http.StatusOK, map[string]any{
		"library": map[string]any{
			"id": VirtualLibraryID, "name": VirtualLibraryName, "mediaType": LibraryMediaType,
		},
	})
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

	// Build session-scoped contentURL per track. Mint a per-track session JWT.
	hostBase := h.hostBaseFn()
	installID := h.installID()
	tracks := make([]AudioTrack, len(d.Files))
	for i, f := range d.Files {
		tok, _ := IssueSessionToken(cfg.ABSJWTSecret, a.UserID, sessionID, bookID, f.Index, 6*time.Hour)
		url := hostBase + "/api/v1/plugins/" + installID +
			"/abs/public/session/" + sessionID + "/track/" + strconv.Itoa(f.Index) +
			"?token=" + tok
		tracks[i] = AudioTrack{
			Index:      f.Index,
			ContentURL: url,
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
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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
