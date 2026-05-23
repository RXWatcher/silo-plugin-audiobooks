package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/auth"
	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/backend"
	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/libsync"
	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/store"
)

// mountAdminRoutes wires admin-only endpoints. Each handler enforces admin
// via auth.RequireAdmin.
func (s *Server) mountAdminRoutes(r chi.Router) {
	r.Get("/admin/backend-config", s.handleGetBackendConfig)
	r.Patch("/admin/backend-config", s.handleUpdateBackendConfig)
	r.Get("/admin/libraries", s.handleAdminListLibraries)
	r.Put("/admin/libraries", s.handleAdminReplaceLibraries)
	r.Get("/admin/backend-libraries", s.handleAdminBackendLibraries)
	r.Post("/admin/libraries/sync", s.handleAdminSyncLibraries)
	r.Get("/admin/requests", s.handleAdminListRequests)
	r.Post("/admin/requests/{id}/approve", s.handleAdminApproveRequest)
	r.Post("/admin/requests/{id}/deny", s.handleAdminDenyRequest)
	r.Get("/admin/sessions", s.handleAdminListSessions)
	r.Post("/admin/sessions/{id}/close", s.handleAdminCloseSession)
	r.Get("/admin/tokens", s.handleAdminListTokens)
	r.Post("/admin/tokens/{id}/revoke", s.handleAdminRevokeToken)
	r.Get("/admin/audit/zero-durations", s.handleAdminAuditZeroDurations)
}

func (s *Server) handleGetBackendConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	cfg, err := s.d.Store.GetBackendConfig(r.Context())
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	// Don't leak the JWT secret over the wire.
	cfg.ABSJWTSecret = nil
	libs, _ := s.d.Store.ListPortalLibraries(r.Context(), false)
	writeJSON(w, http.StatusOK, map[string]any{
		"target_backend_plugin_id":                cfg.TargetBackendPluginID,
		"target_backend_installation_id":          cfg.TargetBackendInstallID,
		"target_request_provider_plugin_id":       cfg.TargetRequestPluginID,
		"target_request_provider_installation_id": cfg.TargetRequestInstallID,
		"auto_approve_requests":                   cfg.AutoApproveRequests,
		"abs_access_token_ttl_hours":              cfg.ABSAccessTTLHours,
		"abs_refresh_token_ttl_days":              cfg.ABSRefreshTTLDays,
		"standalone_http_listen":                  cfg.StandaloneHTTPListen,
		"standalone_login_mode":                   store.NormalizeStandaloneLoginMode(cfg.StandaloneLoginMode),
		// media_signing_secret_set lets the admin UI show whether the secret
		// is configured without leaking the value over the wire.
		"media_signing_secret_set": cfg.MediaSigningSecret != "",
		"libraries":                libs,
	})
}

type backendConfigPayload struct {
	TargetBackendPluginID  *string `json:"target_backend_plugin_id"`
	TargetBackendInstallID *string `json:"target_backend_installation_id"`
	TargetRequestPluginID  *string `json:"target_request_provider_plugin_id"`
	TargetRequestInstallID *string `json:"target_request_provider_installation_id"`
	AutoApproveRequests    *bool   `json:"auto_approve_requests"`
	ABSAccessTTLHours      *int    `json:"abs_access_token_ttl_hours"`
	ABSRefreshTTLDays      *int    `json:"abs_refresh_token_ttl_days"`
	StandaloneHTTPListen   *string `json:"standalone_http_listen"`
	StandaloneLoginMode    *string `json:"standalone_login_mode"`
	MediaSigningSecret     *string `json:"media_signing_secret"`
	RotateABSSecret        bool    `json:"rotate_abs_secret"`
}

func (s *Server) handleUpdateBackendConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	var p backendConfigPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	cur, err := s.d.Store.GetBackendConfig(r.Context())
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	if p.TargetBackendPluginID != nil {
		cur.TargetBackendPluginID = *p.TargetBackendPluginID
	}
	if p.TargetBackendInstallID != nil {
		cur.TargetBackendInstallID = *p.TargetBackendInstallID
	}
	if p.TargetRequestPluginID != nil {
		cur.TargetRequestPluginID = *p.TargetRequestPluginID
	}
	if p.TargetRequestInstallID != nil {
		cur.TargetRequestInstallID = *p.TargetRequestInstallID
	}
	if p.AutoApproveRequests != nil {
		cur.AutoApproveRequests = *p.AutoApproveRequests
	}
	if p.ABSAccessTTLHours != nil {
		cur.ABSAccessTTLHours = *p.ABSAccessTTLHours
	}
	if p.ABSRefreshTTLDays != nil {
		cur.ABSRefreshTTLDays = *p.ABSRefreshTTLDays
	}
	if p.StandaloneHTTPListen != nil {
		cur.StandaloneHTTPListen = *p.StandaloneHTTPListen
	}
	if p.StandaloneLoginMode != nil {
		mode := store.NormalizeStandaloneLoginMode(*p.StandaloneLoginMode)
		if mode != *p.StandaloneLoginMode {
			writeError(w, http.StatusBadRequest, "invalid standalone_login_mode")
			return
		}
		cur.StandaloneLoginMode = mode
	}
	if p.MediaSigningSecret != nil {
		// Reject obviously-weak secrets at the admin boundary. An empty
		// string clears the secret (we let that through — operators may
		// want to disable media signing during incident response). Any
		// non-empty value must be at least 16 bytes (128 bits of entropy
		// in raw form, or ~12 bytes after base64-decode) so we don't
		// silently accept a 1-character HMAC key.
		v := *p.MediaSigningSecret
		if v != "" && len(v) < 16 {
			writeError(w, http.StatusBadRequest, "media_signing_secret must be at least 16 characters")
			return
		}
		cur.MediaSigningSecret = v
	}
	if p.RotateABSSecret {
		secret, err := randomBytes(32)
		if err != nil {
			writeInternal(w, r, err)
			return
		}
		cur.ABSJWTSecret = secret
	}
	if err := s.d.Store.UpdateBackendConfig(r.Context(), cur); err != nil {
		writeInternal(w, r, err)
		return
	}
	cur.ABSJWTSecret = nil
	writeJSON(w, http.StatusOK, cur)
}

func (s *Server) handleAdminListLibraries(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	libs, err := s.d.Store.ListPortalLibraries(r.Context(), false)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": libs})
}

func (s *Server) handleAdminReplaceLibraries(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	var body struct {
		Items []store.PortalLibrary `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.d.Store.ReplacePortalLibraries(r.Context(), body.Items); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Broadcast library_updated so connected ABS clients pick up the
	// new library layout immediately (the SPA polls /libraries
	// independently; the mobile app waits for this event before
	// re-fetching). Singular event covers the common "admin edited
	// the library list" case.
	if s.d.Broadcaster != nil {
		s.d.Broadcaster.Broadcast("library_updated", map[string]any{
			"count": len(body.Items),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminBackendLibraries(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireAdmin(w, r)
	if !ok {
		return
	}
	backendID := r.URL.Query().Get("backend_plugin_id")
	if backendID == "" {
		writeJSON(w, http.StatusOK, map[string]any{"items": []backend.LibraryInfo{}})
		return
	}
	items, err := s.d.Backend.ListLibraries(r.Context(), id.Token, backendID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": []backend.LibraryInfo{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleAdminSyncLibraries(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireAdmin(w, r)
	if !ok {
		return
	}
	backendID := r.URL.Query().Get("backend_plugin_id")
	if backendID == "" {
		writeError(w, http.StatusBadRequest, "backend_plugin_id required")
		return
	}
	if s.d.Backend == nil {
		writeError(w, http.StatusBadGateway, "backend unavailable")
		return
	}
	stats, err := libsync.Sync(r.Context(), s.d.Store, s.d.Backend, id.Token, backendID)
	if err != nil {
		// Don't surface the upstream backend error text to the admin SPA;
		// it carries the host-proxy URL and the truncated upstream body
		// which can leak schema names and internal paths. Log server-side
		// and return an opaque message.
		slog.Warn("audiobooks admin: library sync failed",
			"backend_plugin_id", backendID, "err", err)
		writeError(w, http.StatusBadGateway, "library sync failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"created": stats.Created,
		"updated": stats.Updated,
		"pruned":  stats.Pruned,
		"kept":    stats.Kept,
	})
}

func (s *Server) handleAdminListRequests(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "pending"
	}
	out, err := s.d.Store.ListRequestsByStatus(r.Context(), status, 200)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleAdminApproveRequest(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	reqID := chi.URLParam(r, "id")
	req, err := s.d.Store.GetRequest(r.Context(), reqID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "request not found")
			return
		}
		writeInternal(w, r, err)
		return
	}
	if req.Status != "pending" {
		writeError(w, http.StatusBadRequest, "only pending requests can be approved")
		return
	}
	if err := s.d.Store.UpdateRequestStatus(r.Context(), reqID, "submitted", "", ""); err != nil {
		writeInternal(w, r, err)
		return
	}
	if s.d.Events != nil && req.TargetPluginID != "" {
		s.d.Events.Publish(r.Context(), "request_submitted", map[string]any{
			"request_id":                reqID,
			"requestId":                 reqID,
			"target_plugin_id":          req.TargetPluginID,
			"target_provider_plugin_id": req.TargetPluginID,
			"title":                     req.Title,
			"author":                    req.Author,
			"isbn":                      req.ISBN,
			"requester_user_id":         req.UserID,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type denyPayload struct {
	Reason string `json:"reason"`
}

func (s *Server) handleAdminDenyRequest(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	reqID := chi.URLParam(r, "id")
	var p denyPayload
	_ = json.NewDecoder(r.Body).Decode(&p)
	if err := s.d.Store.UpdateRequestStatus(r.Context(), reqID, "denied", p.Reason, ""); err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminListSessions(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	out, err := s.d.Store.ListActiveABSSessions(r.Context(), 500)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleAdminCloseSession(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if err := s.d.Store.CloseABSSession(r.Context(), id); err != nil {
		writeInternal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdminListTokens(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	userID := r.URL.Query().Get("user_id")
	out, err := s.d.Store.ListABSTokens(r.Context(), userID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleAdminRevokeToken(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	tokens, err := s.d.Store.ListABSTokens(r.Context(), "")
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	for _, t := range tokens {
		if t.ID == id {
			if err := s.d.Store.RevokeABSToken(r.Context(), id, t.UserID); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					writeError(w, http.StatusNotFound, "token not found")
					return
				}
				writeInternal(w, r, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	// No token with this id: return 404 instead of a misleading 204 that
	// would tell the admin a revoke succeeded when nothing was revoked.
	writeError(w, http.StatusNotFound, "token not found")
}

// handleAdminAuditZeroDurations enumerates the backend catalog and reports
// books whose file durations are zero. Mobile audiobookshelf-app's
// currentTrack-finder needs a non-zero duration on the matching track or
// playback stalls (see internal/abs/handler.go's handlePlay comment for
// the underlying bug). The ABS handler falls back to the book-level
// duration when there's a single audio file; multi-file books with any
// zero-duration track stay broken and surface in `broken` below.
//
// Slow by nature: one HTTP round-trip per book to GetDetail. Bounded by
// ?limit=N (default 1000, max 50000). Operator runs it once per catalog
// after a re-scan to verify no remaining zero-duration books.
func (s *Server) handleAdminAuditZeroDurations(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireAdmin(w, r)
	if !ok {
		return
	}
	if s.d.Backend == nil {
		writeError(w, http.StatusServiceUnavailable, "backend unavailable")
		return
	}
	limit := 1000
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
			if limit > 50000 {
				limit = 50000
			}
		}
	}
	libraryFilter := r.URL.Query().Get("library_id")

	type bookReport struct {
		BookID       string `json:"book_id"`
		Title        string `json:"title"`
		FileCount    int    `json:"file_count"`
		ZeroFiles    []int  `json:"zero_file_indexes"`
		BookDuration int    `json:"book_duration_seconds"`
		LibraryID    int64  `json:"library_id"`
		LibraryName  string `json:"library_name"`
	}
	broken := []bookReport{}
	salvageable := []bookReport{}
	scanned := 0
	hitLimit := false

	libs, err := s.d.Store.ListPortalLibraries(r.Context(), true)
	if err != nil {
		writeInternal(w, r, err)
		return
	}

LibraryLoop:
	for _, lib := range libs {
		if libraryFilter != "" && strconv.FormatInt(lib.ID, 10) != libraryFilter {
			continue
		}
		if lib.BackendPluginID == "" {
			continue
		}
		cursor := ""
		for {
			page, err := s.d.Backend.ListCatalog(r.Context(), id.Token, lib.BackendPluginID,
				backend.ListParams{
					LibraryID: backendIDOf(lib),
					Limit:     200,
					Cursor:    cursor,
				})
			if err != nil {
				slog.Warn("audiobooks audit: list catalog failed",
					"library_id", lib.ID, "err", err)
				break
			}
			for _, summary := range page.Items {
				if scanned >= limit {
					hitLimit = true
					break LibraryLoop
				}
				scanned++
				detail, err := s.d.Backend.GetDetail(r.Context(), id.Token, lib.BackendPluginID, summary.ID)
				if err != nil {
					continue
				}
				zero := []int{}
				for _, f := range detail.Files {
					if f.DurationSeconds <= 0 {
						zero = append(zero, f.Index)
					}
				}
				if len(zero) == 0 {
					continue
				}
				rep := bookReport{
					BookID:       detail.ID,
					Title:        detail.Title,
					FileCount:    len(detail.Files),
					ZeroFiles:    zero,
					BookDuration: detail.DurationSeconds,
					LibraryID:    lib.ID,
					LibraryName:  lib.Name,
				}
				// The /play duration fallback rescues single-file books
				// whose book-level duration is populated; everything
				// else stays broken on the mobile player.
				if len(detail.Files) == 1 && detail.DurationSeconds > 0 {
					salvageable = append(salvageable, rep)
				} else {
					broken = append(broken, rep)
				}
			}
			if page.NextCursor == "" || scanned >= limit {
				break
			}
			cursor = page.NextCursor
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"scanned":           scanned,
		"limit":             limit,
		"hit_limit":         hitLimit,
		"broken_count":      len(broken),
		"salvageable_count": len(salvageable),
		"broken":            broken,
		"salvageable":       salvageable,
	})
}

// backendIDOf returns the backend library id for a portal_library row.
// Mirrors the backendLibraryID helper in internal/abs but kept local to
// avoid the abs package dependency. BackendLibraryID is *int64 because
// some libraries don't map cleanly back to a backend id; fall back to
// the portal id which still uniquely identifies a row.
func backendIDOf(lib store.PortalLibrary) int64 {
	if lib.BackendLibraryID != nil && *lib.BackendLibraryID != 0 {
		return *lib.BackendLibraryID
	}
	return lib.ID
}
