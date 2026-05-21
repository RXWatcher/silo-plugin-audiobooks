-- Audit log captures admin actions for operator visibility +
-- compliance. Each row records WHO did WHAT to WHICH entity from
-- WHERE (IP), with an optional payload diff. Append-only — the
-- store has no delete helper; rows age out via a scheduled
-- retention task (90 days default).
CREATE TABLE IF NOT EXISTS audit_log (
  id          TEXT PRIMARY KEY,
  actor_id    TEXT NOT NULL,
  action      TEXT NOT NULL,        -- "upsert_library" / "approve_import" / ...
  entity_type TEXT NOT NULL,        -- "portal_library" / "pending_import" / ...
  entity_id   TEXT,
  ip          TEXT,
  user_agent  TEXT,
  payload     JSONB NOT NULL DEFAULT '{}',
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS audit_log_created_idx ON audit_log (created_at DESC);
CREATE INDEX IF NOT EXISTS audit_log_actor_idx ON audit_log (actor_id, created_at DESC);
CREATE INDEX IF NOT EXISTS audit_log_entity_idx
  ON audit_log (entity_type, entity_id, created_at DESC);
