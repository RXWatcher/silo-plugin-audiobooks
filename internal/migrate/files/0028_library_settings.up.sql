-- Per-library admin-controlled settings. Free-form JSONB so adding
-- a new setting doesn't require a migration; the handler validates
-- the known keys + ignores unknown ones (so a downgrade doesn't
-- explode on rows written by a newer version).
--
-- Known keys (server-side validated):
--   allow_explicit    boolean — does this library surface explicit?
--   default_cover_url string  — fallback when an item has no cover
--   scan_throttle_rpm int     — cap on backend list-catalog calls/min
--   public_visible    boolean — appears in /libraries/public listings
ALTER TABLE portal_library
  ADD COLUMN IF NOT EXISTS settings JSONB NOT NULL DEFAULT '{}';
