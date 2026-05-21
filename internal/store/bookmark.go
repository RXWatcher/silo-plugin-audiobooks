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

// UpdateBookmarkNote sets the note on an existing bookmark by id.
// user_id pins ownership. Idempotent — updating a non-existent row
// is not an error.
func (s *Store) UpdateBookmarkNote(ctx context.Context, id, userID, note string) error {
	if id == "" || userID == "" {
		return fmt.Errorf("id, user_id required")
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE bookmark SET note = $1
		WHERE id = $2 AND user_id = $3
	`, note, id, userID)
	if err != nil {
		return fmt.Errorf("update bookmark note: %w", err)
	}
	return nil
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

// UpsertBookmarkAt creates or updates a bookmark keyed by
// (user_id, book_id, position_seconds). The ABS API addresses
// bookmarks by their position-in-seconds rather than a separate id —
// POST and PATCH both target the same row, and DELETE takes the
// position in the URL. Caller passes an id to use only when creating
// a new row; existing rows keep their id.
func (s *Store) UpsertBookmarkAt(ctx context.Context, b Bookmark) error {
	if b.UserID == "" || b.BookID == "" {
		return fmt.Errorf("user_id, book_id required")
	}
	var chapterID *string
	if b.ChapterID != "" {
		chapterID = &b.ChapterID
	}
	// Two-step rather than ON CONFLICT because (user, book, position)
	// isn't a unique constraint in the existing schema and adding one
	// would conflict with multi-bookmark-at-same-second edge cases.
	// Lookup-then-update / lookup-then-insert keeps the migration
	// surface zero.
	row := s.pool.QueryRow(ctx, `
		SELECT id FROM bookmark
		WHERE user_id = $1 AND book_id = $2 AND position_seconds = $3
		LIMIT 1
	`, b.UserID, b.BookID, b.PositionSeconds)
	var existingID string
	if err := row.Scan(&existingID); err == nil {
		_, uerr := s.pool.Exec(ctx, `
			UPDATE bookmark SET note = $1, chapter_id = $2
			WHERE id = $3
		`, b.Note, chapterID, existingID)
		if uerr != nil {
			return fmt.Errorf("update bookmark: %w", uerr)
		}
		return nil
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

// DeleteBookmarkAt removes a bookmark by (user, book, position).
// Idempotent — deleting a non-existent bookmark is not an error
// (matches real ABS, which 200s either way).
func (s *Store) DeleteBookmarkAt(ctx context.Context, userID, bookID string, positionSeconds int) error {
	if userID == "" || bookID == "" {
		return fmt.Errorf("user_id, book_id required")
	}
	_, err := s.pool.Exec(ctx, `
		DELETE FROM bookmark
		WHERE user_id = $1 AND book_id = $2 AND position_seconds = $3
	`, userID, bookID, positionSeconds)
	if err != nil {
		return fmt.Errorf("delete bookmark at: %w", err)
	}
	return nil
}
