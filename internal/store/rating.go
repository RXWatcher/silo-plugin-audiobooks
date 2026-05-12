package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Rating mirrors the rating table.
type Rating struct {
	UserID    string
	BookID    string
	Rating    int
	CreatedAt time.Time
}

// UpsertRating inserts or updates a (user_id, book_id) row.
func (s *Store) UpsertRating(ctx context.Context, r Rating) error {
	if r.Rating < 1 || r.Rating > 5 {
		return fmt.Errorf("rating must be 1..5")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO rating (user_id, book_id, rating)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, book_id) DO UPDATE SET rating = EXCLUDED.rating
	`, r.UserID, r.BookID, r.Rating)
	if err != nil {
		return fmt.Errorf("upsert rating: %w", err)
	}
	return nil
}

// GetRating reads one (user_id, book_id) row.
func (s *Store) GetRating(ctx context.Context, userID, bookID string) (Rating, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT user_id, book_id, rating, created_at FROM rating
		WHERE user_id = $1 AND book_id = $2
	`, userID, bookID)
	var r Rating
	if err := row.Scan(&r.UserID, &r.BookID, &r.Rating, &r.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Rating{}, ErrNotFound
		}
		return Rating{}, fmt.Errorf("get rating: %w", err)
	}
	return r, nil
}

// DeleteRating removes a (user_id, book_id) rating.
func (s *Store) DeleteRating(ctx context.Context, userID, bookID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM rating WHERE user_id = $1 AND book_id = $2`, userID, bookID)
	if err != nil {
		return fmt.Errorf("delete rating: %w", err)
	}
	return nil
}
