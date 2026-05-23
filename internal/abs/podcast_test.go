package abs

import (
	"strings"
	"testing"
	"time"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
)

// TestEncodeDecodePodcastEpisodeID covers the dispatch sentinel — the
// "pe_" prefix that handleGetProgress / handlePatchProgress branch on.
// A bare id (no prefix) must report isEpisode=false so the audiobook
// progress path runs.
func TestEncodeDecodePodcastEpisodeID(t *testing.T) {
	enc := EncodePodcastEpisodeID("ep-42")
	if !strings.HasPrefix(enc, "pe_") {
		t.Errorf("Encode missing prefix: %q", enc)
	}
	raw, ok := DecodePodcastEpisodeID(enc)
	if !ok || raw != "ep-42" {
		t.Errorf("Decode prefixed: raw=%q ok=%v", raw, ok)
	}
	// Bare id (no prefix) must pass through unchanged with ok=false so
	// the dispatcher falls through to the audiobook path.
	raw, ok = DecodePodcastEpisodeID("book-1")
	if ok || raw != "book-1" {
		t.Errorf("Decode bare: raw=%q ok=%v", raw, ok)
	}
	// Empty input → empty encoding (no prefix added on empty).
	if EncodePodcastEpisodeID("") != "" {
		t.Errorf("Encode empty should stay empty")
	}
}

// TestToPodcastItem confirms the ABS-shaped podcast item carries the
// right mediaType, library prefix, episode list with prefixed ids, and
// stable ordering — clients render episodes in source order, so the
// emit order must match what the store returns.
func TestToPodcastItem(t *testing.T) {
	pubTime := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	idx2 := 2
	idx1 := 1
	p := store.Podcast{
		ID:          "pod-1",
		LibraryID:   7,
		Title:       "Test Show",
		Author:      "Test Host",
		Description: "Description here",
		CoverURL:    "/covers/pod-1.jpg",
		Explicit:    true,
		FeedURL:     "https://example.com/feed.xml",
		CreatedAt:   time.Unix(1700000000, 0),
		UpdatedAt:   time.Unix(1700100000, 0),
	}
	episodes := []store.PodcastEpisode{
		{
			ID: "ep-newer", PodcastID: "pod-1", GUID: "guid-2",
			Title: "Newer Episode", AudioURL: "https://cdn.example.com/2.mp3",
			DurationSeconds: 1800, EpisodeIndex: &idx2,
			PublishedAt: &pubTime,
		},
		{
			ID: "ep-older", PodcastID: "pod-1", GUID: "guid-1",
			Title: "Older Episode", AudioURL: "https://cdn.example.com/1.mp3",
			DurationSeconds: 3600, EpisodeIndex: &idx1,
		},
	}
	item := ToPodcastItem(p, episodes, "li_7:cG9kLTE")

	if item.MediaType != "podcast" {
		t.Errorf("mediaType = %q, want podcast", item.MediaType)
	}
	if item.ID != "li_7:cG9kLTE" {
		t.Errorf("id = %q, want passed-in encoded id", item.ID)
	}
	if item.LibraryID != "7" {
		t.Errorf("libraryId = %q, want 7", item.LibraryID)
	}
	if item.NumEpisodes != 2 {
		t.Errorf("numEpisodes = %d, want 2", item.NumEpisodes)
	}
	if len(item.Media.Episodes) != 2 {
		t.Fatalf("episodes = %d, want 2", len(item.Media.Episodes))
	}
	// Episode 0 should carry the pe_ prefix and an LibraryItemID matching
	// the parent podcast id — clients use this to back-link to the show.
	if item.Media.Episodes[0].ID != "pe_ep-newer" {
		t.Errorf("episode[0].id = %q, want pe_ep-newer", item.Media.Episodes[0].ID)
	}
	if item.Media.Episodes[0].LibraryItemID != "li_7:cG9kLTE" {
		t.Errorf("episode[0].libraryItemId = %q", item.Media.Episodes[0].LibraryItemID)
	}
	if item.Media.Episodes[0].Duration != 1800 {
		t.Errorf("episode[0].duration = %v", item.Media.Episodes[0].Duration)
	}
	if item.Media.Episodes[0].PublishedAt == 0 {
		t.Errorf("episode[0].publishedAt = 0; want unix-ms")
	}
	if item.Media.Episodes[0].Episode != "2" {
		t.Errorf("episode[0].episode = %q, want \"2\"", item.Media.Episodes[0].Episode)
	}
	// Source order preserved — episodes[1] is the older one.
	if item.Media.Episodes[1].ID != "pe_ep-older" {
		t.Errorf("episode[1].id = %q, want pe_ep-older", item.Media.Episodes[1].ID)
	}
}

// TestToPodcastItem_NoEpisodes ensures the episode list is an empty
// array, never nil — ABS clients iterate the list and crash on nil.
func TestToPodcastItem_NoEpisodes(t *testing.T) {
	p := store.Podcast{ID: "pod-x", LibraryID: 1, Title: "Empty"}
	item := ToPodcastItem(p, nil, "li_1:x")
	if item.Media.Episodes == nil {
		t.Error("episodes must be empty array, not nil")
	}
	if len(item.Media.Episodes) != 0 {
		t.Errorf("episodes = %d, want 0", len(item.Media.Episodes))
	}
	if item.NumEpisodes != 0 {
		t.Errorf("numEpisodes = %d, want 0", item.NumEpisodes)
	}
}
