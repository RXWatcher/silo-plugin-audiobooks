package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ListeningStats tracks aggregate listening time for one user/book pair.
type ListeningStats struct {
	UserID          string
	BookID          string
	ListenedSeconds int
	LastPosition    int
	UpdatedAt       time.Time
}

// AddListeningStats increments listened seconds and records the latest position.
func (s *Store) AddListeningStats(ctx context.Context, userID, bookID string, listenedSeconds, lastPosition int) error {
	if userID == "" || bookID == "" {
		return fmt.Errorf("user_id and book_id required")
	}
	if listenedSeconds < 0 {
		listenedSeconds = 0
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO listening_stats (user_id, book_id, listened_seconds, last_position, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (user_id, book_id) DO UPDATE SET
			listened_seconds = listening_stats.listened_seconds + EXCLUDED.listened_seconds,
			last_position    = EXCLUDED.last_position,
			updated_at       = now()
	`, userID, bookID, listenedSeconds, lastPosition)
	if err != nil {
		return fmt.Errorf("add listening stats: %w", err)
	}
	return nil
}

// GetListeningStats reads one user's stats for a book.
func (s *Store) GetListeningStats(ctx context.Context, userID, bookID string) (ListeningStats, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT user_id, book_id, listened_seconds, last_position, updated_at
		FROM listening_stats WHERE user_id = $1 AND book_id = $2
	`, userID, bookID)
	var st ListeningStats
	if err := row.Scan(&st.UserID, &st.BookID, &st.ListenedSeconds, &st.LastPosition, &st.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ListeningStats{}, ErrNotFound
		}
		return ListeningStats{}, fmt.Errorf("get listening stats: %w", err)
	}
	return st, nil
}
