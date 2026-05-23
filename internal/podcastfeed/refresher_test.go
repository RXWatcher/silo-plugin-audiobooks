package podcastfeed_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/podcastfeed"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
)

// fakeStore implements podcastfeed.Store for unit tests. It records every
// UpsertPodcastEpisode call so assertions can inspect what the refresher
// would have written without standing up Postgres.
type fakeStore struct {
	mu sync.Mutex

	podcasts        []store.Podcast
	existingByGUID  map[string]string
	upsertedEpisodes []store.PodcastEpisode
	refreshed       map[string]string // podcast_id → last_error
}

func newFakeStore(pcs ...store.Podcast) *fakeStore {
	return &fakeStore{
		podcasts:       pcs,
		existingByGUID: map[string]string{},
		refreshed:      map[string]string{},
	}
}

func (f *fakeStore) ListPodcasts(_ context.Context, libraryID int64, _ int) ([]store.Podcast, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if libraryID == 0 {
		return append([]store.Podcast(nil), f.podcasts...), nil
	}
	var out []store.Podcast
	for _, p := range f.podcasts {
		if p.LibraryID == libraryID {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeStore) GetPodcastEpisodesByGUID(_ context.Context, _ string, guids []string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for _, g := range guids {
		if id, ok := f.existingByGUID[g]; ok {
			out[g] = id
		}
	}
	return out, nil
}

func (f *fakeStore) UpsertPodcastEpisode(_ context.Context, e store.PodcastEpisode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upsertedEpisodes = append(f.upsertedEpisodes, e)
	return nil
}

func (f *fakeStore) MarkPodcastRefreshed(_ context.Context, podcastID string, lastError string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshed[podcastID] = lastError
	return nil
}

// rssFixture is a small but realistic RSS 2.0 + iTunes-namespaced feed
// covering the fields the refresher actually reads.
const rssFixture = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd">
<channel>
  <title>Test Show</title>
  <itunes:author>Host McHostface</itunes:author>
  <item>
    <title>Episode 1</title>
    <description>First one.</description>
    <pubDate>Mon, 01 Apr 2026 12:00:00 GMT</pubDate>
    <guid>episode-guid-1</guid>
    <enclosure url="https://cdn.example.com/ep1.mp3" length="12345" type="audio/mpeg"/>
    <itunes:duration>00:30:00</itunes:duration>
    <itunes:episode>1</itunes:episode>
    <itunes:season>1</itunes:season>
  </item>
  <item>
    <title>Episode 2</title>
    <description>Second one.</description>
    <pubDate>Mon, 08 Apr 2026 12:00:00 GMT</pubDate>
    <guid>episode-guid-2</guid>
    <enclosure url="https://cdn.example.com/ep2.mp3" length="23456" type="audio/mpeg"/>
    <itunes:duration>45:30</itunes:duration>
    <itunes:episode>2</itunes:episode>
  </item>
</channel>
</rss>`

// TestRefreshOne_InsertsNewEpisodes drives the refresher against a fake
// feed and confirms it upserts every item with the expected fields.
func TestRefreshOne_InsertsNewEpisodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(rssFixture))
	}))
	defer srv.Close()

	pod := store.Podcast{ID: "p-1", LibraryID: 1, Title: "Test Show", FeedURL: srv.URL}
	fs := newFakeStore(pod)
	r := podcastfeed.New(nil).WithHTTPClient(srv.Client())

	if err := r.RefreshOne(context.Background(), fs, pod); err != nil {
		t.Fatalf("RefreshOne: %v", err)
	}

	if len(fs.upsertedEpisodes) != 2 {
		t.Fatalf("upserts = %d, want 2", len(fs.upsertedEpisodes))
	}
	first := fs.upsertedEpisodes[0]
	if first.Title != "Episode 1" {
		t.Errorf("first.Title = %q", first.Title)
	}
	if first.AudioURL != "https://cdn.example.com/ep1.mp3" {
		t.Errorf("first.AudioURL = %q", first.AudioURL)
	}
	if first.AudioMimeType != "audio/mpeg" {
		t.Errorf("first.AudioMimeType = %q", first.AudioMimeType)
	}
	if first.AudioBytes != 12345 {
		t.Errorf("first.AudioBytes = %d, want 12345", first.AudioBytes)
	}
	if first.DurationSeconds != 1800 {
		t.Errorf("first.DurationSeconds = %d, want 1800 (00:30:00)", first.DurationSeconds)
	}
	if first.EpisodeIndex == nil || *first.EpisodeIndex != 1 {
		t.Errorf("first.EpisodeIndex = %+v", first.EpisodeIndex)
	}
	if first.SeasonIndex == nil || *first.SeasonIndex != 1 {
		t.Errorf("first.SeasonIndex = %+v", first.SeasonIndex)
	}
	if first.PublishedAt == nil {
		t.Errorf("first.PublishedAt must be parsed")
	}

	// 45:30 mm:ss must parse to 2730s on the second item.
	if fs.upsertedEpisodes[1].DurationSeconds != 2730 {
		t.Errorf("second.DurationSeconds = %d, want 2730 (45:30)", fs.upsertedEpisodes[1].DurationSeconds)
	}

	// Mark-refreshed bookkeeping must record success (empty last_error).
	if got := fs.refreshed["p-1"]; got != "" {
		t.Errorf("refreshed[p-1] = %q, want empty (success)", got)
	}
}

// TestRefreshOne_ReusesExistingEpisodeID confirms the idempotent-upsert
// contract: when a feed re-emits an item we've already stored, we keep
// the existing ULID id so per-user progress rows don't lose their
// foreign-key target.
func TestRefreshOne_ReusesExistingEpisodeID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(rssFixture))
	}))
	defer srv.Close()

	pod := store.Podcast{ID: "p-1", LibraryID: 1, Title: "Test Show", FeedURL: srv.URL}
	fs := newFakeStore(pod)
	// Pretend episode 1 already exists with a stored id.
	fs.existingByGUID["episode-guid-1"] = "stored-ulid-for-ep1"

	r := podcastfeed.New(nil).WithHTTPClient(srv.Client())
	if err := r.RefreshOne(context.Background(), fs, pod); err != nil {
		t.Fatalf("RefreshOne: %v", err)
	}

	if len(fs.upsertedEpisodes) != 2 {
		t.Fatalf("upserts = %d, want 2", len(fs.upsertedEpisodes))
	}
	if fs.upsertedEpisodes[0].ID != "stored-ulid-for-ep1" {
		t.Errorf("existing episode id was rotated: got %q, want stored-ulid-for-ep1",
			fs.upsertedEpisodes[0].ID)
	}
	// Episode 2 is new — must mint a non-empty id, but not the stored one.
	if fs.upsertedEpisodes[1].ID == "" || fs.upsertedEpisodes[1].ID == "stored-ulid-for-ep1" {
		t.Errorf("new episode id = %q (must be a fresh ULID)", fs.upsertedEpisodes[1].ID)
	}
}

// TestRefreshOne_UpstreamFailure records the error in the podcast row's
// last_error column so operators see the cause via the admin UI without
// having to read plugin logs.
func TestRefreshOne_UpstreamFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "feed gone", http.StatusNotFound)
	}))
	defer srv.Close()

	pod := store.Podcast{ID: "p-1", LibraryID: 1, Title: "Gone", FeedURL: srv.URL}
	fs := newFakeStore(pod)
	r := podcastfeed.New(nil).WithHTTPClient(srv.Client())

	err := r.RefreshOne(context.Background(), fs, pod)
	if err == nil {
		t.Fatal("RefreshOne must surface upstream 404 as an error")
	}
	if fs.refreshed["p-1"] == "" {
		t.Errorf("MarkPodcastRefreshed not called with the error message")
	}
}

// TestRefreshDue_OnlyWalksDuePodcasts confirms the refresh-interval gate:
// a podcast refreshed two minutes ago with a 360-minute interval must
// not be re-fetched.
func TestRefreshDue_OnlyWalksDuePodcasts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(rssFixture))
	}))
	defer srv.Close()

	twoMinAgo := time.Now().Add(-2 * time.Minute)
	hoursAgo := time.Now().Add(-12 * time.Hour)
	fs := newFakeStore(
		store.Podcast{
			ID: "due", LibraryID: 1, Title: "Due", FeedURL: srv.URL,
			LastRefreshedAt: &hoursAgo, RefreshIntervalMinutes: 360,
		},
		store.Podcast{
			ID: "fresh", LibraryID: 1, Title: "Fresh", FeedURL: srv.URL,
			LastRefreshedAt: &twoMinAgo, RefreshIntervalMinutes: 360,
		},
		store.Podcast{
			ID: "never", LibraryID: 1, Title: "Never", FeedURL: srv.URL,
			// LastRefreshedAt is nil → always due
		},
		store.Podcast{
			ID: "noFeed", LibraryID: 1, Title: "No Feed",
			// FeedURL is empty → skipped without attempting
		},
	)

	r := podcastfeed.New(nil).WithHTTPClient(srv.Client())
	attempted, err := r.RefreshDue(context.Background(), fs)
	if err != nil {
		t.Fatalf("RefreshDue: %v", err)
	}
	// "due" + "never" → 2 attempts. "fresh" + "noFeed" → skipped.
	if attempted != 2 {
		t.Errorf("attempted = %d, want 2", attempted)
	}
	if _, ok := fs.refreshed["fresh"]; ok {
		t.Errorf("fresh podcast was refreshed when it shouldn't have been")
	}
	if _, ok := fs.refreshed["due"]; !ok {
		t.Errorf("due podcast was not refreshed")
	}
	if _, ok := fs.refreshed["never"]; !ok {
		t.Errorf("never-refreshed podcast was not refreshed")
	}
}

// TestRefreshOne_SkipsItemsWithoutAudio drops feed items that have no
// audio enclosure (text-only posts that some podcasts mix in).
func TestRefreshOne_SkipsItemsWithoutAudio(t *testing.T) {
	const mixedFixture = `<?xml version="1.0"?>
<rss version="2.0"><channel><title>Mixed</title>
<item><title>Text-only</title><guid>t-1</guid></item>
<item><title>Has audio</title><guid>a-1</guid>
  <enclosure url="https://cdn.example.com/a.mp3" type="audio/mpeg"/>
</item></channel></rss>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(mixedFixture))
	}))
	defer srv.Close()

	pod := store.Podcast{ID: "p", LibraryID: 1, FeedURL: srv.URL}
	fs := newFakeStore(pod)
	r := podcastfeed.New(nil).WithHTTPClient(srv.Client())
	if err := r.RefreshOne(context.Background(), fs, pod); err != nil {
		t.Fatalf("RefreshOne: %v", err)
	}
	if len(fs.upsertedEpisodes) != 1 {
		t.Fatalf("upserts = %d, want 1 (text-only item skipped)", len(fs.upsertedEpisodes))
	}
	if fs.upsertedEpisodes[0].Title != "Has audio" {
		t.Errorf("wrong item kept: %q", fs.upsertedEpisodes[0].Title)
	}
}
