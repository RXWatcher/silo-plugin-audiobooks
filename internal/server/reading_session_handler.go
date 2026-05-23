package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/auth"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
)

// Reading-session telemetry surface — discrete session records the
// SPA posts at session-close + aggregated read-side endpoints for
// the heatmap + year-in-review.

func (s *Server) mountReadingSessionRoutes(r chi.Router) {
	r.Post("/me/reading-sessions", s.handleRecordReadingSession)
	r.Get("/me/heatmap", s.handleHeatmap)
	r.Get("/me/stats/year/{year}", s.handleYearStats)
}

type readingSessionBody struct {
	BookID        string `json:"book_id"`
	StartedAt     string `json:"started_at"`     // RFC3339
	EndedAt       string `json:"ended_at"`       // RFC3339, optional
	SecondsPlayed int    `json:"seconds_played"`
	DeviceLabel   string `json:"device_label"`
}

func (s *Server) handleRecordReadingSession(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	var body readingSessionBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.BookID == "" {
		writeError(w, http.StatusBadRequest, "book_id required")
		return
	}
	started := time.Now()
	if body.StartedAt != "" {
		if t, err := time.Parse(time.RFC3339, body.StartedAt); err == nil {
			started = t
		}
	}
	var endedPtr *time.Time
	if body.EndedAt != "" {
		if t, err := time.Parse(time.RFC3339, body.EndedAt); err == nil {
			endedPtr = &t
		}
	}
	if body.SecondsPlayed < 0 {
		body.SecondsPlayed = 0
	}
	if body.SecondsPlayed > 12*3600 {
		// 12-hour cap — a single session longer than this is a
		// data-entry bug (the SPA forgot to call close) rather
		// than real listening; capping prevents the year stats
		// from being skewed by stale long-open sessions.
		body.SecondsPlayed = 12 * 3600
	}
	sess := store.ReadingSession{
		ID:            ulid.Make().String(),
		UserID:        id.UserID,
		BookID:        body.BookID,
		StartedAt:     started,
		EndedAt:       endedPtr,
		SecondsPlayed: body.SecondsPlayed,
		DeviceLabel:   body.DeviceLabel,
	}
	if err := s.d.Store.InsertReadingSession(r.Context(), sess); err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, sess)
}

func (s *Server) handleHeatmap(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	days := 365
	if v := r.URL.Query().Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 730 {
			days = n
		}
	}
	rows, err := s.d.Store.ListeningHeatmap(r.Context(), id.UserID, days, time.UTC)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": rows})
}

func (s *Server) handleYearStats(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	year, err := strconv.Atoi(chi.URLParam(r, "year"))
	if err != nil || year < 2000 || year > 2100 {
		writeError(w, http.StatusBadRequest, "invalid year")
		return
	}
	stats, err := s.d.Store.ListeningStatsForYear(r.Context(), id.UserID, year, time.UTC)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}
