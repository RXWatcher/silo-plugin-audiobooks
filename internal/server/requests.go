package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/auth"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
)

// mountRequestRoutes wires user-facing + admin request endpoints.
func (s *Server) mountRequestRoutes(r chi.Router) {
	r.Get("/me/requests", s.handleListMyRequests)
	r.Post("/me/requests", s.handleCreateMyRequest)
	r.Delete("/me/requests/{id}", s.handleCancelMyRequest)
}

func (s *Server) handleListMyRequests(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	items, err := s.d.Store.ListUserRequests(r.Context(), id.UserID, 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type requestPayload struct {
	Title  string `json:"title"`
	Author string `json:"author"`
	ISBN   string `json:"isbn"`
}

func (s *Server) handleCreateMyRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	var p requestPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil || p.Title == "" {
		writeError(w, http.StatusBadRequest, "title required")
		return
	}
	cfg, err := s.d.Store.GetBackendConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backend config missing")
		return
	}
	reqID := ulid.Make().String()
	status := "pending"
	if cfg.AutoApproveRequests {
		status = "submitted"
	}
	if err := s.d.Store.InsertRequest(r.Context(), store.Request{
		ID:             reqID,
		UserID:         id.UserID,
		Title:          p.Title,
		Author:         p.Author,
		ISBN:           p.ISBN,
		Status:         status,
		TargetPluginID: cfg.TargetBackendPluginID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if status == "submitted" && s.d.Events != nil && cfg.TargetBackendPluginID != "" {
		s.d.Events.Publish(r.Context(), "request_submitted", map[string]any{
			"request_id":        reqID,
			"target_plugin_id":  cfg.TargetBackendPluginID,
			"title":             p.Title,
			"author":            p.Author,
			"isbn":              p.ISBN,
			"requester_user_id": id.UserID,
		})
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"request_id": reqID,
		"status":     status,
	})
}

func (s *Server) handleCancelMyRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	reqID := chi.URLParam(r, "id")
	if err := s.d.Store.CancelRequest(r.Context(), reqID, id.UserID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "request not found or not cancellable")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
