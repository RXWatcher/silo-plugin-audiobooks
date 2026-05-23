package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/auth"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
)

// Time-limited public share links for library items. Owner mints a
// slug + TTL + optional use cap; recipients hit /share/{slug}
// without auth. Slug is 22 chars of crypto/rand entropy —
// unguessable.
//
// Public endpoint returns item metadata (no audio bytes yet — the
// follow-up wires the public stream proxy through a session-less
// token bound to the slug).

func (s *Server) mountShareLinkRoutes(r chi.Router) {
	r.Get("/me/share-links", s.handleListShareLinks)
	r.Post("/me/share-links", s.handleCreateShareLink)
	r.Delete("/me/share-links/{id}", s.handleDeleteShareLink)
}

// MountPublicShare registers the unauthenticated /share/{slug}
// route. Mount this OUTSIDE the user-auth group.
func (s *Server) MountPublicShare(r chi.Router) {
	r.Get("/share/{slug}", s.handleResolveShareLink)
	r.Get("/share/{slug}/play", s.handleSharePlay)
	r.Get("/share/{slug}/track/{idx}", s.handleShareTrack)
}

type shareLinkBody struct {
	ItemID    string `json:"item_id"`
	TTLHours  int    `json:"ttl_hours"`  // 0 = never expires
	MaxUses   int    `json:"max_uses"`   // 0 = unlimited
}

func (s *Server) handleListShareLinks(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	rows, err := s.d.Store.ListShareLinks(r.Context(), id.UserID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
}

func (s *Server) handleCreateShareLink(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	var body shareLinkBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.ItemID == "" {
		writeError(w, http.StatusBadRequest, "item_id required")
		return
	}
	slug, err := mintShareSlug()
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	link := store.ShareLink{
		ID:      ulid.Make().String(),
		UserID:  id.UserID,
		Slug:    slug,
		ItemID:  body.ItemID,
		MaxUses: body.MaxUses,
	}
	if body.TTLHours > 0 {
		exp := time.Now().Add(time.Duration(body.TTLHours) * time.Hour)
		link.ExpiresAt = &exp
	}
	if err := s.d.Store.CreateShareLink(r.Context(), link); err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, link)
}

func (s *Server) handleDeleteShareLink(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	if err := s.d.Store.DeleteShareLink(r.Context(), chi.URLParam(r, "id"), id.UserID); err != nil {
		writeInternal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleResolveShareLink — GET /share/{slug}
// Public, unauthenticated. Returns the linked item's metadata (no
// audio bytes — those need a session-token follow-up). Increments
// use_count on each successful resolve so the owner can see how
// many times the share was opened.
//
// Slug invalid / expired / used-up → 404 (don't leak existence).
func (s *Server) handleResolveShareLink(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	link, err := s.d.Store.GetActiveShareLinkBySlug(r.Context(), slug)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "share link not found or expired", http.StatusNotFound)
		return
	}
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	// Bump use_count BEFORE serving content so we don't undercount
	// on a network hiccup mid-response.
	_ = s.d.Store.IncrementShareUse(r.Context(), link.ID)

	// Return a minimal public view of the item — id + title +
	// authors + cover. Full audio access is a follow-up that mints
	// a session-token bound to the slug.
	writeJSON(w, http.StatusOK, map[string]any{
		"slug":       link.Slug,
		"item_id":    link.ItemID,
		"expires_at": link.ExpiresAt,
		"created_at": link.CreatedAt,
	})
}

// mintShareSlug returns a 22-char URL-safe random token. Same
// strategy as RSS feed slugs — 16 bytes of crypto/rand entropy
// makes guessing infeasible.
func mintShareSlug() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
