// Package bookdrop scans an operator-configured directory for new
// audio files and enqueues them as pending_import rows. Admin
// reviews + approves; approval fires the audiobook.import event the
// backend listens for.
//
// The scanner is one-shot: each tick walks the directory tree once,
// upserts any new files, and exits. The scheduler runs it on a
// cron; no long-running goroutine, no fsnotify (which would need
// platform-specific handling on macOS/Linux/Windows).
package bookdrop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dhowden/tag"
	"github.com/oklog/ulid/v2"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
)

// Logger is the narrow logging surface the scanner needs. Both hclog
// and slog satisfy it.
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
}

type noopLogger struct{}

func (noopLogger) Debug(string, ...any) {}
func (noopLogger) Info(string, ...any)  {}
func (noopLogger) Warn(string, ...any)  {}

// Scanner walks a directory + enqueues new files. Construct one per
// process (the underlying file system handles are cheap) and call
// ScanOnce from the scheduler tick.
type Scanner struct {
	logger Logger
}

func New(logger Logger) *Scanner {
	if logger == nil {
		logger = noopLogger{}
	}
	return &Scanner{logger: logger}
}

// ParsedMetadata is the shape we write into pending_import.metadata.
// Field set matches what dhowden/tag extracts from ID3v2 / MP4
// atoms / FLAC / Ogg.
type ParsedMetadata struct {
	Title    string   `json:"title"`
	Authors  []string `json:"authors,omitempty"`
	Narrator string   `json:"narrator,omitempty"`
	Series   string   `json:"series,omitempty"`
	Year     int      `json:"year,omitempty"`
	Genre    string   `json:"genre,omitempty"`
	Album    string   `json:"album,omitempty"`
	Comment  string   `json:"comment,omitempty"`
}

// ScanOnce walks `root` recursively, upserting one pending_import
// per audio file found. Returns the count of rows touched + the
// first error. Continues past per-file errors.
func (s *Scanner) ScanOnce(ctx context.Context, st *store.Store, root string) (int, error) {
	if root == "" {
		return 0, errors.New("root path empty")
	}
	info, err := os.Stat(root)
	if err != nil {
		return 0, fmt.Errorf("stat %q: %w", root, err)
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("%q is not a directory", root)
	}

	count := 0
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			s.logger.Warn("bookdrop walk", "path", path, "err", walkErr.Error())
			return nil // continue past per-file walk errors
		}
		if d.IsDir() {
			return nil
		}
		if !isAudioFile(path) {
			return nil
		}
		// Cancellation check between files — a slow scan over a
		// 10K-file directory should yield to a shutdown promptly.
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := s.upsertOne(ctx, st, path); err != nil {
			s.logger.Warn("bookdrop upsert", "path", path, "err", err.Error())
			return nil
		}
		count++
		return nil
	})
	return count, err
}

// upsertOne stats the file, parses tags, writes the row. Idempotent
// on file_path — re-scanning the same file is a no-op when status
// has progressed past 'pending' (admin edits are preserved).
func (s *Scanner) upsertOne(ctx context.Context, st *store.Store, path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}
	meta, coverData, coverMIME := parseTagsAndCover(path)
	if meta.Title == "" {
		// Fall back to the filename minus extension when the file
		// has no embedded title tag. Better than empty in the
		// admin UI.
		meta.Title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal meta: %w", err)
	}
	return st.UpsertPendingImport(ctx, store.PendingImport{
		ID:        ulid.Make().String(),
		FilePath:  path,
		SizeBytes: fi.Size(),
		Metadata:  metaJSON,
		Status:    "pending",
		CoverData: coverData,
		CoverMIME: coverMIME,
	})
}

// parseTagsAndCover combines tag parsing with embedded cover
// extraction in a single file pass. ID3v2 APIC frames + MP4 'covr'
// atoms + FLAC METADATA_BLOCK_PICTURE all expose covers via
// dhowden/tag's Picture() — we copy the bytes + record the MIME
// for the admin review UI.
//
// Returns the parsed metadata + cover bytes + MIME. Cover bytes
// are nil when the file has no embedded cover.
func parseTagsAndCover(path string) (ParsedMetadata, []byte, string) {
	f, err := os.Open(path)
	if err != nil {
		return ParsedMetadata{}, nil, ""
	}
	defer f.Close()
	m, err := tag.ReadFrom(f)
	if err != nil {
		return ParsedMetadata{}, nil, ""
	}
	meta := ParsedMetadata{
		Title:   m.Title(),
		Album:   m.Album(),
		Genre:   m.Genre(),
		Year:    m.Year(),
		Comment: m.Comment(),
	}
	if artist := m.Artist(); artist != "" {
		meta.Authors = splitNames(artist)
	}
	if composer := m.Composer(); composer != "" {
		meta.Narrator = composer
	}
	var coverData []byte
	var coverMIME string
	if pic := m.Picture(); pic != nil && len(pic.Data) > 0 {
		coverData = pic.Data
		coverMIME = pic.MIMEType
		if coverMIME == "" && pic.Ext != "" {
			coverMIME = "image/" + pic.Ext
		}
	}
	return meta, coverData, coverMIME
}

// parseTags reads ID3 / MP4 / FLAC / Ogg metadata from one file.
// Errors are swallowed — a file without tags isn't broken, just
// less informative; the scanner sets a filename-based title in
// upsertOne.
func parseTags(path string) ParsedMetadata {
	f, err := os.Open(path)
	if err != nil {
		return ParsedMetadata{}
	}
	defer f.Close()
	m, err := tag.ReadFrom(f)
	if err != nil {
		return ParsedMetadata{}
	}
	out := ParsedMetadata{
		Title:   m.Title(),
		Album:   m.Album(),
		Genre:   m.Genre(),
		Year:    m.Year(),
		Comment: m.Comment(),
	}
	if artist := m.Artist(); artist != "" {
		out.Authors = splitNames(artist)
	}
	// Many audiobook tagging conventions abuse Album for the series
	// name and Composer for the narrator. We don't normalise these
	// here — the admin sees the raw tag values and edits before
	// approving.
	if composer := m.Composer(); composer != "" {
		out.Narrator = composer
	}
	return out
}

// splitNames breaks a multi-author tag into a slice. Common
// separators: ";", ", ", " & ", " and ". Order preserved.
func splitNames(s string) []string {
	for _, sep := range []string{";", ", ", " & ", " and "} {
		if strings.Contains(s, sep) {
			parts := strings.Split(s, sep)
			out := make([]string, 0, len(parts))
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p != "" {
					out = append(out, p)
				}
			}
			return out
		}
	}
	return []string{strings.TrimSpace(s)}
}

// isAudioFile returns true for extensions our backends accept.
// Matches the audiobookshelf-app's audio-format whitelist.
func isAudioFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3", ".m4a", ".m4b", ".mp4", ".aac", ".flac", ".ogg", ".opus", ".wav":
		return true
	}
	return false
}

// Heartbeat returns a small struct the scheduler can include in its
// status log so the operator sees the scanner is alive.
type Heartbeat struct {
	LastScanAt    time.Time `json:"last_scan_at"`
	LastScanCount int       `json:"last_scan_count"`
	LastError     string    `json:"last_error,omitempty"`
}
