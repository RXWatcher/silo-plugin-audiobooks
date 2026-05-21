package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// PendingImport is one row in the bookdrop queue. status moves
// pending → editing → approved → imported (or → rejected).
type PendingImport struct {
	ID              string
	FilePath        string
	SizeBytes       int64
	Metadata        json.RawMessage
	Status          string
	ErrorMessage    string
	TargetLibraryID *int64
	// CoverData carries the embedded cover the scanner extracted
	// from the audio file's ID3v2 APIC or MP4 'covr' atom. Empty
	// when the file has no embedded cover. CoverMIME pairs with
	// it (typically image/jpeg or image/png).
	CoverData []byte
	CoverMIME string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// UpsertPendingImport is the scanner-side entry point. Idempotent
// on file_path so re-scanning a directory doesn't duplicate rows.
// Existing rows keep their status + admin edits; only size +
// metadata + cover refresh.
func (s *Store) UpsertPendingImport(ctx context.Context, p PendingImport) error {
	if p.ID == "" || p.FilePath == "" {
		return errors.New("id, file_path required")
	}
	if len(p.Metadata) == 0 {
		p.Metadata = json.RawMessage("{}")
	}
	if p.Status == "" {
		p.Status = "pending"
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO pending_import (id, file_path, size_bytes, metadata, status, cover_data, cover_mime)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (file_path) DO UPDATE SET
			size_bytes = EXCLUDED.size_bytes,
			-- Preserve admin edits when the row already has them:
			-- only refresh metadata when status is still 'pending'.
			metadata = CASE
				WHEN pending_import.status = 'pending' THEN EXCLUDED.metadata
				ELSE pending_import.metadata
			END,
			cover_data = CASE
				WHEN pending_import.status = 'pending' THEN EXCLUDED.cover_data
				ELSE pending_import.cover_data
			END,
			cover_mime = CASE
				WHEN pending_import.status = 'pending' THEN EXCLUDED.cover_mime
				ELSE pending_import.cover_mime
			END,
			updated_at = now()
	`, p.ID, p.FilePath, p.SizeBytes, p.Metadata, p.Status, p.CoverData, p.CoverMIME)
	if err != nil {
		return fmt.Errorf("upsert pending_import: %w", err)
	}
	return nil
}

// GetPendingImportCover returns the cover bytes + MIME for one row.
// Separate from GetPendingImport because the cover bytes can be
// ~150 KB and pulling them on every list query is wasteful.
func (s *Store) GetPendingImportCover(ctx context.Context, id string) ([]byte, string, error) {
	var data []byte
	var mime string
	err := s.pool.QueryRow(ctx, `
		SELECT cover_data, cover_mime FROM pending_import WHERE id = $1
	`, id).Scan(&data, &mime)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", ErrNotFound
		}
		return nil, "", fmt.Errorf("get pending_import cover: %w", err)
	}
	return data, mime, nil
}

// UpdatePendingImport sets the admin-editable fields (metadata,
// status, target_library_id, error_message). Used by approve /
// reject / edit handlers.
func (s *Store) UpdatePendingImport(ctx context.Context, p PendingImport) error {
	if p.ID == "" {
		return errors.New("id required")
	}
	if len(p.Metadata) == 0 {
		p.Metadata = json.RawMessage("{}")
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE pending_import SET
			metadata          = $1,
			status            = $2,
			error_message     = NULLIF($3,''),
			target_library_id = $4,
			updated_at        = now()
		WHERE id = $5
	`, p.Metadata, p.Status, p.ErrorMessage, p.TargetLibraryID, p.ID)
	if err != nil {
		return fmt.Errorf("update pending_import: %w", err)
	}
	return nil
}

func (s *Store) GetPendingImport(ctx context.Context, id string) (PendingImport, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, file_path, size_bytes, metadata, status, COALESCE(error_message,''),
		       target_library_id, created_at, updated_at
		FROM pending_import WHERE id = $1
	`, id)
	var p PendingImport
	if err := row.Scan(&p.ID, &p.FilePath, &p.SizeBytes, &p.Metadata, &p.Status,
		&p.ErrorMessage, &p.TargetLibraryID, &p.CreatedAt, &p.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PendingImport{}, ErrNotFound
		}
		return PendingImport{}, fmt.Errorf("get pending_import: %w", err)
	}
	return p, nil
}

func (s *Store) ListPendingImports(ctx context.Context, status string, limit int) ([]PendingImport, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	q := `
		SELECT id, file_path, size_bytes, metadata, status, COALESCE(error_message,''),
		       target_library_id, created_at, updated_at
		FROM pending_import`
	args := []any{}
	if status != "" {
		q += " WHERE status = $1"
		args = append(args, status)
		q += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args)+1)
	} else {
		q += " ORDER BY created_at DESC LIMIT $1"
	}
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list pending_import: %w", err)
	}
	defer rows.Close()
	var out []PendingImport
	for rows.Next() {
		var p PendingImport
		if err := rows.Scan(&p.ID, &p.FilePath, &p.SizeBytes, &p.Metadata, &p.Status,
			&p.ErrorMessage, &p.TargetLibraryID, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan pending_import: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) DeletePendingImport(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("id required")
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM pending_import WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete pending_import: %w", err)
	}
	return nil
}
