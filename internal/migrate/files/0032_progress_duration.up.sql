-- progress.duration_seconds lets the ABS surface emit a real track
-- duration instead of 0. Backfilled to 0 for existing rows; populated
-- going forward from the client's progress-sync payload.
ALTER TABLE progress
  ADD COLUMN IF NOT EXISTS duration_seconds INT NOT NULL DEFAULT 0;
