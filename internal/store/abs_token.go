package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ABSToken mirrors the abs_token table.
type ABSToken struct {
	ID         string
	UserID     string
	JTI        string
	DeviceID   string
	DeviceName string
	DeviceInfo map[string]any
	LastUsedAt time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	CreatedAt  time.Time
}

// InsertABSToken stores a new token row.
func (s *Store) InsertABSToken(ctx context.Context, t ABSToken) error {
	if t.ID == "" || t.UserID == "" || t.JTI == "" {
		return fmt.Errorf("id, user_id, jti required")
	}
	var info []byte
	if len(t.DeviceInfo) > 0 {
		b, err := json.Marshal(t.DeviceInfo)
		if err != nil {
			return fmt.Errorf("encode device_info: %w", err)
		}
		info = b
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO abs_token (id, user_id, jti, device_id, device_name, device_info, expires_at)
		VALUES ($1, $2, $3, NULLIF($4,''), NULLIF($5,''), $6, $7)
	`, t.ID, t.UserID, t.JTI, t.DeviceID, t.DeviceName, info, t.ExpiresAt)
	if err != nil {
		return fmt.Errorf("insert abs_token: %w", err)
	}
	return nil
}

// GetABSTokenByJTI looks up a token by jti. Active tokens have revoked_at IS
// NULL and expires_at > now().
func (s *Store) GetABSTokenByJTI(ctx context.Context, jti string) (ABSToken, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, user_id, jti, COALESCE(device_id,''), COALESCE(device_name,''),
		       device_info, last_used_at, expires_at, revoked_at, created_at
		FROM abs_token WHERE jti = $1
	`, jti)
	var t ABSToken
	var info []byte
	if err := row.Scan(&t.ID, &t.UserID, &t.JTI, &t.DeviceID, &t.DeviceName,
		&info, &t.LastUsedAt, &t.ExpiresAt, &t.RevokedAt, &t.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ABSToken{}, ErrNotFound
		}
		return ABSToken{}, fmt.Errorf("get abs_token: %w", err)
	}
	if len(info) > 0 {
		_ = json.Unmarshal(info, &t.DeviceInfo)
	}
	return t, nil
}

// RevokeABSTokenByJTI marks a token revoked.
func (s *Store) RevokeABSTokenByJTI(ctx context.Context, jti string) error {
	_, err := s.pool.Exec(ctx, `UPDATE abs_token SET revoked_at = now() WHERE jti = $1`, jti)
	if err != nil {
		return fmt.Errorf("revoke abs_token: %w", err)
	}
	return nil
}

// RevokeABSToken marks a token revoked by id with ownership check.
func (s *Store) RevokeABSToken(ctx context.Context, id, userID string) error {
	res, err := s.pool.Exec(ctx,
		`UPDATE abs_token SET revoked_at = now() WHERE id = $1 AND user_id = $2`,
		id, userID)
	if err != nil {
		return fmt.Errorf("revoke abs_token: %w", err)
	}
	if res.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListABSTokens returns tokens for a user (admin context: pass empty userID to list all).
func (s *Store) ListABSTokens(ctx context.Context, userID string) ([]ABSToken, error) {
	q := `
		SELECT id, user_id, jti, COALESCE(device_id,''), COALESCE(device_name,''),
		       device_info, last_used_at, expires_at, revoked_at, created_at
		FROM abs_token`
	args := []any{}
	if userID != "" {
		q += " WHERE user_id = $1"
		args = append(args, userID)
	}
	q += " ORDER BY created_at DESC LIMIT 500"

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list abs_tokens: %w", err)
	}
	defer rows.Close()
	var out []ABSToken
	for rows.Next() {
		var t ABSToken
		var info []byte
		if err := rows.Scan(&t.ID, &t.UserID, &t.JTI, &t.DeviceID, &t.DeviceName,
			&info, &t.LastUsedAt, &t.ExpiresAt, &t.RevokedAt, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan abs_token: %w", err)
		}
		if len(info) > 0 {
			_ = json.Unmarshal(info, &t.DeviceInfo)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TouchABSToken updates last_used_at to now() for the given jti.
func (s *Store) TouchABSToken(ctx context.Context, jti string) error {
	_, err := s.pool.Exec(ctx, `UPDATE abs_token SET last_used_at = now() WHERE jti = $1`, jti)
	return err
}
