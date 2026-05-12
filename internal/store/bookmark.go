package store

import (
	"context"
	"fmt"
	"time"
)

// Bookmark mirrors the bookmark table.
type Bookmark struct {
	ID              string
	UserID          string
	BookID          string
	PositionSeconds int
	ChapterID       string
	Note            string
	CreatedAt       time.Time
}

// InsertBookmark stores a new bookmark.
func (s *Store) InsertBookmark(ctx context.Context, b Bookmark) error {
	if b.ID == "" || b.UserID == "" || b.BookID == "" {
		return fmt.Errorf("id, user_id, book_id required")
	}
	var chapterID *string
	if b.ChapterID != "" {
		chapterID = &b.ChapterID
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO bookmark (id, user_id, book_id, position_seconds, chapter_id, note)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, b.ID, b.UserID, b.BookID, b.PositionSeconds, chapterID, b.Note)
	if err != nil {
		return fmt.Errorf("insert bookmark: %w", err)
	}
	return nil
}

// ListBookmarks returns all bookmarks for a user's book.
func (s *Store) ListBookmarks(ctx context.Context, userID, bookID string) ([]Bookmark, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, book_id, position_seconds, COALESCE(chapter_id,''), note, created_at
		FROM bookmark WHERE user_id = $1 AND book_id = $2
		ORDER BY position_seconds ASC
	`, userID, bookID)
	if err != nil {
		return nil, fmt.Errorf("list bookmarks: %w", err)
	}
	defer rows.Close()
	var out []Bookmark
	for rows.Next() {
		var b Bookmark
		if err := rows.Scan(&b.ID, &b.UserID, &b.BookID, &b.PositionSeconds, &b.ChapterID, &b.Note, &b.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan bookmark: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// DeleteBookmark removes a bookmark by id. user_id is required for
// authorization (the caller must already have checked ownership).
func (s *Store) DeleteBookmark(ctx context.Context, id, userID string) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM bookmark WHERE id = $1 AND user_id = $2
	`, id, userID)
	if err != nil {
		return fmt.Errorf("delete bookmark: %w", err)
	}
	return nil
}
