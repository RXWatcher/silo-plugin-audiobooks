package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// AuditLogEntry mirrors the audit_log table. Append-only — there's
// no UpdateAuditEntry. RetentionRows reaps oldest rows past 90 days.
type AuditLogEntry struct {
	ID         string
	ActorID    string
	Action     string
	EntityType string
	EntityID   string
	IP         string
	UserAgent  string
	Payload    json.RawMessage
	CreatedAt  time.Time
}

func (s *Store) AppendAuditEntry(ctx context.Context, e AuditLogEntry) error {
	if e.ID == "" || e.ActorID == "" || e.Action == "" || e.EntityType == "" {
		return errors.New("id, actor_id, action, entity_type required")
	}
	if len(e.Payload) == 0 {
		e.Payload = json.RawMessage("{}")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO audit_log (id, actor_id, action, entity_type, entity_id, ip, user_agent, payload)
		VALUES ($1, $2, $3, $4, NULLIF($5,''), NULLIF($6,''), NULLIF($7,''), $8)
	`, e.ID, e.ActorID, e.Action, e.EntityType, e.EntityID, e.IP, e.UserAgent, e.Payload)
	if err != nil {
		return fmt.Errorf("append audit_log: %w", err)
	}
	return nil
}

// AuditFilters narrows a query. All fields optional.
type AuditFilters struct {
	ActorID    string
	Action     string
	EntityType string
	EntityID   string
	SinceMs    int64
	UntilMs    int64
	Limit      int
}

func (s *Store) ListAuditEntries(ctx context.Context, f AuditFilters) ([]AuditLogEntry, error) {
	limit := f.Limit
	if limit <= 0 || limit > 2000 {
		limit = 200
	}
	q := `SELECT id, actor_id, action, entity_type, COALESCE(entity_id,''), COALESCE(ip,''),
	             COALESCE(user_agent,''), payload, created_at
	      FROM audit_log WHERE 1=1`
	args := []any{}
	add := func(clause string, v any) {
		args = append(args, v)
		q += fmt.Sprintf(" AND %s $%d", clause, len(args))
	}
	if f.ActorID != "" {
		add("actor_id =", f.ActorID)
	}
	if f.Action != "" {
		add("action =", f.Action)
	}
	if f.EntityType != "" {
		add("entity_type =", f.EntityType)
	}
	if f.EntityID != "" {
		add("entity_id =", f.EntityID)
	}
	if f.SinceMs > 0 {
		add("created_at >=", time.UnixMilli(f.SinceMs))
	}
	if f.UntilMs > 0 {
		add("created_at <=", time.UnixMilli(f.UntilMs))
	}
	args = append(args, limit)
	q += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list audit_log: %w", err)
	}
	defer rows.Close()
	var out []AuditLogEntry
	for rows.Next() {
		var e AuditLogEntry
		if err := rows.Scan(&e.ID, &e.ActorID, &e.Action, &e.EntityType, &e.EntityID,
			&e.IP, &e.UserAgent, &e.Payload, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan audit_log: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PurgeAuditOlderThan deletes rows older than `cutoff`. Scheduled
// task hook; returns the count purged so the scheduler can log.
func (s *Store) PurgeAuditOlderThan(ctx context.Context, cutoff time.Time) (int, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM audit_log WHERE created_at < $1
	`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("purge audit_log: %w", err)
	}
	return int(tag.RowsAffected()), nil
}
