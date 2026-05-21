ALTER TABLE backend_config
  ADD COLUMN IF NOT EXISTS media_signing_secret TEXT;
