package recommend

import (
	"strings"
	"testing"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/backend"
)

// TestBuildEmbeddingText_LeadsWithSemanticContent pins the
// design contract: genres + description come first so the
// embedding doesn't anchor on title words. Author + narrators +
// series appear in the body. Mirrors the host's text.go ordering.
func TestBuildEmbeddingText_LeadsWithSemanticContent(t *testing.T) {
	d := backend.AudiobookDetail{
		AudiobookSummary: backend.AudiobookSummary{
			Title:     "Way of Kings",
			Year:      2010,
			Narrators: []string{"Michael Kramer", "Kate Reading"},
			AuthorRefs: []backend.AuthorRef{
				{ID: "a1", Name: "Brandon Sanderson"},
			},
			SeriesRefs: []backend.SeriesRef{
				{Name: "Stormlight Archive", Sequence: "1"},
			},
		},
		Description: "Roshar is a world of stone and storms, populated by humans and other peoples.",
		Genres:      []string{"Fantasy", "Epic"},
		Publisher:   "Tor Books",
	}
	out := BuildEmbeddingText(d)
	// Lead with the genres + description.
	if !strings.HasPrefix(out, "Fantasy, Epic audiobook about Roshar") {
		t.Errorf("lead does not start with genres+description: %q", out)
	}
	// Title with year follows.
	if !strings.Contains(out, "Way of Kings (2010)") {
		t.Errorf("title+year missing: %q", out)
	}
	if !strings.Contains(out, "By Brandon Sanderson") {
		t.Errorf("author missing: %q", out)
	}
	if !strings.Contains(out, "Narrated by Michael Kramer, Kate Reading") {
		t.Errorf("narrators missing: %q", out)
	}
	if !strings.Contains(out, "Part of Stormlight Archive #1") {
		t.Errorf("series with sequence missing: %q", out)
	}
	if !strings.Contains(out, "Published by Tor Books") {
		t.Errorf("publisher missing: %q", out)
	}
}

// TestBuildEmbeddingText_DegradesGracefully — when most metadata is
// missing, the text still produces a deterministic output without
// panicking. Important because the v1 backend's older summaries don't
// always carry author refs or descriptions.
func TestBuildEmbeddingText_DegradesGracefully(t *testing.T) {
	d := backend.AudiobookDetail{
		AudiobookSummary: backend.AudiobookSummary{Title: "Unknown Book"},
	}
	out := BuildEmbeddingText(d)
	if !strings.Contains(out, "Unknown Book") {
		t.Errorf("title should appear even with no metadata: %q", out)
	}
	if !strings.HasPrefix(out, "Audiobook") {
		t.Errorf("lead should fall back to 'Audiobook': %q", out)
	}
}

// TestBuildEmbeddingText_DeterministicAcrossCalls — calling twice with
// the same input produces the same string. The canonical_text lock-in
// optimization relies on this.
func TestBuildEmbeddingText_DeterministicAcrossCalls(t *testing.T) {
	d := backend.AudiobookDetail{
		AudiobookSummary: backend.AudiobookSummary{
			Title: "X", Year: 2020,
			AuthorRefs: []backend.AuthorRef{{Name: "A"}, {Name: "B"}},
		},
		Description: "A short tale.",
		Genres:      []string{"Mystery"},
	}
	a := BuildEmbeddingText(d)
	b := BuildEmbeddingText(d)
	if a != b {
		t.Errorf("non-deterministic output:\nA: %q\nB: %q", a, b)
	}
}

// TestBuildEmbeddingText_TruncatesLongOverview confirms the maxOverviewRunes
// cap prevents a multi-page synopsis from dominating the embedding.
func TestBuildEmbeddingText_TruncatesLongOverview(t *testing.T) {
	long := strings.Repeat("a", maxOverviewRunes+500)
	d := backend.AudiobookDetail{
		AudiobookSummary: backend.AudiobookSummary{Title: "X"},
		Description:      long,
		Genres:           []string{"X"},
	}
	out := BuildEmbeddingText(d)
	// The truncated overview is the only place "a"s appear; cap is
	// maxOverviewRunes runes (== bytes for ASCII).
	aCount := strings.Count(out, "a")
	// The string "audiobook about" contains 2 'a's; ignore those.
	if aCount > maxOverviewRunes+10 {
		t.Errorf("overview not truncated: %d 'a's in output", aCount)
	}
}
