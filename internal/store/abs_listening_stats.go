package store

import (
	"context"
	"fmt"
	"time"
)

// ABSListeningItemStat is one row of /me/listening-stats's items map.
// Keyed by encoded book id so the mobile client can look it up in its
// catalog state. LastUpdateMs is epoch ms; the mobile client sorts the
// per-item list on the stats page by this.
type ABSListeningItemStat struct {
	BookID         string
	TimeListeningS int64
	LastUpdateMs   int64
}

// ABSListeningSessionSummary is the slim row shape for
// /me/listening-stats's recentSessions array. Mobile renders title +
// minutes from this; mediaMetadata is optional and we omit it (the
// title-bearing stats.vue branch guards `item.mediaMetadata ?`).
type ABSListeningSessionSummary struct {
	ID             string
	BookID         string
	TimeListeningS int64
	UpdatedAtMs    int64
}

// ABSListeningStatsAggregate is the shape /me/listening-stats serves.
// All numbers come from abs_playback_session.time_listening_seconds —
// the delta per sync tick the mobile client accumulates as it listens.
type ABSListeningStatsAggregate struct {
	TotalTimeS     int64
	Items          []ABSListeningItemStat
	Days           map[string]int64 // YYYY-MM-DD → seconds
	DayOfWeek      map[int]int64    // 0 (Sunday) – 6 → seconds
	TodayS         int64
	RecentSessions []ABSListeningSessionSummary
}

// ABSYearStatsAggregate is the year-in-review shape /me/stats/year/{year}
// serves. topAuthors / topGenres / mostListenedNarrator stay empty in
// this implementation because the session row doesn't carry author /
// genre / narrator metadata; the YearInReview.vue client guards the
// optional ones with `if (...)`, so empty arrays render cleanly.
type ABSYearStatsAggregate struct {
	TotalListeningSessions     int
	TotalListeningTimeS        int64
	TotalBookListeningTimeS    int64
	TotalPodcastListeningTimeS int64
	NumBooksFinished           int
	NumBooksListened           int
}

// ABSListeningStatsForUser aggregates non-zero listening sessions for a
// (user, profile) into the /me/listening-stats response shape. The four
// aggregations (totals, per-item, per-day, per-DOW) all touch the
// abs_session_user_time_idx partial index from migration 0037, so this
// is cheap even on a long history of opened-then-immediately-closed
// sessions.
func (s *Store) ABSListeningStatsForUser(ctx context.Context, userID, profileID string) (ABSListeningStatsAggregate, error) {
	out := ABSListeningStatsAggregate{
		Days:      map[string]int64{},
		DayOfWeek: map[int]int64{},
	}

	if err := s.pool.QueryRow(ctx, `
		SELECT
		    COALESCE(SUM(time_listening_seconds), 0),
		    COALESCE(SUM(CASE WHEN date_trunc('day', started_at) = date_trunc('day', now())
		                      THEN time_listening_seconds ELSE 0 END), 0)
		FROM abs_playback_session
		WHERE user_id = $1 AND profile_id = $2 AND time_listening_seconds > 0
	`, userID, profileID).Scan(&out.TotalTimeS, &out.TodayS); err != nil {
		return out, fmt.Errorf("abs listening totals: %w", err)
	}

	itemRows, err := s.pool.Query(ctx, `
		SELECT book_id, SUM(time_listening_seconds), MAX(last_update)
		FROM abs_playback_session
		WHERE user_id = $1 AND profile_id = $2 AND time_listening_seconds > 0
		GROUP BY book_id
		ORDER BY 2 DESC
		LIMIT 200
	`, userID, profileID)
	if err != nil {
		return out, fmt.Errorf("abs listening items: %w", err)
	}
	for itemRows.Next() {
		var it ABSListeningItemStat
		var upd time.Time
		if err := itemRows.Scan(&it.BookID, &it.TimeListeningS, &upd); err != nil {
			itemRows.Close()
			return out, fmt.Errorf("scan abs item: %w", err)
		}
		it.LastUpdateMs = upd.UnixMilli()
		out.Items = append(out.Items, it)
	}
	itemRows.Close()
	if err := itemRows.Err(); err != nil {
		return out, err
	}

	dayRows, err := s.pool.Query(ctx, `
		SELECT to_char(date_trunc('day', started_at), 'YYYY-MM-DD') AS d,
		       SUM(time_listening_seconds)
		FROM abs_playback_session
		WHERE user_id = $1 AND profile_id = $2 AND time_listening_seconds > 0
		GROUP BY d
	`, userID, profileID)
	if err != nil {
		return out, fmt.Errorf("abs listening days: %w", err)
	}
	for dayRows.Next() {
		var d string
		var n int64
		if err := dayRows.Scan(&d, &n); err != nil {
			dayRows.Close()
			return out, fmt.Errorf("scan day: %w", err)
		}
		out.Days[d] = n
	}
	dayRows.Close()
	if err := dayRows.Err(); err != nil {
		return out, err
	}

	dowRows, err := s.pool.Query(ctx, `
		SELECT EXTRACT(DOW FROM started_at)::int AS d,
		       SUM(time_listening_seconds)
		FROM abs_playback_session
		WHERE user_id = $1 AND profile_id = $2 AND time_listening_seconds > 0
		GROUP BY d
	`, userID, profileID)
	if err != nil {
		return out, fmt.Errorf("abs listening dow: %w", err)
	}
	for dowRows.Next() {
		var d int
		var n int64
		if err := dowRows.Scan(&d, &n); err != nil {
			dowRows.Close()
			return out, fmt.Errorf("scan dow: %w", err)
		}
		out.DayOfWeek[d] = n
	}
	dowRows.Close()
	if err := dowRows.Err(); err != nil {
		return out, err
	}

	recentRows, err := s.pool.Query(ctx, `
		SELECT id, book_id, time_listening_seconds, last_update
		FROM abs_playback_session
		WHERE user_id = $1 AND profile_id = $2 AND time_listening_seconds > 0
		ORDER BY last_update DESC
		LIMIT 10
	`, userID, profileID)
	if err != nil {
		return out, fmt.Errorf("abs recent sessions: %w", err)
	}
	for recentRows.Next() {
		var r ABSListeningSessionSummary
		var upd time.Time
		if err := recentRows.Scan(&r.ID, &r.BookID, &r.TimeListeningS, &upd); err != nil {
			recentRows.Close()
			return out, fmt.Errorf("scan recent: %w", err)
		}
		r.UpdatedAtMs = upd.UnixMilli()
		out.RecentSessions = append(out.RecentSessions, r)
	}
	recentRows.Close()
	return out, recentRows.Err()
}

// ABSYearStatsForUser computes the year-in-review aggregates the ABS
// /me/stats/year/{year} endpoint exposes. Podcast episode sessions are
// flagged by the "pe_" prefix on book_id (the audiobooks plugin's own
// encoding); we partition the totals so the mobile app's separate
// book/podcast counters reflect the partition.
func (s *Store) ABSYearStatsForUser(ctx context.Context, userID, profileID string, year int) (ABSYearStatsAggregate, error) {
	var out ABSYearStatsAggregate
	startOfYear := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)
	startOfNextYear := startOfYear.AddDate(1, 0, 0)

	if err := s.pool.QueryRow(ctx, `
		SELECT
		    COUNT(*) FILTER (WHERE time_listening_seconds > 0),
		    COALESCE(SUM(time_listening_seconds), 0),
		    COALESCE(SUM(CASE WHEN book_id LIKE 'pe_%' THEN 0 ELSE time_listening_seconds END), 0),
		    COALESCE(SUM(CASE WHEN book_id LIKE 'pe_%' THEN time_listening_seconds ELSE 0 END), 0),
		    COUNT(DISTINCT book_id) FILTER (WHERE time_listening_seconds > 0)
		FROM abs_playback_session
		WHERE user_id = $1 AND profile_id = $2
		  AND started_at >= $3 AND started_at < $4
	`, userID, profileID, startOfYear, startOfNextYear).Scan(
		&out.TotalListeningSessions, &out.TotalListeningTimeS,
		&out.TotalBookListeningTimeS, &out.TotalPodcastListeningTimeS,
		&out.NumBooksListened,
	); err != nil {
		return out, fmt.Errorf("year totals: %w", err)
	}

	if err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM progress
		WHERE user_id = $1 AND profile_id = $2
		  AND is_finished = TRUE
		  AND updated_at >= $3 AND updated_at < $4
	`, userID, profileID, startOfYear, startOfNextYear).Scan(&out.NumBooksFinished); err != nil {
		return out, fmt.Errorf("year finished: %w", err)
	}
	return out, nil
}
