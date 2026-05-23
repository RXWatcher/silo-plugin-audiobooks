package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/auth"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
)

// Content-restriction admin surface. Per-user blocked lists + an
// explicit-blocked flag. Listeners with no restriction row pass
// everything through; restricted listeners get the filter applied
// at catalog / personalized / search time.
//
// Routes mounted under /api/v1/admin so only admins can touch the
// list. End-users see their own restriction read-only at
// /api/v1/me/content-restriction so they can know what's filtered
// (useful for "why isn't this book showing up?" support).

func (s *Server) mountContentRestrictionRoutes(r chi.Router) {
	r.Get("/admin/content-restrictions", s.handleListContentRestrictions)
	r.Get("/admin/content-restrictions/{userId}", s.handleGetContentRestriction)
	r.Put("/admin/content-restrictions/{userId}", s.handlePutContentRestriction)
	r.Delete("/admin/content-restrictions/{userId}", s.handleDeleteContentRestriction)
	r.Get("/me/content-restriction", s.handleGetMyContentRestriction)
}

type contentRestrictionBody struct {
	BlockedGenres    []string `json:"blocked_genres"`
	BlockedTags      []string `json:"blocked_tags"`
	BlockedAuthors   []string `json:"blocked_authors"`
	BlockedNarrators []string `json:"blocked_narrators"`
	BlockedLibraries []int64  `json:"blocked_libraries"`
	ExplicitBlocked  bool     `json:"explicit_blocked"`
}

func (s *Server) handleListContentRestrictions(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	rows, err := s.d.Store.ListContentRestrictions(r.Context())
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
}

func (s *Server) handleGetContentRestriction(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	userID := chi.URLParam(r, "userId")
	row, err := s.d.Store.GetContentRestriction(r.Context(), userID)
	if errors.Is(err, store.ErrNotFound) {
		// Admin querying a user with no restrictions — return an
		// empty row rather than 404 so the admin UI can render the
		// blank form for editing.
		writeJSON(w, http.StatusOK, store.ContentRestriction{UserID: userID})
		return
	}
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *Server) handlePutContentRestriction(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	userID := chi.URLParam(r, "userId")
	var body contentRestrictionBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	row := store.ContentRestriction{
		UserID:           userID,
		BlockedGenres:    body.BlockedGenres,
		BlockedTags:      body.BlockedTags,
		BlockedAuthors:   body.BlockedAuthors,
		BlockedNarrators: body.BlockedNarrators,
		BlockedLibraries: body.BlockedLibraries,
		ExplicitBlocked:  body.ExplicitBlocked,
	}
	if err := s.d.Store.UpsertContentRestriction(r.Context(), row); err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (s *Server) handleDeleteContentRestriction(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	userID := chi.URLParam(r, "userId")
	if err := s.d.Store.DeleteContentRestriction(r.Context(), userID); err != nil {
		writeInternal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGetMyContentRestriction lets the requesting user see what's
// filtered against their account. Read-only — only admins can
// modify via the routes above.
func (s *Server) handleGetMyContentRestriction(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	row, err := s.d.Store.GetContentRestriction(r.Context(), id.UserID)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, store.ContentRestriction{UserID: id.UserID})
		return
	}
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}
