-- Drops tables + columns belonging to features cut from scope:
-- BookDrop, Audit log, Web Push, Sync change-log (HLC), per-library
-- settings. IF EXISTS so fresh installs that never had these tables
-- run this cleanly.
DROP TABLE IF EXISTS pending_import;
DROP TABLE IF EXISTS audit_log;
DROP TABLE IF EXISTS push_subscription;
DROP TABLE IF EXISTS bookmark_change;
ALTER TABLE portal_library DROP COLUMN IF EXISTS settings;
