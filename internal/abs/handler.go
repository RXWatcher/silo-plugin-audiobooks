package abs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/backend"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/bookref"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/hostlogin"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/mediatoken"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/streaming"
)

// Handler wires the /abs/api/* and /abs/public/* surface.
type Handler struct {
	store        *store.Store
	backend      *backend.Client
	streaming    *streaming.Router
	logger       Logger
	targetFn     func(ctx context.Context) (string, store.BackendConfig, error)
	hostBaseFn   func() string
	installID    func() string // current plugin install ID for building public URLs
	hostLogin    HostLoginValidator
	loginLimiter *LoginLimiter
	publisher    EventPublisher
}

// EventPublisher delivers a realtime event to every Socket.io client
// currently connected for the given userID. Implemented by abssocket.Server;
// surfaced here as an interface so tests can stub and the package stays
// decoupled from Socket.io transport details.
//
// Publishers must be non-blocking — handlers call this in the hot path of
// REST writes and we don't want a slow socket to back up an HTTP response.
//
// Broadcast fans the event to every connected client regardless of user.
// Used for global events like listener_count that aren't user-scoped.
type EventPublisher interface {
	Publish(userID, event string, payload any)
	Broadcast(event string, payload any)
}

// HostLoginValidator validates username/password against the Continuum host.
// Implemented by hostlogin.Client; surfaced as an interface so tests can stub.
type HostLoginValidator interface {
	Validate(ctx context.Context, username, password, deviceName, ip string) (hostlogin.Result, error)
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
	Streaming  *streaming.Router
	Logger     Logger
	TargetFn   func(ctx context.Context) (string, store.BackendConfig, error)
	HostBaseFn func() string
	InstallID  func() string
	// HostLogin validates body-creds against the Continuum host's
	// /api/v1/auth/login endpoint. May be nil; when nil, the body-creds
	// login path returns 503 regardless of admin mode.
	HostLogin HostLoginValidator
	// LoginLimiter throttles standalone-port body-creds login attempts per
	// source IP. Construct one per process (its janitor is a long-lived
	// goroutine) and share it across plugin reconfigures.
	LoginLimiter *LoginLimiter
	// Publisher pushes realtime events to ABS Socket.io clients. May be
	// nil — calls into it short-circuit, so the REST surface keeps
	// working when the realtime hub isn't wired (tests, host-proxied
	// flows where /socket.io isn't reachable).
	Publisher EventPublisher
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
	lim := d.LoginLimiter
	if lim == nil {
		// Tests may construct a Handler without injecting a limiter; spin
		// one up locally. Production code passes a shared limiter via Deps.
		lim = NewLoginLimiter()
	}
	return &Handler{
		store: d.Store, backend: d.Backend, streaming: d.Streaming, logger: d.Logger,
		targetFn: d.TargetFn, hostBaseFn: d.HostBaseFn, installID: d.InstallID,
		hostLogin:    d.HostLogin,
		loginLimiter: lim,
		publisher:    d.Publisher,
	}
}

// publish is a nil-safe wrapper around the optional EventPublisher.
func (h *Handler) publish(userID, event string, payload any) {
	if h.publisher == nil {
		return
	}
	h.publisher.Publish(userID, event, payload)
}

// broadcast is the global-scope counterpart to publish. Used for events
// that aren't tied to a single user (listener_count, item_added, ...).
func (h *Handler) broadcast(event string, payload any) {
	if h.publisher == nil {
		return
	}
	h.publisher.Broadcast(event, payload)
}

// broadcastListenerCount fires a "listener_count" event with the current
// active-session count. Called on session open and close so admin
// dashboards / future per-listener UIs see live numbers without polling.
// Best-effort: a Postgres hiccup logs at debug and skips the broadcast
// rather than failing the surrounding HTTP write.
func (h *Handler) broadcastListenerCount(ctx context.Context) {
	if h.publisher == nil || h.store == nil {
		return
	}
	n, err := h.store.CountActiveABSSessions(ctx)
	if err != nil {
		h.logger.Debug("listener_count: count failed", "err", err.Error())
		return
	}
	h.broadcast("listener_count", map[string]any{"count": n})
}

// absBaseURL returns the URL prefix the ABS client should resolve any
// further response-embedded URLs against. Detection:
//
//   - Standalone listener: X-Continuum-* headers were stripped at
//     httproutes/server.go before the handler ran. r.Host carries the
//     listener's hostname (e.g. abs.example.com). Return
//     "<scheme>://<host>" — origin only, no prefix.
//   - Host-proxied: the continuum host stamps X-Continuum-User-Id on every
//     forwarded request (continuum/internal/plugins/http_proxy.go). Return
//     "<scheme>://<host>/api/v1/plugins/<installID>" so subsequent URLs
//     stay routable through the host's plugin proxy.
//
// Honour X-Forwarded-Proto / X-Forwarded-Host when they are present so
// operators terminating TLS at a reverse proxy don't end up with http:// in
// emitted URLs.
func (h *Handler) absBaseURL(r *http.Request) string {
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	if r.Header.Get("X-Continuum-User-Id") != "" {
		return fmt.Sprintf("%s://%s/api/v1/plugins/%s", scheme, host, h.installID())
	}
	return fmt.Sprintf("%s://%s", scheme, host)
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
		r.Get("/abs/api/libraries/{id}/search", h.handleLibrarySearch)
		r.Get("/abs/api/libraries/{id}/personalized", h.handlePersonalized)
		r.Get("/abs/api/items/{id}", h.handleItem)
		r.Get("/abs/api/items/{id}/cover", h.handleItemCover)
		r.Post("/abs/api/items/{id}/play", h.handlePlay)
		// Podcast play — episode-scoped session. Real ABS uses
		// /api/items/{podcastId}/play/{episodeId}; ours is the same path.
		r.Post("/abs/api/items/{id}/play/{episodeId}", h.handlePlayEpisode)
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

func absLibraryID(lib store.PortalLibrary) string {
	if lib.ID > 0 {
		return strconv.FormatInt(lib.ID, 10)
	}
	return VirtualLibraryID
}

func absLibraryMap(lib store.PortalLibrary) map[string]any {
	name := lib.Name
	if strings.TrimSpace(name) == "" {
		name = VirtualLibraryName
	}
	// Honour the library's declared media type so podcast libraries
	// surface as mediaType=podcast (not the default book). ABS clients
	// branch their UI on this — a podcast library renders an episode-
	// scoped player, not a chapter-scoped one.
	mediaType := lib.MediaType
	if mediaType == "" {
		mediaType = LibraryMediaType
	}
	if mediaType == "audiobook" {
		mediaType = LibraryMediaType // "book" — ABS spec calls audiobooks "book"
	}
	return map[string]any{
		"id":        absLibraryID(lib),
		"name":      name,
		"mediaType": mediaType,
	}
}

func backendLibraryID(lib store.PortalLibrary) int64 {
	if lib.BackendLibraryID == nil {
		return 0
	}
	return *lib.BackendLibraryID
}

func (h *Handler) portalLibraries(ctx context.Context, enabledOnly bool) []store.PortalLibrary {
	if h.store == nil {
		return nil
	}
	libs, err := h.store.ListPortalLibraries(ctx, enabledOnly)
	if err == nil && len(libs) > 0 {
		return libs
	}
	cfg, err := h.store.GetBackendConfig(ctx)
	if err != nil || cfg.TargetBackendPluginID == "" {
		return nil
	}
	return []store.PortalLibrary{{
		Name:            VirtualLibraryName,
		MediaType:       "audiobook",
		BackendPluginID: cfg.TargetBackendPluginID,
		Enabled:         true,
	}}
}

func (h *Handler) defaultPortalLibrary(ctx context.Context) (store.PortalLibrary, error) {
	if h.store != nil {
		if lib, err := h.store.DefaultPortalLibrary(ctx); err == nil {
			return lib, nil
		}
	}
	libs := h.portalLibraries(ctx, true)
	if len(libs) > 0 {
		return libs[0], nil
	}
	return store.PortalLibrary{}, store.ErrNotFound
}

func (h *Handler) portalLibraryFromABSID(ctx context.Context, id string) (store.PortalLibrary, error) {
	if strings.TrimSpace(id) == "" || id == VirtualLibraryID {
		return h.defaultPortalLibrary(ctx)
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return store.PortalLibrary{}, store.ErrNotFound
	}
	return h.store.GetPortalLibrary(ctx, n)
}

func (h *Handler) portalLibraryForBookRef(ctx context.Context, ref string) (store.PortalLibrary, string, string, error) {
	libraryID, backendBookID, encoded := bookref.Decode(ref)
	lib, err := h.defaultPortalLibrary(ctx)
	if libraryID > 0 {
		lib, err = h.store.GetPortalLibrary(ctx, libraryID)
	}
	if err != nil {
		return store.PortalLibrary{}, "", "", err
	}
	if !encoded {
		ref = bookref.Encode(lib.ID, backendBookID)
	}
	return lib, backendBookID, ref, nil
}

func withPortalLibrarySummary(s backend.AudiobookSummary, lib store.PortalLibrary) backend.AudiobookSummary {
	s.ID = bookref.Encode(lib.ID, s.ID)
	s.LibraryID = lib.ID
	s.LibraryName = lib.Name
	s.MediaType = lib.MediaType
	return s
}

func withPortalLibraryDetail(d backend.AudiobookDetail, lib store.PortalLibrary) backend.AudiobookDetail {
	d.AudiobookSummary = withPortalLibrarySummary(d.AudiobookSummary, lib)
	return d
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
		"isInit":        true,
		"language":      "en-us",
		"app":           ServerSourceTag,
		"serverVersion": ServerVersion,
	})
}

// handleLogin mints ABS access + refresh JWTs for the caller.
//
// There are two ways to establish identity here:
//
//  1. Host-proxied path: the X-Continuum-User-Id header is set. The host
//     validated the session cookie / API token before forwarding the request,
//     so reaching this handler with that header set is proof of a valid
//     continuum session. Trusted unconditionally — this branch is unchanged
//     from the pre-standalone-login design.
//
//  2. Standalone-port body-creds path: the header is absent (the standalone
//     listener strips X-Continuum-* before invoking the handler — see
//     httproutes/server.go). The request body holds {username, password}
//     from an official Audiobookshelf client. We validate those credentials
//     against the Continuum host's POST /api/v1/auth/login endpoint, which
//     pins on user.LocalPasswordLoginEnabled in the host's LocalProvider, so
//     listeners without a local password fail closed.
//
// The body-creds path is gated by the admin-managed
// backend_config.standalone_login_mode setting:
//
//   - "disabled": header path only. Body-creds always 401.
//   - "opt_in":   body-creds works for users with a row in
//     abs_standalone_opt_ins.
//   - "all_accounts": body-creds works for any account the host's local
//     provider accepts.
//
// A per-IP rate limiter throttles body-creds attempts. The host-proxied
// branch is never rate-limited.
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if userID := r.Header.Get("X-Continuum-User-Id"); userID != "" {
		h.completeLogin(w, r, userID)
		return
	}
	h.handleStandaloneLogin(w, r)
}

// handleStandaloneLogin runs the body-creds path when no host-proxied
// identity header is present.
func (h *Handler) handleStandaloneLogin(w http.ResponseWriter, r *http.Request) {
	_, cfg, err := h.targetFn(r.Context())
	if err != nil {
		http.Error(w, "config unavailable", http.StatusInternalServerError)
		return
	}
	mode := store.NormalizeStandaloneLoginMode(cfg.StandaloneLoginMode)
	if mode == store.StandaloneLoginModeDisabled {
		http.Error(w, "login must be initiated via the continuum host; standalone /login is not accepted", http.StatusUnauthorized)
		return
	}
	if h.hostLogin == nil {
		// Mode says body-creds is allowed but the deployment didn't wire a
		// host-login client. Fail closed loudly so the operator notices.
		h.logger.Warn("abs.standalone_login: no host-login client configured", "mode", mode)
		http.Error(w, "standalone login is unavailable in this deployment", http.StatusServiceUnavailable)
		return
	}

	ip := clientIP(r)
	if !h.loginLimiter.allow(ip) {
		h.logger.Warn("abs.standalone_login: rate limited", "ip", ip, "mode", mode)
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too many login attempts; try again shortly", http.StatusTooManyRequests)
		return
	}

	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Username) == "" || body.Password == "" {
		http.Error(w, "username and password are required", http.StatusUnauthorized)
		return
	}

	res, err := h.hostLogin.Validate(r.Context(), body.Username, body.Password, r.UserAgent(), ip)
	if err != nil {
		if errors.Is(err, hostlogin.ErrInvalidCredentials) {
			h.logger.Warn("abs.standalone_login: invalid credentials",
				"ip", ip, "username", body.Username, "mode", mode)
			http.Error(w, "invalid username or password", http.StatusUnauthorized)
			return
		}
		h.logger.Warn("abs.standalone_login: upstream error",
			"ip", ip, "username", body.Username, "mode", mode, "err", err.Error())
		http.Error(w, "upstream login unavailable", http.StatusBadGateway)
		return
	}

	if mode == store.StandaloneLoginModeOptIn {
		ok, hErr := h.store.HasStandaloneOptIn(r.Context(), res.UserID)
		if hErr != nil {
			h.logger.Warn("abs.standalone_login: opt-in lookup failed",
				"ip", ip, "user_id", res.UserID, "err", hErr.Error())
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !ok {
			h.logger.Warn("abs.standalone_login: user not opted in",
				"ip", ip, "user_id", res.UserID)
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error":   "not_enabled_for_mobile_login",
				"message": "Mobile-app login is not enabled for this account. Enable it from the audiobooks portal under account settings.",
			})
			return
		}
	}

	h.logger.Debug("abs.standalone_login: success",
		"ip", ip, "user_id", res.UserID, "mode", mode)
	h.completeLogin(w, r, res.UserID)
}

// completeLogin mints ABS access + refresh JWTs for the validated user and
// writes the login response. Shared by both the header path and the
// body-creds path so the response shape is identical.
func (h *Handler) completeLogin(w http.ResponseWriter, r *http.Request, userID string) {
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
	libs := h.portalLibraries(r.Context(), true)
	libraryMaps := make([]map[string]any, 0, len(libs))
	defaultLibraryID := VirtualLibraryID
	for i, lib := range libs {
		if i == 0 {
			defaultLibraryID = absLibraryID(lib)
		}
		libraryMaps = append(libraryMaps, absLibraryMap(lib))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user": map[string]any{
			"id":               userID,
			"username":         userID,
			"defaultLibraryId": defaultLibraryID,
		},
		"accessToken":  access,
		"refreshToken": refresh,
		"libraries":    libraryMaps,
	})
}

type refreshPayload struct {
	RefreshToken string `json:"refreshToken"`
}

// handleRefresh rotates the ABS access + refresh JWT pair. The contract:
//
//  1. Decode the inbound refresh JWT against the active signing secret.
//  2. Confirm the JTI exists in abs_tokens and isn't revoked.
//  3. Mint a fresh access + refresh pair with new JTIs.
//  4. Insert both new JTIs into abs_tokens.
//  5. Revoke the old refresh JTI.
//
// The order matters — if step 4 fails, the client retries with the still-
// valid old refresh token and is no worse off. If step 5 fails after step
// 4 succeeds, the client gets a new pair but the old refresh stays valid
// until step 5 succeeds on a later retry; that's an acceptable degradation
// vs serving an inconsistent pair. Access tokens minted before the rotation
// remain valid until their own TTL expires — that's by design, the same
// way every other access-+-refresh-token system behaves.
//
// Concurrency note: two simultaneous refreshes with the same old token
// each pass the JTI-valid check, each insert their new JTIs (different
// ULIDs so no conflict), and each revoke the old JTI (idempotent). The
// client gets one of two new refresh tokens depending on response order;
// the other is discarded on the next refresh. We don't try to detect
// "double-refresh from different IPs" as a theft signal — ABS clients
// don't have that signal anyway.
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
	access, err := IssueAccessToken(cfg.ABSJWTSecret, claims.UserID, newAccessJTI, accessTTL)
	if err != nil {
		http.Error(w, "token mint failed", http.StatusInternalServerError)
		return
	}
	refresh, err := IssueRefreshToken(cfg.ABSJWTSecret, claims.UserID, newRefreshJTI, refreshTTL)
	if err != nil {
		http.Error(w, "token mint failed", http.StatusInternalServerError)
		return
	}
	// Persist the new tokens and revoke the old refresh jti. If the revoke
	// does NOT persist, the old refresh token stays valid — handing out a
	// new pair anyway would create a refresh-token replay window. Fail the
	// rotation instead; the client keeps using its still-valid old token and
	// can retry. (Inserts are checked too: an unpersisted jti would be
	// treated as revoked on next use and silently log the user out.)
	if err := h.store.InsertABSToken(r.Context(), store.ABSToken{ID: newAccessJTI, UserID: claims.UserID, JTI: newAccessJTI, ExpiresAt: time.Now().Add(accessTTL)}); err != nil {
		http.Error(w, "token persist failed", http.StatusInternalServerError)
		return
	}
	if err := h.store.InsertABSToken(r.Context(), store.ABSToken{ID: newRefreshJTI, UserID: claims.UserID, JTI: newRefreshJTI, ExpiresAt: time.Now().Add(refreshTTL)}); err != nil {
		http.Error(w, "token persist failed", http.StatusInternalServerError)
		return
	}
	if err := h.store.RevokeABSTokenByJTI(r.Context(), claims.JTI); err != nil {
		http.Error(w, "token rotation failed", http.StatusInternalServerError)
		return
	}
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
	lib, _ := h.defaultPortalLibrary(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"id":               a.UserID,
		"username":         a.UserID,
		"defaultLibraryId": absLibraryID(lib),
	})
}

func (h *Handler) handleLibraries(w http.ResponseWriter, r *http.Request) {
	libs := h.portalLibraries(r.Context(), true)
	out := make([]map[string]any, 0, len(libs))
	for _, lib := range libs {
		out = append(out, absLibraryMap(lib))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"libraries": out,
	})
}

func (h *Handler) handleLibraryDetail(w http.ResponseWriter, r *http.Request) {
	lib, err := h.portalLibraryFromABSID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "library not found", http.StatusNotFound)
		return
	}
	resp := map[string]any{
		"library": absLibraryMap(lib),
	}
	if includeHas(r.URL.Query().Get("include"), "filterdata") {
		resp["filterdata"] = h.collectFilterData(r, lib)
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
func (h *Handler) collectFilterData(r *http.Request, lib store.PortalLibrary) map[string]any {
	a, _ := absAuthFrom(r)
	empty := map[string]any{
		"authors":    []AuthorObj{},
		"series":     []SeriesObj{},
		"narrators":  []string{},
		"genres":     []string{},
		"publishers": []string{},
		"languages":  []string{},
		"tags":       []string{},
	}
	if lib.BackendPluginID == "" {
		return empty
	}
	out := empty
	params := backend.ListParams{Limit: 500, LibraryID: backendLibraryID(lib)}

	// Authors — IDs supplied by the backend (slug-based).
	if authors, err := h.backend.BrowseAuthors(r.Context(), a.Token, lib.BackendPluginID, params); err == nil {
		refs := make([]AuthorObj, 0, len(authors.Items))
		for _, s := range authors.Items {
			refs = append(refs, AuthorObj{ID: s.ID, Name: s.Name})
		}
		out["authors"] = refs
	} else {
		h.logger.Warn("filterdata: browse authors", "err", err)
	}
	if series, err := h.backend.BrowseSeries(r.Context(), a.Token, lib.BackendPluginID, params); err == nil {
		refs := make([]SeriesObj, 0, len(series.Items))
		for _, s := range series.Items {
			refs = append(refs, SeriesObj{ID: s.ID, Name: s.Name})
		}
		out["series"] = refs
	} else {
		h.logger.Warn("filterdata: browse series", "err", err)
	}
	if narrators, err := h.backend.BrowseNarrators(r.Context(), a.Token, lib.BackendPluginID, params); err == nil {
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
	lib, err := h.portalLibraryFromABSID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "library not found", http.StatusNotFound)
		return
	}
	// Podcast libraries are served directly from the plugin's DB —
	// there's no backend plugin contract for podcasts, the rows live
	// here and operators seed them via the admin endpoints (or, in a
	// follow-up, via an RSS feed refresher).
	if lib.MediaType == "podcast" {
		h.handlePodcastLibraryItems(w, r, lib)
		return
	}
	if lib.BackendPluginID == "" {
		http.Error(w, "no backend configured", http.StatusPreconditionFailed)
		return
	}

	q := r.URL.Query()
	limit, page := readPagedQuery(r, 30)
	sortBy := q.Get("sort")
	sortDesc := q.Get("desc") == "1"
	filterBy := q.Get("filter")
	minified := q.Get("minified") == "1"
	collapseSeries := q.Get("collapseseries") == "1"
	include := q.Get("include")

	filter, hasFilter := ParseFilter(filterBy)

	// Filter pushdown: forward the filter kind+value to the backend so it
	// can apply an index hit instead of returning the whole catalog.
	// Older backends ignore the params; we still over-fetch and apply
	// locally below so the response is correct regardless. The over-fetch
	// cap stays generous (5000) — once backends honor filter pushdown,
	// the over-fetch will naturally shrink because the backend already
	// reduced the result set.
	fetchLimit := limit
	if hasFilter || limit == 0 {
		fetchLimit = 5000
	}
	p := backend.ListParams{
		Limit:     fetchLimit,
		LibraryID: backendLibraryID(lib),
	}
	if sortBy != "" {
		p.Sort = sortBy
		if sortDesc {
			p.Order = "desc"
		} else {
			p.Order = "asc"
		}
	}
	if hasFilter {
		p.Filter = string(filter.Kind)
		p.FilterValue = filter.Value
	}

	out, err := h.backend.ListCatalog(r.Context(), a.Token, lib.BackendPluginID, p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	// Translate then optionally filter / paginate locally.
	all := make([]LibraryItem, 0, len(out.Items))
	for _, s := range out.Items {
		all = append(all, ToLibrarySummary(withPortalLibrarySummary(s, lib)))
	}
	if hasFilter {
		filtered := make([]LibraryItem, 0, len(all))
		for _, item := range all {
			// Progress fields stay false here — backend summaries don't
			// carry per-user progress; a follow-up join with store.Progress
			// can light up the progress.* filters.
			if filter.Matches(item, false, false, false) {
				filtered = append(filtered, item)
			}
		}
		all = filtered
	}

	total := len(all)
	if !hasFilter && out.Total > total {
		// Backend reported a larger unfiltered total; preserve it so
		// clients don't show fewer "x of y" than really exist.
		total = out.Total
	}

	// Collapse-by-series: real ABS clients pass collapseseries=1 to
	// fold every book in a series into a single representative entry
	// carrying a "collapsedSeries" block listing the constituents. Done
	// BEFORE paging so the page slice is correctly sized in terms of
	// collapsed entries.
	collapsed := all
	if collapseSeries {
		collapsed = CollapseBySeries(all)
		total = len(collapsed)
	}

	// Slice for page/limit. limit=0 is the documented "return all" signal.
	pageStart, pageEnd := 0, len(collapsed)
	if limit > 0 {
		pageStart = page * limit
		if pageStart > len(collapsed) {
			pageStart = len(collapsed)
		}
		pageEnd = pageStart + limit
		if pageEnd > len(collapsed) {
			pageEnd = len(collapsed)
		}
	}
	pageSlice := collapsed[pageStart:pageEnd]

	// Serialise — minified mode reshapes each item.
	var results any
	if minified {
		mins := make([]MinifiedLibraryItem, len(pageSlice))
		for i, it := range pageSlice {
			mins[i] = Minify(it)
		}
		results = mins
	} else {
		results = pageSlice
	}

	writeJSON(w, http.StatusOK, pagedEnvelope(results, total, limit, page, sortBy, sortDesc, filterBy, minified, include))
}

func (h *Handler) handleItem(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	lib, backendBookID, encodedBookID, err := h.portalLibraryForBookRef(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "item not found", http.StatusNotFound)
		return
	}
	// Podcast libraries: backendBookID holds the podcast id directly
	// (we don't proxy through a backend plugin for podcasts), and the
	// detail comes from the plugin's own DB.
	if lib.MediaType == "podcast" {
		h.handlePodcastItem(w, r, lib, backendBookID, encodedBookID)
		return
	}
	if lib.BackendPluginID == "" {
		http.Error(w, "no backend configured", http.StatusPreconditionFailed)
		return
	}
	d, err := h.backend.GetDetail(r.Context(), a.Token, lib.BackendPluginID, backendBookID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	d = withPortalLibraryDetail(d, lib)
	// The contentUrl for each track in the item-detail response is
	// informational — ABS clients that follow the canonical flow POST
	// /api/items/{id}/play first to mint a session-scoped contentUrl
	// (see handlePlay below), which is what actually carries the
	// signed media token. The URL we emit here is origin-aware so a
	// client that pre-fetches against it lands on the right host, but
	// it points at the play-flow entry rather than at a raw stream
	// URL that wouldn't carry a token.
	baseURL := h.absBaseURL(r)
	contentURLFn := func(_ int) string {
		return baseURL + "/abs/api/items/" + encodedBookID + "/play"
	}
	item := ToLibraryItem(d, contentURLFn)
	item.ID = encodedBookID
	writeJSON(w, http.StatusOK, item)
}

// handleItemCover proxies cover bytes from the backend plugin rather than
// redirecting. Two reasons: (1) some ABS clients don't follow redirects on
// cover URLs (booklore-ng documents this with the same workaround at
// src/lib/audiobookshelf/cover-handler.ts:41), and (2) the backend plugin's
// cover endpoint lives under /api/v1/plugins/<install>/... on the continuum
// host; redirecting an ABS client connected to the standalone listener
// (e.g. abs.example.com) to that path would 404 because the standalone
// listener doesn't serve the host's /api/v1/plugins/* surface.
//
// Body is buffered into memory via HostClient.GetBinary; covers are typically
// well under maxResponseBytes (10 MiB), so this is fine. Audio streaming
// keeps its redirect-based flow — proxying multi-hundred-MB files through
// the plugin host would dwarf the cost of an extra config knob.
func (h *Handler) handleItemCover(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	lib, backendBookID, _, err := h.portalLibraryForBookRef(r.Context(), chi.URLParam(r, "id"))
	if err != nil || lib.BackendPluginID == "" {
		http.Error(w, "no backend configured", http.StatusPreconditionFailed)
		return
	}
	size := r.URL.Query().Get("size")
	body, contentType, err := h.backend.FetchCover(r.Context(), a.Token, lib.BackendPluginID, backendBookID, size)
	if err != nil {
		h.logger.Warn("abs cover fetch failed",
			"book_id", backendBookID, "size", size, "err", err.Error())
		http.Error(w, "cover unavailable", http.StatusBadGateway)
		return
	}
	if contentType == "" {
		contentType = "image/jpeg"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	_, _ = w.Write(body)
}

type playPayload struct {
	DeviceInfo  map[string]any `json:"deviceInfo"`
	MediaPlayer string         `json:"mediaPlayer"`
}

func (h *Handler) handlePlay(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	_, cfg, err := h.targetFn(r.Context())
	if err != nil {
		http.Error(w, "config unavailable", http.StatusInternalServerError)
		return
	}
	lib, backendBookID, encodedBookID, err := h.portalLibraryForBookRef(r.Context(), chi.URLParam(r, "id"))
	if err != nil || lib.BackendPluginID == "" {
		http.Error(w, "no backend configured", http.StatusPreconditionFailed)
		return
	}
	var p playPayload
	_ = json.NewDecoder(r.Body).Decode(&p)
	deviceID, _ := p.DeviceInfo["deviceId"].(string)
	if deviceID == "" {
		deviceID = "unknown"
	}
	d, err := h.backend.GetDetail(r.Context(), a.Token, lib.BackendPluginID, backendBookID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	sessionID := ulid.Make().String()
	sess := store.ABSSession{
		ID:          sessionID,
		UserID:      a.UserID,
		BookID:      encodedBookID,
		DeviceID:    deviceID,
		DeviceInfo:  p.DeviceInfo,
		MediaPlayer: p.MediaPlayer,
	}
	if err := h.store.InsertABSSession(r.Context(), sess); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Build session-scoped contentURL per track. The URL must resolve from
	// whichever origin the ABS client is connected to — standalone listener
	// or host plugin proxy — so we build an absolute URL based on the
	// inbound request's origin rather than the env-supplied hostBase, which
	// only describes the host's internal API URL.
	baseURL := h.absBaseURL(r)
	tracks := make([]AudioTrack, len(d.Files))
	for i, f := range d.Files {
		tok, _ := IssueSessionToken(cfg.ABSJWTSecret, a.UserID, sessionID, encodedBookID, f.Index, 6*time.Hour)
		trackURL := baseURL +
			"/abs/public/session/" + sessionID + "/track/" + strconv.Itoa(f.Index) +
			"?token=" + tok
		tracks[i] = AudioTrack{
			Index:      f.Index,
			ContentURL: trackURL,
			MimeType:   f.MimeType,
			Duration:   float64(f.DurationSeconds),
			Codec:      f.Format,
		}
	}
	h.publish(a.UserID, "user_session_open", map[string]any{
		"id":            sessionID,
		"libraryItemId": encodedBookID,
		"deviceId":      deviceID,
		"mediaPlayer":   p.MediaPlayer,
	})
	h.broadcastListenerCount(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"id":            sessionID,
		"libraryItemId": encodedBookID,
		"audioTracks":   tracks,
		"mediaPlayer":   p.MediaPlayer,
	})
}

type syncPayload struct {
	CurrentTime  float64 `json:"currentTime"`
	TimeListened float64 `json:"timeListened"`
}

func (h *Handler) handleSessionSync(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	sid := chi.URLParam(r, "sid")
	var p syncPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	// Ownership gate: a session id (ULID) must belong to the caller. Fetch
	// first and verify owner so a user can't update/seed-progress-from
	// another user's session (IDOR). 404 (not 403) so existence isn't
	// leaked.
	sess, err := h.store.GetABSSession(r.Context(), sid)
	if err != nil || sess.UserID != a.UserID {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	if err := h.store.UpdateABSSession(r.Context(), sid, int(p.CurrentTime)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Mirror only the position (sess.BookID is verified to belong to
	// a.UserID). Must NOT use Upsert*Progress here: it would write
	// is_finished=false / progress_pct=0 every sync tick and silently
	// un-finish a book/episode the user explicitly marked finished.
	//
	// Dispatch by sess.BookID prefix: "pe_..." sessions are podcast
	// episodes and write to podcast_episode_progress; everything else
	// is an audiobook session.
	if rawEpisodeID, isEpisode := DecodePodcastEpisodeID(sess.BookID); isEpisode {
		_ = h.store.UpdatePodcastEpisodeProgressPosition(r.Context(), a.UserID, rawEpisodeID, int(p.CurrentTime))
	} else {
		_ = h.store.UpdateProgressPosition(r.Context(), a.UserID, sess.BookID, int(p.CurrentTime))
	}

	// Push the new position to the user's other connected clients. We
	// publish a slim shape rather than re-fetching progress + serialising
	// — this is the hot path of the playback session and the consumer
	// only needs the moved-to time.
	// ABS clients pattern-match on the {data: ...} wrapper for this
	// event (audiobookshelf-app plugins/server.js:124). Other events
	// don't carry the wrapper; only user_item_progress_updated does.
	progressPayload := map[string]any{
		"libraryItemId": sess.BookID,
		"currentTime":   p.CurrentTime,
		"sessionId":     sid,
	}
	h.publish(a.UserID, "user_item_progress_updated", map[string]any{"data": progressPayload})
	// user_session_updated mirrors what real ABS emits on every session
	// tick. Some clients key off session events specifically (e.g. to
	// keep the "now playing" widget in sync with another device) — they
	// don't read user_item_progress_updated. Emit both.
	h.publish(a.UserID, "user_session_updated", map[string]any{
		"id":            sid,
		"libraryItemId": sess.BookID,
		"currentTime":   p.CurrentTime,
		"timeListened":  p.TimeListened,
	})

	resp := map[string]any{"ok": true}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleSessionClose(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	sid := chi.URLParam(r, "sid")
	// Only the owning user may close their session (IDOR guard). Admins use
	// the separate /admin path which intentionally closes any session.
	sess, err := h.store.GetABSSession(r.Context(), sid)
	if err != nil || sess.UserID != a.UserID {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	_ = h.store.CloseABSSession(r.Context(), sid)
	h.publish(a.UserID, "user_session_closed", map[string]any{
		"id":            sid,
		"libraryItemId": sess.BookID,
	})
	h.broadcastListenerCount(r.Context())
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
	_, cfg, err := h.targetFn(r.Context())
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
	// Bind the capability token to the session row: it must have been minted
	// for this session's user and book, not merely any valid session token.
	if claims.UserID != sess.UserID || (claims.BookID != "" && claims.BookID != sess.BookID) {
		http.Error(w, "invalid session token", http.StatusUnauthorized)
		return
	}
	lib, backendBookID, _, err := h.portalLibraryForBookRef(r.Context(), sess.BookID)
	if err != nil || lib.BackendPluginID == "" {
		http.Error(w, "no backend configured", http.StatusPreconditionFailed)
		return
	}
	// Proxy audio bytes from the backend rather than redirecting. ABS
	// clients connected via the standalone listener cannot follow a
	// redirect into /api/v1/plugins/<install>/... (that path lives on the
	// continuum host, not on the standalone listener's origin). Proxying
	// the bytes ourselves means the stream stays on the listener the
	// client is already talking to.
	//
	// We still mint the signed media token — the backend's stream route
	// validates ?token= without consulting the host's plugin proxy auth,
	// which keeps the byte path public-routable from the backend's
	// manifest perspective. The signature is bound to (userID, bookID,
	// fileIdx) and expires in 15 minutes, so a leaked URL stops working
	// quickly even though the audio response is large.
	if cfg.MediaSigningSecret == "" {
		http.Error(w, "media signing not configured", http.StatusServiceUnavailable)
		return
	}
	mediaTok, err := mediatoken.Mint(cfg.MediaSigningSecret, sess.UserID, backendBookID, idx)
	if err != nil {
		http.Error(w, "mint media token", http.StatusInternalServerError)
		return
	}
	backendPath := "/api/v1/stream/" + neturl.PathEscape(backendBookID) + "/" + strconv.Itoa(idx) +
		"?token=" + neturl.QueryEscape(mediaTok)

	// Forward the inbound Range header so the ABS client can seek inside
	// the audiobook file. The backend's stream route honors Range; we just
	// pass it through. If-Match / If-None-Match / If-Modified-Since are
	// also passed through for caching correctness (most ABS clients don't
	// send them but they're cheap to forward).
	hdrs := map[string]string{}
	for _, h := range []string{"Range", "If-Match", "If-None-Match", "If-Modified-Since"} {
		if v := r.Header.Get(h); v != "" {
			hdrs[h] = v
		}
	}

	resp, err := h.backend.HostClient().GetStream(r.Context(), "", lib.BackendPluginID, backendPath, hdrs)
	if err != nil {
		h.logger.Warn("abs stream proxy: upstream error",
			"book_id", backendBookID, "file_idx", idx, "err", err.Error())
		http.Error(w, "stream unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Pass through everything that matters for byte serving + caching. We
	// don't blindly copy resp.Header because that would include hop-by-hop
	// headers (Transfer-Encoding etc.) that Go's response writer manages
	// itself.
	for _, h := range []string{
		"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges",
		"ETag", "Last-Modified", "Cache-Control",
	} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		// Common when the client closes the connection mid-playback
		// (skip-forward, app backgrounded). Debug-level only.
		h.logger.Debug("abs stream proxy: copy ended",
			"book_id", backendBookID, "file_idx", idx, "err", err.Error())
	}
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
	lib, err := h.portalLibraryFromABSID(r.Context(), chi.URLParam(r, "id"))
	if err != nil || lib.BackendPluginID == "" {
		http.Error(w, "no backend configured", http.StatusPreconditionFailed)
		return
	}
	limit, page := readPagedQuery(r, 50)
	out, err := h.backend.BrowseAuthors(r.Context(), a.Token, lib.BackendPluginID, backend.ListParams{Limit: limit, LibraryID: backendLibraryID(lib)})
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
			"libraryId": absLibraryID(lib),
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
	lib, err := h.portalLibraryFromABSID(r.Context(), chi.URLParam(r, "id"))
	if err != nil || lib.BackendPluginID == "" {
		http.Error(w, "no backend configured", http.StatusPreconditionFailed)
		return
	}
	limit, page := readPagedQuery(r, 25)
	out, err := h.backend.BrowseSeries(r.Context(), a.Token, lib.BackendPluginID, backend.ListParams{Limit: limit, LibraryID: backendLibraryID(lib)})
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
			"libraryId": absLibraryID(lib),
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

// handleLibrarySearch mirrors ABS GET /api/libraries/{id}/search?q=&limit=.
// Real ABS returns a multi-bucket object — clients render the buckets in
// separate sections on the search-results screen, so a flat items array
// would silently break their layout.
//
// Buckets:
//
//	book    — [{libraryItem, matchKey, matchText}]
//	podcast — [{libraryItem, matchKey, matchText}] (podcasts only)
//	series  — [{series, books[]}]
//	authors — [{id, name, ...}]
//	tags    — [string]            (we don't surface tags yet; empty array)
//
// The matchKey is the field that matched (title/author/series/narrator);
// matchText is the highlighted excerpt to render. We fill matchKey with
// "title" for now since the backend's ListCatalog?q= performs a fulltext
// match without breakdown — a follow-up can widen this to per-field
// indication once the backend exposes which field caused the hit.
func (h *Handler) handleLibrarySearch(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	lib, err := h.portalLibraryFromABSID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "library not found", http.StatusNotFound)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := 12
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	empty := map[string]any{
		"book":    []any{},
		"podcast": []any{},
		"series":  []any{},
		"authors": []any{},
		"tags":    []any{},
	}
	if q == "" {
		writeJSON(w, http.StatusOK, empty)
		return
	}

	// Podcast libraries: search the plugin's own podcast table.
	if lib.MediaType == "podcast" {
		out := empty
		podcasts, err := h.store.ListPodcasts(r.Context(), lib.ID, 0)
		if err == nil {
			needle := strings.ToLower(q)
			hits := make([]map[string]any, 0)
			for _, p := range podcasts {
				if len(hits) >= limit {
					break
				}
				if !strings.Contains(strings.ToLower(p.Title), needle) &&
					!strings.Contains(strings.ToLower(p.Author), needle) {
					continue
				}
				encoded := bookref.Encode(p.LibraryID, p.ID)
				hits = append(hits, map[string]any{
					"libraryItem": ToPodcastSummary(p, 0, encoded),
					"matchKey":    "title",
					"matchText":   p.Title,
				})
			}
			out["podcast"] = hits
		}
		writeJSON(w, http.StatusOK, out)
		return
	}

	if lib.BackendPluginID == "" {
		writeJSON(w, http.StatusOK, empty)
		return
	}
	out := empty

	// Books — fulltext q against the backend catalog.
	catalog, err := h.backend.ListCatalog(r.Context(), a.Token, lib.BackendPluginID, backend.ListParams{
		Query: q, Limit: limit, LibraryID: backendLibraryID(lib),
	})
	if err == nil {
		hits := make([]map[string]any, 0, len(catalog.Items))
		for _, s := range catalog.Items {
			hits = append(hits, map[string]any{
				"libraryItem": ToLibrarySummary(withPortalLibrarySummary(s, lib)),
				"matchKey":    "title",
				"matchText":   s.Title,
			})
		}
		out["book"] = hits
	} else {
		h.logger.Warn("search: catalog query", "q", q, "err", err.Error())
	}

	// Series — substring match on the browse-series result. Real ABS
	// emits each hit with the matching books inlined; we mirror the
	// shape but leave books[] empty pending a backend "series_detail"
	// endpoint (audiobook_backend.v1 doesn't expose series→books).
	if series, err := h.backend.BrowseSeries(r.Context(), a.Token, lib.BackendPluginID, backend.ListParams{
		Limit: 500, LibraryID: backendLibraryID(lib),
	}); err == nil {
		needle := strings.ToLower(q)
		hits := make([]map[string]any, 0)
		for _, s := range series.Items {
			if len(hits) >= limit {
				break
			}
			if !strings.Contains(strings.ToLower(s.Name), needle) {
				continue
			}
			hits = append(hits, map[string]any{
				"series": map[string]any{
					"id":        s.ID,
					"name":      s.Name,
					"numBooks":  s.Count,
					"libraryId": absLibraryID(lib),
				},
				"books": []any{},
			})
		}
		out["series"] = hits
	}

	// Authors — substring match on the browse-authors result.
	if authors, err := h.backend.BrowseAuthors(r.Context(), a.Token, lib.BackendPluginID, backend.ListParams{
		Limit: 500, LibraryID: backendLibraryID(lib),
	}); err == nil {
		needle := strings.ToLower(q)
		hits := make([]map[string]any, 0)
		for _, s := range authors.Items {
			if len(hits) >= limit {
				break
			}
			if !strings.Contains(strings.ToLower(s.Name), needle) {
				continue
			}
			hits = append(hits, map[string]any{
				"id":        s.ID,
				"name":      s.Name,
				"numBooks":  s.Count,
				"libraryId": absLibraryID(lib),
			})
		}
		out["authors"] = hits
	}

	writeJSON(w, http.StatusOK, out)
}

// readPagedQuery parses the ABS ?limit=&page= query, falling back to the
// supplied default limit.
//
// Special case: real ABS treats limit=0 as "return everything, no pagination",
// NOT as "return zero rows". A client sending limit=0 explicitly asks for the
// full result set. We surface that intent by returning limit=0 from this
// helper and let the caller short-circuit pagination.
func readPagedQuery(r *http.Request, defaultLimit int) (limit, page int) {
	limit = defaultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
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

// pagedEnvelope builds the full ABS pagination response shape. Real ABS
// emits eight fields on every paged endpoint; clients that only read
// `results` and `total` ignore the rest, but clients that read `filterBy`
// (to render selected filter chips) or `sortBy`/`sortDesc` (to show the
// active sort) silently break when those fields are absent. Centralising
// the shape here means every paged handler gets the same envelope.
func pagedEnvelope(results any, total, limit, page int, sortBy string, sortDesc bool, filterBy string, minified bool, include string) map[string]any {
	return map[string]any{
		"results":  results,
		"total":    total,
		"limit":    limit,
		"page":     page,
		"sortBy":   sortBy,
		"sortDesc": sortDesc,
		"filterBy": filterBy,
		"minified": minified,
		"include":  include,
	}
}

// ---------- Personalized shelves ----------

// handlePersonalized returns the 6 standard ABS home-screen shelves. Each
// shelf carries up to `limit` (default 10) entities; if there's no data
// for a shelf (no progress yet, no backend) the shelf is emitted with an
// empty entities array — clients iterate the shelf list, so omitting a
// shelf breaks the layout.
func (h *Handler) handlePersonalized(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	lib, err := h.portalLibraryFromABSID(r.Context(), chi.URLParam(r, "id"))
	if err != nil || lib.BackendPluginID == "" {
		writeJSON(w, http.StatusOK, []map[string]any{})
		return
	}
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
		progressLibraryID, backendBookID, encoded := bookref.Decode(p.BookID)
		if encoded && progressLibraryID != lib.ID {
			continue
		}
		if !encoded && lib.ID > 0 {
			continue
		}
		if p.IsFinished {
			if len(againPaths) >= limit {
				continue
			}
			if d, derr := h.backend.GetDetail(r.Context(), a.Token, lib.BackendPluginID, backendBookID); derr == nil {
				againPaths = append(againPaths, ToLibraryItem(withPortalLibraryDetail(d, lib), func(int) string { return "" }))
			}
			continue
		}
		if p.ProgressPct <= 0 {
			continue
		}
		if len(contPaths) >= limit {
			continue
		}
		if d, derr := h.backend.GetDetail(r.Context(), a.Token, lib.BackendPluginID, backendBookID); derr == nil {
			contPaths = append(contPaths, ToLibraryItem(withPortalLibraryDetail(d, lib), func(int) string { return "" }))
		}
	}
	shelves[0]["entities"] = contPaths
	shelves[5]["entities"] = againPaths

	// newest + discover come from the catalog list. We sort by added_at
	// desc when the backend supports it. For "discover" we exclude items
	// the user already has progress on.
	progressBookIDs := map[string]bool{}
	for _, p := range progRows {
		_, backendBookID, _ := bookref.Decode(p.BookID)
		progressBookIDs[backendBookID] = true
	}
	listOut, err := h.backend.ListCatalog(r.Context(), a.Token, lib.BackendPluginID, backend.ListParams{Limit: limit * 3, Sort: "added", Order: "desc", LibraryID: backendLibraryID(lib)})
	if err != nil {
		h.logger.Warn("personalized: list catalog", "err", err)
	} else {
		newest := make([]LibraryItem, 0, limit)
		discover := make([]LibraryItem, 0, limit)
		for _, s := range listOut.Items {
			s = withPortalLibrarySummary(s, lib)
			if len(newest) < limit {
				newest = append(newest, ToLibrarySummary(s))
			}
			_, backendBookID, _ := bookref.Decode(s.ID)
			if !progressBookIDs[backendBookID] && len(discover) < limit {
				discover = append(discover, ToLibrarySummary(s))
			}
		}
		shelves[2]["entities"] = newest
		shelves[4]["entities"] = discover
	}

	// recent-series: take the first N series the backend returns. Each
	// entity is a thin series object; clients render the name + cover.
	if seriesOut, err := h.backend.BrowseSeries(r.Context(), a.Token, lib.BackendPluginID, backend.ListParams{Limit: limit, LibraryID: backendLibraryID(lib)}); err == nil {
		recent := make([]map[string]any, 0, len(seriesOut.Items))
		for _, s := range seriesOut.Items {
			recent = append(recent, map[string]any{
				"id":        s.ID,
				"name":      s.Name,
				"numBooks":  s.Count,
				"libraryId": absLibraryID(lib),
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
	// Dispatch by id shape: "pe_<...>" routes to the per-episode progress
	// table; everything else is an audiobook progress lookup. The prefix
	// is stripped before the store call — stored episode ids are bare.
	if rawEpisodeID, isEpisode := DecodePodcastEpisodeID(itemID); isEpisode {
		p, err := h.store.GetPodcastEpisodeProgress(r.Context(), a.UserID, rawEpisodeID)
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "progress not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, podcastProgressToABS(a.UserID, p))
		return
	}
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
	// Podcast progress goes to the per-episode table; same merge
	// semantics as audiobook progress so a sync tick doesn't un-finish
	// a manually-finished episode.
	if rawEpisodeID, isEpisode := DecodePodcastEpisodeID(itemID); isEpisode {
		cur, err := h.store.GetPodcastEpisodeProgress(r.Context(), a.UserID, rawEpisodeID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		next := makePodcastProgress(a.UserID, rawEpisodeID, cur, body)
		if err := h.store.UpsertPodcastEpisodeProgress(r.Context(), next); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		updated, err := h.store.GetPodcastEpisodeProgress(r.Context(), a.UserID, rawEpisodeID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		payload := podcastProgressToABS(a.UserID, updated)
		h.publish(a.UserID, "user_item_progress_updated", map[string]any{"data": payload})
		writeJSON(w, http.StatusOK, payload)
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
	payload := progressToABS(a.UserID, updated)
	// Realtime push so other devices on the same account update without a
	// poll. Event name + {data} wrapper match the real-ABS Socket.io
	// convention so the official mobile/web clients react without any
	// client-side changes.
	h.publish(a.UserID, "user_item_progress_updated", map[string]any{"data": payload})
	writeJSON(w, http.StatusOK, payload)
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
