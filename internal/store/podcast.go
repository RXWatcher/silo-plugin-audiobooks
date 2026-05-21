package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Podcast represents a feed-or-manually-seeded podcast in the portal.
// The catalog row is plugin-owned; audio bytes live wherever
// PodcastEpisode.AudioURL points (typically external CDN).
type Podcast struct {
	ID                     string
	LibraryID              int64
	Title                  string
	Author                 string
	Description            string
	CoverURL               string
	Language               string
	Explicit               bool
	ITunesCategory         string
	FeedURL                string
	LastRefreshedAt        *time.Time
	RefreshIntervalMinutes int
	LastError              string
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// PodcastEpisode is one playable episode of a Podcast. The unique
// (podcast_id, guid) constraint at the DB level lets feed refreshers
// upsert episodes without producing duplicates when the source re-emits.
type PodcastEpisode struct {
	ID              string
	PodcastID       string
	GUID            string
	Title           string
	Description     string
	AudioURL        string
	AudioMimeType   string
	AudioBytes      int64
	DurationSeconds int
	EpisodeIndex    *int
	SeasonIndex     *int
	PublishedAt     *time.Time
	CoverURL        string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// PodcastEpisodeProgress is a single user's per-episode progress. Mirrors
// the audiobook Progress shape so the ABS /me/progress/{id} handler can
// dispatch to either table without the client noticing.
type PodcastEpisodeProgress struct {
	UserID         string
	EpisodeID      string
	CurrentSeconds int
	ProgressPct    float32
	IsFinished     bool
	UpdatedAt      time.Time
}

// UpsertPodcast inserts or updates a podcast by ID. The Created/UpdatedAt
// fields are managed by the DB; the caller's values for those are ignored.
func (s *Store) UpsertPodcast(ctx context.Context, p Podcast) error {
	if p.ID == "" || p.Title == "" || p.LibraryID <= 0 {
		return errors.New("id, title, library_id required")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO podcast (
			id, library_id, title, author, description, cover_url, language,
			explicit, itunes_category, feed_url, refresh_interval_minutes
		) VALUES ($1, $2, $3, NULLIF($4,''), NULLIF($5,''), NULLIF($6,''),
			NULLIF($7,''), $8, NULLIF($9,''), NULLIF($10,''), $11)
		ON CONFLICT (id) DO UPDATE SET
			library_id               = EXCLUDED.library_id,
			title                    = EXCLUDED.title,
			author                   = EXCLUDED.author,
			description              = EXCLUDED.description,
			cover_url                = EXCLUDED.cover_url,
			language                 = EXCLUDED.language,
			explicit                 = EXCLUDED.explicit,
			itunes_category          = EXCLUDED.itunes_category,
			feed_url                 = EXCLUDED.feed_url,
			refresh_interval_minutes = EXCLUDED.refresh_interval_minutes,
			updated_at               = now()
	`, p.ID, p.LibraryID, p.Title, p.Author, p.Description, p.CoverURL, p.Language,
		p.Explicit, p.ITunesCategory, p.FeedURL, p.RefreshIntervalMinutes)
	if err != nil {
		return fmt.Errorf("upsert podcast: %w", err)
	}
	return nil
}

// GetPodcast reads one podcast by ID. Returns ErrNotFound on miss.
func (s *Store) GetPodcast(ctx context.Context, id string) (Podcast, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, library_id, title, COALESCE(author,''), COALESCE(description,''),
		       COALESCE(cover_url,''), COALESCE(language,''), explicit,
		       COALESCE(itunes_category,''), COALESCE(feed_url,''),
		       last_refreshed_at, refresh_interval_minutes,
		       COALESCE(last_error,''), created_at, updated_at
		FROM podcast WHERE id = $1
	`, id)
	var p Podcast
	if err := row.Scan(&p.ID, &p.LibraryID, &p.Title, &p.Author, &p.Description,
		&p.CoverURL, &p.Language, &p.Explicit, &p.ITunesCategory, &p.FeedURL,
		&p.LastRefreshedAt, &p.RefreshIntervalMinutes, &p.LastError,
		&p.CreatedAt, &p.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Podcast{}, ErrNotFound
		}
		return Podcast{}, fmt.Errorf("get podcast: %w", err)
	}
	return p, nil
}

// ListPodcasts returns podcasts in a library, newest-updated first. Pass
// libraryID=0 to list every podcast across the portal.
func (s *Store) ListPodcasts(ctx context.Context, libraryID int64, limit int) ([]Podcast, error) {
	if limit <= 0 {
		limit = 500
	}
	var rows pgx.Rows
	var err error
	if libraryID > 0 {
		rows, err = s.pool.Query(ctx, `
			SELECT id, library_id, title, COALESCE(author,''), COALESCE(description,''),
			       COALESCE(cover_url,''), COALESCE(language,''), explicit,
			       COALESCE(itunes_category,''), COALESCE(feed_url,''),
			       last_refreshed_at, refresh_interval_minutes,
			       COALESCE(last_error,''), created_at, updated_at
			FROM podcast WHERE library_id = $1 ORDER BY updated_at DESC LIMIT $2
		`, libraryID, limit)
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT id, library_id, title, COALESCE(author,''), COALESCE(description,''),
			       COALESCE(cover_url,''), COALESCE(language,''), explicit,
			       COALESCE(itunes_category,''), COALESCE(feed_url,''),
			       last_refreshed_at, refresh_interval_minutes,
			       COALESCE(last_error,''), created_at, updated_at
			FROM podcast ORDER BY updated_at DESC LIMIT $1
		`, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("list podcasts: %w", err)
	}
	defer rows.Close()
	var out []Podcast
	for rows.Next() {
		var p Podcast
		if err := rows.Scan(&p.ID, &p.LibraryID, &p.Title, &p.Author, &p.Description,
			&p.CoverURL, &p.Language, &p.Explicit, &p.ITunesCategory, &p.FeedURL,
			&p.LastRefreshedAt, &p.RefreshIntervalMinutes, &p.LastError,
			&p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan podcast: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeletePodcast removes a podcast and (via CASCADE) its episodes + progress.
func (s *Store) DeletePodcast(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM podcast WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete podcast: %w", err)
	}
	return nil
}

// MarkPodcastRefreshed records a successful (last_error="") or failed
// (last_error="...") feed-refresh attempt with the current timestamp.
func (s *Store) MarkPodcastRefreshed(ctx context.Context, id string, lastError string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE podcast SET last_refreshed_at = now(),
		                   last_error = NULLIF($2,''),
		                   updated_at = now()
		WHERE id = $1
	`, id, lastError)
	if err != nil {
		return fmt.Errorf("mark refreshed: %w", err)
	}
	return nil
}

// UpsertPodcastEpisode inserts or updates an episode keyed by
// (podcast_id, guid). A feed re-emitting the same item idempotently
// updates rather than duplicating.
func (s *Store) UpsertPodcastEpisode(ctx context.Context, e PodcastEpisode) error {
	if e.ID == "" || e.PodcastID == "" || e.GUID == "" || e.Title == "" || e.AudioURL == "" {
		return errors.New("id, podcast_id, guid, title, audio_url required")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO podcast_episode (
			id, podcast_id, guid, title, description, audio_url, audio_mime_type,
			audio_bytes, duration_seconds, episode_index, season_index,
			published_at, cover_url
		) VALUES ($1, $2, $3, $4, NULLIF($5,''), $6, NULLIF($7,''), $8, $9,
			$10, $11, $12, NULLIF($13,''))
		ON CONFLICT (podcast_id, guid) DO UPDATE SET
			title           = EXCLUDED.title,
			description     = EXCLUDED.description,
			audio_url       = EXCLUDED.audio_url,
			audio_mime_type = EXCLUDED.audio_mime_type,
			audio_bytes     = EXCLUDED.audio_bytes,
			duration_seconds = EXCLUDED.duration_seconds,
			episode_index   = EXCLUDED.episode_index,
			season_index    = EXCLUDED.season_index,
			published_at    = EXCLUDED.published_at,
			cover_url       = EXCLUDED.cover_url,
			updated_at      = now()
	`, e.ID, e.PodcastID, e.GUID, e.Title, e.Description, e.AudioURL,
		e.AudioMimeType, e.AudioBytes, e.DurationSeconds,
		e.EpisodeIndex, e.SeasonIndex, e.PublishedAt, e.CoverURL)
	if err != nil {
		return fmt.Errorf("upsert episode: %w", err)
	}
	return nil
}

// ListPodcastEpisodes returns episodes for a podcast, newest-published first.
func (s *Store) ListPodcastEpisodes(ctx context.Context, podcastID string, limit int) ([]PodcastEpisode, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, podcast_id, guid, title, COALESCE(description,''), audio_url,
		       COALESCE(audio_mime_type,''), COALESCE(audio_bytes, 0),
		       duration_seconds, episode_index, season_index, published_at,
		       COALESCE(cover_url,''), created_at, updated_at
		FROM podcast_episode WHERE podcast_id = $1
		ORDER BY published_at DESC NULLS LAST, episode_index DESC NULLS LAST, created_at DESC
		LIMIT $2
	`, podcastID, limit)
	if err != nil {
		return nil, fmt.Errorf("list episodes: %w", err)
	}
	defer rows.Close()
	var out []PodcastEpisode
	for rows.Next() {
		var e PodcastEpisode
		if err := rows.Scan(&e.ID, &e.PodcastID, &e.GUID, &e.Title, &e.Description,
			&e.AudioURL, &e.AudioMimeType, &e.AudioBytes, &e.DurationSeconds,
			&e.EpisodeIndex, &e.SeasonIndex, &e.PublishedAt, &e.CoverURL,
			&e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan episode: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetPodcastEpisodesByGUID returns a map of guid → episode_id for every
// known episode of a podcast that matches the supplied guid list. Used
// by the feed refresher to reuse stored episode ULIDs across feed
// refreshes (so per-user progress rows stay bound to the same row even
// when the upstream feed re-emits an item).
//
// An empty guids slice short-circuits to an empty map without a query.
func (s *Store) GetPodcastEpisodesByGUID(ctx context.Context, podcastID string, guids []string) (map[string]string, error) {
	out := make(map[string]string, len(guids))
	if podcastID == "" || len(guids) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, guid FROM podcast_episode
		WHERE podcast_id = $1 AND guid = ANY($2::text[])
	`, podcastID, guids)
	if err != nil {
		return nil, fmt.Errorf("guids lookup: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, guid string
		if err := rows.Scan(&id, &guid); err != nil {
			return nil, fmt.Errorf("scan guid: %w", err)
		}
		out[guid] = id
	}
	return out, rows.Err()
}

// GetPodcastEpisode reads one episode by ID. Returns ErrNotFound on miss.
func (s *Store) GetPodcastEpisode(ctx context.Context, id string) (PodcastEpisode, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, podcast_id, guid, title, COALESCE(description,''), audio_url,
		       COALESCE(audio_mime_type,''), COALESCE(audio_bytes, 0),
		       duration_seconds, episode_index, season_index, published_at,
		       COALESCE(cover_url,''), created_at, updated_at
		FROM podcast_episode WHERE id = $1
	`, id)
	var e PodcastEpisode
	if err := row.Scan(&e.ID, &e.PodcastID, &e.GUID, &e.Title, &e.Description,
		&e.AudioURL, &e.AudioMimeType, &e.AudioBytes, &e.DurationSeconds,
		&e.EpisodeIndex, &e.SeasonIndex, &e.PublishedAt, &e.CoverURL,
		&e.CreatedAt, &e.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PodcastEpisode{}, ErrNotFound
		}
		return PodcastEpisode{}, fmt.Errorf("get episode: %w", err)
	}
	return e, nil
}

// DeletePodcastEpisode removes one episode. Used by feed refreshes when an
// episode disappears from the feed (e.g. take-down or retroactive edit).
func (s *Store) DeletePodcastEpisode(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM podcast_episode WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete episode: %w", err)
	}
	return nil
}

// UpsertPodcastEpisodeProgress writes per-user-per-episode progress.
// Mirror of UpsertProgress for audiobooks so the ABS /me/progress dispatch
// can branch by id type and write to the right table.
func (s *Store) UpsertPodcastEpisodeProgress(ctx context.Context, p PodcastEpisodeProgress) error {
	if p.UserID == "" || p.EpisodeID == "" {
		return errors.New("user_id, episode_id required")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO podcast_episode_progress (
			user_id, episode_id, current_seconds, progress_pct, is_finished
		) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id, episode_id) DO UPDATE SET
			current_seconds = EXCLUDED.current_seconds,
			progress_pct    = EXCLUDED.progress_pct,
			is_finished     = EXCLUDED.is_finished,
			updated_at      = now()
	`, p.UserID, p.EpisodeID, p.CurrentSeconds, p.ProgressPct, p.IsFinished)
	if err != nil {
		return fmt.Errorf("upsert episode progress: %w", err)
	}
	return nil
}

// UpdatePodcastEpisodeProgressPosition only writes the position. Matches
// the audiobook UpdateProgressPosition helper — used by ABS session sync,
// which mustn't reset is_finished on every tick.
func (s *Store) UpdatePodcastEpisodeProgressPosition(ctx context.Context, userID, episodeID string, currentSeconds int) error {
	if userID == "" || episodeID == "" {
		return errors.New("user_id, episode_id required")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO podcast_episode_progress (user_id, episode_id, current_seconds)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, episode_id) DO UPDATE SET
			current_seconds = EXCLUDED.current_seconds,
			updated_at      = now()
	`, userID, episodeID, currentSeconds)
	if err != nil {
		return fmt.Errorf("update episode progress position: %w", err)
	}
	return nil
}

// GetPodcastEpisodeProgress reads one user's progress for one episode.
// Returns ErrNotFound on miss.
func (s *Store) GetPodcastEpisodeProgress(ctx context.Context, userID, episodeID string) (PodcastEpisodeProgress, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT user_id, episode_id, current_seconds, progress_pct, is_finished, updated_at
		FROM podcast_episode_progress WHERE user_id = $1 AND episode_id = $2
	`, userID, episodeID)
	var p PodcastEpisodeProgress
	if err := row.Scan(&p.UserID, &p.EpisodeID, &p.CurrentSeconds,
		&p.ProgressPct, &p.IsFinished, &p.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PodcastEpisodeProgress{}, ErrNotFound
		}
		return PodcastEpisodeProgress{}, fmt.Errorf("get episode progress: %w", err)
	}
	return p, nil
}
