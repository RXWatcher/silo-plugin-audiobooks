ALTER TABLE backend_config
  ADD COLUMN IF NOT EXISTS standalone_login_mode TEXT NOT NULL DEFAULT 'disabled';

CREATE TABLE IF NOT EXISTS abs_standalone_opt_ins (
  user_id    TEXT PRIMARY KEY,
  enabled_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
