DROP INDEX IF EXISTS abs_session_user_time_idx;
ALTER TABLE abs_playback_session DROP COLUMN IF EXISTS time_listening_seconds;
