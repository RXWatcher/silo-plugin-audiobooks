package abs

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// handleListeningStats serves /api/me/listening-stats. The mobile stats
// page (audiobookshelf-app/pages/stats.vue:96-104) reads totalTime, days,
// recentSessions, and per-item timeListening. Everything is computed from
// abs_playback_session.time_listening_seconds, accumulated on each sync
// tick via UpdateABSSession. Mediametadata on items / recent sessions is
// optional; we omit it because the session row doesn't carry it, and the
// client renders the title with a `item.mediaMetadata ?` guard.
func (h *Handler) handleListeningStats(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	agg, err := h.store.ABSListeningStatsForUser(r.Context(), a.UserID, a.ProfileID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// The mobile client iterates items as a map keyed by libraryItemId;
	// we shape it as such even though Go could emit a slice. dayOfWeek
	// is keyed by 0-6 (Sunday-Saturday) — Postgres EXTRACT(DOW) matches
	// JavaScript's Date.getDay() so no translation needed.
	items := make(map[string]any, len(agg.Items))
	for _, it := range agg.Items {
		items[it.BookID] = map[string]any{
			"id":            it.BookID,
			"timeListening": it.TimeListeningS,
			"lastUpdate":    it.LastUpdateMs,
		}
	}
	dow := make(map[string]int64, len(agg.DayOfWeek))
	for k, v := range agg.DayOfWeek {
		dow[strconv.Itoa(k)] = v
	}
	recent := make([]map[string]any, 0, len(agg.RecentSessions))
	for _, sr := range agg.RecentSessions {
		recent = append(recent, map[string]any{
			"id":            sr.ID,
			"libraryItemId": sr.BookID,
			"timeListening": sr.TimeListeningS,
			"updatedAt":     sr.UpdatedAtMs,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"totalTime":      agg.TotalTimeS,
		"items":          items,
		"days":           agg.Days,
		"dayOfWeek":      dow,
		"today":          agg.TodayS,
		"recentSessions": recent,
	})
}

// handleYearStats serves /api/me/stats/year/{year}. The YearInReview.vue
// screen renders totalListeningTime, totalListeningSessions,
// numBooksFinished, numBooksListened; topAuthors / topGenres /
// mostListenedNarrator / mostListenedMonth are optional (each guarded
// with `if (...)` on the client) and we emit empty/null because the
// session row doesn't carry author/genre/narrator metadata.
//
// Year format: URL path param, validated 2000-9999 to mirror real
// ABS MeController.getStatsForYear:507-515.
func (h *Handler) handleYearStats(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	year, err := strconv.Atoi(chi.URLParam(r, "year"))
	if err != nil || year < 2000 || year > 9999 {
		http.Error(w, "invalid year", http.StatusBadRequest)
		return
	}
	agg, err := h.store.ABSYearStatsForUser(r.Context(), a.UserID, a.ProfileID, year)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"totalListeningSessions":     agg.TotalListeningSessions,
		"totalListeningTime":         agg.TotalListeningTimeS,
		"totalBookListeningTime":     agg.TotalBookListeningTimeS,
		"totalPodcastListeningTime":  agg.TotalPodcastListeningTimeS,
		"numBooksFinished":           agg.NumBooksFinished,
		"numBooksListened":           agg.NumBooksListened,
		"topAuthors":                 []any{},
		"topGenres":                  []any{},
		"mostListenedNarrator":       nil,
		"mostListenedMonth":          nil,
		"longestAudiobookFinished":   nil,
		"booksWithCovers":            []any{},
		"finishedBooksWithCovers":    []any{},
	})
}
