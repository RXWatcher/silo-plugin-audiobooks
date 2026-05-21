-- Published RSS feeds — listeners "open" a feed pointed at a
-- library item (an audiobook served as one episode per track), a
-- series (one episode per book), or a collection (same).
--
-- The slug is an unguessable random token; the public route
-- /feed/{slug}.xml renders the RSS by reading this row + walking
-- the referenced source. Closing a feed deletes the row, breaking
-- the URL.
CREATE TABLE IF NOT EXISTS rss_feed (
  id          TEXT PRIMARY KEY,
  user_id     TEXT NOT NULL,
  slug        TEXT NOT NULL UNIQUE,
  -- entity_type: 'item' | 'series' | 'collection'
  entity_type TEXT NOT NULL,
  entity_id   TEXT NOT NULL,
  title       TEXT NOT NULL,
  description TEXT,
  -- cover_path is the relative path we serve for itunes:image — set
  -- from the source entity's cover at open-time. Plain-text so
  -- regeneration is trivial.
  cover_path  TEXT,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS rss_feed_user_idx ON rss_feed (user_id);
CREATE INDEX IF NOT EXISTS rss_feed_entity_idx
  ON rss_feed (entity_type, entity_id);
