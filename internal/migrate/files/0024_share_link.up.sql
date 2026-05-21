-- Time-limited public share links for library items. The owner
-- mints a slug + optional TTL + optional use-count cap; recipients
-- access /share/{slug} without authentication. Useful for sending
-- an audiobook to a friend without giving them an account.
--
-- Expiry: rows past expires_at are filtered server-side at read.
-- A scheduled cleanup task purges them; nothing else depends on
-- their presence after expiry.
CREATE TABLE IF NOT EXISTS share_link (
  id           TEXT PRIMARY KEY,
  user_id      TEXT NOT NULL,
  slug         TEXT NOT NULL UNIQUE,
  item_id      TEXT NOT NULL,
  expires_at   TIMESTAMPTZ,
  -- max_uses caps how many recipients can open the share before it
  -- locks. 0 = unlimited (subject to expires_at).
  max_uses     INT NOT NULL DEFAULT 0,
  -- use_count increments on every successful public access.
  use_count    INT NOT NULL DEFAULT 0,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS share_link_user_idx ON share_link (user_id);
CREATE INDEX IF NOT EXISTS share_link_expires_idx ON share_link (expires_at);
