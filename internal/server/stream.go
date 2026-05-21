package server

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/auth"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/bookref"
)

// mountStreamRoutes wires GET /audiobooks/{id}/files/{idx}/stream.
func (s *Server) mountStreamRoutes(r chi.Router) {
	r.Get("/audiobooks/{id}/files/{idx}/stream", s.handleStream)
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	bookRef := chi.URLParam(r, "id")
	libraryID, bookID, _ := bookref.Decode(bookRef)
	idxStr := chi.URLParam(r, "idx")
	idx, err := strconv.Atoi(idxStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "idx must be int")
		return
	}
	lib, err := s.targetLibrary(r, libraryID)
	if err != nil {
		writeError(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	if s.d.Streaming == nil {
		writeError(w, http.StatusInternalServerError, "streaming router not initialised")
		return
	}
	s.d.Streaming.Stream(w, r, id.UserID, lib.BackendPluginID, bookID, idx)
}
