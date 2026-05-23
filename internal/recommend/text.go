package recommend

import (
	"fmt"
	"strings"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/backend"
)

// maxOverviewRunes caps the description we feed into the embedding so
// a 50-page Goodreads-style synopsis doesn't dominate the vector
// space. The host uses the same 1000-rune cap on movie/TV overviews.
const maxOverviewRunes = 1000

// BuildEmbeddingText constructs the canonical embedding-input string
// for one audiobook. Structure mirrors the host's BuildEmbeddingText
// (silo/internal/recommendations/embeddings/text.go) — lead with
// semantic content (genres + description) so the embedding model
// doesn't anchor on title words; trailing fields add narrative spice
// without dominating.
//
// Output is deterministic for a given input — calling twice with the
// same AudiobookDetail produces the same string. That's the contract
// the canonical_text refresh-check relies on (we only re-embed when
// the rendered text actually changed).
func BuildEmbeddingText(d backend.AudiobookDetail) string {
	var parts []string

	// Lead block: genres + audiobook + description. When all three
	// are present, format as "<genres> audiobook about <description>".
	// Drops cleanly when any are missing.
	genres := strings.Join(d.Genres, ", ")
	overview := truncateRunes(d.Description, maxOverviewRunes)
	switch {
	case genres != "" && overview != "":
		parts = append(parts, fmt.Sprintf("%s audiobook about %s", genres, overview))
	case genres != "":
		parts = append(parts, genres+" audiobook")
	case overview != "":
		parts = append(parts, "Audiobook. "+overview)
	default:
		parts = append(parts, "Audiobook")
	}

	// Title (+ year) — present but no longer leading.
	if d.Year > 0 {
		parts = append(parts, fmt.Sprintf("%s (%d)", d.Title, d.Year))
	} else if d.Title != "" {
		parts = append(parts, d.Title)
	}

	// Authors. Prefer AuthorRefs (canonical refs from the v1.1
	// backend); fall back to the flat Authors list.
	authors := authorNames(d)
	if len(authors) > 0 {
		parts = append(parts, "By "+strings.Join(authors, ", "))
	}

	// Series. SeriesRefs is the modern shape; flat Series is legacy.
	series := seriesNames(d)
	if len(series) > 0 {
		parts = append(parts, "Part of "+strings.Join(series, " and "))
	}

	// Narrators.
	if len(d.Narrators) > 0 {
		parts = append(parts, "Narrated by "+strings.Join(d.Narrators, ", "))
	}

	// Publisher.
	if d.Publisher != "" {
		parts = append(parts, "Published by "+d.Publisher)
	}

	// Top-of-mind metadata trailing — these add minor signal but
	// shouldn't dominate. Mirrored from the host's ordering.
	if d.ISBN != "" {
		parts = append(parts, "ISBN: "+d.ISBN)
	}

	return strings.ToValidUTF8(strings.Join(parts, ". "), "")
}

func authorNames(d backend.AudiobookDetail) []string {
	if len(d.AuthorRefs) > 0 {
		out := make([]string, 0, len(d.AuthorRefs))
		for _, a := range d.AuthorRefs {
			if a.Name != "" {
				out = append(out, a.Name)
			}
		}
		return out
	}
	out := make([]string, 0, len(d.Authors))
	for _, a := range d.Authors {
		a = strings.TrimSpace(a)
		if a != "" {
			out = append(out, a)
		}
	}
	return out
}

func seriesNames(d backend.AudiobookDetail) []string {
	if len(d.SeriesRefs) > 0 {
		out := make([]string, 0, len(d.SeriesRefs))
		for _, s := range d.SeriesRefs {
			if s.Name != "" {
				name := s.Name
				if s.Sequence != "" {
					name += " #" + s.Sequence
				}
				out = append(out, name)
			}
		}
		return out
	}
	if d.Series != "" {
		name := d.Series
		if d.SeriesIndex > 0 {
			name = fmt.Sprintf("%s #%g", name, d.SeriesIndex)
		}
		return []string{name}
	}
	return nil
}

// truncateRunes returns the first n runes of s. Used to cap the
// description before embedding; passing the raw long-form synopsis
// would bias the embedding toward whatever the long tail of the
// description contains.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}
