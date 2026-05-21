package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// RSSFeed mirrors the rss_feed row. Slug is an unguessable random
// token (caller mints with crypto/rand); closing a feed deletes
// the row and breaks the public URL.
type RSSFeed struct {
	ID          string
	UserID      string
	Slug        string
	EntityType  string // "item" | "series" | "collection"
	EntityID    string
	Title       string
	Description string
	CoverPath   string
	CreatedAt   time.Time
}

func (s *Store) UpsertRSSFeed(ctx context.Context, f RSSFeed) error {
	if f.ID == "" || f.UserID == "" || f.Slug == "" || f.EntityType == "" || f.EntityID == "" {
		return errors.New("id, user_id, slug, entity_type, entity_id required")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO rss_feed (id, user_id, slug, entity_type, entity_id, title, description, cover_path)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7,''), NULLIF($8,''))
		ON CONFLICT (id) DO UPDATE SET
			title       = EXCLUDED.title,
			description = EXCLUDED.description,
			cover_path  = EXCLUDED.cover_path
	`, f.ID, f.UserID, f.Slug, f.EntityType, f.EntityID, f.Title, f.Description, f.CoverPath)
	if err != nil {
		return fmt.Errorf("upsert rss_feed: %w", err)
	}
	return nil
}

func (s *Store) GetRSSFeedBySlug(ctx context.Context, slug string) (RSSFeed, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, user_id, slug, entity_type, entity_id, title, COALESCE(description,''),
		       COALESCE(cover_path,''), created_at
		FROM rss_feed WHERE slug = $1
	`, slug)
	var f RSSFeed
	if err := row.Scan(&f.ID, &f.UserID, &f.Slug, &f.EntityType, &f.EntityID, &f.Title,
		&f.Description, &f.CoverPath, &f.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RSSFeed{}, ErrNotFound
		}
		return RSSFeed{}, fmt.Errorf("get rss_feed: %w", err)
	}
	return f, nil
}

// GetRSSFeedForEntity returns the existing feed for a given
// (entity_type, entity_id) tuple if any. The handler uses this for
// idempotent open-feed: re-opening returns the same slug rather
// than minting a new one.
func (s *Store) GetRSSFeedForEntity(ctx context.Context, entityType, entityID, userID string) (RSSFeed, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, user_id, slug, entity_type, entity_id, title, COALESCE(description,''),
		       COALESCE(cover_path,''), created_at
		FROM rss_feed
		WHERE entity_type = $1 AND entity_id = $2 AND user_id = $3
		LIMIT 1
	`, entityType, entityID, userID)
	var f RSSFeed
	if err := row.Scan(&f.ID, &f.UserID, &f.Slug, &f.EntityType, &f.EntityID, &f.Title,
		&f.Description, &f.CoverPath, &f.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RSSFeed{}, ErrNotFound
		}
		return RSSFeed{}, fmt.Errorf("get rss_feed by entity: %w", err)
	}
	return f, nil
}

func (s *Store) ListRSSFeedsForUser(ctx context.Context, userID string) ([]RSSFeed, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, slug, entity_type, entity_id, title, COALESCE(description,''),
		       COALESCE(cover_path,''), created_at
		FROM rss_feed WHERE user_id = $1
		ORDER BY created_at DESC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list rss_feed: %w", err)
	}
	defer rows.Close()
	var out []RSSFeed
	for rows.Next() {
		var f RSSFeed
		if err := rows.Scan(&f.ID, &f.UserID, &f.Slug, &f.EntityType, &f.EntityID, &f.Title,
			&f.Description, &f.CoverPath, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan rss_feed: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) DeleteRSSFeed(ctx context.Context, id, userID string) error {
	if id == "" || userID == "" {
		return errors.New("id, user_id required")
	}
	_, err := s.pool.Exec(ctx, `
		DELETE FROM rss_feed WHERE id = $1 AND user_id = $2
	`, id, userID)
	if err != nil {
		return fmt.Errorf("delete rss_feed: %w", err)
	}
	return nil
}
