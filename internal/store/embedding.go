package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"
)

// AudiobookEmbedding is one (book_id, library_id) row from
// audiobook_embedding. The Embedding field carries the pgvector
// halfvec — pgvector-go encodes/decodes the wire format.
type AudiobookEmbedding struct {
	BookID        string
	LibraryID     int64
	Embedding     pgvector.Vector
	Model         string
	CanonicalText string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// UpsertAudiobookEmbedding writes one vector + metadata. The (book,
// library) primary key means a re-embedding overwrites the previous
// row; canonical_text + model live alongside so the refresh-check
// can decide when a vector is stale.
func (s *Store) UpsertAudiobookEmbedding(ctx context.Context, e AudiobookEmbedding) error {
	if e.BookID == "" || e.LibraryID <= 0 {
		return errors.New("book_id, library_id required")
	}
	if e.Model == "" {
		return errors.New("model required")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO audiobook_embedding (book_id, library_id, embedding, model, canonical_text)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (book_id, library_id) DO UPDATE SET
			embedding      = EXCLUDED.embedding,
			model          = EXCLUDED.model,
			canonical_text = EXCLUDED.canonical_text,
			updated_at     = now()
	`, e.BookID, e.LibraryID, e.Embedding, e.Model, e.CanonicalText)
	if err != nil {
		return fmt.Errorf("upsert audiobook_embedding: %w", err)
	}
	return nil
}

// GetAudiobookEmbedding reads one row. ErrNotFound on miss.
func (s *Store) GetAudiobookEmbedding(ctx context.Context, libraryID int64, bookID string) (AudiobookEmbedding, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT book_id, library_id, embedding, model, canonical_text, created_at, updated_at
		FROM audiobook_embedding WHERE library_id = $1 AND book_id = $2
	`, libraryID, bookID)
	var e AudiobookEmbedding
	if err := row.Scan(&e.BookID, &e.LibraryID, &e.Embedding, &e.Model, &e.CanonicalText, &e.CreatedAt, &e.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AudiobookEmbedding{}, ErrNotFound
		}
		return AudiobookEmbedding{}, fmt.Errorf("get audiobook_embedding: %w", err)
	}
	return e, nil
}

// SimilarAudiobook is a single search result: candidate book id + the
// cosine-derived similarity score in [0, 1]. The handler joins this
// back to library metadata for the response shape.
type SimilarAudiobook struct {
	BookID     string
	LibraryID  int64
	Similarity float64
}

// FindSimilarAudiobooks does a top-K nearest-neighbour search by
// cosine distance. excludeBookIDs is typically the source book — we
// don't want a book matching itself with similarity 1.0 dominating
// the results.
//
// pgvector's `<=>` cosine-distance operator returns 0 for identical
// vectors and 2 for opposite; similarity = 1 - distance/2 maps that
// back to a [0, 1] number.
func (s *Store) FindSimilarAudiobooks(ctx context.Context, source pgvector.Vector, excludeBookIDs []string, limit int) ([]SimilarAudiobook, error) {
	if limit <= 0 {
		limit = 25
	}
	excludeStr := excludeBookIDs
	if excludeStr == nil {
		excludeStr = []string{}
	}
	rows, err := s.pool.Query(ctx, `
		SELECT book_id, library_id, (embedding <=> $1) AS distance
		FROM audiobook_embedding
		WHERE NOT (book_id = ANY($2::text[]))
		ORDER BY embedding <=> $1
		LIMIT $3
	`, source, excludeStr, limit)
	if err != nil {
		return nil, fmt.Errorf("find similar: %w", err)
	}
	defer rows.Close()
	out := make([]SimilarAudiobook, 0, limit)
	for rows.Next() {
		var r SimilarAudiobook
		var distance float64
		if err := rows.Scan(&r.BookID, &r.LibraryID, &distance); err != nil {
			return nil, fmt.Errorf("scan similar: %w", err)
		}
		r.Similarity = 1.0 - distance/2.0
		out = append(out, r)
	}
	return out, rows.Err()
}

// AudiobookRecommendationCache mirrors the cache table. items is a
// JSON-encoded []SimilarAudiobook so we don't pay vector decode cost
// on a cache hit.
type AudiobookRecommendationCache struct {
	BookID    string
	LibraryID int64
	RecType   string
	Items     json.RawMessage
	ExpiresAt time.Time
	CreatedAt time.Time
}

// GetRecommendationCache returns the cached recommendation list for
// (book, library, type) when one exists and isn't expired. Returns
// ErrNotFound on miss or expiry.
func (s *Store) GetRecommendationCache(ctx context.Context, libraryID int64, bookID, recType string) (AudiobookRecommendationCache, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT book_id, library_id, rec_type, items, expires_at, created_at
		FROM audiobook_recommendation_cache
		WHERE library_id = $1 AND book_id = $2 AND rec_type = $3
		  AND expires_at > now()
	`, libraryID, bookID, recType)
	var c AudiobookRecommendationCache
	if err := row.Scan(&c.BookID, &c.LibraryID, &c.RecType, &c.Items, &c.ExpiresAt, &c.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AudiobookRecommendationCache{}, ErrNotFound
		}
		return AudiobookRecommendationCache{}, fmt.Errorf("get rec cache: %w", err)
	}
	return c, nil
}

// SetRecommendationCache writes the cache entry with an explicit TTL.
// Upsert by (book, library, type) so refreshes overwrite cleanly.
func (s *Store) SetRecommendationCache(ctx context.Context, libraryID int64, bookID, recType string, items json.RawMessage, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO audiobook_recommendation_cache (book_id, library_id, rec_type, items, expires_at)
		VALUES ($1, $2, $3, $4, now() + $5::interval)
		ON CONFLICT (book_id, library_id, rec_type) DO UPDATE SET
			items      = EXCLUDED.items,
			expires_at = EXCLUDED.expires_at,
			created_at = now()
	`, bookID, libraryID, recType, items, fmt.Sprintf("%d seconds", int(ttl.Seconds())))
	if err != nil {
		return fmt.Errorf("set rec cache: %w", err)
	}
	return nil
}

// PurgeExpiredRecommendations is called periodically (scheduler tick)
// to drop rows whose expires_at has passed. Returning the count lets
// the scheduler log progress.
func (s *Store) PurgeExpiredRecommendations(ctx context.Context) (int, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM audiobook_recommendation_cache WHERE expires_at <= now()
	`)
	if err != nil {
		return 0, fmt.Errorf("purge expired: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
