package store_test

import (
	"testing"
	"time"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
)

// TestPodcast_RoundTrip exercises the basic CRUD path against a real
// Postgres: insert a library + podcast + episode, list, read by id,
// update, delete. Covers the schema and the store helpers in one go.
func TestPodcast_RoundTrip(t *testing.T) {
	st, ctx := newStore(t)

	// A podcast library is just a portal_library row with media_type=podcast.
	if err := st.ReplacePortalLibraries(ctx, []store.PortalLibrary{{
		Name: "Shows", MediaType: "podcast", Enabled: true, SortOrder: 0,
	}}); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	libs, err := st.ListPortalLibraries(ctx, false)
	if err != nil || len(libs) != 1 {
		t.Fatalf("list libs: err=%v libs=%+v", err, libs)
	}
	libID := libs[0].ID

	// Upsert the podcast and confirm read-back.
	p := store.Podcast{
		ID:                     "pod-1",
		LibraryID:              libID,
		Title:                  "Demo Show",
		Author:                 "Host McHostface",
		Description:            "All about hosts.",
		CoverURL:               "/covers/pod-1.jpg",
		FeedURL:                "https://example.com/feed.xml",
		RefreshIntervalMinutes: 360,
	}
	if err := st.UpsertPodcast(ctx, p); err != nil {
		t.Fatalf("upsert podcast: %v", err)
	}
	got, err := st.GetPodcast(ctx, "pod-1")
	if err != nil {
		t.Fatalf("get podcast: %v", err)
	}
	if got.Title != p.Title || got.LibraryID != libID || got.FeedURL != p.FeedURL {
		t.Errorf("readback mismatch: %+v", got)
	}

	// Upsert an episode and confirm read-back via list.
	pubTime := time.Now().UTC()
	idx := 1
	e := store.PodcastEpisode{
		ID:              "ep-1",
		PodcastID:       "pod-1",
		GUID:            "guid-1",
		Title:           "Episode 1",
		AudioURL:        "https://cdn.example.com/ep-1.mp3",
		AudioMimeType:   "audio/mpeg",
		DurationSeconds: 1800,
		EpisodeIndex:    &idx,
		PublishedAt:     &pubTime,
	}
	if err := st.UpsertPodcastEpisode(ctx, e); err != nil {
		t.Fatalf("upsert episode: %v", err)
	}
	episodes, err := st.ListPodcastEpisodes(ctx, "pod-1", 0)
	if err != nil || len(episodes) != 1 {
		t.Fatalf("list episodes: err=%v episodes=%+v", err, episodes)
	}
	if episodes[0].Title != "Episode 1" {
		t.Errorf("episode title = %q", episodes[0].Title)
	}

	// Idempotent re-upsert by (podcast_id, guid) — same row, new title.
	e.Title = "Episode 1 (renamed)"
	if err := st.UpsertPodcastEpisode(ctx, e); err != nil {
		t.Fatalf("re-upsert episode: %v", err)
	}
	episodes, _ = st.ListPodcastEpisodes(ctx, "pod-1", 0)
	if len(episodes) != 1 {
		t.Errorf("after re-upsert episodes count = %d, want 1 (idempotent)", len(episodes))
	}
	if episodes[0].Title != "Episode 1 (renamed)" {
		t.Errorf("re-upsert title not updated: %q", episodes[0].Title)
	}

	// Episode progress round-trip — Upsert + UpdatePosition mirror the
	// audiobook progress contract.
	if err := st.UpsertPodcastEpisodeProgress(ctx, store.PodcastEpisodeProgress{
		UserID: "u-1", EpisodeID: "ep-1", CurrentSeconds: 60, ProgressPct: 0.05, IsFinished: false,
	}); err != nil {
		t.Fatalf("upsert progress: %v", err)
	}
	if err := st.UpdatePodcastEpisodeProgressPosition(ctx, "u-1", "ep-1", 120); err != nil {
		t.Fatalf("update position: %v", err)
	}
	progress, err := st.GetPodcastEpisodeProgress(ctx, "u-1", "ep-1")
	if err != nil {
		t.Fatalf("get progress: %v", err)
	}
	if progress.CurrentSeconds != 120 {
		t.Errorf("position = %d, want 120", progress.CurrentSeconds)
	}
	// Critically: UpdatePosition must not reset progress_pct or
	// is_finished. Mirrors the audiobook bug we hardened against.
	if progress.ProgressPct != 0.05 {
		t.Errorf("position update reset progress_pct: got %v, want 0.05", progress.ProgressPct)
	}

	// Delete cascades: removing the podcast removes its episodes and
	// per-user progress rows.
	if err := st.DeletePodcast(ctx, "pod-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := st.GetPodcast(ctx, "pod-1"); err == nil {
		t.Errorf("get post-delete must error")
	}
	episodes, _ = st.ListPodcastEpisodes(ctx, "pod-1", 0)
	if len(episodes) != 0 {
		t.Errorf("episodes survived cascade delete: %+v", episodes)
	}
}

// TestPodcast_UniqueFeedURL confirms the partial unique index on
// feed_url. Operators can't add the same podcast feed twice — would
// otherwise result in duplicate episode rows after a feed refresh.
// Two podcasts with NULL feed_url are allowed (manually-seeded ones).
func TestPodcast_UniqueFeedURL(t *testing.T) {
	st, ctx := newStore(t)
	if err := st.ReplacePortalLibraries(ctx, []store.PortalLibrary{{
		Name: "Shows", MediaType: "podcast", Enabled: true,
	}}); err != nil {
		t.Fatalf("seed lib: %v", err)
	}
	libs, _ := st.ListPortalLibraries(ctx, false)
	libID := libs[0].ID

	if err := st.UpsertPodcast(ctx, store.Podcast{
		ID: "a", LibraryID: libID, Title: "A",
		FeedURL: "https://feed.example/x.xml",
	}); err != nil {
		t.Fatalf("first podcast: %v", err)
	}
	err := st.UpsertPodcast(ctx, store.Podcast{
		ID: "b", LibraryID: libID, Title: "B",
		FeedURL: "https://feed.example/x.xml",
	})
	if err == nil {
		t.Error("duplicate feed_url must error (unique index)")
	}

	// Two podcasts with empty feed_url are fine — partial unique index
	// excludes NULL rows.
	if err := st.UpsertPodcast(ctx, store.Podcast{ID: "c", LibraryID: libID, Title: "C"}); err != nil {
		t.Fatalf("manual c: %v", err)
	}
	if err := st.UpsertPodcast(ctx, store.Podcast{ID: "d", LibraryID: libID, Title: "D"}); err != nil {
		t.Fatalf("manual d: %v", err)
	}
}
