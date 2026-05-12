package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Request mirrors the request table.
type Request struct {
	ID             string
	UserID         string
	Title          string
	Author         string
	ISBN           string
	Status         string
	TargetPluginID string
	ExternalID     string
	DeniedReason   string
	FailureReason  string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	FulfilledAt    *time.Time
}

// InsertRequest stores a new request row.
func (s *Store) InsertRequest(ctx context.Context, r Request) error {
	if r.ID == "" || r.UserID == "" || r.Title == "" {
		return fmt.Errorf("id, user_id, title required")
	}
	status := r.Status
	if status == "" {
		status = "pending"
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO request (id, user_id, title, author, isbn, status, target_plugin_id)
		VALUES ($1, $2, $3, NULLIF($4,''), NULLIF($5,''), $6, $7)
	`, r.ID, r.UserID, r.Title, r.Author, r.ISBN, status, r.TargetPluginID)
	if err != nil {
		return fmt.Errorf("insert request: %w", err)
	}
	return nil
}

// GetRequest reads a request by id.
func (s *Store) GetRequest(ctx context.Context, id string) (Request, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, user_id, title, COALESCE(author,''), COALESCE(isbn,''), status,
		       target_plugin_id, COALESCE(external_id,''),
		       COALESCE(denied_reason,''), COALESCE(failure_reason,''),
		       created_at, updated_at, fulfilled_at
		FROM request WHERE id = $1
	`, id)
	return scanRequest(row)
}

// ListUserRequests returns all of a user's requests, newest first.
func (s *Store) ListUserRequests(ctx context.Context, userID string, limit int) ([]Request, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, title, COALESCE(author,''), COALESCE(isbn,''), status,
		       target_plugin_id, COALESCE(external_id,''),
		       COALESCE(denied_reason,''), COALESCE(failure_reason,''),
		       created_at, updated_at, fulfilled_at
		FROM request WHERE user_id = $1
		ORDER BY created_at DESC LIMIT $2
	`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list user requests: %w", err)
	}
	defer rows.Close()
	return collectRequests(rows)
}

// ListByStatus returns requests across all users with the given status.
func (s *Store) ListRequestsByStatus(ctx context.Context, status string, limit int) ([]Request, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, title, COALESCE(author,''), COALESCE(isbn,''), status,
		       target_plugin_id, COALESCE(external_id,''),
		       COALESCE(denied_reason,''), COALESCE(failure_reason,''),
		       created_at, updated_at, fulfilled_at
		FROM request WHERE status = $1
		ORDER BY created_at DESC LIMIT $2
	`, status, limit)
	if err != nil {
		return nil, fmt.Errorf("list requests by status: %w", err)
	}
	defer rows.Close()
	return collectRequests(rows)
}

// ListReconcileCandidates returns acknowledged requests with non-empty
// external_id that haven't been fulfilled. Used by the reconciler.
func (s *Store) ListReconcileCandidates(ctx context.Context, limit int) ([]Request, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, title, COALESCE(author,''), COALESCE(isbn,''), status,
		       target_plugin_id, COALESCE(external_id,''),
		       COALESCE(denied_reason,''), COALESCE(failure_reason,''),
		       created_at, updated_at, fulfilled_at
		FROM request
		WHERE external_id IS NOT NULL AND status IN ('submitted','acknowledged')
		ORDER BY updated_at ASC LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list reconcile candidates: %w", err)
	}
	defer rows.Close()
	return collectRequests(rows)
}

// UpdateRequestStatus transitions a request to the given status.
func (s *Store) UpdateRequestStatus(ctx context.Context, id, status string, denied, failure string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE request SET status = $2,
			denied_reason = COALESCE(NULLIF($3,''), denied_reason),
			failure_reason = COALESCE(NULLIF($4,''), failure_reason),
			updated_at = now()
		WHERE id = $1
	`, id, status, denied, failure)
	if err != nil {
		return fmt.Errorf("update request status: %w", err)
	}
	return nil
}

// SetRequestExternal stores the backend's external_id and status (typically
// "submitted" or "acknowledged").
func (s *Store) SetRequestExternal(ctx context.Context, id, externalID, status string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE request SET external_id = NULLIF($2,''), status = $3, updated_at = now()
		WHERE id = $1
	`, id, externalID, status)
	if err != nil {
		return fmt.Errorf("set request external: %w", err)
	}
	return nil
}

// GetByExternalIDStub reads the request matching external_id. Returns
// ErrNotFound when missing.
func (s *Store) GetByExternalIDStub(ctx context.Context, externalID string) (Request, error) {
	if externalID == "" {
		return Request{}, ErrNotFound
	}
	row := s.pool.QueryRow(ctx, `
		SELECT id, user_id, title, COALESCE(author,''), COALESCE(isbn,''), status,
		       target_plugin_id, COALESCE(external_id,''),
		       COALESCE(denied_reason,''), COALESCE(failure_reason,''),
		       created_at, updated_at, fulfilled_at
		FROM request WHERE external_id = $1
	`, externalID)
	return scanRequest(row)
}

// MarkRequestFulfilled sets status='imported' and fulfilled_at=now() for the
// request matching external_id.
func (s *Store) MarkRequestFulfilled(ctx context.Context, externalID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE request SET status = 'imported', fulfilled_at = now(), updated_at = now()
		WHERE external_id = $1 AND status NOT IN ('imported','denied','cancelled')
	`, externalID)
	if err != nil {
		return fmt.Errorf("mark request fulfilled: %w", err)
	}
	return nil
}

// CancelRequest sets status='cancelled' for the user's own request.
func (s *Store) CancelRequest(ctx context.Context, id, userID string) error {
	res, err := s.pool.Exec(ctx, `
		UPDATE request SET status = 'cancelled', updated_at = now()
		WHERE id = $1 AND user_id = $2 AND status IN ('pending','submitted','acknowledged')
	`, id, userID)
	if err != nil {
		return fmt.Errorf("cancel request: %w", err)
	}
	if res.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func scanRequest(row pgx.Row) (Request, error) {
	var r Request
	var fulfilled *time.Time
	err := row.Scan(&r.ID, &r.UserID, &r.Title, &r.Author, &r.ISBN, &r.Status,
		&r.TargetPluginID, &r.ExternalID, &r.DeniedReason, &r.FailureReason,
		&r.CreatedAt, &r.UpdatedAt, &fulfilled)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Request{}, ErrNotFound
		}
		return Request{}, fmt.Errorf("scan request: %w", err)
	}
	r.FulfilledAt = fulfilled
	return r, nil
}

func collectRequests(rows pgx.Rows) ([]Request, error) {
	var out []Request
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
