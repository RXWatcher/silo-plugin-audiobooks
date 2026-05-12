package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// FileCacheEntry mirrors audiobook_file_cache.
type FileCacheEntry struct {
	ID               string
	CacheKey         string
	BookID           string
	FileIdx          *int
	Filename         string
	MimeType         string
	ContentLength    int64
	Codec            string
	DurationSeconds  *int
	Status           string // pending|downloading|ready|failed
	DownloadProgress float32
	ErrorMessage     string
	RelativePath     string
	BytesOnDisk      int64
	LastAccessedAt   time.Time
	CreatedAt        time.Time
}

// InsertFileCacheEntry stores a new cache row.
func (s *Store) InsertFileCacheEntry(ctx context.Context, e FileCacheEntry) error {
	if e.ID == "" || e.CacheKey == "" || e.BookID == "" || e.RelativePath == "" {
		return fmt.Errorf("id, cache_key, book_id, relative_path required")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO audiobook_file_cache
			(id, cache_key, book_id, file_idx, filename, mime_type, content_length,
			 codec, duration_seconds, status, relative_path)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8,''), $9, $10, $11)
		ON CONFLICT (cache_key) DO NOTHING
	`, e.ID, e.CacheKey, e.BookID, e.FileIdx, e.Filename, e.MimeType, e.ContentLength,
		e.Codec, e.DurationSeconds, e.Status, e.RelativePath)
	if err != nil {
		return fmt.Errorf("insert file_cache: %w", err)
	}
	return nil
}

// GetFileCacheByKey looks up an entry by cache_key.
func (s *Store) GetFileCacheByKey(ctx context.Context, key string) (FileCacheEntry, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, cache_key, book_id, file_idx, filename, mime_type, content_length,
		       COALESCE(codec,''), duration_seconds, status, download_progress,
		       COALESCE(error_message,''), relative_path, bytes_on_disk,
		       last_accessed_at, created_at
		FROM audiobook_file_cache WHERE cache_key = $1
	`, key)
	return scanFileCacheRow(row)
}

// UpdateFileCacheStatus marks an entry's status (and optional error/bytes).
func (s *Store) UpdateFileCacheStatus(ctx context.Context, key, status string, bytesOnDisk int64, errMessage string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE audiobook_file_cache
		SET status = $2,
		    bytes_on_disk = CASE WHEN $3 > 0 THEN $3 ELSE bytes_on_disk END,
		    error_message = NULLIF($4,''),
		    download_progress = CASE WHEN $2 = 'ready' THEN 1.0 ELSE download_progress END
		WHERE cache_key = $1
	`, key, status, bytesOnDisk, errMessage)
	if err != nil {
		return fmt.Errorf("update file_cache: %w", err)
	}
	return nil
}

// TouchFileCache updates last_accessed_at to now().
func (s *Store) TouchFileCache(ctx context.Context, key string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE audiobook_file_cache SET last_accessed_at = now() WHERE cache_key = $1`, key)
	return err
}

// TotalCacheBytes returns the sum of bytes_on_disk for ready entries.
func (s *Store) TotalCacheBytes(ctx context.Context) (int64, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(bytes_on_disk),0) FROM audiobook_file_cache WHERE status = 'ready'`)
	var total int64
	if err := row.Scan(&total); err != nil {
		return 0, fmt.Errorf("total cache bytes: %w", err)
	}
	return total, nil
}

// ListFileCacheLRU returns ready entries sorted by oldest last_accessed_at.
func (s *Store) ListFileCacheLRU(ctx context.Context, limit int) ([]FileCacheEntry, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, cache_key, book_id, file_idx, filename, mime_type, content_length,
		       COALESCE(codec,''), duration_seconds, status, download_progress,
		       COALESCE(error_message,''), relative_path, bytes_on_disk,
		       last_accessed_at, created_at
		FROM audiobook_file_cache WHERE status = 'ready'
		ORDER BY last_accessed_at ASC LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list file_cache lru: %w", err)
	}
	defer rows.Close()
	var out []FileCacheEntry
	for rows.Next() {
		e, err := scanFileCacheRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// DeleteFileCacheByKey removes an entry from the table.
func (s *Store) DeleteFileCacheByKey(ctx context.Context, key string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM audiobook_file_cache WHERE cache_key = $1`, key)
	return err
}

func scanFileCacheRow(row pgx.Row) (FileCacheEntry, error) {
	var e FileCacheEntry
	err := row.Scan(&e.ID, &e.CacheKey, &e.BookID, &e.FileIdx, &e.Filename, &e.MimeType, &e.ContentLength,
		&e.Codec, &e.DurationSeconds, &e.Status, &e.DownloadProgress,
		&e.ErrorMessage, &e.RelativePath, &e.BytesOnDisk,
		&e.LastAccessedAt, &e.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return FileCacheEntry{}, ErrNotFound
		}
		return FileCacheEntry{}, fmt.Errorf("scan file_cache: %w", err)
	}
	return e, nil
}
