package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// CustomMetadataProvider mirrors the ABS Custom Metadata Provider
// surface — an external HTTP /search endpoint the plugin can proxy
// for hand-import metadata lookups. AuthHeader is the literal value
// the provider expects in the request's auth header (spec uses
// "AUTHORIZATION").
type CustomMetadataProvider struct {
	ID         string
	Name       string
	URL        string
	AuthHeader string
	Enabled    bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

func (s *Store) UpsertCustomMetadataProvider(ctx context.Context, p CustomMetadataProvider) error {
	if p.ID == "" || p.Name == "" || p.URL == "" {
		return errors.New("id, name, url required")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO custom_metadata_provider (id, name, url, auth_header, enabled)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (id) DO UPDATE SET
			name        = EXCLUDED.name,
			url         = EXCLUDED.url,
			auth_header = EXCLUDED.auth_header,
			enabled     = EXCLUDED.enabled,
			updated_at  = now()
	`, p.ID, p.Name, p.URL, p.AuthHeader, p.Enabled)
	if err != nil {
		return fmt.Errorf("upsert provider: %w", err)
	}
	return nil
}

func (s *Store) GetCustomMetadataProvider(ctx context.Context, id string) (CustomMetadataProvider, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, name, url, auth_header, enabled, created_at, updated_at
		FROM custom_metadata_provider WHERE id = $1
	`, id)
	var p CustomMetadataProvider
	if err := row.Scan(&p.ID, &p.Name, &p.URL, &p.AuthHeader, &p.Enabled, &p.CreatedAt, &p.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CustomMetadataProvider{}, ErrNotFound
		}
		return CustomMetadataProvider{}, fmt.Errorf("get provider: %w", err)
	}
	return p, nil
}

// ListCustomMetadataProviders returns all configured providers,
// enabled first then alpha. The admin UI renders the whole list;
// /api/search/providers includes only the enabled ones.
func (s *Store) ListCustomMetadataProviders(ctx context.Context, enabledOnly bool) ([]CustomMetadataProvider, error) {
	q := `
		SELECT id, name, url, auth_header, enabled, created_at, updated_at
		FROM custom_metadata_provider`
	if enabledOnly {
		q += " WHERE enabled = TRUE"
	}
	q += " ORDER BY enabled DESC, LOWER(name)"
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}
	defer rows.Close()
	var out []CustomMetadataProvider
	for rows.Next() {
		var p CustomMetadataProvider
		if err := rows.Scan(&p.ID, &p.Name, &p.URL, &p.AuthHeader, &p.Enabled, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan provider: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) DeleteCustomMetadataProvider(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("id required")
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM custom_metadata_provider WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete provider: %w", err)
	}
	return nil
}
