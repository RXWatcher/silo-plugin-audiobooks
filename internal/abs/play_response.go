package abs

import (
	"crypto/md5"
	"encoding/hex"
	"path"
	"strconv"
	"strings"

	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/backend"
	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/store"
)

// The /play response shape the official audiobookshelf-app expects is far
// richer than the slim envelope we used to send. The Vue layer reads from
// playbackSession.libraryItem.media.tracks, mediaMetadata.descriptionPlain,
// displayTitle, displayAuthor, audioTracks[i].ino / metadata / bitRate / etc.
// — any undefined access in those code paths causes the mobile audio loader
// to silently bail before fetching bytes, which is the "spinner forever"
// symptom we kept chasing.
//
// This module mirrors booklore-ng's known-good /api/abs/api/items/{id}/play
// response (booklore-ng /src/app/api/abs/api/items/[id]/play/route.ts) and
// librarymanagerre's session-service.ts. Field-for-field where the names
// matter; sensible defaults for fields our backend doesn't expose
// (bitRate, channels, timeBase, etc.) — the mobile client tolerates
// defaults but does NOT tolerate missing keys on the codepaths it reads.

// trackInoFor derives a stable, ABS-compatible inode string from the
// backend book id + file index. Real ABS uses the filesystem inode (a
// large positive BigInt-shaped string); we hash to a 12-hex-digit prefix
// and parse it as a decimal so the client sees an identifier of the
// same shape. Stability matters: the mobile app keys offline downloads
// by ino.
func trackInoFor(bookID string, fileIdx int) string {
	sum := md5.Sum([]byte(bookID + "/" + strconv.Itoa(fileIdx)))
	hexStr := hex.EncodeToString(sum[:6])
	n, _ := strconv.ParseUint(hexStr, 16, 64)
	return strconv.FormatUint(n, 10)
}

// extForFile returns ".mp3"-style extension from a backend file's
// Format, falling back to ".mp3" so the mobile UI's format chip never
// lands on an empty string.
func extForFile(f backend.AudiobookFile) string {
	switch strings.ToLower(strings.TrimSpace(f.Format)) {
	case "mp3", "":
		return ".mp3"
	case "m4a":
		return ".m4a"
	case "m4b":
		return ".m4b"
	case "ogg":
		return ".ogg"
	case "opus":
		return ".opus"
	case "flac":
		return ".flac"
	case "aac":
		return ".aac"
	}
	return "." + strings.ToLower(f.Format)
}

// mimeOf returns a deterministic MIME for an extension when the backend
// didn't supply one — necessary because the mobile audio element rejects
// an empty Content-Type during load.
func mimeOf(ext string) string {
	switch strings.ToLower(ext) {
	case ".mp3":
		return "audio/mpeg"
	case ".m4a", ".m4b", ".aac":
		return "audio/mp4"
	case ".ogg":
		return "audio/ogg"
	case ".opus":
		return "audio/opus"
	case ".flac":
		return "audio/flac"
	}
	return "audio/mpeg"
}

// titleIgnorePrefix strips the leading article (a/an/the) for sort-key
// purposes — matches real ABS LibraryItemController behaviour.
func titleIgnorePrefix(title string) string {
	lower := strings.ToLower(title)
	for _, p := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(lower, p) {
			return title[len(p):]
		}
	}
	return title
}

// buildPlayMediaMetadata produces the playbackSession.mediaMetadata object
// the mobile player reads to render the "Now Playing" widget and the
// playback-history records. Field shape matches booklore-ng's exactly so
// the official ABS client doesn't need to branch.
func buildPlayMediaMetadata(d backend.AudiobookDetail) map[string]any {
	// Author objects: prefer AuthorRefs (have IDs) and fall back to flat
	// Authors strings when the backend hasn't migrated.
	authors := make([]map[string]any, 0)
	authorNames := make([]string, 0)
	for _, a := range d.AuthorRefs {
		authors = append(authors, map[string]any{"id": a.ID, "name": a.Name})
		authorNames = append(authorNames, a.Name)
	}
	if len(authors) == 0 {
		for i, name := range d.Authors {
			authors = append(authors, map[string]any{
				"id":   "author-" + strconv.Itoa(i) + "-" + name,
				"name": name,
			})
			authorNames = append(authorNames, name)
		}
	}
	authorName := strings.Join(authorNames, ", ")
	lastFirsts := make([]string, len(authorNames))
	for i, n := range authorNames {
		lastFirsts[i] = lastFirst(n)
	}
	authorNameLF := strings.Join(lastFirsts, ", ")

	// Series: emit the structured array the mobile reads, plus the flat
	// seriesName that older fields key on.
	series := make([]map[string]any, 0)
	for _, s := range d.SeriesRefs {
		entry := map[string]any{"id": s.ID, "name": s.Name}
		if s.Sequence != "" {
			entry["sequence"] = s.Sequence
		} else {
			entry["sequence"] = nil
		}
		series = append(series, entry)
	}
	if len(series) == 0 && strings.TrimSpace(d.Series) != "" {
		entry := map[string]any{"id": slugify(d.Series), "name": d.Series}
		if d.SeriesIndex > 0 {
			entry["sequence"] = formatSequence(d.SeriesIndex)
		} else {
			entry["sequence"] = nil
		}
		series = append(series, entry)
	}
	seriesName := ""
	if len(series) > 0 {
		seriesName, _ = series[0]["name"].(string)
		if seq, ok := series[0]["sequence"].(string); ok && seq != "" {
			seriesName = seriesName + " #" + seq
		}
	}

	title := d.Title

	// publishedYear is a string in the ABS wire format ("2019") even
	// though it's numeric in source — clients string-compare it.
	publishedYear := ""
	if d.Year > 0 {
		publishedYear = strconv.Itoa(d.Year)
	}

	return map[string]any{
		"title":              title,
		"titleIgnorePrefix":  titleIgnorePrefix(title),
		"subtitle":           nil,
		"authors":            authors,
		"authorName":         authorName,
		"authorNameLF":       authorNameLF,
		"narrators":          d.Narrators,
		"narratorName":       strings.Join(d.Narrators, ", "),
		"series":             series,
		"seriesName":         seriesName,
		"genres":             nonNilStrings(d.Genres),
		"publishedYear":      publishedYear,
		"publishedDate":      nil,
		"publisher":          nilIfEmpty(d.Publisher),
		"description":        nilIfEmpty(d.Description),
		"descriptionPlain":   nilIfEmpty(stripHTML(d.Description)),
		"isbn":               nilIfEmpty(d.ISBN),
		"asin":               nil,
		"language":           "en",
		"explicit":           false,
		"abridged":           false,
	}
}

// buildPlayAudioTracks builds the audioTracks[] array with the full set
// of fields the mobile client reads — Vue components access bitRate,
// codec, channels, metaTags, format, title, etc. and a missing key on
// any of those silently aborts the audio loader.
//
// wireIdxToBackend is the convention from handlePublicTrack: the mobile
// app sends 1-based track indexes (ABS LibraryItemController.js:500)
// and we subtract 1 when calling the backend. urlFor returns the
// session-scoped URL with the signed session-JWT in the query string.
func buildPlayAudioTracks(d backend.AudiobookDetail, bookID string, urlFor func(wireIdx int) string) []map[string]any {
	tracks := make([]map[string]any, 0, len(d.Files))
	var cumulative float64
	for _, f := range d.Files {
		wireIdx := f.Index + 1
		// Duration fallback: see handlePlay's comment block for why this
		// matters. The mobile player's findIndex needs a non-zero
		// duration on the matching track or the spinner runs forever.
		trackDuration := float64(f.DurationSeconds)
		if trackDuration <= 0 && len(d.Files) == 1 && d.DurationSeconds > 0 {
			trackDuration = float64(d.DurationSeconds)
		}
		ext := extForFile(f)
		mime := f.MimeType
		if mime == "" {
			mime = mimeOf(ext)
		}
		filename := bookID + ext
		ino := trackInoFor(bookID, f.Index)
		fileTitle := strings.TrimSuffix(path.Base(filename), ext)
		// embeddedCoverArt is a presence sentinel ("yes" or nil) — not a
		// blob. Mobile keys offline-cache decisions off this.
		embeddedCover := any(nil)
		if d.CoverPath != "" || d.CoverURL != "" || d.HasCover {
			embeddedCover = "yes"
		}
		tracks = append(tracks, map[string]any{
			"index": wireIdx,
			"ino":   ino,
			"metadata": map[string]any{
				"filename":    filename,
				"ext":         ext,
				"path":        filename,
				"relPath":     filename,
				"size":        f.SizeBytes,
				"mtimeMs":     d.UpdatedAtMs,
				"ctimeMs":     d.AddedAtMs,
				"birthtimeMs": d.AddedAtMs,
			},
			"addedAt":               d.AddedAtMs,
			"updatedAt":             d.UpdatedAtMs,
			"trackNumFromMeta":      nil,
			"discNumFromMeta":       nil,
			"trackNumFromFilename":  nil,
			"discNumFromFilename":   nil,
			"manuallyVerified":      false,
			"exclude":               false,
			"error":                 nil,
			"format":                strings.ToUpper(strings.TrimPrefix(ext, ".")),
			"duration":              trackDuration,
			"bitRate":               128000,
			"language":              nil,
			"codec":                 firstNonEmpty(f.Format, "mp3"),
			"timeBase":              "1/14112000",
			"channels":              2,
			"channelLayout":         "stereo",
			"chapters":              []any{},
			"embeddedCoverArt":      embeddedCover,
			"metaTags":              map[string]any{},
			"mimeType":              mime,
			"title":                 fileTitle,
			"startOffset":           cumulative,
			"contentUrl":            urlFor(wireIdx),
		})
		cumulative += trackDuration
	}
	return tracks
}

// buildPlayLibraryItem builds the playbackSession.libraryItem nested
// object. Mobile components read libraryItem.media.tracks /
// libraryItem.media.metadata / libraryItem.libraryFiles for offline
// download decisions and UI rendering — missing this object is one of
// the silent-bail paths in AbsAudioPlayer's setAudioPlayer flow.
func buildPlayLibraryItem(
	d backend.AudiobookDetail,
	lib store.PortalLibrary,
	bookID string,
	mediaMetadata map[string]any,
	audioTracks []map[string]any,
	chapters []map[string]any,
	totalDuration float64,
) map[string]any {
	firstIno := bookID
	totalSize := int64(0)
	for _, f := range d.Files {
		totalSize += f.SizeBytes
	}
	if len(audioTracks) > 0 {
		if s, ok := audioTracks[0]["ino"].(string); ok && s != "" {
			firstIno = s
		}
	}
	libraryFiles := make([]map[string]any, 0, len(audioTracks))
	for _, t := range audioTracks {
		libraryFiles = append(libraryFiles, map[string]any{
			"ino":              t["ino"],
			"metadata":         t["metadata"],
			"isSupplementary":  false,
			"addedAt":          t["addedAt"],
			"updatedAt":        t["updatedAt"],
			"fileType":         "audio",
		})
	}
	coverPath := d.CoverPath
	if coverPath == "" && d.CoverURL != "" {
		coverPath = d.CoverURL
	}
	return map[string]any{
		"id":                bookID,
		"ino":               firstIno,
		"oldLibraryItemId":  nil,
		"libraryId":         absLibraryID(lib),
		"folderId":          VirtualFolderID,
		"path":              bookID,
		"relPath":           bookID,
		"isFile":            true,
		"mtimeMs":           nil,
		"ctimeMs":           nil,
		"birthtimeMs":       nil,
		"addedAt":           d.AddedAtMs,
		"updatedAt":         d.UpdatedAtMs,
		"lastScan":          d.AddedAtMs,
		"scanVersion":       ServerVersion,
		"isMissing":         false,
		"isInvalid":         false,
		"mediaType":         "book",
		"media": map[string]any{
			"id":             bookID,
			"libraryItemId":  bookID,
			"metadata":       mediaMetadata,
			"coverPath":      nilIfEmpty(coverPath),
			"tags":           []any{},
			"audioFiles":     audioTracks,
			"chapters":       chapters,
			"ebookFile":      nil,
			"duration":       totalDuration,
			"size":           totalSize,
			"tracks":         audioTracks,
		},
		"libraryFiles": libraryFiles,
		// size on the outer libraryItem is a STRING in real ABS — mobile
		// reads it via a sort comparator that string-compares.
		"size": strconv.FormatInt(totalSize, 10),
	}
}

// --- small helpers ----------------------------------------------------

func nilIfEmpty(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func firstNonEmpty(in ...string) string {
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// stripHTML strips angle-bracket tags from a description so the mobile
// "Now Playing" body renderer doesn't have to. Cheap and good enough for
// the descriptions our backend currently produces.
func stripHTML(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}
