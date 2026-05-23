package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ABSSession mirrors the abs_playback_session table.
type ABSSession struct {
	ID          string
	UserID      string
	ProfileID   string
	BookID      string
	DeviceID    string
	DeviceInfo  map[string]any
	PlayMethod  string
	MediaPlayer string
	StartTime   int
	CurrentTime int
	StartedAt   time.Time
	LastUpdate  time.Time
	ClosedAt    *time.Time
}

// InsertABSSession stores a new session row.
func (s *Store) InsertABSSession(ctx context.Context, sess ABSSession) error {
	if sess.ID == "" || sess.UserID == "" || sess.BookID == "" || sess.DeviceID == "" {
		return fmt.Errorf("id, user_id, book_id, device_id required")
	}
	method := sess.PlayMethod
	if method == "" {
		method = "directplay"
	}
	var info []byte
	if len(sess.DeviceInfo) > 0 {
		b, _ := json.Marshal(sess.DeviceInfo)
		info = b
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO abs_playback_session
			(id, user_id, profile_id, book_id, device_id, device_info, play_method, media_player,
			 start_time, current_time_ms)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8,''), $9, $10)
	`, sess.ID, sess.UserID, sess.ProfileID, sess.BookID, sess.DeviceID, info, method, sess.MediaPlayer,
		sess.StartTime, sess.CurrentTime)
	if err != nil {
		return fmt.Errorf("insert abs_session: %w", err)
	}
	return nil
}

// UpdateABSSession bumps current_time_ms, last_update, and accumulates the
// delta-since-last-tick on time_listening_seconds. The mobile client sends a
// monotonically increasing currentTime AND a per-tick delta (timeListened);
// we use the delta because the absolute time can jump (chapter skip, seek)
// in ways that aren't actual listening. listenedDelta clamps to 0 — guarding
// against negative values from a buggy client or a clock reset.
func (s *Store) UpdateABSSession(ctx context.Context, id string, currentTime int, listenedDelta int) error {
	if listenedDelta < 0 {
		listenedDelta = 0
	}
	res, err := s.pool.Exec(ctx, `
		UPDATE abs_playback_session
		SET current_time_ms = $2,
		    last_update = now(),
		    time_listening_seconds = time_listening_seconds + $3
		WHERE id = $1 AND closed_at IS NULL
	`, id, currentTime, listenedDelta)
	if err != nil {
		return fmt.Errorf("update abs_session: %w", err)
	}
	if res.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CloseABSSession sets closed_at = now() for the given session.
func (s *Store) CloseABSSession(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE abs_playback_session SET closed_at = now() WHERE id = $1 AND closed_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("close abs_session: %w", err)
	}
	return nil
}

// GetABSSession fetches a session by id.
func (s *Store) GetABSSession(ctx context.Context, id string) (ABSSession, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, user_id, profile_id, book_id, device_id, device_info, play_method,
		       COALESCE(media_player,''), start_time, current_time_ms, started_at,
		       last_update, closed_at
		FROM abs_playback_session WHERE id = $1
	`, id)
	var sess ABSSession
	var info []byte
	if err := row.Scan(&sess.ID, &sess.UserID, &sess.ProfileID, &sess.BookID, &sess.DeviceID,
		&info, &sess.PlayMethod, &sess.MediaPlayer, &sess.StartTime, &sess.CurrentTime,
		&sess.StartedAt, &sess.LastUpdate, &sess.ClosedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ABSSession{}, ErrNotFound
		}
		return ABSSession{}, fmt.Errorf("get abs_session: %w", err)
	}
	if len(info) > 0 {
		_ = json.Unmarshal(info, &sess.DeviceInfo)
	}
	return sess, nil
}

// CountActiveABSSessions returns the number of sessions with closed_at
// IS NULL. Cheap COUNT(*) suitable for the "listener_count" Socket.io
// broadcast that fires on every session-open / session-close — we don't
// want to load the full row set just to len() it.
func (s *Store) CountActiveABSSessions(ctx context.Context) (int, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM abs_playback_session WHERE closed_at IS NULL
	`)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("count active sessions: %w", err)
	}
	return n, nil
}

// ListActiveABSSessions returns all sessions with closed_at IS NULL.
func (s *Store) ListActiveABSSessions(ctx context.Context, limit int) ([]ABSSession, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, profile_id, book_id, device_id, device_info, play_method,
		       COALESCE(media_player,''), start_time, current_time_ms, started_at,
		       last_update, closed_at
		FROM abs_playback_session WHERE closed_at IS NULL
		ORDER BY last_update DESC LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list active sessions: %w", err)
	}
	defer rows.Close()
	var out []ABSSession
	for rows.Next() {
		var sess ABSSession
		var info []byte
		if err := rows.Scan(&sess.ID, &sess.UserID, &sess.ProfileID, &sess.BookID, &sess.DeviceID,
			&info, &sess.PlayMethod, &sess.MediaPlayer, &sess.StartTime, &sess.CurrentTime,
			&sess.StartedAt, &sess.LastUpdate, &sess.ClosedAt); err != nil {
			return nil, fmt.Errorf("scan abs_session: %w", err)
		}
		if len(info) > 0 {
			_ = json.Unmarshal(info, &sess.DeviceInfo)
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// ListActiveABSSessionsForUser returns active sessions owned by one user under
// a specific profile. Pass profileID="" for the primary profile.
func (s *Store) ListActiveABSSessionsForUser(ctx context.Context, userID string, profileID string, limit int) ([]ABSSession, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, profile_id, book_id, device_id, device_info, play_method,
		       COALESCE(media_player,''), start_time, current_time_ms, started_at,
		       last_update, closed_at
		FROM abs_playback_session
		WHERE closed_at IS NULL AND user_id = $1 AND profile_id = $2
		ORDER BY last_update DESC LIMIT $3
	`, userID, profileID, limit)
	if err != nil {
		return nil, fmt.Errorf("list active user sessions: %w", err)
	}
	defer rows.Close()
	var out []ABSSession
	for rows.Next() {
		var sess ABSSession
		var info []byte
		if err := rows.Scan(&sess.ID, &sess.UserID, &sess.ProfileID, &sess.BookID, &sess.DeviceID,
			&info, &sess.PlayMethod, &sess.MediaPlayer, &sess.StartTime, &sess.CurrentTime,
			&sess.StartedAt, &sess.LastUpdate, &sess.ClosedAt); err != nil {
			return nil, fmt.Errorf("scan abs_session: %w", err)
		}
		if len(info) > 0 {
			_ = json.Unmarshal(info, &sess.DeviceInfo)
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// ReapIdleABSSessions closes any sessions whose last_update is older than the
// given age. Returns count.
func (s *Store) ReapIdleABSSessions(ctx context.Context, maxAge time.Duration) (int, error) {
	res, err := s.pool.Exec(ctx, `
		UPDATE abs_playback_session SET closed_at = now()
		WHERE closed_at IS NULL AND last_update < now() - $1::interval
	`, fmt.Sprintf("%d seconds", int(maxAge.Seconds())))
	if err != nil {
		return 0, fmt.Errorf("reap idle sessions: %w", err)
	}
	return int(res.RowsAffected()), nil
}
