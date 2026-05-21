-- Add cover bytes + mime to pending_import so the BookDrop scanner
-- can stash the embedded ID3 / MP4-atom cover for the admin review
-- UI. Cover sizes are typically 30–150 KB; bytea storage is fine
-- without external blob infra.
ALTER TABLE pending_import
  ADD COLUMN IF NOT EXISTS cover_data BYTEA,
  ADD COLUMN IF NOT EXISTS cover_mime TEXT NOT NULL DEFAULT '';
