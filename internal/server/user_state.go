package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/auth"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
)

// mountUserStateRoutes wires progress, bookmark, and rating endpoints.
func (s *Server) mountUserStateRoutes(r chi.Router) {
	r.Get("/me/progress", s.handleListMyProgress)
	r.Patch("/audiobooks/{id}/progress", s.handleUpsertProgress)
	r.Get("/audiobooks/{id}/bookmarks", s.handleListBookmarks)
	r.Post("/audiobooks/{id}/bookmarks", s.handleCreateBookmark)
	r.Delete("/audiobooks/{id}/bookmarks/{bm_id}", s.handleDeleteBookmark)
	r.Put("/audiobooks/{id}/rating", s.handleUpsertRating)
	r.Delete("/audiobooks/{id}/rating", s.handleDeleteRating)
}

func (s *Server) handleListMyProgress(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	limit := 24
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}
	out, err := s.d.Store.ListRecentProgress(r.Context(), id.UserID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

type progressPayload struct {
	CurrentSeconds int     `json:"current_seconds"`
	ProgressPct    float32 `json:"progress_pct"`
	IsFinished     bool    `json:"is_finished"`
}

func (s *Server) handleUpsertProgress(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	bookID := chi.URLParam(r, "id")
	var p progressPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.d.Store.UpsertProgress(r.Context(), store.Progress{
		UserID:         id.UserID,
		BookID:         bookID,
		CurrentSeconds: p.CurrentSeconds,
		ProgressPct:    p.ProgressPct,
		IsFinished:     p.IsFinished,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleListBookmarks(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	bookID := chi.URLParam(r, "id")
	out, err := s.d.Store.ListBookmarks(r.Context(), id.UserID, bookID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

type bookmarkPayload struct {
	PositionSeconds int    `json:"position_seconds"`
	ChapterID       string `json:"chapter_id"`
	Note            string `json:"note"`
}

func (s *Server) handleCreateBookmark(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	bookID := chi.URLParam(r, "id")
	var p bookmarkPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	bk := store.Bookmark{
		ID:              ulid.Make().String(),
		UserID:          id.UserID,
		BookID:          bookID,
		PositionSeconds: p.PositionSeconds,
		ChapterID:       p.ChapterID,
		Note:            p.Note,
	}
	if err := s.d.Store.InsertBookmark(r.Context(), bk); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, bk)
}

func (s *Server) handleDeleteBookmark(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	bmID := chi.URLParam(r, "bm_id")
	if err := s.d.Store.DeleteBookmark(r.Context(), bmID, id.UserID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type ratingPayload struct {
	Rating int `json:"rating"`
}

func (s *Server) handleUpsertRating(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	bookID := chi.URLParam(r, "id")
	var p ratingPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.d.Store.UpsertRating(r.Context(), store.Rating{
		UserID: id.UserID, BookID: bookID, Rating: p.Rating,
	}); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDeleteRating(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	bookID := chi.URLParam(r, "id")
	if err := s.d.Store.DeleteRating(r.Context(), id.UserID, bookID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
