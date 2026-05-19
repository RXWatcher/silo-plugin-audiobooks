CREATE TABLE IF NOT EXISTS listening_stats (
  user_id          TEXT NOT NULL,
  book_id          TEXT NOT NULL,
  listened_seconds INT NOT NULL DEFAULT 0,
  last_position    INT NOT NULL DEFAULT 0,
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, book_id)
);
