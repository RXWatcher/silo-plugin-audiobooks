package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// StandaloneLoginMode is the operator on/off switch for the standalone-port
// /abs/api/login body-creds path.
const (
	StandaloneLoginModeDisabled = "disabled"
	StandaloneLoginModeEnabled  = "enabled"
)

// NormalizeStandaloneLoginMode coerces any truthy legacy value
// (enabled / opt_in / all_accounts) to "enabled", everything else to
// "disabled". opt_in / all_accounts are pre-profile legacy values.
func NormalizeStandaloneLoginMode(v string) string {
	switch v {
	case StandaloneLoginModeEnabled, "opt_in", "all_accounts":
		return StandaloneLoginModeEnabled
	default:
		return StandaloneLoginModeDisabled
	}
}

// BackendConfig is the singleton row in backend_config (id=1). The portal no
// longer owns streaming/caching knobs — backend plugins serve bytes from
// their own filesystem mounts. Fields removed (still present as unused DB
// columns until a future migration drops them): streaming_mode, cache_dir,
// cache_max_size_gb, cache_download_concurrency, path_remappings.
type BackendConfig struct {
	TargetBackendPluginID  string
	TargetBackendInstallID string
	TargetRequestPluginID  string
	TargetRequestInstallID string
	AutoApproveRequests    bool
	ABSJWTSecret           []byte
	ABSAccessTTLHours      int
	ABSRefreshTTLDays      int
	StandaloneHTTPListen   string
	// StandaloneLoginMode is one of the StandaloneLoginMode* constants.
	StandaloneLoginMode string
	// MediaSigningSecret is the HMAC key the portal signs media URL tokens
	// with. Backend plugins (bw-audio, local-audiobooks) must hold the same
	// secret in their stream_signing_secret field. Stored as base64 in the
	// DB; the portal/back end accept both base64 and raw bytes at verify time.
	MediaSigningSecret string
	UpdatedAt          time.Time
}

type LegacyBackendConfig struct {
	StandaloneHTTPListen string
}

func (c BackendConfig) BackendInstallID() string {
	if c.TargetBackendInstallID != "" {
		return c.TargetBackendInstallID
	}
	if isNumericID(c.TargetBackendPluginID) {
		return c.TargetBackendPluginID
	}
	return ""
}

func (c BackendConfig) BackendPluginID() string {
	if isNumericID(c.TargetBackendPluginID) {
		return ""
	}
	return c.TargetBackendPluginID
}

func (c BackendConfig) RequestProviderInstallID() string {
	if c.TargetRequestInstallID != "" {
		return c.TargetRequestInstallID
	}
	if isNumericID(c.TargetRequestPluginID) {
		return c.TargetRequestPluginID
	}
	return c.BackendInstallID()
}

func (c BackendConfig) RequestProviderPluginID() string {
	if c.TargetRequestPluginID == "" {
		return c.BackendPluginID()
	}
	if isNumericID(c.TargetRequestPluginID) {
		return ""
	}
	return c.TargetRequestPluginID
}

func isNumericID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// GetBackendConfig returns the singleton row; if no row exists yet, returns
// ErrNotFound (caller should initialise).
func (s *Store) GetBackendConfig(ctx context.Context) (BackendConfig, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT target_backend_plugin_id, target_backend_installation_id,
		       target_request_provider_plugin_id, target_request_provider_installation_id,
		       auto_approve_requests, abs_jwt_secret,
		       abs_access_token_ttl_hours, abs_refresh_token_ttl_days,
		       COALESCE(standalone_http_listen,''), COALESCE(standalone_login_mode,''),
		       COALESCE(media_signing_secret,''), updated_at
		FROM backend_config WHERE id = 1
	`)
	var cfg BackendConfig
	if err := row.Scan(
		&cfg.TargetBackendPluginID, &cfg.TargetBackendInstallID,
		&cfg.TargetRequestPluginID, &cfg.TargetRequestInstallID,
		&cfg.AutoApproveRequests, &cfg.ABSJWTSecret,
		&cfg.ABSAccessTTLHours, &cfg.ABSRefreshTTLDays,
		&cfg.StandaloneHTTPListen, &cfg.StandaloneLoginMode,
		&cfg.MediaSigningSecret, &cfg.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return BackendConfig{}, ErrNotFound
		}
		return BackendConfig{}, fmt.Errorf("get backend_config: %w", err)
	}
	cfg.StandaloneLoginMode = NormalizeStandaloneLoginMode(cfg.StandaloneLoginMode)
	return cfg, nil
}

// EnsureBackendConfig inserts the singleton row if missing, with sane defaults
// and the provided ABS JWT secret. Returns the resulting config.
func (s *Store) EnsureBackendConfig(ctx context.Context, defaultSecret []byte) (BackendConfig, error) {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO backend_config (id, abs_jwt_secret, standalone_http_listen) VALUES (1, $1, $2)
		ON CONFLICT (id) DO NOTHING
	`, defaultSecret, defaultBackendConfigShape().StandaloneHTTPListen)
	if err != nil {
		return BackendConfig{}, fmt.Errorf("ensure backend_config: %w", err)
	}
	return s.GetBackendConfig(ctx)
}

// UpdateBackendConfig writes the supplied fields atomically. The legacy
// streaming/cache/path_remappings columns are still in the table for back-
// compat with the schema; they're never read or written by this code.
func (s *Store) UpdateBackendConfig(ctx context.Context, cfg BackendConfig) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO backend_config
			(id, target_backend_plugin_id, target_backend_installation_id,
			 target_request_provider_plugin_id, target_request_provider_installation_id,
			 auto_approve_requests,
			 abs_jwt_secret, abs_access_token_ttl_hours, abs_refresh_token_ttl_days,
			 standalone_http_listen, standalone_login_mode,
			 media_signing_secret, updated_at)
		VALUES (1, $1, $2, $3, $4, $5, $6, $7, $8, NULLIF($9,''), $10, NULLIF($11,''), now())
		ON CONFLICT (id) DO UPDATE SET
			target_backend_plugin_id    = EXCLUDED.target_backend_plugin_id,
			target_backend_installation_id = EXCLUDED.target_backend_installation_id,
			target_request_provider_plugin_id = EXCLUDED.target_request_provider_plugin_id,
			target_request_provider_installation_id = EXCLUDED.target_request_provider_installation_id,
			auto_approve_requests       = EXCLUDED.auto_approve_requests,
			abs_jwt_secret              = COALESCE(EXCLUDED.abs_jwt_secret, backend_config.abs_jwt_secret),
			abs_access_token_ttl_hours  = EXCLUDED.abs_access_token_ttl_hours,
			abs_refresh_token_ttl_days  = EXCLUDED.abs_refresh_token_ttl_days,
			standalone_http_listen      = EXCLUDED.standalone_http_listen,
			standalone_login_mode       = EXCLUDED.standalone_login_mode,
			media_signing_secret        = COALESCE(EXCLUDED.media_signing_secret, backend_config.media_signing_secret),
			updated_at                  = now()
	`,
		cfg.TargetBackendPluginID, cfg.TargetBackendInstallID,
		cfg.TargetRequestPluginID, cfg.TargetRequestInstallID,
		cfg.AutoApproveRequests,
		cfg.ABSJWTSecret, cfg.ABSAccessTTLHours, cfg.ABSRefreshTTLDays,
		cfg.StandaloneHTTPListen, NormalizeStandaloneLoginMode(cfg.StandaloneLoginMode),
		cfg.MediaSigningSecret,
	)
	if err != nil {
		return fmt.Errorf("update backend_config: %w", err)
	}
	return nil
}

func (s *Store) ImportLegacyBackendConfig(ctx context.Context, legacy LegacyBackendConfig) (BackendConfig, error) {
	current, err := s.GetBackendConfig(ctx)
	if err != nil {
		return BackendConfig{}, err
	}
	if !backendConfigIsDefault(current) {
		return current, nil
	}
	next := current
	next.StandaloneHTTPListen = legacy.StandaloneHTTPListen
	if reflect.DeepEqual(backendConfigComparable(next), backendConfigComparable(current)) {
		return current, nil
	}
	if err := s.UpdateBackendConfig(ctx, next); err != nil {
		return BackendConfig{}, err
	}
	return s.GetBackendConfig(ctx)
}

func defaultBackendConfigShape() BackendConfig {
	return BackendConfig{
		ABSAccessTTLHours: 24,
		ABSRefreshTTLDays: 30,
		// Bind 0.0.0.0, not 127.0.0.1. The plugin runs as a subprocess inside
		// the silo container, which publishes 9998-9999. Docker forwards
		// the published port to the container's eth0 interface via DNAT — a
		// listener on 127.0.0.1 is reachable only from inside the container,
		// so a loopback bind makes the published port dead and ABS clients
		// get connection-refused before they ever reach /status or /login.
		// Standalone /login stays gated by StandaloneLoginMode, so binding
		// 0.0.0.0 by default exposes only /status + /ping until an admin
		// opts a login mode in.
		StandaloneHTTPListen: "0.0.0.0:9998",
		StandaloneLoginMode:  StandaloneLoginModeDisabled,
	}
}

func backendConfigIsDefault(c BackendConfig) bool {
	return reflect.DeepEqual(backendConfigComparable(c), backendConfigComparable(defaultBackendConfigShape()))
}

func backendConfigComparable(c BackendConfig) BackendConfig {
	c.ABSJWTSecret = nil
	c.UpdatedAt = time.Time{}
	return c
}
