package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ShareLink is one row in share_link. Slug is the public capability;
// expires_at + max_uses gate access at read time.
type ShareLink struct {
	ID        string
	UserID    string
	Slug      string
	ItemID    string
	ExpiresAt *time.Time
	MaxUses   int
	UseCount  int
	CreatedAt time.Time
}

func (s *Store) CreateShareLink(ctx context.Context, l ShareLink) error {
	if l.ID == "" || l.UserID == "" || l.Slug == "" || l.ItemID == "" {
		return errors.New("id, user_id, slug, item_id required")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO share_link (id, user_id, slug, item_id, expires_at, max_uses)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, l.ID, l.UserID, l.Slug, l.ItemID, l.ExpiresAt, l.MaxUses)
	if err != nil {
		return fmt.Errorf("create share_link: %w", err)
	}
	return nil
}

// GetActiveShareLinkBySlug returns the share when it's still valid
// (not expired + uses-remaining). ErrNotFound on miss OR when the
// share is locked. The caller treats both the same — "this slug
// doesn't grant access right now."
func (s *Store) GetActiveShareLinkBySlug(ctx context.Context, slug string) (ShareLink, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, user_id, slug, item_id, expires_at, max_uses, use_count, created_at
		FROM share_link WHERE slug = $1
		  AND (expires_at IS NULL OR expires_at > now())
		  AND (max_uses = 0 OR use_count < max_uses)
	`, slug)
	var l ShareLink
	if err := row.Scan(&l.ID, &l.UserID, &l.Slug, &l.ItemID, &l.ExpiresAt,
		&l.MaxUses, &l.UseCount, &l.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ShareLink{}, ErrNotFound
		}
		return ShareLink{}, fmt.Errorf("get share_link: %w", err)
	}
	return l, nil
}

// IncrementShareUse bumps use_count atomically. Returns
// ErrNotFound when the row no longer matches (race with delete).
func (s *Store) IncrementShareUse(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE share_link SET use_count = use_count + 1 WHERE id = $1
	`, id)
	if err != nil {
		return fmt.Errorf("increment share_link: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListShareLinks(ctx context.Context, userID string) ([]ShareLink, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, slug, item_id, expires_at, max_uses, use_count, created_at
		FROM share_link WHERE user_id = $1
		ORDER BY created_at DESC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list share_link: %w", err)
	}
	defer rows.Close()
	var out []ShareLink
	for rows.Next() {
		var l ShareLink
		if err := rows.Scan(&l.ID, &l.UserID, &l.Slug, &l.ItemID, &l.ExpiresAt,
			&l.MaxUses, &l.UseCount, &l.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan share_link: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) DeleteShareLink(ctx context.Context, id, userID string) error {
	if id == "" || userID == "" {
		return errors.New("id, user_id required")
	}
	_, err := s.pool.Exec(ctx, `
		DELETE FROM share_link WHERE id = $1 AND user_id = $2
	`, id, userID)
	if err != nil {
		return fmt.Errorf("delete share_link: %w", err)
	}
	return nil
}

// PurgeExpiredShareLinks deletes rows past their expires_at.
// Scheduler calls this periodically; returns the count purged.
func (s *Store) PurgeExpiredShareLinks(ctx context.Context) (int, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM share_link WHERE expires_at IS NOT NULL AND expires_at <= now()
	`)
	if err != nil {
		return 0, fmt.Errorf("purge share_link: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
