package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/auth"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/bookdrop"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
)

// BookDrop admin surface — list / get / update / approve / reject /
// scan-now / delete. The scanner runs from a scheduler tick on the
// backend_config.bookdrop_path; admins can also force a scan.
//
// Approval semantics: setting status='approved' fires an
// audiobook.import event with the parsed metadata + file path so
// the backend can ingest. The pending row stays around (status
// transitions to 'imported' after backend confirms) for audit.

func (s *Server) mountBookDropRoutes(r chi.Router) {
	r.Get("/admin/bookdrop", s.handleListPendingImports)
	r.Get("/admin/bookdrop/{id}", s.handleGetPendingImport)
	r.Patch("/admin/bookdrop/{id}", s.handleUpdatePendingImport)
	r.Post("/admin/bookdrop/{id}/approve", s.handleApprovePendingImport)
	r.Post("/admin/bookdrop/{id}/reject", s.handleRejectPendingImport)
	r.Delete("/admin/bookdrop/{id}", s.handleDeletePendingImport)
	r.Post("/admin/bookdrop/scan", s.handleScanBookDrop)
	r.Get("/admin/bookdrop/{id}/cover", s.handleGetBookDropCover)
}

// handleGetBookDropCover streams the embedded cover the scanner
// stashed for a pending_import row. Admin-only — the cover may
// contain PII or copyright-restricted artwork the operator
// doesn't want exposed publicly.
func (s *Server) handleGetBookDropCover(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	data, mime, err := s.d.Store.GetPendingImportCover(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	if len(data) == 0 {
		writeError(w, http.StatusNotFound, "no embedded cover")
		return
	}
	if mime == "" {
		mime = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "private, max-age=300")
	_, _ = w.Write(data)
}

func (s *Server) handleListPendingImports(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	status := r.URL.Query().Get("status")
	limit := 200
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	rows, err := s.d.Store.ListPendingImports(r.Context(), status, limit)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
}

func (s *Server) handleGetPendingImport(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	p, err := s.d.Store.GetPendingImport(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

type pendingImportBody struct {
	Metadata        json.RawMessage `json:"metadata"`
	Status          string          `json:"status"`
	TargetLibraryID *int64          `json:"target_library_id"`
}

func (s *Server) handleUpdatePendingImport(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	existing, err := s.d.Store.GetPendingImport(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	var body pendingImportBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if len(body.Metadata) > 0 {
		existing.Metadata = body.Metadata
	}
	if body.Status != "" {
		existing.Status = body.Status
	} else if existing.Status == "pending" {
		existing.Status = "editing"
	}
	if body.TargetLibraryID != nil {
		existing.TargetLibraryID = body.TargetLibraryID
	}
	if err := s.d.Store.UpdatePendingImport(r.Context(), existing); err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, existing)
}

func (s *Server) handleApprovePendingImport(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	p, err := s.d.Store.GetPendingImport(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	p.Status = "approved"
	p.ErrorMessage = ""
	if err := s.d.Store.UpdatePendingImport(r.Context(), p); err != nil {
		writeInternal(w, r, err)
		return
	}
	// Fire the import event the backend listens for. Payload
	// carries the file path + parsed metadata + target library so
	// the backend doesn't need to re-read the file or re-parse
	// tags — it can ingest directly from this snapshot.
	if s.d.Events != nil {
		payload := map[string]any{
			"pending_import_id": p.ID,
			"file_path":         p.FilePath,
			"metadata":          json.RawMessage(p.Metadata),
		}
		if p.TargetLibraryID != nil {
			payload["target_library_id"] = *p.TargetLibraryID
		}
		s.d.Events.Publish(r.Context(), "audiobook.import_requested", payload)
	}
	writeJSON(w, http.StatusAccepted, p)
}

func (s *Server) handleRejectPendingImport(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	p, err := s.d.Store.GetPendingImport(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	p.Status = "rejected"
	if err := s.d.Store.UpdatePendingImport(r.Context(), p); err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleDeletePendingImport(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	if err := s.d.Store.DeletePendingImport(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeInternal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleScanBookDrop triggers a one-shot scan of the configured
// directory. The same scan runs from the scheduler; this is the
// admin "scan now" button.
func (s *Server) handleScanBookDrop(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	path := s.bookDropPath(r.Context())
	if path == "" {
		writeError(w, http.StatusServiceUnavailable, "bookdrop_path not configured")
		return
	}
	if _, err := os.Stat(path); err != nil {
		writeError(w, http.StatusServiceUnavailable, "bookdrop path unreachable: "+err.Error())
		return
	}
	scanner := bookdrop.New(nil)
	count, err := scanner.ScanOnce(r.Context(), s.d.Store, path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scanned": count})
}

// bookDropPath reads the operator-configured directory from
// backend_config. We hang it off the existing config row rather
// than a new column so admins set it via the existing settings
// surface; a small additional field would need a new column +
// migration, which is more weight than this single string warrants.
//
// For now we read from the BOOKDROP_PATH env var as a stopgap
// until the backend_config column lands.
func (s *Server) bookDropPath(_ context.Context) string {
	return os.Getenv("BOOKDROP_PATH")
}
