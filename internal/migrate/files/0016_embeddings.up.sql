-- Embedding-based recommendations for audiobooks. Mirrors the host's
-- recommendations schema (silo/migrations/001_schema.up.sql +
-- 014_recommendations_embedding_lock_in.up.sql) so the engine can be
-- adapted with minimal divergence.
--
-- pgvector is required: an operator who runs this migration without
-- the extension will get a clear CREATE EXTENSION error on the first
-- line rather than a confusing schema error 10 lines down.
CREATE EXTENSION IF NOT EXISTS vector;

-- audiobook_embedding stores one vector per (book_id, library_id) —
-- the library scope keeps embeddings from one library distinct from
-- another's even when book ids collide (unlikely but cheap to enforce).
--
-- canonical_text is the embedding-input text we built when generating
-- the vector. We compare it to a freshly-rendered text on every
-- refresh tick so we only regenerate when the metadata actually
-- changed (the host calls this the "embedding lock-in" optimisation).
CREATE TABLE IF NOT EXISTS audiobook_embedding (
  book_id        TEXT NOT NULL,
  library_id     BIGINT NOT NULL REFERENCES portal_library(id) ON DELETE CASCADE,
  embedding      VECTOR(1536) NOT NULL,
  model          TEXT NOT NULL,
  canonical_text TEXT NOT NULL DEFAULT '',
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (book_id, library_id)
);

-- HNSW index on halfvec(3072) cosine ops is the host's pattern; for
-- 1536-dim vectors HNSW with cosine_ops gives sub-millisecond top-K
-- search up to a few million rows. Index build cost is paid once at
-- migrate time + amortised over inserts.
CREATE INDEX IF NOT EXISTS audiobook_embedding_hnsw_idx
  ON audiobook_embedding
  USING hnsw (embedding vector_cosine_ops);

-- audiobook_recommendation_cache stores precomputed similar-items
-- lists per (source_book, library) so the similar-items endpoint
-- doesn't run the vector search + blend pipeline on every request.
-- expires_at lets the worker invalidate stale results when the
-- catalog changes (new books added that might be similar).
CREATE TABLE IF NOT EXISTS audiobook_recommendation_cache (
  book_id    TEXT NOT NULL,
  library_id BIGINT NOT NULL REFERENCES portal_library(id) ON DELETE CASCADE,
  rec_type   TEXT NOT NULL,         -- 'similar', 'discover', ...
  items      JSONB NOT NULL,        -- [{book_id, score, reasons[]}, ...]
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (book_id, library_id, rec_type)
);
CREATE INDEX IF NOT EXISTS audiobook_recommendation_cache_expires_idx
  ON audiobook_recommendation_cache (expires_at);
