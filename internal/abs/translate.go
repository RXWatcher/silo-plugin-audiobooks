package abs

import (
	"strconv"
	"strings"
	"unicode"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/backend"
)

// VirtualLibraryID is the single library exposed to ABS clients.
const (
	VirtualLibraryID   = "silo-audiobooks"
	VirtualLibraryName = "Audiobooks"
	VirtualFolderID    = "main"
	LibraryMediaType   = "book"
	// ServerVersion must be ≥ 2.26.0 for the official ABS mobile app
	// to take its JWT path; below that it falls into "old token" mode
	// and rejects modern refresh-token semantics. See
	// /opt/audiobookshelf-app/components/connect/ServerConnectForm.vue:731.
	ServerVersion = "2.35.0"
	ServerSourceTag    = "silo"
)

func libraryIDString(id int64) string {
	if id > 0 {
		return strconv.FormatInt(id, 10)
	}
	return VirtualLibraryID
}

// AuthorObj is the ABS-shaped author reference. ABS clients filter by id;
// some screens render only name.
type AuthorObj struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SeriesObj is the ABS-shaped series reference; sequence is the per-book
// position string (e.g. "1", "1.5").
type SeriesObj struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Sequence string `json:"sequence,omitempty"`
}

// LibraryItem is the ABS-shaped audiobook summary. AddedAt / UpdatedAt are
// Unix milliseconds; some shelves on the home screen sort by these and
// clients also expect them as ints (not strings).
//
// CollapsedSeries is non-nil only on items returned with
// collapseseries=1. It folds every book in a series into a single
// representative entry. ABS clients pattern-match on the presence of
// this field to switch from "list of books" to "list of series" UI.
type LibraryItem struct {
	ID              string             `json:"id"`
	LibraryID       string             `json:"libraryId"`
	FolderID        string             `json:"folderId"`
	MediaType       string             `json:"mediaType"`
	// IsMissing / IsInvalid are gating fields the ABS mobile client checks
	// before rendering the play affordance — `showPlay = !isMissing &&
	// !isInvalid && (numTracks || episodes.length)`. We always emit them
	// (no omitempty) so the client never sees them as undefined; the
	// catalog we serve is by definition present and valid.
	// Ref: /opt/audiobookshelf-app/pages/item/_id/index.vue:445
	IsMissing       bool               `json:"isMissing"`
	IsInvalid       bool               `json:"isInvalid"`
	Media           LibraryItemMedia   `json:"media"`
	NumTracks       int                `json:"numTracks,omitempty"`
	AddedAt         int64              `json:"addedAt"`
	UpdatedAt       int64              `json:"updatedAt"`
	CollapsedSeries *CollapsedSeriesV1 `json:"collapsedSeries,omitempty"`
}

// CollapsedSeriesV1 is the per-item annotation real ABS attaches when
// collapseseries=1. The shape is "name + count + per-book books[]"; we
// emit a stable subset since clients differ on which fields they read.
type CollapsedSeriesV1 struct {
	ID            string                 `json:"id"`
	Name          string                 `json:"name"`
	NameIgnorePrefix string              `json:"nameIgnorePrefix,omitempty"`
	NumBooks      int                    `json:"numBooks"`
	LibraryItemIDs []string              `json:"libraryItemIds"`
}

// LibraryItemMedia carries the bulk of the metadata.
//
// ABS distinguishes between `audioFiles` (file-level metadata) and `tracks`
// (the playback ordering the player iterates). For most audiobooks they're
// the same slice; we emit both because the item-detail page reads
// `media.tracks.length` to decide whether to render the play button
// (see /opt/audiobookshelf-app/pages/item/_id/index.vue:423-427,445), while
// card / list views read `media.numTracks`.
type LibraryItemMedia struct {
	Metadata   Metadata     `json:"metadata"`
	Duration   float64      `json:"duration"`
	CoverPath  string       `json:"coverPath"`
	AudioFiles []AudioTrack `json:"audioFiles"`
	Tracks     []AudioTrack `json:"tracks"`
	Chapters   []ChapterABS `json:"chapters"`
	NumTracks  int          `json:"numTracks"`
}

// Metadata is the book-level metadata block. Authors / Narrators / Series
// match the ABS spec: arrays of references (or strings for Narrators).
type Metadata struct {
	Title         string      `json:"title"`
	Authors       []AuthorObj `json:"authors"`
	Narrators     []string    `json:"narrators"`
	Series        []SeriesObj `json:"series"`
	Description   string      `json:"description,omitempty"`
	PublishedYear string      `json:"publishedYear,omitempty"`
	ISBN          string      `json:"isbn,omitempty"`
	Publisher     string      `json:"publisher,omitempty"`
	Genres        []string    `json:"genres,omitempty"`
}

// AudioTrack is a single playable file as the ABS mobile client expects to
// see it. The shape is rich because the official audiobookshelf-app's Vue
// layer reads many fields off each track — ino + metadata for download URL
// construction and offline-cache decisions, bitRate / channels / codec /
// format for the "Now Playing" detail UI, embeddedCoverArt for whether to
// fall back to the item-level cover, metaTags for ID3-style display. A
// missing key on any of those code paths makes the player silently abort
// the audio load (the "spinner forever" we kept chasing before).
//
// The same struct is emitted by both translate.go's ToLibraryItem (the
// /api/items/{id} response) AND handler.go's handlePlay (the /play
// response) so that downloads built off either shape resolve to the same
// ino. Defaults (bitRate=128000, channels=2, channelLayout="stereo",
// timeBase="1/14112000", language=nil) come from the same defaults
// booklore-ng's working implementation emits.
type AudioTrack struct {
	Index                int                 `json:"index"`
	Ino                  string              `json:"ino"`
	Metadata             *AudioTrackMetadata `json:"metadata,omitempty"`
	AddedAt              int64               `json:"addedAt,omitempty"`
	UpdatedAt            int64               `json:"updatedAt,omitempty"`
	TrackNumFromMeta     *int                `json:"trackNumFromMeta"`
	DiscNumFromMeta      *int                `json:"discNumFromMeta"`
	TrackNumFromFilename *int                `json:"trackNumFromFilename"`
	DiscNumFromFilename  *int                `json:"discNumFromFilename"`
	ManuallyVerified     bool                `json:"manuallyVerified"`
	Exclude              bool                `json:"exclude"`
	Error                *string             `json:"error"`
	Format               string              `json:"format,omitempty"`
	Duration             float64             `json:"duration"`
	BitRate              int                 `json:"bitRate,omitempty"`
	Language             *string             `json:"language"`
	Codec                string              `json:"codec,omitempty"`
	TimeBase             string              `json:"timeBase,omitempty"`
	Channels             int                 `json:"channels,omitempty"`
	ChannelLayout        string              `json:"channelLayout,omitempty"`
	Chapters             []ChapterABS        `json:"chapters,omitempty"`
	EmbeddedCoverArt     any                 `json:"embeddedCoverArt"`
	MetaTags             map[string]string   `json:"metaTags,omitempty"`
	MimeType             string              `json:"mimeType"`
	Title                string              `json:"title,omitempty"`
	StartOffset          float64             `json:"startOffset"`
	ContentURL           string              `json:"contentUrl"`
}

// AudioTrackMetadata is the file-level metadata block nested inside each
// AudioTrack. The mobile downloader reads filename/ext to name the local
// copy, size to budget storage, and the mtime fields for cache invalidation.
type AudioTrackMetadata struct {
	Filename    string `json:"filename"`
	Ext         string `json:"ext"`
	Path        string `json:"path"`
	RelPath     string `json:"relPath"`
	Size        int64  `json:"size"`
	MtimeMs     int64  `json:"mtimeMs"`
	CtimeMs     int64  `json:"ctimeMs"`
	BirthtimeMs int64  `json:"birthtimeMs"`
}

// buildAudioTracks is the canonical builder for the AudioTrack slice.
// Both translate.ToLibraryItem (item detail) and handler.handlePlay (play
// session) call this so the ino, metadata, and codec fields are identical
// on both response shapes. Item detail downloads built off the ino in
// /api/items/{id} resolve to the same backend file as the ino in /play.
//
// urlFor receives the 1-based wire index and returns the contentUrl to
// embed. Callers supply different URL functions (signed session URL for
// /play, plain /api/items/{id}/file/{ino}/download for item detail).
func buildAudioTracks(d backend.AudiobookDetail, urlFor func(wireIdx int, ino string) string) []AudioTrack {
	tracks := make([]AudioTrack, 0, len(d.Files))
	var cumulative float64
	for _, f := range d.Files {
		wireIdx := f.Index + 1
		// Duration fallback: see handler.handlePlay's comment for the
		// underlying mobile-player bug; if we leave the matching track
		// at duration=0 the spinner runs forever.
		trackDuration := float64(f.DurationSeconds)
		if trackDuration <= 0 && len(d.Files) == 1 && d.DurationSeconds > 0 {
			trackDuration = float64(d.DurationSeconds)
		}
		ino := trackInoFor(d.ID, f.Index)
		ext := extForFile(f)
		mime := f.MimeType
		if mime == "" {
			mime = mimeOf(ext)
		}
		filename := d.ID + ext
		embeddedCover := any(nil)
		if d.CoverPath != "" || d.CoverURL != "" || d.HasCover {
			embeddedCover = "yes"
		}
		fileTitle := strings.TrimSuffix(filename, ext)
		tracks = append(tracks, AudioTrack{
			Index: wireIdx,
			Ino:   ino,
			Metadata: &AudioTrackMetadata{
				Filename:    filename,
				Ext:         ext,
				Path:        filename,
				RelPath:     filename,
				Size:        f.SizeBytes,
				MtimeMs:     d.UpdatedAtMs,
				CtimeMs:     d.AddedAtMs,
				BirthtimeMs: d.AddedAtMs,
			},
			AddedAt:          d.AddedAtMs,
			UpdatedAt:        d.UpdatedAtMs,
			ManuallyVerified: false,
			Exclude:          false,
			Format:           strings.ToUpper(strings.TrimPrefix(ext, ".")),
			Duration:         trackDuration,
			BitRate:          128000,
			Codec:            firstNonEmpty(f.Format, "mp3"),
			TimeBase:         "1/14112000",
			Channels:         2,
			ChannelLayout:    "stereo",
			EmbeddedCoverArt: embeddedCover,
			MetaTags:         map[string]string{},
			MimeType:         mime,
			Title:            fileTitle,
			StartOffset:      cumulative,
			ContentURL:       urlFor(wireIdx, ino),
		})
		cumulative += trackDuration
	}
	return tracks
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
	meta := buildMetadata(d.AudiobookSummary)
	meta.Description = d.Description
	meta.ISBN = d.ISBN
	meta.Publisher = d.Publisher
	meta.Genres = d.Genres

	// If the backend provided no AuthorRefs but did provide a flat
	// series string, derive the series ref from the detail. Same for
	// authors above (done inside buildMetadata).
	if len(meta.Series) == 0 && strings.TrimSpace(d.Series) != "" {
		meta.Series = []SeriesObj{{
			ID:       slugify(d.Series),
			Name:     d.Series,
			Sequence: formatSequence(d.SeriesIndex),
		}}
	}

	// Use the shared builder so item-detail and /play emit the same
	// rich shape — same ino, same metadata, same codec/format. Mobile
	// constructs download URLs off the ino in whichever response it has
	// first; if the two shapes disagreed, downloads would break.
	tracks := buildAudioTracks(d, func(wireIdx int, _ string) string {
		// Item-detail callers pass a content URL function that knows
		// nothing about playback sessions. We honour their wireIdx so
		// the link still resolves through file_handler.handleItemFile.
		return contentURLFn(wireIdx)
	})
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
		LibraryID: libraryIDString(d.LibraryID),
		FolderID:  VirtualFolderID,
		MediaType: LibraryMediaType,
		Media: LibraryItemMedia{
			Metadata:   meta,
			Duration:   float64(d.DurationSeconds),
			CoverPath:  pickCoverPath(d.AudiobookSummary),
			AudioFiles: tracks,
			Tracks:     tracks,
			Chapters:   chapters,
			NumTracks:  len(d.Files),
		},
		NumTracks: len(d.Files),
		AddedAt:   d.AddedAtMs,
		UpdatedAt: d.UpdatedAtMs,
	}
}

// ToLibrarySummary translates a backend AudiobookSummary into a slim ABS
// LibraryItem (no tracks/chapters).
func ToLibrarySummary(s backend.AudiobookSummary) LibraryItem {
	return LibraryItem{
		ID:        s.ID,
		LibraryID: libraryIDString(s.LibraryID),
		FolderID:  VirtualFolderID,
		MediaType: LibraryMediaType,
		Media: LibraryItemMedia{
			Metadata:   buildMetadata(s),
			Duration:   float64(s.DurationSeconds),
			CoverPath:  pickCoverPath(s),
			AudioFiles: []AudioTrack{},
			Tracks:     []AudioTrack{},
			Chapters:   []ChapterABS{},
		},
		AddedAt:   s.AddedAtMs,
		UpdatedAt: s.UpdatedAtMs,
	}
}

// buildMetadata centralises the AuthorObj / SeriesObj construction. When the
// backend exposes refs we use them as-is; otherwise we synthesise refs from
// the legacy flat string fields by slugging the names.
//
// FALLBACK NOTE: synthesising IDs by slugging the name is a short-term
// measure for older audiobook_backend.v1 servers that don't yet emit
// author_refs/series_refs. Clients that round-trip these IDs through
// /libraries/{id}/items?filter=authors.<base64-id> will work as long as the
// /libraries/{id}/authors endpoint here returns IDs derived the same way.
func buildMetadata(s backend.AudiobookSummary) Metadata {
	m := Metadata{
		Title:     s.Title,
		Narrators: append([]string(nil), s.Narrators...),
		Authors:   []AuthorObj{},
		Series:    []SeriesObj{},
	}
	if m.Narrators == nil {
		m.Narrators = []string{}
	}
	if s.Year > 0 {
		m.PublishedYear = strconv.Itoa(s.Year)
	}

	switch {
	case len(s.AuthorRefs) > 0:
		m.Authors = make([]AuthorObj, len(s.AuthorRefs))
		for i, a := range s.AuthorRefs {
			m.Authors[i] = AuthorObj{ID: a.ID, Name: a.Name}
		}
	case len(s.Authors) > 0:
		m.Authors = make([]AuthorObj, 0, len(s.Authors))
		for _, name := range s.Authors {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			m.Authors = append(m.Authors, AuthorObj{ID: slugify(name), Name: name})
		}
	}

	if len(s.SeriesRefs) > 0 {
		m.Series = make([]SeriesObj, len(s.SeriesRefs))
		for i, r := range s.SeriesRefs {
			m.Series[i] = SeriesObj{ID: r.ID, Name: r.Name, Sequence: r.Sequence}
		}
	}
	return m
}

// pickCoverPath prefers the explicit cover_path from the backend, falling
// back to cover_url, then to a synthesised plugin-route path. ABS clients
// only require a non-empty string here — they fetch the real bytes via
// /api/items/{id}/cover.
func pickCoverPath(s backend.AudiobookSummary) string {
	if s.CoverPath != "" {
		return s.CoverPath
	}
	if s.CoverURL != "" {
		return s.CoverURL
	}
	return "/api/items/" + s.ID + "/cover"
}

// slugify produces a stable ID-from-name. Mirrors the bookwarehouse-audio
// plugin's catalog.Slugify so derived IDs round-trip identically across
// the contract boundary.
func slugify(name string) string {
	var b strings.Builder
	prevDash := true
	for _, r := range strings.ToLower(name) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteRune('-')
				prevDash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// formatSequence renders a series_index (float) as a short string, dropping
// trailing zeros: 1.0 → "1", 1.5 → "1.5", 0 → "".
func formatSequence(v float64) string {
	if v == 0 {
		return ""
	}
	if v == float64(int64(v)) {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}
