-- ABS "Remove from Continue Listening" lets users hide a book they're
-- partway through without losing their position. progress.hidden_from_continue
-- is the flag the /me/items-in-progress and personalized "Continue
-- Listening" shelves filter on. Readd-to-continue-listening clears it.
ALTER TABLE progress
  ADD COLUMN IF NOT EXISTS hidden_from_continue BOOLEAN NOT NULL DEFAULT FALSE;
CREATE INDEX IF NOT EXISTS progress_user_updated_visible_idx
  ON progress (user_id, updated_at DESC) WHERE hidden_from_continue = FALSE;
