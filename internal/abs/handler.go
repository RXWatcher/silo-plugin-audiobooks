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
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/bookref"
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
	return map[string]any{
		"id":        absLibraryID(lib),
		"name":      name,
		"mediaType": LibraryMediaType,
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
	if err != nil || lib.BackendPluginID == "" {
		http.Error(w, "no backend configured", http.StatusPreconditionFailed)
		return
	}
	p := backend.ListParams{}
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, e := strconv.Atoi(l); e == nil {
			p.Limit = n
		}
	}
	p.LibraryID = backendLibraryID(lib)
	out, err := h.backend.ListCatalog(r.Context(), a.Token, lib.BackendPluginID, p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	results := make([]LibraryItem, len(out.Items))
	for i, s := range out.Items {
		results[i] = ToLibrarySummary(withPortalLibrarySummary(s, lib))
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results, "total": out.Total})
}

func (h *Handler) handleItem(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	lib, backendBookID, encodedBookID, err := h.portalLibraryForBookRef(r.Context(), chi.URLParam(r, "id"))
	if err != nil || lib.BackendPluginID == "" {
		http.Error(w, "no backend configured", http.StatusPreconditionFailed)
		return
	}
	d, err := h.backend.GetDetail(r.Context(), a.Token, lib.BackendPluginID, backendBookID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	d = withPortalLibraryDetail(d, lib)
	// The contentUrl for each track is the bearer-protected web stream URL.
	// Drift from spec: spec wanted a session-scoped URL; we'd need to mint
	// a session JWT here. For now, use bearer-protected web stream.
	contentURLFn := func(idx int) string {
		return h.backend.StreamURL(lib.BackendPluginID, backendBookID, idx)
	}
	item := ToLibraryItem(d, contentURLFn)
	item.ID = encodedBookID
	writeJSON(w, http.StatusOK, item)
}

func (h *Handler) handleItemCover(w http.ResponseWriter, r *http.Request) {
	lib, backendBookID, _, err := h.portalLibraryForBookRef(r.Context(), chi.URLParam(r, "id"))
	if err != nil || lib.BackendPluginID == "" {
		http.Error(w, "no backend configured", http.StatusPreconditionFailed)
		return
	}
	size := r.URL.Query().Get("size")
	http.Redirect(w, r, h.backend.CoverURL(lib.BackendPluginID, backendBookID, size), http.StatusFound)
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
			tok, _ := cdn.MintStreamToken(cdnSecret, a.UserID, encodedBookID, f.Index, 5*time.Minute)
			trackURL = cdn.PresignedURL(cdnHostname, encodedBookID, f.Index, tok)
		} else {
			tok, _ := IssueSessionToken(cfg.ABSJWTSecret, a.UserID, sessionID, encodedBookID, f.Index, 6*time.Hour)
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
	// a.UserID). Must NOT use UpsertProgress here: it would write
	// is_finished=false / progress_pct=0 every sync tick and silently
	// un-finish a book the user explicitly marked finished.
	_ = h.store.UpdateProgressPosition(r.Context(), a.UserID, sess.BookID, int(p.CurrentTime))

	resp := map[string]any{"ok": true}

	// In CDN mode, return freshly-minted track URLs so the client can keep
	// streaming past the 5-minute presigned token window without re-calling
	// /play. We only do this when we can also fetch the book detail.
	cdnHostname, cdnSecretB64 := h.cdnFn()
	if cdnHostname != "" && cdnSecretB64 != "" && err == nil {
		cdnSecret, decErr := base64.StdEncoding.DecodeString(cdnSecretB64)
		if decErr == nil && len(cdnSecret) > 0 {
			lib, backendBookID, encodedBookID, targetErr := h.portalLibraryForBookRef(r.Context(), sess.BookID)
			if targetErr == nil && lib.BackendPluginID != "" {
				d, detailErr := h.backend.GetDetail(r.Context(), a.Token, lib.BackendPluginID, backendBookID)
				if detailErr == nil {
					tracks := make([]AudioTrack, len(d.Files))
					for i, f := range d.Files {
						tok, _ := cdn.MintStreamToken(cdnSecret, a.UserID, encodedBookID, f.Index, 5*time.Minute)
						tracks[i] = AudioTrack{
							Index:      f.Index,
							ContentURL: cdn.PresignedURL(cdnHostname, encodedBookID, f.Index, tok),
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
	// Redirect to the backend's stream URL. (The proxy will validate the
	// inbound bearer the audio client would otherwise need — here we let the
	// session-token-already-verified state stand in.)
	http.Redirect(w, r, h.backend.StreamURL(lib.BackendPluginID, backendBookID, idx), http.StatusFound)
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
