package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/auth"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
)

// mountABSStandaloneRoutes wires the user-facing endpoints behind the
// "Allow mobile-app login" toggle. The toggle is meaningful only when the
// admin has set standalone_login_mode = "opt_in"; the GET endpoint surfaces
// the current mode so the SPA can show the toggle conditionally.
func (s *Server) mountABSStandaloneRoutes(r chi.Router) {
	r.Get("/me/abs-standalone", s.handleGetABSStandaloneOptIn)
	r.Post("/me/abs-standalone", s.handleEnableABSStandaloneOptIn)
	r.Delete("/me/abs-standalone", s.handleDisableABSStandaloneOptIn)
}

func (s *Server) handleGetABSStandaloneOptIn(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	cfg, err := s.d.Store.GetBackendConfig(r.Context())
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	enabled, err := s.d.Store.HasStandaloneOptIn(r.Context(), id.UserID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":    store.NormalizeStandaloneLoginMode(cfg.StandaloneLoginMode),
		"enabled": enabled,
	})
}

func (s *Server) handleEnableABSStandaloneOptIn(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	if err := s.d.Store.EnableStandaloneOptIn(r.Context(), id.UserID); err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true})
}

func (s *Server) handleDisableABSStandaloneOptIn(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	if err := s.d.Store.DisableStandaloneOptIn(r.Context(), id.UserID); err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
}
