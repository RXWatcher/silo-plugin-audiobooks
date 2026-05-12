package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/auth"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
)

// mountAdminRoutes wires admin-only endpoints. Each handler enforces admin
// via auth.RequireAdmin.
func (s *Server) mountAdminRoutes(r chi.Router) {
	r.Get("/admin/backend-config", s.handleGetBackendConfig)
	r.Patch("/admin/backend-config", s.handleUpdateBackendConfig)
	r.Get("/admin/requests", s.handleAdminListRequests)
	r.Post("/admin/requests/{id}/approve", s.handleAdminApproveRequest)
	r.Post("/admin/requests/{id}/deny", s.handleAdminDenyRequest)
	r.Get("/admin/sessions", s.handleAdminListSessions)
	r.Post("/admin/sessions/{id}/close", s.handleAdminCloseSession)
	r.Get("/admin/tokens", s.handleAdminListTokens)
	r.Post("/admin/tokens/{id}/revoke", s.handleAdminRevokeToken)
}

func (s *Server) handleGetBackendConfig(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	cfg, err := s.d.Store.GetBackendConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Don't leak the JWT secret over the wire.
	cfg.ABSJWTSecret = nil
	writeJSON(w, http.StatusOK, cfg)
}

type backendConfigPayload struct {
	TargetBackendPluginID    *string            `json:"target_backend_plugin_id"`
	AutoApproveRequests      *bool              `json:"auto_approve_requests"`
	StreamingMode            *string            `json:"streaming_mode"`
	CacheDir                 *string            `json:"cache_dir"`
	CacheMaxSizeGB           *int               `json:"cache_max_size_gb"`
	CacheDownloadConcurrency *int               `json:"cache_download_concurrency"`
	PathRemappings           *[]store.PathRemap `json:"path_remappings"`
	ABSAccessTTLHours        *int               `json:"abs_access_token_ttl_hours"`
	ABSRefreshTTLDays        *int               `json:"abs_refresh_token_ttl_days"`
	RotateABSSecret          bool               `json:"rotate_abs_secret"`
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
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if p.TargetBackendPluginID != nil {
		cur.TargetBackendPluginID = *p.TargetBackendPluginID
	}
	if p.AutoApproveRequests != nil {
		cur.AutoApproveRequests = *p.AutoApproveRequests
	}
	if p.StreamingMode != nil {
		cur.StreamingMode = *p.StreamingMode
	}
	if p.CacheDir != nil {
		cur.CacheDir = *p.CacheDir
	}
	if p.CacheMaxSizeGB != nil {
		cur.CacheMaxSizeGB = *p.CacheMaxSizeGB
	}
	if p.CacheDownloadConcurrency != nil {
		cur.CacheDownloadConcurrency = *p.CacheDownloadConcurrency
	}
	if p.PathRemappings != nil {
		cur.PathRemappings = *p.PathRemappings
	}
	if p.ABSAccessTTLHours != nil {
		cur.ABSAccessTTLHours = *p.ABSAccessTTLHours
	}
	if p.ABSRefreshTTLDays != nil {
		cur.ABSRefreshTTLDays = *p.ABSRefreshTTLDays
	}
	if p.RotateABSSecret {
		secret, err := randomBytes(32)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "rotate secret: "+err.Error())
			return
		}
		cur.ABSJWTSecret = secret
	}
	if err := s.d.Store.UpdateBackendConfig(r.Context(), cur); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cur.ABSJWTSecret = nil
	writeJSON(w, http.StatusOK, cur)
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
		writeError(w, http.StatusInternalServerError, err.Error())
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
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if req.Status != "pending" {
		writeError(w, http.StatusBadRequest, "only pending requests can be approved")
		return
	}
	if err := s.d.Store.UpdateRequestStatus(r.Context(), reqID, "submitted", "", ""); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.d.Events != nil && req.TargetPluginID != "" {
		s.d.Events.Publish(r.Context(), "request_submitted", map[string]any{
			"request_id":        reqID,
			"target_plugin_id":  req.TargetPluginID,
			"title":             req.Title,
			"author":            req.Author,
			"isbn":              req.ISBN,
			"requester_user_id": req.UserID,
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
		writeError(w, http.StatusInternalServerError, err.Error())
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
		writeError(w, http.StatusInternalServerError, err.Error())
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
		writeError(w, http.StatusInternalServerError, err.Error())
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
		writeError(w, http.StatusInternalServerError, err.Error())
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
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, t := range tokens {
		if t.ID == id {
			_ = s.d.Store.RevokeABSToken(r.Context(), id, t.UserID)
			break
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
