package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Collection mirrors the collection table.
type Collection struct {
	ID          string
	UserID      string
	Name        string
	Color       string
	IsPublic    bool
	IsPinned    bool
	CoverBookID string
	CreatedAt   time.Time
}

// CollectionItem mirrors the collection_item table.
type CollectionItem struct {
	CollectionID string
	BookID       string
	Position     int
	AddedAt      time.Time
}

// CreateCollection inserts a new collection row.
func (s *Store) CreateCollection(ctx context.Context, c Collection) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO collection (id, user_id, name, color, is_public, is_pinned, cover_book_id)
		VALUES ($1, $2, $3, NULLIF($4,''), $5, $6, NULLIF($7,''))
	`, c.ID, c.UserID, c.Name, c.Color, c.IsPublic, c.IsPinned, c.CoverBookID)
	if err != nil {
		return fmt.Errorf("create collection: %w", err)
	}
	return nil
}

// UpdateCollection writes a collection's mutable fields. ownerID is the
// authorising user; pass empty to update unconditionally (admin context).
func (s *Store) UpdateCollection(ctx context.Context, c Collection, ownerID string) error {
	args := []any{c.ID, c.Name, c.Color, c.IsPublic, c.IsPinned, c.CoverBookID}
	q := `UPDATE collection SET
		name = $2,
		color = NULLIF($3,''),
		is_public = $4,
		is_pinned = $5,
		cover_book_id = NULLIF($6,'')
		WHERE id = $1`
	if ownerID != "" {
		q += " AND user_id = $7"
		args = append(args, ownerID)
	}
	res, err := s.pool.Exec(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("update collection: %w", err)
	}
	if res.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteCollection removes a collection (and its items via ON DELETE CASCADE).
func (s *Store) DeleteCollection(ctx context.Context, id, ownerID string) error {
	res, err := s.pool.Exec(ctx, `DELETE FROM collection WHERE id = $1 AND user_id = $2`, id, ownerID)
	if err != nil {
		return fmt.Errorf("delete collection: %w", err)
	}
	if res.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetCollection fetches one collection row.
func (s *Store) GetCollection(ctx context.Context, id string) (Collection, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, user_id, name, COALESCE(color,''), is_public, is_pinned,
		       COALESCE(cover_book_id,''), created_at
		FROM collection WHERE id = $1
	`, id)
	return scanCollection(row)
}

// ListUserCollections returns the user's own collections.
func (s *Store) ListUserCollections(ctx context.Context, userID string) ([]Collection, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, name, COALESCE(color,''), is_public, is_pinned,
		       COALESCE(cover_book_id,''), created_at
		FROM collection WHERE user_id = $1
		ORDER BY is_pinned DESC, name ASC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list user collections: %w", err)
	}
	defer rows.Close()
	return collectCollections(rows)
}

// ListPublicCollections returns all collections marked public.
func (s *Store) ListPublicCollections(ctx context.Context, limit int) ([]Collection, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, name, COALESCE(color,''), is_public, is_pinned,
		       COALESCE(cover_book_id,''), created_at
		FROM collection WHERE is_public = true
		ORDER BY created_at DESC LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list public collections: %w", err)
	}
	defer rows.Close()
	return collectCollections(rows)
}

// AddCollectionItem appends an item with monotonic position.
func (s *Store) AddCollectionItem(ctx context.Context, collectionID, bookID string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO collection_item (collection_id, book_id, position)
		VALUES ($1, $2,
			(SELECT COALESCE(MAX(position),0) + 1 FROM collection_item WHERE collection_id = $1))
		ON CONFLICT (collection_id, book_id) DO NOTHING
	`, collectionID, bookID)
	if err != nil {
		return fmt.Errorf("add collection item: %w", err)
	}
	return nil
}

// RemoveCollectionItem deletes a (collection, book) row.
func (s *Store) RemoveCollectionItem(ctx context.Context, collectionID, bookID string) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM collection_item WHERE collection_id = $1 AND book_id = $2`,
		collectionID, bookID)
	if err != nil {
		return fmt.Errorf("remove collection item: %w", err)
	}
	return nil
}

// ListCollectionItems returns the items in a collection, ordered by position.
func (s *Store) ListCollectionItems(ctx context.Context, collectionID string) ([]CollectionItem, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT collection_id, book_id, position, added_at
		FROM collection_item WHERE collection_id = $1
		ORDER BY position ASC
	`, collectionID)
	if err != nil {
		return nil, fmt.Errorf("list collection items: %w", err)
	}
	defer rows.Close()
	var out []CollectionItem
	for rows.Next() {
		var ci CollectionItem
		if err := rows.Scan(&ci.CollectionID, &ci.BookID, &ci.Position, &ci.AddedAt); err != nil {
			return nil, fmt.Errorf("scan collection item: %w", err)
		}
		out = append(out, ci)
	}
	return out, rows.Err()
}

func scanCollection(row pgx.Row) (Collection, error) {
	var c Collection
	if err := row.Scan(&c.ID, &c.UserID, &c.Name, &c.Color, &c.IsPublic, &c.IsPinned, &c.CoverBookID, &c.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Collection{}, ErrNotFound
		}
		return Collection{}, fmt.Errorf("scan collection: %w", err)
	}
	return c, nil
}

func collectCollections(rows pgx.Rows) ([]Collection, error) {
	var out []Collection
	for rows.Next() {
		c, err := scanCollection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
