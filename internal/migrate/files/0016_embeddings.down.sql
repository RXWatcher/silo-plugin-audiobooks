DROP TABLE IF EXISTS audiobook_recommendation_cache;
DROP TABLE IF EXISTS audiobook_embedding;
-- Don't drop the pgvector extension — other tables may depend on it.
