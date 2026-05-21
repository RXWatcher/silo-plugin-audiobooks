-- Custom Metadata Providers — ABS-compat surface for letting
-- admins point at an external HTTP /search endpoint that follows
-- the upstream provider spec (custom-metadata-provider-
-- specification.yaml). The plugin proxies search queries through
-- the configured provider URL + auth header so the user-facing
-- search bar can surface external metadata for hand-import.
CREATE TABLE IF NOT EXISTS custom_metadata_provider (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  url         TEXT NOT NULL,
  -- auth_header is the literal value the provider expects in the
  -- Authorization (or "AUTHORIZATION" per the spec) request header.
  -- Stored plain; rotate by setting a new value.
  auth_header TEXT NOT NULL DEFAULT '',
  enabled     BOOLEAN NOT NULL DEFAULT TRUE,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
