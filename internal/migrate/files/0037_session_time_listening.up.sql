-- Cumulative listening seconds per session row. The mobile client sends
-- timeListened on every sync tick (the delta since the last tick), and we
-- accumulate it here. This is the single source of truth for the listening
-- stats endpoints (/me/listening-stats and /me/stats/year/{year}); per-item
-- aggregation is computed by GROUP BY book_id at read time.
ALTER TABLE abs_playback_session
    ADD COLUMN IF NOT EXISTS time_listening_seconds BIGINT NOT NULL DEFAULT 0;

-- Partial index for the year/listening-stats aggregations, which only care
-- about rows that have non-zero listening time. Skipping the zero rows
-- keeps the index small even on a long history of opened-then-immediately-
-- closed sessions.
CREATE INDEX IF NOT EXISTS abs_session_user_time_idx
    ON abs_playback_session (user_id, profile_id, started_at)
    WHERE time_listening_seconds > 0;
