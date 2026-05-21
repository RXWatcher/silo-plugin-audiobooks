DROP INDEX IF EXISTS progress_user_updated_visible_idx;
ALTER TABLE progress DROP COLUMN IF EXISTS hidden_from_continue;
