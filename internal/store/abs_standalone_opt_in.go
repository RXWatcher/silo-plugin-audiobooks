package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// EnableStandaloneOptIn records that the given user has opted into
// standalone-port ABS body-creds login. Idempotent.
func (s *Store) EnableStandaloneOptIn(ctx context.Context, userID string) error {
	if userID == "" {
		return errors.New("user id required")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO abs_standalone_opt_ins (user_id) VALUES ($1)
		ON CONFLICT (user_id) DO NOTHING
	`, userID)
	if err != nil {
		return fmt.Errorf("enable standalone opt-in: %w", err)
	}
	return nil
}

// DisableStandaloneOptIn removes the opt-in row. Idempotent. Existing ABS
// tokens are not revoked here — that is the operator's call via the admin
// token-revoke endpoint.
func (s *Store) DisableStandaloneOptIn(ctx context.Context, userID string) error {
	if userID == "" {
		return errors.New("user id required")
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM abs_standalone_opt_ins WHERE user_id = $1`, userID)
	if err != nil {
		return fmt.Errorf("disable standalone opt-in: %w", err)
	}
	return nil
}

// HasStandaloneOptIn reports whether the user has opted into body-creds
// login. Returns (false, nil) when there is no row.
func (s *Store) HasStandaloneOptIn(ctx context.Context, userID string) (bool, error) {
	if userID == "" {
		return false, nil
	}
	row := s.pool.QueryRow(ctx, `SELECT 1 FROM abs_standalone_opt_ins WHERE user_id = $1`, userID)
	var one int
	if err := row.Scan(&one); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("has standalone opt-in: %w", err)
	}
	return true, nil
}
