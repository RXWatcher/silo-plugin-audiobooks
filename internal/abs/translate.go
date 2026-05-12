package abs

import "github.com/ContinuumApp/continuum-plugin-audiobooks/internal/backend"

// VirtualLibraryID is the single library exposed to ABS clients.
const (
	VirtualLibraryID = "continuum-audiobooks"
	VirtualLibraryName = "Audiobooks"
	VirtualFolderID  = "main"
	LibraryMediaType = "book"
	ServerVersion    = "2.25.1"
	ServerSourceTag  = "continuum"
)

// LibraryItem is the ABS-shaped audiobook summary.
type LibraryItem struct {
	ID          string         `json:"id"`
	LibraryID   string         `json:"libraryId"`
	FolderID    string         `json:"folderId"`
	MediaType   string         `json:"mediaType"`
	Media       LibraryItemMedia `json:"media"`
	NumTracks   int            `json:"numTracks,omitempty"`
}

// LibraryItemMedia carries the bulk of the metadata.
type LibraryItemMedia struct {
	Metadata Metadata     `json:"metadata"`
	Duration float64      `json:"duration"`
	CoverPath string      `json:"coverPath,omitempty"`
	Tracks   []AudioTrack `json:"audioFiles,omitempty"`
	Chapters []ChapterABS `json:"chapters,omitempty"`
}

// Metadata is the book-level metadata block.
type Metadata struct {
	Title       string   `json:"title"`
	AuthorName  string   `json:"authorName,omitempty"`
	NarratorName string  `json:"narratorName,omitempty"`
	Description string   `json:"description,omitempty"`
	PublishedYear string `json:"publishedYear,omitempty"`
	ISBN        string   `json:"isbn,omitempty"`
	Publisher   string   `json:"publisher,omitempty"`
	SeriesName  string   `json:"seriesName,omitempty"`
	Genres      []string `json:"genres,omitempty"`
}

// AudioTrack is a single playable file.
type AudioTrack struct {
	Index      int     `json:"index"`
	ContentURL string  `json:"contentUrl"`
	MimeType   string  `json:"mimeType"`
	Duration   float64 `json:"duration"`
	Codec      string  `json:"codec"`
}

// ChapterABS is the ABS chapter shape (`start`/`end` in seconds, float).
type ChapterABS struct {
	ID    int     `json:"id"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Title string  `json:"title"`
}

// ToLibraryItem translates a backend AudiobookDetail into an ABS LibraryItem.
// contentURLFn returns the URL clients hit for each audio file.
func ToLibraryItem(d backend.AudiobookDetail, contentURLFn func(int) string) LibraryItem {
	meta := Metadata{
		Title: d.Title,
		Description: d.Description,
		ISBN: d.ISBN,
		Publisher: d.Publisher,
		SeriesName: d.Series,
		Genres: d.Genres,
	}
	if len(d.Authors) > 0 {
		meta.AuthorName = joinSemicolon(d.Authors)
	}
	if len(d.Narrators) > 0 {
		meta.NarratorName = joinSemicolon(d.Narrators)
	}
	if d.Year > 0 {
		meta.PublishedYear = itoa(d.Year)
	}

	tracks := make([]AudioTrack, len(d.Files))
	for i, f := range d.Files {
		tracks[i] = AudioTrack{
			Index:      f.Index,
			MimeType:   f.MimeType,
			Codec:      f.Format,
			Duration:   float64(f.DurationSeconds),
			ContentURL: contentURLFn(f.Index),
		}
	}
	chapters := make([]ChapterABS, len(d.Chapters))
	for i, c := range d.Chapters {
		chapters[i] = ChapterABS{
			ID:    i,
			Start: float64(c.StartSeconds),
			End:   float64(c.EndSeconds),
			Title: c.Title,
		}
	}

	return LibraryItem{
		ID:        d.ID,
		LibraryID: VirtualLibraryID,
		FolderID:  VirtualFolderID,
		MediaType: LibraryMediaType,
		Media: LibraryItemMedia{
			Metadata: meta,
			Duration: float64(d.DurationSeconds),
			Tracks:   tracks,
			Chapters: chapters,
		},
		NumTracks: len(d.Files),
	}
}

// ToLibrarySummary translates a backend AudiobookSummary into a slim ABS
// LibraryItem (no tracks/chapters).
func ToLibrarySummary(s backend.AudiobookSummary) LibraryItem {
	meta := Metadata{Title: s.Title}
	if len(s.Authors) > 0 {
		meta.AuthorName = joinSemicolon(s.Authors)
	}
	if len(s.Narrators) > 0 {
		meta.NarratorName = joinSemicolon(s.Narrators)
	}
	if s.Year > 0 {
		meta.PublishedYear = itoa(s.Year)
	}
	return LibraryItem{
		ID:        s.ID,
		LibraryID: VirtualLibraryID,
		FolderID:  VirtualFolderID,
		MediaType: LibraryMediaType,
		Media: LibraryItemMedia{
			Metadata: meta,
			Duration: float64(s.DurationSeconds),
		},
	}
}

func joinSemicolon(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += "; " + p
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	negative := false
	if n < 0 {
		negative = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
