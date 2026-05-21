package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/auth"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
)

// mountPodcastAdminRoutes wires admin CRUD on podcasts + episodes.
// Operators use these to seed podcasts manually until the RSS feed
// refresher lands as a follow-up. Listing and read endpoints are
// available to any authenticated user; mutations are admin-gated.
func (s *Server) mountPodcastAdminRoutes(r chi.Router) {
	// Catalog reads — any authenticated portal user.
	r.Get("/podcasts", s.handleListPodcasts)
	r.Get("/podcasts/{id}", s.handleGetPodcast)
	r.Get("/podcasts/{id}/episodes", s.handleListPodcastEpisodes)

	// Mutations — admin only.
	r.Post("/admin/podcasts", s.handleCreatePodcast)
	r.Patch("/admin/podcasts/{id}", s.handleUpdatePodcast)
	r.Delete("/admin/podcasts/{id}", s.handleDeletePodcast)
	r.Post("/admin/podcasts/{id}/episodes", s.handleCreatePodcastEpisode)
	r.Delete("/admin/podcasts/{id}/episodes/{episodeId}", s.handleDeletePodcastEpisode)
	r.Post("/admin/podcasts/{id}/refresh", s.handleRefreshPodcast)
}

type podcastBody struct {
	ID                     string  `json:"id"`
	LibraryID              int64   `json:"library_id"`
	Title                  string  `json:"title"`
	Author                 string  `json:"author"`
	Description            string  `json:"description"`
	CoverURL               string  `json:"cover_url"`
	Language               string  `json:"language"`
	Explicit               bool    `json:"explicit"`
	ITunesCategory         string  `json:"itunes_category"`
	FeedURL                string  `json:"feed_url"`
	RefreshIntervalMinutes int     `json:"refresh_interval_minutes"`
}

type podcastPatchBody struct {
	Title                  *string `json:"title"`
	Author                 *string `json:"author"`
	Description            *string `json:"description"`
	CoverURL               *string `json:"cover_url"`
	Language               *string `json:"language"`
	Explicit               *bool   `json:"explicit"`
	ITunesCategory         *string `json:"itunes_category"`
	FeedURL                *string `json:"feed_url"`
	RefreshIntervalMinutes *int    `json:"refresh_interval_minutes"`
}

type podcastEpisodeBody struct {
	GUID            string `json:"guid"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	AudioURL        string `json:"audio_url"`
	AudioMimeType   string `json:"audio_mime_type"`
	AudioBytes      int64  `json:"audio_bytes"`
	DurationSeconds int    `json:"duration_seconds"`
	EpisodeIndex    *int   `json:"episode_index"`
	SeasonIndex     *int   `json:"season_index"`
	PublishedAt     string `json:"published_at"` // RFC3339
	CoverURL        string `json:"cover_url"`
}

func (s *Server) handleListPodcasts(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireUser(w, r); !ok {
		return
	}
	libraryID := int64(0)
	if v := r.URL.Query().Get("library_id"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			libraryID = n
		}
	}
	out, err := s.d.Store.ListPodcasts(r.Context(), libraryID, 0)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleGetPodcast(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireUser(w, r); !ok {
		return
	}
	p, err := s.d.Store.GetPodcast(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "podcast not found")
		return
	}
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleListPodcastEpisodes(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireUser(w, r); !ok {
		return
	}
	out, err := s.d.Store.ListPodcastEpisodes(r.Context(), chi.URLParam(r, "id"), 0)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleCreatePodcast(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	var body podcastBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Title == "" || body.LibraryID <= 0 {
		writeError(w, http.StatusBadRequest, "title and library_id required")
		return
	}
	if body.ID == "" {
		body.ID = ulid.Make().String()
	}
	if body.RefreshIntervalMinutes <= 0 {
		body.RefreshIntervalMinutes = 360
	}
	p := store.Podcast{
		ID:                     body.ID,
		LibraryID:              body.LibraryID,
		Title:                  body.Title,
		Author:                 body.Author,
		Description:            body.Description,
		CoverURL:               body.CoverURL,
		Language:               body.Language,
		Explicit:               body.Explicit,
		ITunesCategory:         body.ITunesCategory,
		FeedURL:                body.FeedURL,
		RefreshIntervalMinutes: body.RefreshIntervalMinutes,
	}
	if err := s.d.Store.UpsertPodcast(r.Context(), p); err != nil {
		writeInternal(w, r, err)
		return
	}
	created, err := s.d.Store.GetPodcast(r.Context(), p.ID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleUpdatePodcast(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	cur, err := s.d.Store.GetPodcast(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "podcast not found")
		return
	}
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	var body podcastPatchBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Title != nil {
		cur.Title = *body.Title
	}
	if body.Author != nil {
		cur.Author = *body.Author
	}
	if body.Description != nil {
		cur.Description = *body.Description
	}
	if body.CoverURL != nil {
		cur.CoverURL = *body.CoverURL
	}
	if body.Language != nil {
		cur.Language = *body.Language
	}
	if body.Explicit != nil {
		cur.Explicit = *body.Explicit
	}
	if body.ITunesCategory != nil {
		cur.ITunesCategory = *body.ITunesCategory
	}
	if body.FeedURL != nil {
		cur.FeedURL = *body.FeedURL
	}
	if body.RefreshIntervalMinutes != nil && *body.RefreshIntervalMinutes > 0 {
		cur.RefreshIntervalMinutes = *body.RefreshIntervalMinutes
	}
	if err := s.d.Store.UpsertPodcast(r.Context(), cur); err != nil {
		writeInternal(w, r, err)
		return
	}
	updated, err := s.d.Store.GetPodcast(r.Context(), cur.ID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeletePodcast(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	if err := s.d.Store.DeletePodcast(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeInternal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCreatePodcastEpisode(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	podcastID := chi.URLParam(r, "id")
	if _, err := s.d.Store.GetPodcast(r.Context(), podcastID); err != nil {
		writeError(w, http.StatusNotFound, "podcast not found")
		return
	}
	var body podcastEpisodeBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Title == "" || body.AudioURL == "" {
		writeError(w, http.StatusBadRequest, "title and audio_url required")
		return
	}
	if body.GUID == "" {
		// When a GUID isn't supplied (manual seeding), generate a
		// stable random one so feed refreshes don't accidentally upsert
		// against this row.
		body.GUID = "manual-" + ulid.Make().String()
	}
	var publishedAt *time.Time
	if body.PublishedAt != "" {
		if t, err := time.Parse(time.RFC3339, body.PublishedAt); err == nil {
			publishedAt = &t
		}
	}
	e := store.PodcastEpisode{
		ID:              ulid.Make().String(),
		PodcastID:       podcastID,
		GUID:            body.GUID,
		Title:           body.Title,
		Description:     body.Description,
		AudioURL:        body.AudioURL,
		AudioMimeType:   body.AudioMimeType,
		AudioBytes:      body.AudioBytes,
		DurationSeconds: body.DurationSeconds,
		EpisodeIndex:    body.EpisodeIndex,
		SeasonIndex:     body.SeasonIndex,
		PublishedAt:     publishedAt,
		CoverURL:        body.CoverURL,
	}
	if err := s.d.Store.UpsertPodcastEpisode(r.Context(), e); err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, e)
}

func (s *Server) handleDeletePodcastEpisode(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	if err := s.d.Store.DeletePodcastEpisode(r.Context(), chi.URLParam(r, "episodeId")); err != nil {
		writeInternal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRefreshPodcast(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	if s.d.PodcastFeed == nil {
		writeError(w, http.StatusServiceUnavailable, "feed refresher not configured")
		return
	}
	p, err := s.d.Store.GetPodcast(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "podcast not found")
		return
	}
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	if err := s.d.PodcastFeed.RefreshOne(r.Context(), s.d.Store, p); err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	updated, _ := s.d.Store.GetPodcast(r.Context(), p.ID)
	writeJSON(w, http.StatusOK, updated)
}
