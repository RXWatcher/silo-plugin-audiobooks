package recommend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pgvector/pgvector-go"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/backend"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
)

// Logger is the narrow logging surface — hclog + slog both satisfy.
type Logger interface {
	Debug(msg string, args ...any)
	Warn(msg string, args ...any)
}

type noopLogger struct{}

func (noopLogger) Debug(string, ...any) {}
func (noopLogger) Warn(string, ...any)  {}

// Engine is the long-lived recommend service. Construct once at
// process scope and share — it's stateless beyond its dependencies.
type Engine struct {
	client  *Client
	store   *store.Store
	backend *backend.Client
	logger  Logger
}

// New builds an Engine. cfg may be a zero-value ClientConfig — the
// engine will silently no-op embedding work until an operator
// configures the EMBEDDING_BASE_URL / EMBEDDING_MODEL env vars.
func New(cfg ClientConfig, st *store.Store, bk *backend.Client, logger Logger) *Engine {
	if logger == nil {
		logger = noopLogger{}
	}
	return &Engine{
		client:  NewClient(cfg),
		store:   st,
		backend: bk,
		logger:  logger,
	}
}

// Configured reports whether the engine has enough config to do
// real work. Background workers + the similar-items endpoint check
// this before scheduling embedding generation; an unconfigured
// deployment returns empty results without errors.
func (e *Engine) Configured() bool {
	return e != nil && e.client.Configured() && e.store != nil
}

// EmbedAndStore generates an embedding for one audiobook and writes
// it to audiobook_embedding. Skipped (no-op) when the canonical text
// hasn't changed since the last embedding (the "lock-in" trick) —
// re-embedding identical text wastes API calls.
//
// Idempotent: calling twice with the same detail is safe. Returns
// nil + a logged Debug when the embedding already matches.
func (e *Engine) EmbedAndStore(ctx context.Context, libraryID int64, d backend.AudiobookDetail) error {
	if !e.Configured() {
		return nil
	}
	canonicalText := BuildEmbeddingText(d)
	// Lock-in check — skip when text and model both match what we
	// already stored.
	if existing, err := e.store.GetAudiobookEmbedding(ctx, libraryID, d.ID); err == nil {
		if existing.CanonicalText == canonicalText && existing.Model == e.client.Model() {
			e.logger.Debug("recommend: embedding unchanged",
				"library_id", libraryID, "book_id", d.ID)
			return nil
		}
	}
	vecs, err := e.client.Embed(ctx, []string{canonicalText})
	if err != nil {
		return fmt.Errorf("embed: %w", err)
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return errors.New("embed returned no vector")
	}
	return e.store.UpsertAudiobookEmbedding(ctx, store.AudiobookEmbedding{
		BookID:        d.ID,
		LibraryID:     libraryID,
		Embedding:     pgvector.NewVector(vecs[0]),
		Model:         e.client.Model(),
		CanonicalText: canonicalText,
	})
}

// Similar returns up to `limit` similar audiobook ids for the given
// source, using the cached entry when fresh and recomputing otherwise.
// Recomputed results are stored in audiobook_recommendation_cache with
// a 6-hour TTL (host uses 24h; we err shorter while we're still
// figuring out tuning).
func (e *Engine) Similar(ctx context.Context, libraryID int64, bookID string, limit int) ([]store.SimilarAudiobook, error) {
	if !e.Configured() {
		return nil, nil
	}
	// Cache hit.
	if cached, err := e.store.GetRecommendationCache(ctx, libraryID, bookID, "similar"); err == nil {
		var items []store.SimilarAudiobook
		if err := json.Unmarshal(cached.Items, &items); err == nil {
			if len(items) > limit {
				items = items[:limit]
			}
			return items, nil
		}
	}

	// Source vector.
	src, err := e.store.GetAudiobookEmbedding(ctx, libraryID, bookID)
	if err != nil {
		// Not embedded yet — return empty rather than error. A worker
		// or the next /play tick will backfill the embedding.
		e.logger.Debug("recommend: no source embedding",
			"library_id", libraryID, "book_id", bookID, "err", err.Error())
		return nil, nil
	}

	// Vector search with the source excluded. Over-fetch 3x to leave
	// headroom for future blend/filter stages without re-running the
	// search.
	candidates, err := e.store.FindSimilarAudiobooks(ctx, src.Embedding, []string{bookID}, limit*3)
	if err != nil {
		return nil, fmt.Errorf("find similar: %w", err)
	}
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	// Cache the trimmed result. JSON-encode once so subsequent reads
	// don't pay the encode cost.
	if blob, err := json.Marshal(candidates); err == nil {
		_ = e.store.SetRecommendationCache(ctx, libraryID, bookID, "similar", blob, 6*time.Hour)
	}
	return candidates, nil
}

// BackfillLibrary walks every audiobook in a library and embeds any
// that don't yet have a vector (or whose canonical text has drifted).
// Returns the number of embeddings written, exclusive of skipped /
// unchanged rows. Designed to be called from a scheduler tick — the
// per-book EmbedAndStore is no-op when nothing changed, so a daily
// re-walk is cheap.
//
// limit caps how many books we'll embed in one call (so a misconfigured
// embedding endpoint can't spend the whole world's worth of API
// credits on a single sweep). 0 → 200, matching the host's default.
func (e *Engine) BackfillLibrary(ctx context.Context, lib store.PortalLibrary, bearer string, limit int) (int, error) {
	if !e.Configured() {
		return 0, nil
	}
	if lib.BackendPluginID == "" {
		return 0, nil
	}
	if limit <= 0 {
		limit = 200
	}
	pager := backend.ListParams{Limit: limit, LibraryID: backendLibraryID(lib)}
	out, err := e.backend.ListCatalog(ctx, bearer, lib.BackendPluginID, pager)
	if err != nil {
		return 0, fmt.Errorf("list catalog: %w", err)
	}
	embedded := 0
	for _, s := range out.Items {
		select {
		case <-ctx.Done():
			return embedded, ctx.Err()
		default:
		}
		// Hydrate to AudiobookDetail when the summary already carries
		// description + genres. Otherwise call GetDetail. The v1.1
		// summary has author refs but not description; skip if no
		// description (the text-builder produces a degraded vector
		// without it).
		detail, derr := e.backend.GetDetail(ctx, bearer, lib.BackendPluginID, s.ID)
		if derr != nil {
			e.logger.Warn("recommend: detail fetch failed",
				"book_id", s.ID, "err", derr.Error())
			continue
		}
		if err := e.EmbedAndStore(ctx, lib.ID, detail); err != nil {
			e.logger.Warn("recommend: embed failed",
				"book_id", s.ID, "err", err.Error())
			continue
		}
		embedded++
	}
	return embedded, nil
}

// backendLibraryID returns 0 when the PortalLibrary doesn't carry a
// BackendLibraryID pointer. Mirrors the abs/handler helper of the
// same name so the engine doesn't have to reach into that package.
func backendLibraryID(lib store.PortalLibrary) int64 {
	if lib.BackendLibraryID == nil {
		return 0
	}
	return *lib.BackendLibraryID
}

// PurgeExpiredCache deletes stale rows from audiobook_recommendation_cache.
// Scheduler calls this periodically; the table also self-cleans because
// every read filters expires_at > now().
func (e *Engine) PurgeExpiredCache(ctx context.Context) (int, error) {
	if e.store == nil {
		return 0, nil
	}
	return e.store.PurgeExpiredRecommendations(ctx)
}

// LoadConfigFromEnv reads EMBEDDING_BASE_URL / EMBEDDING_MODEL /
// EMBEDDING_API_KEY from the process env. main.go calls this at boot.
// Empty results when the env vars aren't set; the engine then no-ops.
func LoadConfigFromEnv(get func(string) string) ClientConfig {
	return ClientConfig{
		BaseURL: strings.TrimSpace(get("EMBEDDING_BASE_URL")),
		Model:   strings.TrimSpace(get("EMBEDDING_MODEL")),
		APIKey:  strings.TrimSpace(get("EMBEDDING_API_KEY")),
	}
}
