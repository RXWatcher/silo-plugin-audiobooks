package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// PathRemap represents a single source→target rewrite rule for direct mode.
type PathRemap struct {
	SourcePath string `json:"source_path"`
	TargetPath string `json:"target_path"`
}

// BackendConfig is the singleton row in backend_config (id=1).
type BackendConfig struct {
	TargetBackendPluginID    string
	AutoApproveRequests      bool
	StreamingMode            string
	CacheDir                 string
	CacheMaxSizeGB           int
	CacheDownloadConcurrency int
	PathRemappings           []PathRemap
	ABSJWTSecret             []byte
	ABSAccessTTLHours        int
	ABSRefreshTTLDays        int
	UpdatedAt                time.Time
}

// GetBackendConfig returns the singleton row; if no row exists yet, returns
// ErrNotFound (caller should initialise).
func (s *Store) GetBackendConfig(ctx context.Context) (BackendConfig, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT target_backend_plugin_id, auto_approve_requests, streaming_mode,
		       COALESCE(cache_dir,''), cache_max_size_gb, cache_download_concurrency,
		       path_remappings, abs_jwt_secret,
		       abs_access_token_ttl_hours, abs_refresh_token_ttl_days, updated_at
		FROM backend_config WHERE id = 1
	`)
	var cfg BackendConfig
	var remapsJSON []byte
	if err := row.Scan(
		&cfg.TargetBackendPluginID, &cfg.AutoApproveRequests, &cfg.StreamingMode,
		&cfg.CacheDir, &cfg.CacheMaxSizeGB, &cfg.CacheDownloadConcurrency,
		&remapsJSON, &cfg.ABSJWTSecret,
		&cfg.ABSAccessTTLHours, &cfg.ABSRefreshTTLDays, &cfg.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return BackendConfig{}, ErrNotFound
		}
		return BackendConfig{}, fmt.Errorf("get backend_config: %w", err)
	}
	if len(remapsJSON) > 0 {
		_ = json.Unmarshal(remapsJSON, &cfg.PathRemappings)
	}
	return cfg, nil
}

// EnsureBackendConfig inserts the singleton row if missing, with sane defaults
// and the provided ABS JWT secret. Returns the resulting config.
func (s *Store) EnsureBackendConfig(ctx context.Context, defaultSecret []byte) (BackendConfig, error) {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO backend_config (id, abs_jwt_secret) VALUES (1, $1)
		ON CONFLICT (id) DO NOTHING
	`, defaultSecret)
	if err != nil {
		return BackendConfig{}, fmt.Errorf("ensure backend_config: %w", err)
	}
	return s.GetBackendConfig(ctx)
}

// UpdateBackendConfig writes the supplied non-zero fields atomically. Fields
// passed as their zero values are not modified — this matches the form's
// patch-style admin behaviour.
func (s *Store) UpdateBackendConfig(ctx context.Context, cfg BackendConfig) error {
	remaps, err := json.Marshal(cfg.PathRemappings)
	if err != nil {
		return fmt.Errorf("encode path_remappings: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO backend_config
			(id, target_backend_plugin_id, auto_approve_requests, streaming_mode,
			 cache_dir, cache_max_size_gb, cache_download_concurrency, path_remappings,
			 abs_jwt_secret, abs_access_token_ttl_hours, abs_refresh_token_ttl_days, updated_at)
		VALUES (1, $1, $2, $3, NULLIF($4,''), $5, $6, $7, $8, $9, $10, now())
		ON CONFLICT (id) DO UPDATE SET
			target_backend_plugin_id    = EXCLUDED.target_backend_plugin_id,
			auto_approve_requests       = EXCLUDED.auto_approve_requests,
			streaming_mode              = EXCLUDED.streaming_mode,
			cache_dir                   = COALESCE(EXCLUDED.cache_dir, backend_config.cache_dir),
			cache_max_size_gb           = EXCLUDED.cache_max_size_gb,
			cache_download_concurrency  = EXCLUDED.cache_download_concurrency,
			path_remappings             = EXCLUDED.path_remappings,
			abs_jwt_secret              = COALESCE(EXCLUDED.abs_jwt_secret, backend_config.abs_jwt_secret),
			abs_access_token_ttl_hours  = EXCLUDED.abs_access_token_ttl_hours,
			abs_refresh_token_ttl_days  = EXCLUDED.abs_refresh_token_ttl_days,
			updated_at                  = now()
	`,
		cfg.TargetBackendPluginID, cfg.AutoApproveRequests, cfg.StreamingMode,
		cfg.CacheDir, cfg.CacheMaxSizeGB, cfg.CacheDownloadConcurrency, remaps,
		cfg.ABSJWTSecret, cfg.ABSAccessTTLHours, cfg.ABSRefreshTTLDays,
	)
	if err != nil {
		return fmt.Errorf("update backend_config: %w", err)
	}
	return nil
}
