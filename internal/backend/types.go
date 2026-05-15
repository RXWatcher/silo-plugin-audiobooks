// Package backend is the portal's typed client for any audiobook_backend.v1
// plugin. All calls go through the host plugin proxy with the user's bearer
// forwarded.
package backend

// AuthorRef carries a stable ID + display name for an author. IDs are
// supplied by the backend (the audiobook_backend.v1 contract); when the
// upstream lacks IDs, the backend derives them as slugs from the name.
type AuthorRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SeriesRef is the same idea for series; sequence may be empty.
type SeriesRef struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Sequence string `json:"sequence,omitempty"`
}

// AudiobookSummary mirrors the backend's contract.
type AudiobookSummary struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	Authors         []string `json:"authors,omitempty"`
	Narrators       []string `json:"narrators,omitempty"`
	DurationSeconds int      `json:"duration_seconds"`
	CoverURL        string   `json:"cover_url,omitempty"`
	HasCover        bool     `json:"has_cover"`
	Year            int      `json:"year,omitempty"`
	Rating          float64  `json:"rating,omitempty"`
	// Reference-shaped fields supplied by the v1.1 contract. When the
	// upstream is older these will be empty and ToLibrary*** falls back to
	// deriving them from the legacy Authors/Series strings.
	AuthorRefs  []AuthorRef `json:"author_refs,omitempty"`
	SeriesRefs  []SeriesRef `json:"series_refs,omitempty"`
	CoverPath   string      `json:"cover_path,omitempty"`
	AddedAtMs   int64       `json:"added_at_ms,omitempty"`
	UpdatedAtMs int64       `json:"updated_at_ms,omitempty"`
}

// AudiobookFile describes one streamable file in an audiobook.
type AudiobookFile struct {
	Index           int    `json:"index"`
	Format          string `json:"format"`
	SizeBytes       int64  `json:"size_bytes"`
	DurationSeconds int    `json:"duration_seconds"`
	MimeType        string `json:"mime_type"`
}

// Chapter mirrors the backend's chapter marker.
type Chapter struct {
	StartSeconds int    `json:"start_seconds"`
	EndSeconds   int    `json:"end_seconds"`
	Title        string `json:"title"`
}

// AudiobookDetail is the full shape returned by GetDetail.
type AudiobookDetail struct {
	AudiobookSummary
	Description string          `json:"description,omitempty"`
	ISBN        string          `json:"isbn,omitempty"`
	Publisher   string          `json:"publisher,omitempty"`
	Series      string          `json:"series,omitempty"`
	SeriesIndex float64         `json:"series_index,omitempty"`
	Genres      []string        `json:"genres,omitempty"`
	Chapters    []Chapter       `json:"chapters,omitempty"`
	Files       []AudiobookFile `json:"files,omitempty"`
}

// PageEnvelope is the standard cursor-paged response.
type PageEnvelope[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
	Total      int    `json:"total,omitempty"`
}

// AuthorSummary / SeriesSummary / NarratorSummary mirror browse items.
type AuthorSummary struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count,omitempty"`
}

type SeriesSummary struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count,omitempty"`
}

type NarratorSummary struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count,omitempty"`
}

// RequestSnapshot is the reconciler payload from the backend.
type RequestSnapshot struct {
	RequestID  string `json:"request_id"`
	ExternalID string `json:"external_id"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}
