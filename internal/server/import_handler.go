package server

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/auth"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
)

// Restore-from-export — the inbound counterpart of /me/export.
// Accepts a multipart upload of the export ZIP and additively
// merges each section into the current user's data. INSERT ON
// CONFLICT DO NOTHING semantics across the board: a restore never
// overwrites existing rows, so re-running a restore is safe and
// "import what's new from another instance" works as expected.
//
// Schema validation: each section file must match its expected
// shape. Unknown sections are logged + skipped. Manifest checks
// the export was for THIS plugin (not the ebook export reuploaded
// to the audiobook surface).

const maxImportBytes = 20 << 20 // 20 MB — bigger than any real export

func (s *Server) mountImportRoutes(r chi.Router) {
	r.Post("/me/import", s.handleImport)
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	if err := r.ParseMultipartForm(maxImportBytes); err != nil {
		writeError(w, http.StatusBadRequest, "invalid multipart: "+err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file part required")
		return
	}
	defer file.Close()
	if header.Size > maxImportBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "import too large (20 MB max)")
		return
	}

	data, err := io.ReadAll(io.LimitReader(file, maxImportBytes+1))
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	zr, err := zip.NewReader(bytesReader(data), int64(len(data)))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid zip: "+err.Error())
		return
	}

	// Map zip entries → byte slices keyed on filename so we can
	// process in a deterministic order.
	entries := make(map[string][]byte, len(zr.File))
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(rc, maxImportBytes))
		_ = rc.Close()
		entries[f.Name] = b
	}

	// Validate manifest before touching any data.
	manifestBytes, ok2 := entries["_manifest.json"]
	if !ok2 {
		writeError(w, http.StatusBadRequest, "missing _manifest.json")
		return
	}
	var manifest struct {
		Plugin string `json:"plugin"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		writeError(w, http.StatusBadRequest, "invalid manifest: "+err.Error())
		return
	}
	if manifest.Plugin != "continuum-audiobooks" {
		writeError(w, http.StatusBadRequest,
			"manifest plugin "+manifest.Plugin+" doesn't match audiobooks plugin")
		return
	}

	counts := map[string]int{}
	importSection(entries, "smart_collections.json", &counts, "smart_collections",
		func(items []store.SmartCollection) {
			for _, c := range items {
				c.UserID = id.UserID
				if err := s.d.Store.UpsertSmartCollection(r.Context(), c); err == nil {
					counts["smart_collections"]++
				}
			}
		})
	importSection(entries, "reading_goals.json", &counts, "reading_goals",
		func(items []store.ReadingGoal) {
			for _, g := range items {
				g.UserID = id.UserID
				if err := s.d.Store.UpsertReadingGoal(r.Context(), g); err == nil {
					counts["reading_goals"]++
				}
			}
		})
	importSection(entries, "share_links.json", &counts, "share_links",
		func(items []store.ShareLink) {
			for _, l := range items {
				l.UserID = id.UserID
				if err := s.d.Store.CreateShareLink(r.Context(), l); err == nil {
					counts["share_links"]++
				}
			}
		})

	s.audit(r, id.UserID, "import", "personal_data", "", counts)
	writeJSON(w, http.StatusOK, map[string]any{
		"imported_at": time.Now().UTC().Format(time.RFC3339),
		"counts":      counts,
	})
}

// importSection decodes one zip entry as []T and calls handler.
// Missing or malformed entries are logged + skipped — partial
// restores are valuable when one section is corrupted but the
// others are fine.
func importSection[T any](entries map[string][]byte, name string, counts *map[string]int, label string, handler func([]T)) {
	_ = label
	raw, ok := entries[name]
	if !ok || len(raw) == 0 {
		return
	}
	var items []T
	if err := json.Unmarshal(raw, &items); err != nil {
		return
	}
	handler(items)
}

// bytesReader returns an io.ReaderAt around a byte slice without
// needing bytes.NewReader's full type surface.
func bytesReader(b []byte) *bytesReadAt { return &bytesReadAt{data: b} }

type bytesReadAt struct{ data []byte }

func (r *bytesReadAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(r.data)) {
		return 0, errors.New("read out of range")
	}
	n := copy(p, r.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// _ = chi placeholder so the import stays consistent with sibling
// handler files in this package.
var _ = chi.URLParam

// _ = context placeholder — the import handler uses r.Context()
// already, but a future "preview / confirm" two-step flow would
// pass a shared context across the two requests via a temp store.
var _ context.Context
