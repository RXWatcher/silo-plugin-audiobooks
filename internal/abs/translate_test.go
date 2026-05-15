package abs_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/abs"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/backend"
)

// TestToLibraryItem_ShapesMetadataPerABSSpec encodes the contract change
// that triggered the transformer rewrite: clients need authors/series as
// objects (with stable IDs) and narrators as a string array. The old
// authorName/narratorName/seriesName flat strings must NOT appear in
// the JSON — they would shadow the new fields on the client side.
func TestToLibraryItem_ShapesMetadataPerABSSpec(t *testing.T) {
	d := backend.AudiobookDetail{
		AudiobookSummary: backend.AudiobookSummary{
			ID:              "bw-1",
			Title:           "Project Hail Mary",
			Authors:         []string{"Andy Weir"},
			Narrators:       []string{"Ray Porter"},
			DurationSeconds: 57132,
			HasCover:        true,
			CoverURL:        "https://cdn/cover.jpg",
			Year:            2021,
			AddedAtMs:       1699000000000,
			UpdatedAtMs:     1699999999999,
		},
		Description: "A lone astronaut...",
		Series:      "Project Hail Mary",
		SeriesIndex: 1.0,
	}
	got := abs.ToLibraryItem(d, func(int) string { return "" })
	if len(got.Media.Metadata.Authors) != 1 || got.Media.Metadata.Authors[0].Name != "Andy Weir" {
		t.Fatalf("authors = %+v", got.Media.Metadata.Authors)
	}
	if got.Media.Metadata.Authors[0].ID != "andy-weir" {
		t.Errorf("author ID = %q, want andy-weir", got.Media.Metadata.Authors[0].ID)
	}
	if len(got.Media.Metadata.Series) != 1 || got.Media.Metadata.Series[0].Sequence != "1" {
		t.Errorf("series = %+v", got.Media.Metadata.Series)
	}
	if got.Media.Metadata.Narrators[0] != "Ray Porter" {
		t.Errorf("narrators = %+v", got.Media.Metadata.Narrators)
	}
	if got.AddedAt != 1699000000000 || got.UpdatedAt != 1699999999999 {
		t.Errorf("timestamps = %d / %d", got.AddedAt, got.UpdatedAt)
	}
	if got.Media.CoverPath == "" {
		t.Errorf("coverPath empty")
	}

	// JSON shape: ensure the obsolete flat fields aren't emitted.
	raw, _ := json.Marshal(got)
	s := string(raw)
	for _, bad := range []string{`"authorName"`, `"narratorName"`, `"seriesName"`} {
		if strings.Contains(s, bad) {
			t.Errorf("JSON still contains %s: %s", bad, s)
		}
	}
	// Required ABS keys are present.
	for _, must := range []string{`"authors"`, `"narrators"`, `"series"`, `"coverPath"`, `"addedAt"`, `"updatedAt"`} {
		if !strings.Contains(s, must) {
			t.Errorf("JSON missing %s: %s", must, s)
		}
	}
}

// TestToLibraryItem_FallsBackToSlugIDs covers the upgrade path where the
// backend hasn't yet learned to emit author_refs/series_refs: we still
// emit a valid {id,name} pair by slugging the legacy flat strings.
func TestToLibraryItem_FallsBackToSlugIDs(t *testing.T) {
	d := backend.AudiobookDetail{
		AudiobookSummary: backend.AudiobookSummary{
			ID:      "bw-2",
			Title:   "X",
			Authors: []string{"Iain M. Banks", "  "},
		},
		Series:      "The Culture",
		SeriesIndex: 0,
	}
	got := abs.ToLibraryItem(d, func(int) string { return "" })
	// The empty/whitespace author entry should be dropped, not propagated
	// as a {"id":"","name":""} ghost.
	if len(got.Media.Metadata.Authors) != 1 {
		t.Fatalf("authors = %+v", got.Media.Metadata.Authors)
	}
	if got.Media.Metadata.Authors[0].ID != "iain-m-banks" {
		t.Errorf("author ID = %q, want iain-m-banks", got.Media.Metadata.Authors[0].ID)
	}
	if got.Media.Metadata.Series[0].ID != "the-culture" {
		t.Errorf("series ID = %q, want the-culture", got.Media.Metadata.Series[0].ID)
	}
	// Sequence omitted when index is zero.
	if got.Media.Metadata.Series[0].Sequence != "" {
		t.Errorf("series sequence = %q, want empty", got.Media.Metadata.Series[0].Sequence)
	}
}

// TestToLibraryItem_PrefersBackendRefs verifies that when the backend
// supplies AuthorRefs/SeriesRefs, the translator uses them verbatim
// instead of re-slugging the names.
func TestToLibraryItem_PrefersBackendRefs(t *testing.T) {
	d := backend.AudiobookDetail{
		AudiobookSummary: backend.AudiobookSummary{
			ID:    "bw-3",
			Title: "X",
			// Legacy strings present but should be ignored when refs exist.
			Authors:    []string{"Wrong Name"},
			AuthorRefs: []backend.AuthorRef{{ID: "real-id-7", Name: "Andy Weir"}},
			SeriesRefs: []backend.SeriesRef{{ID: "series-x", Name: "Real", Sequence: "2"}},
		},
	}
	got := abs.ToLibraryItem(d, func(int) string { return "" })
	if got.Media.Metadata.Authors[0].ID != "real-id-7" {
		t.Errorf("author = %+v, want id real-id-7", got.Media.Metadata.Authors)
	}
	if got.Media.Metadata.Series[0].ID != "series-x" || got.Media.Metadata.Series[0].Sequence != "2" {
		t.Errorf("series = %+v", got.Media.Metadata.Series)
	}
}
