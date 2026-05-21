// Package podcastfeed fetches and parses RSS / Atom podcast feeds and
// upserts the resulting episodes into the store. The Refresher is
// invoked from two places: the scheduler (periodic background refresh
// for every podcast whose refresh window has elapsed) and an admin
// POST endpoint (force-refresh for troubleshooting / manual triggers
// after seeding a feed URL).
//
// Episode identity is keyed by the feed's <guid>, matching real
// podcast-client conventions. Upserts via store.UpsertPodcastEpisode
// idempotently update existing rows so listener progress / per-episode
// metadata survive feed re-emits.
package podcastfeed

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"
	"github.com/oklog/ulid/v2"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
)

// Store is the narrow surface Refresher needs. Implemented by *store.Store;
// surfaced as an interface so tests can inject a stub without standing up
// Postgres.
type Store interface {
	ListPodcasts(ctx context.Context, libraryID int64, limit int) ([]store.Podcast, error)
	GetPodcastEpisodesByGUID(ctx context.Context, podcastID string, guids []string) (map[string]string, error)
	UpsertPodcastEpisode(ctx context.Context, e store.PodcastEpisode) error
	MarkPodcastRefreshed(ctx context.Context, podcastID string, lastError string) error
}

// Refresher is the long-lived feed-refresh worker. One instance lives in
// the scheduler and another can be created on demand by the admin force-
// refresh endpoint — both share the same HTTP client + parser.
type Refresher struct {
	hc     *http.Client
	parser *gofeed.Parser
	logger Logger
}

// Logger is the narrow logging surface — slog and hclog both satisfy it.
type Logger interface {
	Debug(msg string, args ...any)
	Warn(msg string, args ...any)
}

type noopLogger struct{}

func (noopLogger) Debug(string, ...any) {}
func (noopLogger) Warn(string, ...any)  {}

// New builds a Refresher with sensible defaults: 30-second HTTP timeout
// (podcast feeds are typically &lt; 1 MB but the parser pulls the whole
// document into memory, so a hard timeout is the safety net).
func New(logger Logger) *Refresher {
	if logger == nil {
		logger = noopLogger{}
	}
	return &Refresher{
		hc:     &http.Client{Timeout: 30 * time.Second},
		parser: gofeed.NewParser(),
		logger: logger,
	}
}

// WithHTTPClient overrides the default HTTP client. Used in tests to
// point at httptest.NewServer fixtures.
func (r *Refresher) WithHTTPClient(hc *http.Client) *Refresher {
	r.hc = hc
	return r
}

// RefreshDue walks every podcast in the store and refreshes the ones
// whose last_refreshed_at + refresh_interval_minutes has elapsed.
// Returns the count of podcasts attempted (whether successful or not)
// so the scheduler can log progress. Per-podcast failures are logged
// and don't stop the walk.
func (r *Refresher) RefreshDue(ctx context.Context, s Store) (int, error) {
	podcasts, err := s.ListPodcasts(ctx, 0, 0)
	if err != nil {
		return 0, fmt.Errorf("list podcasts: %w", err)
	}
	now := time.Now()
	attempted := 0
	for _, p := range podcasts {
		if p.FeedURL == "" {
			continue
		}
		if !isDue(p, now) {
			continue
		}
		attempted++
		if err := r.RefreshOne(ctx, s, p); err != nil {
			r.logger.Warn("podcast refresh failed", "podcast_id", p.ID, "feed_url", p.FeedURL, "err", err.Error())
		}
	}
	return attempted, nil
}

// RefreshOne refreshes a single podcast. Public so the admin force-
// refresh endpoint can call it directly. Records success or failure in
// the podcast row's last_error column regardless of outcome.
func (r *Refresher) RefreshOne(ctx context.Context, s Store, p store.Podcast) error {
	if p.FeedURL == "" {
		// Nothing to refresh, but mark anyway so we don't keep
		// re-evaluating the same row on every tick.
		_ = s.MarkPodcastRefreshed(ctx, p.ID, "no feed_url configured")
		return errors.New("no feed_url configured")
	}
	feed, err := r.fetchAndParse(ctx, p.FeedURL)
	if err != nil {
		_ = s.MarkPodcastRefreshed(ctx, p.ID, err.Error())
		return err
	}
	count, err := r.upsertItems(ctx, s, p, feed)
	if err != nil {
		_ = s.MarkPodcastRefreshed(ctx, p.ID, err.Error())
		return err
	}
	_ = s.MarkPodcastRefreshed(ctx, p.ID, "")
	r.logger.Debug("podcast refreshed", "podcast_id", p.ID, "episodes", count)
	return nil
}

func (r *Refresher) fetchAndParse(ctx context.Context, feedURL string) (*gofeed.Feed, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	// Some feed hosts gate on a recognisable User-Agent (defending
	// against scrapers). Identify the plugin clearly — operators who
	// see traffic from this UA know what's hitting them.
	req.Header.Set("User-Agent", "continuum-audiobooks/podcast-refresher (+https://continuumapp.com)")
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml;q=0.9, */*;q=0.5")
	resp, err := r.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %q: %w", feedURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %q: status %d", feedURL, resp.StatusCode)
	}
	feed, err := r.parser.Parse(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", feedURL, err)
	}
	return feed, nil
}

func (r *Refresher) upsertItems(ctx context.Context, s Store, p store.Podcast, feed *gofeed.Feed) (int, error) {
	if feed == nil || len(feed.Items) == 0 {
		return 0, nil
	}

	// Build the upsert list. For new episodes we mint a ULID id; for
	// already-known episodes (matched by guid) we reuse the stored id
	// so listener progress rows keep pointing at the same primary key.
	guids := make([]string, 0, len(feed.Items))
	for _, item := range feed.Items {
		g := strings.TrimSpace(item.GUID)
		if g == "" {
			g = strings.TrimSpace(item.Link)
		}
		if g == "" {
			continue
		}
		guids = append(guids, g)
	}
	existing, err := s.GetPodcastEpisodesByGUID(ctx, p.ID, guids)
	if err != nil {
		return 0, fmt.Errorf("guid lookup: %w", err)
	}

	count := 0
	for _, item := range feed.Items {
		guid := strings.TrimSpace(item.GUID)
		if guid == "" {
			guid = strings.TrimSpace(item.Link)
		}
		if guid == "" || item.Title == "" {
			continue
		}
		audioURL, mimeType, audioBytes := pickEnclosure(item)
		if audioURL == "" {
			// No audio — could be a text-only post; skip.
			continue
		}
		id, ok := existing[guid]
		if !ok {
			id = ulid.Make().String()
		}
		e := store.PodcastEpisode{
			ID:              id,
			PodcastID:       p.ID,
			GUID:            guid,
			Title:           item.Title,
			Description:     item.Description,
			AudioURL:        audioURL,
			AudioMimeType:   mimeType,
			AudioBytes:      audioBytes,
			DurationSeconds: durationFromItem(item),
			EpisodeIndex:    intPtrFromItem(item, "episode"),
			SeasonIndex:     intPtrFromItem(item, "season"),
			PublishedAt:     item.PublishedParsed,
			CoverURL:        coverFromItem(item),
		}
		if err := s.UpsertPodcastEpisode(ctx, e); err != nil {
			r.logger.Warn("upsert episode failed",
				"podcast_id", p.ID, "guid", guid, "err", err.Error())
			continue
		}
		count++
	}
	return count, nil
}

// isDue reports whether a podcast's refresh window has elapsed. A
// podcast that has never been refreshed (LastRefreshedAt == nil) is
// always due.
func isDue(p store.Podcast, now time.Time) bool {
	if p.LastRefreshedAt == nil {
		return true
	}
	interval := time.Duration(p.RefreshIntervalMinutes) * time.Minute
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	return now.Sub(*p.LastRefreshedAt) >= interval
}

// pickEnclosure returns the first audio enclosure on a feed item. RSS
// items typically carry one; some podcasts carry multiple (preview /
// transcript variants) — we always pick the first audio/* enclosure.
func pickEnclosure(item *gofeed.Item) (audioURL, mimeType string, audioBytes int64) {
	for _, enc := range item.Enclosures {
		mt := strings.ToLower(enc.Type)
		if !strings.HasPrefix(mt, "audio/") && mt != "" {
			continue
		}
		audioBytes = 0
		if enc.Length != "" {
			var n int64
			_, _ = fmt.Sscan(enc.Length, &n)
			audioBytes = n
		}
		return enc.URL, enc.Type, audioBytes
	}
	return "", "", 0
}

// durationFromItem reads the iTunes-namespaced duration field from a
// gofeed item, parsing the canonical "HH:MM:SS" / "MM:SS" / "SSS" forms.
// Returns 0 on missing or unparseable input.
func durationFromItem(item *gofeed.Item) int {
	if item.ITunesExt == nil {
		return 0
	}
	raw := strings.TrimSpace(item.ITunesExt.Duration)
	if raw == "" {
		return 0
	}
	return parseDuration(raw)
}

func parseDuration(raw string) int {
	parts := strings.Split(raw, ":")
	var h, m, s int
	switch len(parts) {
	case 1:
		_, _ = fmt.Sscan(parts[0], &s)
	case 2:
		_, _ = fmt.Sscan(parts[0], &m)
		_, _ = fmt.Sscan(parts[1], &s)
	case 3:
		_, _ = fmt.Sscan(parts[0], &h)
		_, _ = fmt.Sscan(parts[1], &m)
		_, _ = fmt.Sscan(parts[2], &s)
	default:
		return 0
	}
	return h*3600 + m*60 + s
}

// intPtrFromItem returns the iTunes-namespaced season/episode value as
// an *int, or nil if absent. Real feeds vary on which they emit — we
// pull whatever's there and let clients render conditionally.
func intPtrFromItem(item *gofeed.Item, field string) *int {
	if item.ITunesExt == nil {
		return nil
	}
	var raw string
	switch field {
	case "episode":
		raw = item.ITunesExt.Episode
	case "season":
		raw = item.ITunesExt.Season
	default:
		return nil
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var n int
	if _, err := fmt.Sscan(raw, &n); err != nil {
		return nil
	}
	return &n
}

// coverFromItem extracts an episode-level cover image, falling back to
// the iTunes image when present.
func coverFromItem(item *gofeed.Item) string {
	if item.Image != nil && item.Image.URL != "" {
		return item.Image.URL
	}
	if item.ITunesExt != nil && item.ITunesExt.Image != "" {
		return item.ITunesExt.Image
	}
	return ""
}
