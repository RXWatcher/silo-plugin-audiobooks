package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Progress mirrors the progress table.
type Progress struct {
	UserID         string
	BookID         string
	CurrentSeconds int
	ProgressPct    float32
	IsFinished     bool
	UpdatedAt      time.Time
}

// UpsertProgress inserts or updates a (user_id, book_id) row.
func (s *Store) UpsertProgress(ctx context.Context, p Progress) error {
	if p.UserID == "" || p.BookID == "" {
		return fmt.Errorf("user_id and book_id required")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO progress (user_id, book_id, current_seconds, progress_pct, is_finished, updated_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (user_id, book_id) DO UPDATE SET
			current_seconds = EXCLUDED.current_seconds,
			progress_pct    = EXCLUDED.progress_pct,
			is_finished     = EXCLUDED.is_finished,
			updated_at      = now()
	`, p.UserID, p.BookID, p.CurrentSeconds, p.ProgressPct, p.IsFinished)
	if err != nil {
		return fmt.Errorf("upsert progress: %w", err)
	}
	return nil
}

// GetProgress reads one (user_id, book_id) row.
func (s *Store) GetProgress(ctx context.Context, userID, bookID string) (Progress, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT user_id, book_id, current_seconds, progress_pct, is_finished, updated_at
		FROM progress WHERE user_id = $1 AND book_id = $2
	`, userID, bookID)
	var p Progress
	if err := row.Scan(&p.UserID, &p.BookID, &p.CurrentSeconds, &p.ProgressPct, &p.IsFinished, &p.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Progress{}, ErrNotFound
		}
		return Progress{}, fmt.Errorf("get progress: %w", err)
	}
	return p, nil
}

// ListRecentProgress returns the user's most recently-updated progress rows
// (descending by updated_at).
func (s *Store) ListRecentProgress(ctx context.Context, userID string, limit int) ([]Progress, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `
		SELECT user_id, book_id, current_seconds, progress_pct, is_finished, updated_at
		FROM progress WHERE user_id = $1
		ORDER BY updated_at DESC LIMIT $2
	`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list progress: %w", err)
	}
	defer rows.Close()
	out := make([]Progress, 0, limit)
	for rows.Next() {
		var p Progress
		if err := rows.Scan(&p.UserID, &p.BookID, &p.CurrentSeconds, &p.ProgressPct, &p.IsFinished, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan progress: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
