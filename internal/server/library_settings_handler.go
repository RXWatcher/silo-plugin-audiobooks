package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/auth"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
)

// Per-library settings admin surface. Read by anyone (settings are
// public hints — "this library allows explicit" — not secrets);
// write requires admin.

func (s *Server) mountLibrarySettingsRoutes(r chi.Router) {
	r.Get("/libraries/{id}/settings", s.handleGetLibrarySettings)
	r.Put("/admin/libraries/{id}/settings", s.handlePutLibrarySettings)
}

func (s *Server) handleGetLibrarySettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireUser(w, r); !ok {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	ls, err := s.d.Store.GetLibrarySettings(r.Context(), id)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ls)
}

func (s *Server) handlePutLibrarySettings(w http.ResponseWriter, r *http.Request) {
	actor, ok := auth.RequireAdmin(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body store.LibrarySettings
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.d.Store.SetLibrarySettings(r.Context(), id, body); err != nil {
		writeInternal(w, r, err)
		return
	}
	s.audit(r, actor.UserID, "update_library_settings", "portal_library",
		strconv.FormatInt(id, 10), body)
	writeJSON(w, http.StatusOK, body)
}
