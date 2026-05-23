package abs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	runtimehost "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginsdk/runtimehost"

	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/backend"
	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/bookref"
	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/mediatoken"
	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/store"
	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/streaming"
)

// Handler wires the /abs/api/* and /abs/public/* surface.
type Handler struct {
	store         *store.Store
	backend       *backend.Client
	streaming     *streaming.Router
	logger        Logger
	targetFn      func(ctx context.Context) (string, store.BackendConfig, error)
	hostBaseFn    func() string
	installID     func() string // current plugin install ID for building public URLs
	credValidator ProfileCredentialValidator
	loginLimiter  *LoginLimiter
	publisher     EventPublisher
	recommender   Recommender
}

// Recommender is the narrow surface the ABS handler uses to fetch
// similar-items results. Implemented by recommend.Engine; surfaced as
// an interface to keep the abs package decoupled from pgvector.
type Recommender interface {
	Similar(ctx context.Context, libraryID int64, bookID string, limit int) ([]store.SimilarAudiobook, error)
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

// ProfileCredentialValidator resolves a third-party "user#profile" /
// "password#pin" login against the Continuum host. Implemented by the
// SDK runtimehost client; an interface so tests can stub it.
type ProfileCredentialValidator interface {
	ValidateProfileCredential(ctx context.Context, username, password string) (*runtimehost.ProfileCredential, error)
}

// Logger is a minimal interface to keep Handler decoupled from hclog.
type Logger interface {
	Warn(msg string, args ...any)
	Info(msg string, args ...any)
	Debug(msg string, args ...any)
}

// noopLogger is the default.
type noopLogger struct{}

func (noopLogger) Warn(string, ...any)  {}
func (noopLogger) Info(string, ...any)  {}
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
	// CredValidator resolves standalone-port body credentials against the
	// Continuum host via the ValidateProfileCredential RPC. May be nil;
	// when nil, the body-creds login path returns 503 regardless of admin
	// mode.
	CredValidator ProfileCredentialValidator
	// LoginLimiter throttles standalone-port body-creds login attempts per
	// source IP. Construct one per process (its janitor is a long-lived
	// goroutine) and share it across plugin reconfigures.
	LoginLimiter *LoginLimiter
	// Publisher pushes realtime events to ABS Socket.io clients. May be
	// nil — calls into it short-circuit, so the REST surface keeps
	// working when the realtime hub isn't wired (tests, host-proxied
	// flows where /socket.io isn't reachable).
	Publisher EventPublisher
	// Recommender powers the /items/{id}/similar endpoint. nil means
	// embeddings aren't wired (no EMBEDDING_BASE_URL env) and the
	// route returns an empty list.
	Recommender Recommender
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
		credValidator: d.CredValidator,
		loginLimiter:  lim,
		publisher:     d.Publisher,
		recommender:   d.Recommender,
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
// Mount registers the ABS-compatible routes at both the legacy
// /abs/api/* prefix (used by host-proxied callers and our own SPA)
// and the upstream-canonical paths the official ABS clients build
// against. Mainline mobile + web clients construct URLs as
// `${serverAddress}/login`, `${serverAddress}/api/libraries`, etc. —
// no /abs/api prefix — so without the root-mount the apps cannot
// even reach the login endpoint.
//
// The dual mount means an operator can point the official ABS app
// directly at `https://abs.example.com` (no path prefix) and have
// every endpoint resolve, while our internal SPA continues to call
// /abs/api/* on the same listener.
func (h *Handler) Mount(parent chi.Router) {
	// Wrap all ABS routes in their own inline group so the access-log
	// middleware applies only to ABS traffic. r.Use() can't be called on
	// the parent because server.Handler() may have already mounted other
	// routes on it — chi panics with "all middlewares must be defined
	// before routes on a mux" if a route is registered before Use(). The
	// Group idiom creates a fresh inline mux that inherits the parent's
	// path space, so our middleware stack is independent.
	parent.Group(func(r chi.Router) {
		// Temporary access log while we debug mobile playback. Path-only;
		// no query string so ?token= never lands in logs. Demote / remove
		// once playback is settled.
		r.Use(h.accessLog)
		h.mountRoutes(r)
	})
}

func (h *Handler) mountRoutes(r chi.Router) {
	// Auth + probe routes: real ABS puts these at server ROOT (no /api
	// prefix). Mobile app does `${serverAddress}/login`,
	// `${serverAddress}/status`, etc.
	for _, prefix := range []string{"/abs/api", ""} {
		r.Get(prefix+"/ping", h.handlePing)
		r.Get(prefix+"/healthcheck", h.handlePing)
		r.Get(prefix+"/init", h.handleInit)
		r.Get(prefix+"/status", h.handleStatus)
		r.Post(prefix+"/login", h.handleLogin)
		r.Post(prefix+"/auth/refresh", h.handleRefresh)
		r.Post(prefix+"/logout", h.handleLogout)
	}
	// Also expose the legacy /abs/api/auth/logout shape we shipped
	// originally — third-party tooling may still call it.
	r.Post("/abs/api/auth/logout", h.handleLogout)

	// Unauthenticated cover route. Real ABS server has served covers
	// without auth since 2.17 (we report 2.35.0), so the mobile client
	// builds <serverAddress>/api/items/<id>/cover with NO query token
	// and NO Authorization header — `getDoesServerImagesRequireToken`
	// returns false for our version. Mounting it inside bearerAuth
	// returns 401 and the client shows the placeholder.
	// Ref: /opt/audiobookshelf-app/store/index.js:95
	for _, prefix := range []string{"/abs/api", "/api"} {
		r.Get(prefix+"/items/{id}/cover", h.handleItemCover)
		// Author images share the same unauthenticated contract as
		// item covers — the mobile AuthorImage.vue:65-67 doesn't
		// attach a token. We don't have author image data yet, so
		// the handler returns a clean 404 and the client renders the
		// placeholder.
		r.Get(prefix+"/authors/{id}/image", h.handleAuthorImage)
	}

	// Bearer-authenticated routes: real ABS puts these under /api. Our
	// legacy /abs/api/* prefix and the new /api/* prefix both work.
	r.Group(func(r chi.Router) {
		r.Use(h.bearerAuth)
		for _, prefix := range []string{"/abs/api", "/api"} {
			r.Post(prefix+"/authorize", h.handleAuthorize)
			r.Get(prefix+"/me", h.handleMe)
			r.Get(prefix+"/libraries", h.handleLibraries)
			r.Get(prefix+"/libraries/{id}", h.handleLibraryDetail)
			r.Get(prefix+"/libraries/{id}/items", h.handleLibraryItems)
			r.Get(prefix+"/libraries/{id}/authors", h.handleLibraryAuthors)
			r.Get(prefix+"/libraries/{id}/series", h.handleLibrarySeries)
			r.Get(prefix+"/libraries/{id}/search", h.handleLibrarySearch)
			r.Get(prefix+"/libraries/{id}/personalized", h.handlePersonalized)
			r.Get(prefix+"/items/{id}", h.handleItem)
			r.Get(prefix+"/items/{id}/similar", h.handleSimilarItems)
			r.Post(prefix+"/items/{id}/play", h.handlePlay)
			r.Post(prefix+"/items/{id}/play/{episodeId}", h.handlePlayEpisode)
			// File download / streaming — the mobile + web clients use
			// these for offline saves and iOS audio streaming. ?token=
			// in the URL is the access JWT (not a media token); the
			// bearerAuth middleware accepts it via the query fallback.
			r.Get(prefix+"/items/{id}/file/{ino}", h.handleItemFile)
			r.Get(prefix+"/items/{id}/file/{ino}/download", h.handleItemFile)
			r.Get(prefix+"/me/progress/{itemId}", h.handleGetProgress)
			r.Patch(prefix+"/me/progress/{itemId}", h.handlePatchProgress)
			r.Delete(prefix+"/me/progress/{itemId}", h.handleDeleteProgress)
			// GET (not DELETE) on these — they toggle a visibility flag,
			// not the underlying progress. Mirror upstream exactly.
			r.Get(prefix+"/me/progress/{itemId}/remove-from-continue-listening", h.handleHideFromContinue)
			r.Get(prefix+"/me/progress/{itemId}/readd-to-continue-listening", h.handleUnhideFromContinue)
			r.Get(prefix+"/me/items-in-progress", h.handleItemsInProgress)
			// Aggregate listening stats — the mobile stats page and the
			// year-in-review screen both rely on these. Sourced from
			// abs_playback_session.time_listening_seconds (migration 0037).
			r.Get(prefix+"/me/listening-stats", h.handleListeningStats)
			r.Get(prefix+"/me/stats/year/{year}", h.handleYearStats)
			// Author / series detail. Real ABS exposes these as
			// per-id endpoints; we synthesize from the backend's
			// browse endpoints + a catalog filter pushdown.
			r.Get(prefix+"/authors/{id}", h.handleAuthorDetail)
			r.Get(prefix+"/series/{id}", h.handleSeriesDetail)
			r.Post(prefix+"/me/item/{itemId}/bookmark", h.handleCreateBookmark)
			r.Patch(prefix+"/me/item/{itemId}/bookmark", h.handleUpdateBookmark)
			r.Delete(prefix+"/me/item/{itemId}/bookmark/{time}", h.handleDeleteBookmark)
			r.Patch(prefix+"/session/{sid}", h.handleSessionSync)
			r.Post(prefix+"/session/{sid}/close", h.handleSessionClose)
			h.mountSmartCollectionRoutes(prefix, r)
			h.mountCollectionsRoutes(prefix, r)
			h.mountPlaylistsRoutes(prefix, r)
			h.mountRSSFeedRoutes(prefix, r)
		}
	})

	// Public routes — session token in query is the capability.
	// Real ABS keeps these at /public/ (no /api prefix); we mount both.
	for _, prefix := range []string{"/abs/public", "/public"} {
		r.Get(prefix+"/session/{sid}/track/{idx}", h.handlePublicTrack)
	}
	// RSS feeds — slug in path is the capability.
	h.MountPublicFeed(r)
}

// ctxKey is the ABS auth context key.
type ctxKey struct{}

type ctxAuth struct {
	UserID    string
	ProfileID string
	JTI       string
	Token     string
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
			UserID:    claims.UserID,
			ProfileID: claims.ProfileID,
			JTI:       claims.JTI,
			Token:     raw,
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
		"success": true,
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
		"app":           "audiobookshelf",
		"authMethods":   []string{"local"},
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
//   - "all_accounts": body-creds works for any account the host's local
//     provider accepts.
//
// A per-IP rate limiter throttles body-creds attempts. The host-proxied
// branch is never rate-limited.
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	// Path A — host-proxied: the host stamped identity headers.
	if userID := r.Header.Get("X-Continuum-User-Id"); userID != "" {
		profileID := r.Header.Get("X-Continuum-Profile-Id") // empty = primary
		name := r.Header.Get("X-Continuum-Profile-Name")
		if name == "" {
			name = r.Header.Get("X-Continuum-User-Name")
		}
		h.completeLogin(w, r, userID, profileID, name)
		return
	}
	// Path B — standalone port: validate body credentials via the host RPC.
	h.handleStandaloneLogin(w, r)
}

// handleStandaloneLogin runs the body-creds path when no host-proxied
// identity header is present. Credentials are resolved against the
// Continuum host's ValidateProfileCredential RPC, which owns all
// "user#profile" / "password#pin" parsing and verification.
func (h *Handler) handleStandaloneLogin(w http.ResponseWriter, r *http.Request) {
	_, cfg, err := h.targetFn(r.Context())
	if err != nil {
		http.Error(w, "config unavailable", http.StatusInternalServerError)
		return
	}
	if store.NormalizeStandaloneLoginMode(cfg.StandaloneLoginMode) == store.StandaloneLoginModeDisabled {
		http.Error(w, "standalone login is disabled on this server", http.StatusUnauthorized)
		return
	}
	if h.credValidator == nil {
		h.logger.Warn("abs.standalone_login: no credential validator configured")
		http.Error(w, "standalone login is unavailable in this deployment", http.StatusServiceUnavailable)
		return
	}
	ip := clientIP(r)
	if !h.loginLimiter.allow(ip) {
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
	// The plugin never parses '#': the host owns username#profile /
	// password#pin parsing inside ValidateProfileCredential.
	cred, err := h.credValidator.ValidateProfileCredential(r.Context(), body.Username, body.Password)
	if err != nil {
		switch status.Code(err) {
		case codes.Unauthenticated:
			h.logger.Warn("abs.standalone_login: invalid credentials", "ip", ip, "username", body.Username)
			http.Error(w, "invalid username or password", http.StatusUnauthorized)
		default:
			h.logger.Warn("abs.standalone_login: validator error", "ip", ip, "err", err.Error())
			http.Error(w, "login service unavailable", http.StatusServiceUnavailable)
		}
		return
	}
	// Display name: the profile portion of the typed username (after '#'),
	// else the whole username.
	displayName := body.Username
	if i := strings.LastIndexByte(displayName, '#'); i >= 0 && i < len(displayName)-1 {
		displayName = displayName[i+1:]
	}
	h.completeLogin(w, r, cred.UserID, cred.ProfileID, displayName)
}

// AbsServerSettings is the serverSettings envelope ABS clients read on
// login. Modelled on the upstream shape so clients that branch on these
// fields (auth method list, OpenID flags, view prefs) behave correctly.
func AbsServerSettings() map[string]any {
	return map[string]any{
		"id":                         "server-settings",
		"version":                    ServerVersion,
		"language":                   "en-us",
		"buildNumber":                1,
		"chromecastEnabled":          false,
		"dateFormat":                 "MM/dd/yyyy",
		"timeFormat":                 "HH:mm",
		"homeBookshelfView":          1,
		"bookshelfView":              1,
		"sortingIgnorePrefix":        false,
		"sortingPrefixes":            []string{"the", "a"},
		"rateLimitLoginRequests":     10,
		"rateLimitLoginWindow":       600000,
		"allowIframe":                false,
		"authActiveAuthMethods":      []string{"local"},
		"authOpenIDAutoLaunch":       false,
		"authOpenIDAutoRegister":     false,
		"authOpenIDButtonText":       "Login with OpenId",
		"authOpenIDIssuerURL":        nil,
		"authOpenIDAuthorizationURL": nil,
		"authOpenIDTokenURL":         nil,
		"authOpenIDUserInfoURL":      nil,
		"authOpenIDJwksURL":          nil,
		"authOpenIDLogoutURL":        nil,
	}
}

// absUserObject builds the ABS `user` envelope shared by /login,
// /authorize and /me. mediaProgress is hydrated from the user's recent
// progress rows so clients show resume positions without a second call.
func (h *Handler) absUserObject(ctx context.Context, userID, profileID, displayName, defaultLibraryID string) map[string]any {
	progress := make([]map[string]any, 0)
	if h.store != nil {
		rows, _ := h.store.ListRecentProgress(ctx, userID, profileID, 200)
		for _, p := range rows {
			progress = append(progress, progressToABS(userID, p))
		}
	}
	// librariesAccessible drives the mobile app's library picker
	// (audiobookshelf-app/layouts/default.vue:198). Empty made the
	// client either hide everything or fall through to a degraded
	// "show all" path. Emit the enabled portal libraries' ABS ids.
	libs := h.portalLibraries(ctx, true)
	libraryIDs := make([]string, 0, len(libs))
	for _, lib := range libs {
		libraryIDs = append(libraryIDs, absLibraryID(lib))
	}
	// bookmarks seeds the mobile app's bookmark store so they appear
	// without an extra per-item GET. Shape matches the per-item
	// writeBookmarkList output. Best-effort: an error returns an empty
	// list rather than failing the login/authorize flow.
	bookmarks := make([]map[string]any, 0)
	if h.store != nil {
		rows, _ := h.store.ListRecentBookmarksForUser(ctx, userID, profileID, 200)
		for _, b := range rows {
			bookmarks = append(bookmarks, map[string]any{
				"libraryItemId": b.BookID,
				"title":         b.Note,
				"time":          b.PositionSeconds,
				"createdAt":     b.CreatedAt.UnixMilli(),
			})
		}
	}
	name := displayName
	if name == "" {
		name = userID
	}
	return map[string]any{
		"id":                  userID,
		"username":            name,
		"type":                "user",
		"defaultLibraryId":    defaultLibraryID,
		"librariesAccessible": libraryIDs,
		"mediaProgress":       progress,
		"bookmarks":           bookmarks,
		"isOldToken":          false,
		"permissions": map[string]any{
			"update":                true,
			"delete":                true,
			"download":              true,
			"accessExplicitContent": true,
		},
	}
}

// completeLogin mints ABS access + refresh JWTs for the validated user and
// writes the login response. Shared by both the header path and the
// body-creds path so the response shape is identical.
func (h *Handler) completeLogin(w http.ResponseWriter, r *http.Request, userID, profileID, displayName string) {
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
	access, err := IssueAccessToken(cfg.ABSJWTSecret, userID, profileID, accessJTI, accessTTL)
	if err != nil {
		http.Error(w, "mint access: "+err.Error(), http.StatusInternalServerError)
		return
	}
	refresh, err := IssueRefreshToken(cfg.ABSJWTSecret, userID, profileID, refreshJTI, refreshTTL)
	if err != nil {
		http.Error(w, "mint refresh: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.store.InsertABSToken(r.Context(), store.ABSToken{
		ID:        accessJTI,
		UserID:    userID,
		ProfileID: profileID,
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
		ProfileID: profileID,
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
	// Real ABS clients pattern-match on a much richer login envelope —
	// see the audiobookshelf-app review for the full field list. Some
	// fields are best-effort (we don't have a real "permissions" surface
	// yet, but emitting the canonical {update, delete, download,
	// accessExplicitContent} shape with defaults keeps the app from
	// branching into degraded mode).
	//
	// x-return-tokens header opt-in: when present, include accessToken
	// and refreshToken on the user object too (some clients read them
	// from there, others from the top-level fields — emitting both
	// covers every mainline reader).
	returnTokens := strings.EqualFold(r.Header.Get("x-return-tokens"), "true")
	user := h.absUserObject(r.Context(), userID, profileID, displayName, defaultLibraryID)
	user["token"] = access // legacy field some 2.17- clients still read
	if returnTokens {
		user["accessToken"] = access
		user["refreshToken"] = refresh
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":                 user,
		"userDefaultLibraryId": defaultLibraryID,
		"serverSettings":       AbsServerSettings(),
		"ereaderDevices":       []any{},
		// Legacy top-level token fields for clients that read them
		// directly (mainline web reads from the user object; some
		// third-party clients still read top-level).
		"accessToken":  access,
		"refreshToken": refresh,
		"libraries":    libraryMaps,
	})
}

// handleAuthorize — POST /abs/api/authorize
// The re-auth handshake mobile clients call on launch with their stored
// access token. Returns the same shape as /login except WITHOUT the new
// token pair (the client already has them). Without this endpoint the
// app can't recover an existing session and re-prompts for credentials
// every launch.
func (h *Handler) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
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
	user := h.absUserObject(r.Context(), a.UserID, a.ProfileID, "", defaultLibraryID)
	writeJSON(w, http.StatusOK, map[string]any{
		"user":                 user,
		"userDefaultLibraryId": defaultLibraryID,
		"serverSettings":       AbsServerSettings(),
		"ereaderDevices":       []any{},
		"libraries":            libraryMaps,
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
	// Real ABS clients send the refresh token via the x-refresh-token
	// header with an empty body. Our pre-existing contract accepted
	// {refreshToken} in the JSON body; we keep that for back-compat
	// but prefer the header when both are present.
	refreshTok := strings.TrimSpace(r.Header.Get("x-refresh-token"))
	if refreshTok == "" {
		var p refreshPayload
		if err := json.NewDecoder(r.Body).Decode(&p); err == nil {
			refreshTok = p.RefreshToken
		}
	}
	if refreshTok == "" {
		http.Error(w, "refreshToken required", http.StatusBadRequest)
		return
	}
	_, cfg, err := h.targetFn(r.Context())
	if err != nil {
		http.Error(w, "config unavailable", http.StatusInternalServerError)
		return
	}
	claims, err := ParseToken(cfg.ABSJWTSecret, refreshTok)
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
	access, err := IssueAccessToken(cfg.ABSJWTSecret, claims.UserID, claims.ProfileID, newAccessJTI, accessTTL)
	if err != nil {
		http.Error(w, "token mint failed", http.StatusInternalServerError)
		return
	}
	refresh, err := IssueRefreshToken(cfg.ABSJWTSecret, claims.UserID, claims.ProfileID, newRefreshJTI, refreshTTL)
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
	// Real ABS refresh response shape is {user:{accessToken, refreshToken}}
	// — NOT a flat token pair. Emit both forms for client compatibility:
	// mainline app reads from user{}, third-party readers may read from
	// the top level.
	writeJSON(w, http.StatusOK, map[string]any{
		"user": map[string]any{
			"id":           claims.UserID,
			"accessToken":  access,
			"refreshToken": refresh,
		},
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
	writeJSON(w, http.StatusOK, h.absUserObject(r.Context(), a.UserID, a.ProfileID, "", absLibraryID(lib)))
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
	if authors, err := h.backend.BrowseAuthors(r.Context(), "", lib.BackendPluginID, params); err == nil {
		refs := make([]AuthorObj, 0, len(authors.Items))
		for _, s := range authors.Items {
			refs = append(refs, AuthorObj{ID: s.ID, Name: s.Name})
		}
		out["authors"] = refs
	} else {
		h.logger.Warn("filterdata: browse authors", "err", err)
	}
	if series, err := h.backend.BrowseSeries(r.Context(), "", lib.BackendPluginID, params); err == nil {
		refs := make([]SeriesObj, 0, len(series.Items))
		for _, s := range series.Items {
			refs = append(refs, SeriesObj{ID: s.ID, Name: s.Name})
		}
		out["series"] = refs
	} else {
		h.logger.Warn("filterdata: browse series", "err", err)
	}
	if narrators, err := h.backend.BrowseNarrators(r.Context(), "", lib.BackendPluginID, params); err == nil {
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

	out, err := h.backend.ListCatalog(r.Context(), "", lib.BackendPluginID, p)
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
	d, err := h.backend.GetDetail(r.Context(), "", lib.BackendPluginID, backendBookID)
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
	lib, backendBookID, _, err := h.portalLibraryForBookRef(r.Context(), chi.URLParam(r, "id"))
	if err != nil || lib.BackendPluginID == "" {
		http.Error(w, "no backend configured", http.StatusPreconditionFailed)
		return
	}
	// bookwarehouse-audio gates its /cover handler on a signed `?token=`
	// capability (catalog/handler.go:205, tokens.Verify with CoverFileIdx
	// sentinel) — the host's bearer auth doesn't cover this. Mint the same
	// MediaSigningSecret-signed token here, just like handlePublicTrack does
	// for stream tokens, and append it to the backend URL. Without the
	// token the backend returns 401 and the mobile client renders the
	// placeholder image.
	_, cfg, err := h.targetFn(r.Context())
	if err != nil || cfg.MediaSigningSecret == "" {
		http.Error(w, "media signing not configured", http.StatusServiceUnavailable)
		return
	}
	// Cover tokens carry a synthetic userID because mediatoken.Mint requires
	// a non-empty subject; the backend's tokens.Verify ignores `sub` for
	// CoverFileIdx (covers aren't per-user). Any stable placeholder works.
	coverTok, err := mediatoken.Mint(cfg.MediaSigningSecret, "abs-cover", backendBookID, mediatoken.CoverFileIdx)
	if err != nil {
		http.Error(w, "mint cover token", http.StatusInternalServerError)
		return
	}
	size := r.URL.Query().Get("size")
	if size == "" {
		size = "large"
	}
	backendPath := "/api/v1/cover/" + neturl.PathEscape(backendBookID) + "/" + neturl.PathEscape(size) +
		"?token=" + neturl.QueryEscape(coverTok)
	body, contentType, err := h.backend.HostClient().GetBinary(r.Context(), "", lib.BackendPluginID, backendPath)
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
	d, err := h.backend.GetDetail(r.Context(), "", lib.BackendPluginID, backendBookID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	indexes := make([]int, 0, len(d.Files))
	fileDurations := make([]int, 0, len(d.Files))
	for _, f := range d.Files {
		indexes = append(indexes, f.Index)
		fileDurations = append(fileDurations, f.DurationSeconds)
	}
	h.logger.Info("abs /play",
		"book_id", backendBookID,
		"encoded_id", encodedBookID,
		"file_count", len(d.Files),
		"backend_indexes", indexes,
		"file_durations", fileDurations,
		"book_duration", d.DurationSeconds,
	)
	sessionID := ulid.Make().String()
	// ProfileID is required on insert: the ABSSession row is read back
	// only by id (no profile filter on GetABSSession), but downstream
	// progress writes scope by (user, profile, book), so an unstamped
	// row writes progress against the empty primary profile while the
	// active profile reads from its own scope. Same root cause as the
	// internal/server fix in 9e695a7 — repeated here in internal/abs.
	sess := store.ABSSession{
		ID:          sessionID,
		UserID:      a.UserID,
		ProfileID:   a.ProfileID,
		BookID:      encodedBookID,
		DeviceID:    deviceID,
		DeviceInfo:  p.DeviceInfo,
		MediaPlayer: p.MediaPlayer,
	}
	if err := h.store.InsertABSSession(r.Context(), sess); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Build audio tracks. urlFor takes the 1-based wire index, mints a
	// session-scoped JWT bound to that index, and returns the absolute
	// /abs/public/session/.../track/{idx}?token=... URL the mobile
	// client embeds. The mobile player actually IGNORES this URL and
	// builds its own /public/session/.../track/{index||1} with a Bearer
	// header — see handlePublicTrack — but real ABS clients (web,
	// third-party readers) read the contentUrl, so we still emit a
	// signed one.
	baseURL := h.absBaseURL(r)
	urlFor := func(wireIdx int) string {
		tok, _ := IssueSessionToken(cfg.ABSJWTSecret, a.UserID, sessionID, encodedBookID, wireIdx, 6*time.Hour)
		return baseURL +
			"/abs/public/session/" + sessionID + "/track/" + strconv.Itoa(wireIdx) +
			"?token=" + tok
	}
	audioTracks := buildPlayAudioTracks(d, encodedBookID, urlFor)
	for _, t := range audioTracks {
		if dur, ok := t["duration"].(float64); !ok || dur <= 0 {
			h.logger.Warn("abs /play: zero track duration",
				"book_id", backendBookID,
				"wire_idx", t["index"],
				"file_count", len(d.Files),
				"book_duration", d.DurationSeconds,
			)
		}
	}

	h.publish(a.UserID, "user_session_open", map[string]any{
		"id":            sessionID,
		"libraryItemId": encodedBookID,
		"deviceId":      deviceID,
		"mediaPlayer":   p.MediaPlayer,
	})
	h.broadcastListenerCount(r.Context())

	// Resume position: the mobile player reads playbackSession.currentTime
	// to seed the audio element's initial position. Without it the player
	// stays in BUFFERING and the spinner runs forever.
	var currentTime float64
	if prog, err := h.store.GetProgress(r.Context(), a.UserID, a.ProfileID, encodedBookID); err == nil {
		currentTime = float64(prog.CurrentSeconds)
	}

	totalDuration := float64(0)
	for _, t := range audioTracks {
		if dur, ok := t["duration"].(float64); ok {
			totalDuration += dur
		}
	}
	if totalDuration <= 0 {
		totalDuration = float64(d.DurationSeconds)
	}

	// Chapters: ABS shape is {id, start, end, title}, start/end in
	// seconds. Mobile renders the chapter list AND uses these for
	// in-chapter skip.
	chapters := make([]map[string]any, len(d.Chapters))
	for i, c := range d.Chapters {
		chapters[i] = map[string]any{
			"id":    i,
			"start": float64(c.StartSeconds),
			"end":   float64(c.EndSeconds),
			"title": c.Title,
		}
	}

	mediaMetadata := buildPlayMediaMetadata(d)
	libraryItem := buildPlayLibraryItem(d, lib, encodedBookID, mediaMetadata, audioTracks, chapters, totalDuration)

	// Display fields the "Now Playing" widget reads off the top-level
	// session. Missing them makes the widget render with empty author /
	// title pills but the audio loader silently aborts if it reads
	// displayAuthor for the cover-initial fallback and gets undefined.
	displayTitle := d.Title
	displayAuthor := ""
	if md, ok := mediaMetadata["authorName"].(string); ok {
		displayAuthor = md
	}
	coverPath := d.CoverPath
	if coverPath == "" && d.CoverURL != "" {
		coverPath = d.CoverURL
	}

	now := time.Now()
	nowMs := now.UnixMilli()
	dateStr := now.UTC().Format("2006-01-02")
	dayOfWeek := now.UTC().Weekday().String()

	// DeviceInfo is echoed back to the client with a stable shape. Real
	// ABS clients pattern-match on these field names (deviceId,
	// manufacturer, model, sdkVersion, clientVersion) so emit the keys
	// even when the request didn't carry them.
	deviceInfo := map[string]any{
		"deviceId":      deviceID,
		"manufacturer":  pickStr(p.DeviceInfo, "manufacturer", "Unknown"),
		"model":         pickStr(p.DeviceInfo, "model", "Unknown"),
		"sdkVersion":    pickAny(p.DeviceInfo, "sdkVersion", 0),
		"clientVersion": pickStr(p.DeviceInfo, "clientVersion", "0.0.0"),
	}

	playbackSession := map[string]any{
		"id":             sessionID,
		"userId":         a.UserID,
		"libraryId":      absLibraryID(lib),
		"libraryItemId":  encodedBookID,
		"bookId":         encodedBookID,
		"episodeId":      nil,
		"mediaType":      "book",
		"mediaMetadata":  mediaMetadata,
		"chapters":       chapters,
		"displayTitle":   displayTitle,
		"displayAuthor":  displayAuthor,
		"coverPath":      nilIfEmpty(coverPath),
		"duration":       totalDuration,
		"playMethod":     0, // DIRECTPLAY
		"mediaPlayer":    firstNonEmpty(p.MediaPlayer, "exo-player"),
		"deviceInfo":     deviceInfo,
		"serverVersion":  ServerVersion,
		"date":           dateStr,
		"dayOfWeek":      dayOfWeek,
		"timeListening":  0,
		"startTime":      currentTime,
		"currentTime":    currentTime,
		"startedAt":      nowMs,
		"updatedAt":      nowMs,
		"audioTracks":    audioTracks,
		"libraryItem":    libraryItem,
	}
	writeJSON(w, http.StatusOK, playbackSession)
}

// pickStr returns the string value at key from m, or fallback when missing.
func pickStr(m map[string]any, key, fallback string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return fallback
}

// pickAny returns the any value at key from m, or fallback when missing.
func pickAny(m map[string]any, key string, fallback any) any {
	if v, ok := m[key]; ok && v != nil {
		return v
	}
	return fallback
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
	if err := h.store.UpdateABSSession(r.Context(), sid, int(p.CurrentTime), int(p.TimeListened)); err != nil {
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
		_ = h.store.UpdateProgressPosition(r.Context(), a.UserID, a.ProfileID, sess.BookID, int(p.CurrentTime))
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

// handlePublicTrack serves a session-scoped audio stream.
//
// Two auth shapes are accepted:
//   - `?token=<session-jwt>` — the legacy capability we mint into the
//     /play response's audioTracks[i].contentUrl. Self-describing: the
//     JWT carries (userID, sessionID, fileIdx) so we can validate
//     without consulting any other store beyond the session row.
//   - `Authorization: Bearer <abs-access-jwt>` — what the official ABS
//     mobile app actually sends. It IGNORES our contentUrl and builds
//     its own /public/session/<sid>/track/<idx> URL with the bearer in
//     the header (plugins/capacitor/AbsAudioPlayer.js:254-263 +
//     plugins/axios.js:48-50). The first shape was unreachable from the
//     mobile path; without this fallback, tapping play loaded a 401 and
//     the loading spinner ran forever.
func (h *Handler) handlePublicTrack(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sid")
	idxStr := chi.URLParam(r, "idx")
	// URL idx is the ABS 1-based wire index (see handlePlay /
	// translate.ToLibraryItem for the convention). The session-token
	// FileIdx claim and the URL idx are both 1-based; the backend's
	// stream route is 0-based, so we subtract one when minting the
	// media token and when building the backend path below.
	idx, err := strconv.Atoi(idxStr)
	if err != nil {
		http.Error(w, "idx must be int", http.StatusBadRequest)
		return
	}
	_, cfg, err := h.targetFn(r.Context())
	if err != nil {
		http.Error(w, "config unavailable", http.StatusInternalServerError)
		return
	}
	sess, err := h.store.GetABSSession(r.Context(), sid)
	if err != nil || sess.ClosedAt != nil {
		http.Error(w, "session closed or missing", http.StatusGone)
		return
	}

	if qtok := r.URL.Query().Get("token"); qtok != "" {
		claims, err := ParseToken(cfg.ABSJWTSecret, qtok)
		if err != nil || claims.Type != "session" || claims.SessionID != sid || claims.FileIdx != idx {
			http.Error(w, "invalid session token", http.StatusUnauthorized)
			return
		}
		if claims.UserID != sess.UserID || (claims.BookID != "" && claims.BookID != sess.BookID) {
			http.Error(w, "invalid session token", http.StatusUnauthorized)
			return
		}
	} else {
		raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if raw == "" {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		claims, err := ParseToken(cfg.ABSJWTSecret, raw)
		if err != nil || claims.Type != "access" {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		row, err := h.store.GetABSTokenByJTI(r.Context(), claims.JTI)
		if err != nil || row.RevokedAt != nil {
			http.Error(w, "token revoked", http.StatusUnauthorized)
			return
		}
		if claims.UserID != sess.UserID {
			http.Error(w, "session not owned by caller", http.StatusForbidden)
			return
		}
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
	backendIdx := idx - 1
	if backendIdx < 0 {
		http.Error(w, "idx out of range", http.StatusBadRequest)
		return
	}
	mediaTok, err := mediatoken.Mint(cfg.MediaSigningSecret, sess.UserID, backendBookID, backendIdx)
	if err != nil {
		http.Error(w, "mint media token", http.StatusInternalServerError)
		return
	}
	backendPath := "/api/v1/stream/" + neturl.PathEscape(backendBookID) + "/" + strconv.Itoa(backendIdx) +
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
			"book_id", backendBookID, "wire_idx", idx, "backend_idx", backendIdx, "err", err.Error())
		http.Error(w, "stream unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	h.logger.Info("abs stream proxy: upstream response",
		"book_id", backendBookID,
		"wire_idx", idx,
		"backend_idx", backendIdx,
		"upstream_status", resp.StatusCode,
		"upstream_content_type", resp.Header.Get("Content-Type"),
		"upstream_content_length", resp.Header.Get("Content-Length"),
		"upstream_content_range", resp.Header.Get("Content-Range"),
		"client_range", r.Header.Get("Range"),
	)
	if resp.StatusCode >= 400 {
		h.logger.Warn("abs stream proxy: upstream non-2xx",
			"book_id", backendBookID,
			"wire_idx", idx,
			"backend_idx", backendIdx,
			"upstream_status", resp.StatusCode,
		)
	}

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
	lib, err := h.portalLibraryFromABSID(r.Context(), chi.URLParam(r, "id"))
	if err != nil || lib.BackendPluginID == "" {
		http.Error(w, "no backend configured", http.StatusPreconditionFailed)
		return
	}
	limit, page := readPagedQuery(r, 50)
	out, err := h.backend.BrowseAuthors(r.Context(), "", lib.BackendPluginID, backend.ListParams{Limit: limit, LibraryID: backendLibraryID(lib)})
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
	lib, err := h.portalLibraryFromABSID(r.Context(), chi.URLParam(r, "id"))
	if err != nil || lib.BackendPluginID == "" {
		http.Error(w, "no backend configured", http.StatusPreconditionFailed)
		return
	}
	limit, page := readPagedQuery(r, 25)
	out, err := h.backend.BrowseSeries(r.Context(), "", lib.BackendPluginID, backend.ListParams{Limit: limit, LibraryID: backendLibraryID(lib)})
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
	catalog, err := h.backend.ListCatalog(r.Context(), "", lib.BackendPluginID, backend.ListParams{
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
	if series, err := h.backend.BrowseSeries(r.Context(), "", lib.BackendPluginID, backend.ListParams{
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
	if authors, err := h.backend.BrowseAuthors(r.Context(), "", lib.BackendPluginID, backend.ListParams{
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
	if err != nil {
		writeJSON(w, http.StatusOK, []map[string]any{})
		return
	}
	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	// Podcast libraries have their own shelf set (recent-episodes,
	// newest-podcasts). Real ABS emits these for mediaType=podcast
	// libraries; mobile renders a different home tab for them.
	if lib.MediaType == "podcast" {
		h.podcastPersonalized(w, r, lib, limit)
		return
	}

	if lib.BackendPluginID == "" {
		writeJSON(w, http.StatusOK, []map[string]any{})
		return
	}

	shelves := []map[string]any{
		{"id": "continue-listening", "label": "Continue Listening", "labelStringKey": "LabelContinueListening", "type": "book", "entities": []any{}, "total": 0},
		{"id": "continue-series", "label": "Continue Series", "labelStringKey": "LabelContinueSeries", "type": "book", "entities": []any{}, "total": 0},
		{"id": "newest", "label": "Newest", "labelStringKey": "LabelNewest", "type": "book", "entities": []any{}, "total": 0},
		{"id": "recent-series", "label": "Recent Series", "labelStringKey": "LabelRecentSeries", "type": "series", "entities": []any{}, "total": 0},
		{"id": "discover", "label": "Discover", "labelStringKey": "LabelDiscover", "type": "book", "entities": []any{}, "total": 0},
		{"id": "listen-again", "label": "Listen Again", "labelStringKey": "LabelListenAgain", "type": "book", "entities": []any{}, "total": 0},
	}

	// Resolve progress rows; classify by is_finished + progress_pct.
	progRows, err := h.store.ListRecentProgress(r.Context(), a.UserID, a.ProfileID, 50)
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
			if d, derr := h.backend.GetDetail(r.Context(), "", lib.BackendPluginID, backendBookID); derr == nil {
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
		if d, derr := h.backend.GetDetail(r.Context(), "", lib.BackendPluginID, backendBookID); derr == nil {
			contPaths = append(contPaths, ToLibraryItem(withPortalLibraryDetail(d, lib), func(int) string { return "" }))
		}
	}
	shelves[0]["entities"] = contPaths
	shelves[0]["total"] = len(contPaths)
	shelves[5]["entities"] = againPaths
	shelves[5]["total"] = len(againPaths)

	// newest + discover come from the catalog list. We sort by added_at
	// desc when the backend supports it. For "discover" we exclude items
	// the user already has progress on.
	progressBookIDs := map[string]bool{}
	for _, p := range progRows {
		_, backendBookID, _ := bookref.Decode(p.BookID)
		progressBookIDs[backendBookID] = true
	}
	listOut, err := h.backend.ListCatalog(r.Context(), "", lib.BackendPluginID, backend.ListParams{Limit: limit * 3, Sort: "added", Order: "desc", LibraryID: backendLibraryID(lib)})
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
		shelves[2]["total"] = listOut.Total
		shelves[4]["entities"] = discover
		shelves[4]["total"] = listOut.Total // approx — discover's filtering happens after
	}

	// recent-series: take the first N series the backend returns. Each
	// entity is a thin series object; clients render the name + cover.
	if seriesOut, err := h.backend.BrowseSeries(r.Context(), "", lib.BackendPluginID, backend.ListParams{Limit: limit, LibraryID: backendLibraryID(lib)}); err == nil {
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
		shelves[3]["total"] = seriesOut.Total
	}

	// continue-series: drift — the v1 backend has no "next book in series
	// I've started" query, so we leave it empty. Future work would join
	// the user's progress rows against the book.series_id to surface the
	// next sequence.

	writeJSON(w, http.StatusOK, shelves)
}

// podcastPersonalized emits the podcast-library variant of the home
// shelves. Real ABS uses three shelves for podcast libraries:
//
//	recent-episodes — most-recently-published episodes across all
//	                  subscribed podcasts; each entity is a LibraryItem
//	                  with a `recentEpisode` field set
//	listen-again    — finished podcasts (analogue of book listen-again)
//	newest-podcasts — most-recently-added podcasts in this library
//
// We populate recent-episodes + newest-podcasts; listen-again is
// emitted with an empty entities[] (podcast "finished" semantics are
// per-episode, not per-podcast).
func (h *Handler) podcastPersonalized(w http.ResponseWriter, r *http.Request, lib store.PortalLibrary, limit int) {
	shelves := []map[string]any{
		{"id": "recent-episodes", "label": "Recent Episodes", "labelStringKey": "LabelRecentEpisodes", "type": "episode", "entities": []any{}, "total": 0},
		{"id": "newest-podcasts", "label": "Newest Podcasts", "labelStringKey": "LabelNewestPodcasts", "type": "podcast", "entities": []any{}, "total": 0},
		{"id": "listen-again", "label": "Listen Again", "labelStringKey": "LabelListenAgain", "type": "episode", "entities": []any{}, "total": 0},
	}

	podcasts, err := h.store.ListPodcasts(r.Context(), lib.ID, 0)
	if err != nil {
		h.logger.Warn("podcast personalized: list podcasts", "err", err.Error())
		writeJSON(w, http.StatusOK, shelves)
		return
	}

	// newest-podcasts: top N by updated_at desc (ListPodcasts returns
	// in that order already).
	newest := make([]any, 0, limit)
	for i, p := range podcasts {
		if i >= limit {
			break
		}
		encoded := bookref.Encode(p.LibraryID, p.ID)
		// Count episodes for the shelf entity badge.
		episodes, _ := h.store.ListPodcastEpisodes(r.Context(), p.ID, 0)
		newest = append(newest, ToPodcastSummary(p, len(episodes), encoded))
	}
	shelves[1]["entities"] = newest
	shelves[1]["total"] = len(podcasts)

	// recent-episodes: gather every podcast's episodes, sort by
	// published_at desc, take top N. Episodes carry the parent
	// libraryItem (the podcast) + a recentEpisode pointer so clients
	// render "podcast title — episode title".
	type recent struct {
		podcast store.Podcast
		episode store.PodcastEpisode
	}
	all := make([]recent, 0, 256)
	for _, p := range podcasts {
		eps, err := h.store.ListPodcastEpisodes(r.Context(), p.ID, 100)
		if err != nil {
			continue
		}
		for _, e := range eps {
			all = append(all, recent{podcast: p, episode: e})
		}
	}
	// Sort: published_at desc (nil published_at sinks to bottom via the
	// zero-value time, which is fine for ordering — clients don't sort
	// again).
	sort.Slice(all, func(i, j int) bool {
		ip, jp := timeOrZero(all[i].episode.PublishedAt), timeOrZero(all[j].episode.PublishedAt)
		return ip.After(jp)
	})
	entities := make([]any, 0, limit)
	for i, r := range all {
		if i >= limit {
			break
		}
		encoded := bookref.Encode(r.podcast.LibraryID, r.podcast.ID)
		// Embed the podcast as the parent LibraryItem; the
		// recentEpisode block tells clients which episode triggered
		// the recency without a second fetch.
		podcastItem := ToPodcastSummary(r.podcast, 1, encoded)
		ep := r.episode
		var pubMs int64
		if ep.PublishedAt != nil {
			pubMs = ep.PublishedAt.UnixMilli()
		}
		entities = append(entities, map[string]any{
			"id":        encoded,
			"libraryId": podcastItem.LibraryID,
			"folderId":  podcastItem.FolderID,
			"mediaType": "podcast",
			"media":     podcastItem.Media,
			"addedAt":   podcastItem.AddedAt,
			"updatedAt": podcastItem.UpdatedAt,
			"recentEpisode": map[string]any{
				"id":            EncodePodcastEpisodeID(ep.ID),
				"libraryItemId": encoded,
				"title":         ep.Title,
				"description":   ep.Description,
				"duration":      float64(ep.DurationSeconds),
				"publishedAt":   pubMs,
				"audioBytes":    ep.AudioBytes,
				"mimeType":      ep.AudioMimeType,
			},
		})
	}
	shelves[0]["entities"] = entities
	shelves[0]["total"] = len(all)

	writeJSON(w, http.StatusOK, shelves)
}

// timeOrZero returns *time.Time's value, or the zero time if nil.
// Used in sort.Slice closures where we need a comparable time.
func timeOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
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
	p, err := h.store.GetProgress(r.Context(), a.UserID, a.ProfileID, itemID)
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
	cur, err := h.store.GetProgress(r.Context(), a.UserID, a.ProfileID, itemID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	next := store.Progress{
		UserID:          a.UserID,
		ProfileID:       a.ProfileID,
		BookID:          itemID,
		CurrentSeconds:  cur.CurrentSeconds,
		DurationSeconds: cur.DurationSeconds,
		ProgressPct:     cur.ProgressPct,
		IsFinished:      cur.IsFinished,
	}
	if body.CurrentTime != nil {
		next.CurrentSeconds = int(*body.CurrentTime)
	}
	if body.Duration != nil {
		next.DurationSeconds = int(*body.Duration)
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
	updated, err := h.store.GetProgress(r.Context(), a.UserID, a.ProfileID, itemID)
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
		"duration":      float64(p.DurationSeconds),
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
