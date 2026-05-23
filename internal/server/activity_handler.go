package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/auth"
)

// Per-book activity timeline — merged events the SPA renders on
// the book detail page.

func (s *Server) mountActivityRoutes(r chi.Router) {
	r.Get("/me/books/{id}/activity", s.handleBookActivity)
}

func (s *Server) handleBookActivity(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	bookID := chi.URLParam(r, "id")
	events, err := s.d.Store.BookActivity(r.Context(), id.UserID, profileID(r), bookID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}
