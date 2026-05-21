package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// LibrarySettings is the typed view over portal_library.settings.
// JSONB on disk; struct in Go. New keys add here + flow through
// without a migration. Unknown keys on read are silently ignored
// so a downgrade doesn't explode on rows written by a newer
// version.
type LibrarySettings struct {
	AllowExplicit   bool   `json:"allow_explicit"`
	DefaultCoverURL string `json:"default_cover_url,omitempty"`
	ScanThrottleRPM int    `json:"scan_throttle_rpm,omitempty"`
	PublicVisible   bool   `json:"public_visible"`
}

// GetLibrarySettings reads + decodes the settings column for one
// portal_library row. Returns defaults when the row exists but has
// no settings; ErrNotFound when the library itself is missing.
func (s *Store) GetLibrarySettings(ctx context.Context, libraryID int64) (LibrarySettings, error) {
	if libraryID <= 0 {
		return LibrarySettings{}, errors.New("library_id required")
	}
	var raw json.RawMessage
	err := s.pool.QueryRow(ctx, `
		SELECT settings FROM portal_library WHERE id = $1
	`, libraryID).Scan(&raw)
	if err != nil {
		return LibrarySettings{}, fmt.Errorf("get library_settings: %w", err)
	}
	var out LibrarySettings
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out, nil
}

func (s *Store) SetLibrarySettings(ctx context.Context, libraryID int64, ls LibrarySettings) error {
	if libraryID <= 0 {
		return errors.New("library_id required")
	}
	b, err := json.Marshal(ls)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE portal_library SET settings = $1 WHERE id = $2
	`, b, libraryID)
	if err != nil {
		return fmt.Errorf("set library_settings: %w", err)
	}
	return nil
}
