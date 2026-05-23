package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/auth"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
)

// Reading goals — per-user yearly targets for books finished or
// hours listened. Progress is derived read-side from existing
// progress + reading_session data, so the goal table is metadata-
// only and a brand-new goal immediately reflects historic progress.

func (s *Server) mountReadingGoalRoutes(r chi.Router) {
	r.Get("/me/goals", s.handleListGoals)
	r.Put("/me/goals/{year}/{kind}", s.handlePutGoal)
	r.Delete("/me/goals/{year}/{kind}", s.handleDeleteGoal)
	r.Get("/me/goals/progress", s.handleGoalProgress)
}

func (s *Server) handleListGoals(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	year := 0
	if v := r.URL.Query().Get("year"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			year = n
		}
	}
	rows, err := s.d.Store.ListReadingGoals(r.Context(), id.UserID, year)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
}

func (s *Server) handlePutGoal(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	year, err := strconv.Atoi(chi.URLParam(r, "year"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid year")
		return
	}
	kind := chi.URLParam(r, "kind")
	var body struct {
		Target int `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	g := store.ReadingGoal{
		UserID: id.UserID,
		Year:   year,
		Kind:   kind,
		Target: body.Target,
	}
	if err := s.d.Store.UpsertReadingGoal(r.Context(), g); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) handleDeleteGoal(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	year, err := strconv.Atoi(chi.URLParam(r, "year"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid year")
		return
	}
	if err := s.d.Store.DeleteReadingGoal(r.Context(), id.UserID, year, chi.URLParam(r, "kind")); err != nil {
		writeInternal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGoalProgress returns each of the user's goals plus its
// derived progress (actual / percent / on-pace flag). Defaults to
// the current year when no ?year= query is supplied.
func (s *Server) handleGoalProgress(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	year := time.Now().UTC().Year()
	if v := r.URL.Query().Get("year"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 2000 {
			year = n
		}
	}
	prog, err := s.d.Store.GoalProgressForUser(r.Context(), id.UserID, profileID(r), year, time.UTC)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"year":  year,
		"goals": prog,
	})
}
