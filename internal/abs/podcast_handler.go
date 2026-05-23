package abs

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/bookref"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
)

// handlePodcastLibraryItems returns the podcast-shaped catalog for a
// portal_library whose media_type is "podcast". Unlike audiobook
// libraries (which fetch from a backend plugin), podcast libraries are
// stored directly in the audiobooks plugin's database — operators add
// podcasts via the admin endpoints below or, in a follow-up, via an RSS
// feed refresher.
func (h *Handler) handlePodcastLibraryItems(w http.ResponseWriter, r *http.Request, lib store.PortalLibrary) {
	q := r.URL.Query()
	limit, page := readPagedQuery(r, 30)
	minified := q.Get("minified") == "1"
	include := q.Get("include")

	all, err := h.store.ListPodcasts(r.Context(), lib.ID, 0)
	if err != nil {
		http.Error(w, "list podcasts: "+err.Error(), http.StatusInternalServerError)
		return
	}

	total := len(all)
	pageStart, pageEnd := 0, len(all)
	if limit > 0 {
		pageStart = page * limit
		if pageStart > len(all) {
			pageStart = len(all)
		}
		pageEnd = pageStart + limit
		if pageEnd > len(all) {
			pageEnd = len(all)
		}
	}
	pageSlice := all[pageStart:pageEnd]

	results := make([]PodcastItem, 0, len(pageSlice))
	for _, p := range pageSlice {
		// Count episodes for the shelf row badge. Cheap (indexed) and
		// the page slice is bounded by `limit`, so we don't N+1 the
		// entire library — only the visible page.
		episodes, _ := h.store.ListPodcastEpisodes(r.Context(), p.ID, 0)
		encoded := bookref.Encode(p.LibraryID, p.ID)
		results = append(results, ToPodcastSummary(p, len(episodes), encoded))
	}

	writeJSON(w, http.StatusOK, pagedEnvelope(results, total, limit, page, "", false, "", minified, include))
}

// handlePodcastItem returns the full podcast detail with episodes[].
// Called from handleItem after it detects the library is a podcast library.
func (h *Handler) handlePodcastItem(w http.ResponseWriter, r *http.Request, lib store.PortalLibrary, podcastID, encodedID string) {
	p, err := h.store.GetPodcast(r.Context(), podcastID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "podcast not found", http.StatusNotFound)
			return
		}
		http.Error(w, "get podcast: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if p.LibraryID != lib.ID {
		// Defensive — the library id in the encoded ref must match the
		// podcast's owning library. Mismatch means the ref was forged or
		// the podcast was moved.
		http.Error(w, "podcast not found in this library", http.StatusNotFound)
		return
	}
	episodes, err := h.store.ListPodcastEpisodes(r.Context(), p.ID, 0)
	if err != nil {
		http.Error(w, "list episodes: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, ToPodcastItem(p, episodes, encodedID))
}

// handlePlayEpisode starts a session bound to a single podcast episode.
// Real ABS uses /api/items/{podcastId}/play/{episodeId} — we route the
// same path. The returned audioTracks[0] carries the signed contentURL
// the ABS client puts in <audio src>.
//
// Drift note: for audiobooks the contentURL is a signed media token on
// the plugin's /abs/public/session/... route which proxies bytes through
// the standalone listener. For podcast episodes, the audio lives on an
// external CDN (most feeds) — we emit the raw audio URL directly. The
// session token gates progress writes, not byte access.
func (h *Handler) handlePlayEpisode(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	_, cfg, err := h.targetFn(r.Context())
	if err != nil {
		http.Error(w, "config unavailable", http.StatusInternalServerError)
		return
	}
	encodedID := chi.URLParam(r, "id")
	episodeIDRaw := chi.URLParam(r, "episodeId")
	episodeID, _ := DecodePodcastEpisodeID(episodeIDRaw)

	libID, podcastID, _ := bookref.Decode(encodedID)
	if podcastID == "" {
		http.Error(w, "invalid podcast id", http.StatusBadRequest)
		return
	}
	lib, err := h.store.GetPortalLibrary(r.Context(), libID)
	if err != nil || lib.MediaType != "podcast" {
		http.Error(w, "not a podcast library", http.StatusPreconditionFailed)
		return
	}
	podcast, err := h.store.GetPodcast(r.Context(), podcastID)
	if err != nil || podcast.LibraryID != libID {
		http.Error(w, "podcast not found", http.StatusNotFound)
		return
	}
	episode, err := h.store.GetPodcastEpisode(r.Context(), episodeID)
	if err != nil || episode.PodcastID != podcastID {
		http.Error(w, "episode not found", http.StatusNotFound)
		return
	}

	var playPayload struct {
		DeviceInfo  map[string]any `json:"deviceInfo"`
		MediaPlayer string         `json:"mediaPlayer"`
	}
	_ = json.NewDecoder(r.Body).Decode(&playPayload)
	deviceID, _ := playPayload.DeviceInfo["deviceId"].(string)
	if deviceID == "" {
		deviceID = "unknown"
	}

	sessionID := ulid.Make().String()
	// We store the encoded EPISODE id in book_id so the same session
	// handlers (sync/close) work without branching: progress writes are
	// dispatched by the "pe_" prefix.
	encodedEpisodeID := EncodePodcastEpisodeID(episode.ID)
	sess := store.ABSSession{
		ID:          sessionID,
		UserID:      a.UserID,
		ProfileID:   a.ProfileID,
		BookID:      encodedEpisodeID,
		DeviceID:    deviceID,
		DeviceInfo:  playPayload.DeviceInfo,
		MediaPlayer: playPayload.MediaPlayer,
	}
	if err := h.store.InsertABSSession(r.Context(), sess); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Session token gates progress writes — we still mint one even though
	// audio bytes come from the external CDN, because the client uses
	// this token on /session/{sid} sync calls.
	tok, _ := IssueSessionToken(cfg.ABSJWTSecret, a.UserID, sessionID, encodedEpisodeID, 0, 6*time.Hour)

	h.publish(a.UserID, "user_session_open", map[string]any{
		"id":            sessionID,
		"libraryItemId": encodedID,
		"episodeId":     encodedEpisodeID,
		"deviceId":      deviceID,
		"mediaPlayer":   playPayload.MediaPlayer,
	})
	h.broadcastListenerCount(r.Context())

	// Resume position: read prior per-episode progress so the player
	// seeds startTime/currentTime correctly. Without these the mobile
	// player loads at 0 and gets stuck in BUFFERING — same root cause
	// as the audiobook handlePlay fix. Podcast progress is keyed on
	// (userID, episodeID); not profile-scoped today.
	var currentTime float64
	if prog, err := h.store.GetPodcastEpisodeProgress(r.Context(), a.UserID, episode.ID); err == nil {
		currentTime = float64(prog.CurrentSeconds)
	}
	nowMs := time.Now().UnixMilli()
	writeJSON(w, http.StatusOK, map[string]any{
		"id":            sessionID,
		"userId":        a.UserID,
		"libraryItemId": encodedID,
		"episodeId":     encodedEpisodeID,
		"mediaType":     "podcast",
		"playMethod":    0,
		"mediaPlayer":   playPayload.MediaPlayer,
		"deviceInfo":    playPayload.DeviceInfo,
		"serverVersion": ServerVersion,
		"audioTracks": []map[string]any{{
			// ABS uses 1-based file indexing on the wire — the mobile
			// audio player does `track.index || 1`, which silently
			// converts a 0 to 1. See handlePlay's translate-comment
			// for the full story. Podcasts only ever have one track.
			"index":       1,
			"startOffset": 0.0,
			"contentUrl":  episode.AudioURL,
			"mimeType":    episode.AudioMimeType,
			"duration":    float64(episode.DurationSeconds),
		}},
		"chapters":      []ChapterABS{},
		"duration":      float64(episode.DurationSeconds),
		"currentTime":   currentTime,
		"startTime":     currentTime,
		"timeListening": 0,
		"startedAt":     nowMs,
		"updatedAt":     nowMs,
		"sessionToken":  tok,
	})
}

// podcastProgressToABS shapes per-episode progress into the ABS payload
// real clients expect. Mirrors progressToABS for audiobook progress but
// keys on episodeId rather than libraryItemId, since podcast progress
// lives per-episode in the ABS data model.
func podcastProgressToABS(userID string, p store.PodcastEpisodeProgress) map[string]any {
	return map[string]any{
		"id":             userID + "-" + p.EpisodeID,
		"libraryItemId":  EncodePodcastEpisodeID(p.EpisodeID),
		"episodeId":      EncodePodcastEpisodeID(p.EpisodeID),
		"currentTime":    p.CurrentSeconds,
		"progress":       p.ProgressPct,
		"isFinished":     p.IsFinished,
		"lastUpdate":     p.UpdatedAt.UnixMilli(),
	}
}

// makePodcastProgress turns a PATCH body + existing progress row into the
// next progress shape. Same merge semantics as audiobook progress: fields
// absent from the body keep their current value, so sync ticks don't
// silently un-finish a manually-marked episode.
func makePodcastProgress(userID, episodeID string, cur store.PodcastEpisodeProgress, body progressBody) store.PodcastEpisodeProgress {
	next := store.PodcastEpisodeProgress{
		UserID:         userID,
		EpisodeID:      episodeID,
		CurrentSeconds: cur.CurrentSeconds,
		ProgressPct:    cur.ProgressPct,
		IsFinished:     cur.IsFinished,
	}
	if body.CurrentTime != nil {
		next.CurrentSeconds = int(*body.CurrentTime)
	}
	if body.Progress != nil {
		next.ProgressPct = float32(*body.Progress)
	}
	if body.IsFinished != nil {
		next.IsFinished = *body.IsFinished
	}
	return next
}

