package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/auth"
	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/store"
)

// mountUserStateRoutes wires progress, bookmark, and rating endpoints.
func (s *Server) mountUserStateRoutes(r chi.Router) {
	r.Get("/me/progress", s.handleListMyProgress)
	r.Get("/me/streak", s.handleGetStreak)
	r.Get("/me/listening-stats/{id}", s.handleGetListeningStats)
	r.Get("/me/playback-sessions", s.handleListMyPlaybackSessions)
	r.Patch("/audiobooks/{id}/progress", s.handleUpsertProgress)
	r.Post("/audiobooks/{id}/playback-session", s.handleCreatePlaybackSession)
	r.Patch("/playback-sessions/{sid}", s.handleUpdatePlaybackSession)
	r.Post("/playback-sessions/{sid}/close", s.handleClosePlaybackSession)
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
	out, err := s.d.Store.ListRecentProgress(r.Context(), id.UserID, profileID(r), limit)
	if err != nil {
		writeInternal(w, r, err)
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
		ProfileID:      profileID(r),
		BookID:         bookID,
		CurrentSeconds: p.CurrentSeconds,
		ProgressPct:    p.ProgressPct,
		IsFinished:     p.IsFinished,
	}); err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type playbackSessionCreatePayload struct {
	DeviceID       string         `json:"device_id"`
	DeviceInfo     map[string]any `json:"device_info"`
	CurrentSeconds int            `json:"current_seconds"`
	MediaPlayer    string         `json:"media_player"`
}

type playbackSessionSyncPayload struct {
	CurrentSeconds *int     `json:"current_seconds"`
	Duration       *int     `json:"duration_seconds"`
	ProgressPct    *float32 `json:"progress_pct"`
	IsFinished     *bool    `json:"is_finished"`
	TimeListened   *int     `json:"time_listened_seconds"`
}

func (s *Server) handleGetListeningStats(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	bookID := chi.URLParam(r, "id")
	stats, err := s.d.Store.GetListeningStats(r.Context(), id.UserID, bookID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusOK, store.ListeningStats{UserID: id.UserID, BookID: bookID})
			return
		}
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleListMyPlaybackSessions(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	sessions, err := s.d.Store.ListActiveABSSessionsForUser(r.Context(), id.UserID, profileID(r), 100)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": sessions})
}

func (s *Server) handleCreatePlaybackSession(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	bookID := chi.URLParam(r, "id")
	var p playbackSessionCreatePayload
	_ = json.NewDecoder(r.Body).Decode(&p)
	deviceID := strings.TrimSpace(p.DeviceID)
	if deviceID == "" {
		deviceID = "continuum-web"
	}
	mediaPlayer := strings.TrimSpace(p.MediaPlayer)
	if mediaPlayer == "" {
		mediaPlayer = "continuum-web"
	}
	info := p.DeviceInfo
	if info == nil {
		info = map[string]any{}
	}
	if ua := r.UserAgent(); ua != "" {
		info["userAgent"] = ua
	}
	sess := store.ABSSession{
		ID:          ulid.Make().String(),
		UserID:      id.UserID,
		ProfileID:   profileID(r),
		BookID:      bookID,
		DeviceID:    deviceID,
		DeviceInfo:  info,
		MediaPlayer: mediaPlayer,
		StartTime:   p.CurrentSeconds,
		CurrentTime: p.CurrentSeconds,
	}
	if err := s.d.Store.InsertABSSession(r.Context(), sess); err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":              sess.ID,
		"book_id":         sess.BookID,
		"current_seconds": sess.CurrentTime,
	})
}

func (s *Server) handleUpdatePlaybackSession(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	sid := chi.URLParam(r, "sid")
	var p playbackSessionSyncPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	sess, ok := s.playbackSessionForOwner(w, r, sid, id.UserID)
	if !ok {
		return
	}
	current := sess.CurrentTime
	if p.CurrentSeconds != nil {
		current = *p.CurrentSeconds
	}
	listened := 0
	if p.TimeListened != nil && *p.TimeListened > 0 {
		listened = *p.TimeListened
	}
	if err := s.d.Store.UpdateABSSession(r.Context(), sid, current, listened); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusConflict, "playback session is closed")
			return
		}
		writeInternal(w, r, err)
		return
	}
	if err := s.syncPlaybackProgress(r, id.UserID, sess.BookID, current, p); err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleClosePlaybackSession(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	sid := chi.URLParam(r, "sid")
	var p playbackSessionSyncPayload
	_ = json.NewDecoder(r.Body).Decode(&p)
	sess, ok := s.playbackSessionForOwner(w, r, sid, id.UserID)
	if !ok {
		return
	}
	current := sess.CurrentTime
	if p.CurrentSeconds != nil {
		current = *p.CurrentSeconds
	}
	if err := s.syncPlaybackProgress(r, id.UserID, sess.BookID, current, p); err != nil {
		writeInternal(w, r, err)
		return
	}
	if err := s.d.Store.CloseABSSession(r.Context(), sid); err != nil {
		writeInternal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) playbackSessionForOwner(w http.ResponseWriter, r *http.Request, sid string, userID string) (store.ABSSession, bool) {
	sess, err := s.d.Store.GetABSSession(r.Context(), sid)
	if err != nil || sess.UserID != userID {
		writeError(w, http.StatusNotFound, "playback session not found")
		return store.ABSSession{}, false
	}
	if sess.ClosedAt != nil {
		writeError(w, http.StatusConflict, "playback session is closed")
		return store.ABSSession{}, false
	}
	return sess, true
}

func (s *Server) syncPlaybackProgress(r *http.Request, userID, bookID string, current int, p playbackSessionSyncPayload) error {
	if p.TimeListened != nil && *p.TimeListened > 0 {
		if err := s.d.Store.AddListeningStats(r.Context(), userID, bookID, *p.TimeListened, current); err != nil {
			return err
		}
	}
	if p.Duration == nil && p.ProgressPct == nil && p.IsFinished == nil {
		return s.d.Store.UpdateProgressPosition(r.Context(), userID, profileID(r), bookID, current)
	}
	progressPct := float32(0)
	isFinished := false
	cur, err := s.d.Store.GetProgress(r.Context(), userID, profileID(r), bookID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if err == nil {
		progressPct = cur.ProgressPct
		isFinished = cur.IsFinished
	}
	if p.Duration != nil && *p.Duration > 0 {
		progressPct = float32(current) / float32(*p.Duration)
	}
	if p.ProgressPct != nil {
		progressPct = *p.ProgressPct
	}
	if p.IsFinished != nil {
		isFinished = *p.IsFinished
	}
	if progressPct >= 0.95 {
		isFinished = true
	}
	return s.d.Store.UpsertProgress(r.Context(), store.Progress{
		UserID:         userID,
		ProfileID:      profileID(r),
		BookID:         bookID,
		CurrentSeconds: current,
		ProgressPct:    progressPct,
		IsFinished:     isFinished,
	})
}

func (s *Server) handleListBookmarks(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	bookID := chi.URLParam(r, "id")
	out, err := s.d.Store.ListBookmarks(r.Context(), id.UserID, profileID(r), bookID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// handleGetStreak — GET /api/v1/me/streak
// Returns {current, longest, last_active_date} computed from
// progress.updated_at distinct dates. Timezone is UTC by default; a
// future per-user TZ setting would override.
func (s *Server) handleGetStreak(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	streak, err := s.d.Store.StreakForUser(r.Context(), id.UserID, profileID(r), time.UTC)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, streak)
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
		ProfileID:       profileID(r),
		BookID:          bookID,
		PositionSeconds: p.PositionSeconds,
		ChapterID:       p.ChapterID,
		Note:            p.Note,
	}
	if err := s.d.Store.InsertBookmark(r.Context(), bk); err != nil {
		writeInternal(w, r, err)
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
	if err := s.d.Store.DeleteBookmark(r.Context(), bmID, id.UserID, profileID(r)); err != nil {
		writeInternal(w, r, err)
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
		writeInternal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
