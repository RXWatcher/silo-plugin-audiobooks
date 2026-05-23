package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/auth"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
)

// mountCollectionRoutes wires collection CRUD endpoints.
func (s *Server) mountCollectionRoutes(r chi.Router) {
	r.Get("/me/collections", s.handleListMyCollections)
	r.Post("/me/collections", s.handleCreateCollection)
	r.Patch("/me/collections/{id}", s.handleUpdateCollection)
	r.Delete("/me/collections/{id}", s.handleDeleteCollection)
	r.Get("/me/collections/{id}/items", s.handleListCollectionItems)
	r.Post("/me/collections/{id}/items", s.handleAddCollectionItem)
	r.Delete("/me/collections/{id}/items/{book_id}", s.handleRemoveCollectionItem)
	r.Get("/collections/public", s.handleListPublicCollections)
}

func (s *Server) handleListMyCollections(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	out, err := s.d.Store.ListUserCollections(r.Context(), id.UserID, profileID(r))
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleListPublicCollections(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireUser(w, r); !ok {
		return
	}
	out, err := s.d.Store.ListPublicCollections(r.Context(), 200)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

type collectionPayload struct {
	Name        string `json:"name"`
	Color       string `json:"color"`
	IsPublic    bool   `json:"is_public"`
	IsPinned    bool   `json:"is_pinned"`
	CoverBookID string `json:"cover_book_id"`
}

func (s *Server) handleCreateCollection(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	var p collectionPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil || p.Name == "" {
		writeError(w, http.StatusBadRequest, "name required")
		return
	}
	c := store.Collection{
		ID:          ulid.Make().String(),
		UserID:      id.UserID,
		ProfileID:   profileID(r),
		Name:        p.Name,
		Color:       p.Color,
		IsPublic:    p.IsPublic,
		IsPinned:    p.IsPinned,
		CoverBookID: p.CoverBookID,
	}
	if err := s.d.Store.CreateCollection(r.Context(), c); err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) handleUpdateCollection(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	collID := chi.URLParam(r, "id")
	var p collectionPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.d.Store.UpdateCollection(r.Context(), store.Collection{
		ID:          collID,
		Name:        p.Name,
		Color:       p.Color,
		IsPublic:    p.IsPublic,
		IsPinned:    p.IsPinned,
		CoverBookID: p.CoverBookID,
	}, id.UserID, profileID(r)); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDeleteCollection(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	collID := chi.URLParam(r, "id")
	if err := s.d.Store.DeleteCollection(r.Context(), collID, id.UserID, profileID(r)); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeInternal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListCollectionItems(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	collID := chi.URLParam(r, "id")
	out, err := s.d.Store.ListCollectionItems(r.Context(), collID, id.UserID, profileID(r))
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

type collItemPayload struct {
	BookID string `json:"book_id"`
}

func (s *Server) handleAddCollectionItem(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	collID := chi.URLParam(r, "id")
	var p collItemPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil || p.BookID == "" {
		writeError(w, http.StatusBadRequest, "book_id required")
		return
	}
	if err := s.d.Store.AddCollectionItem(r.Context(), collID, p.BookID, id.UserID, profileID(r)); err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

func (s *Server) handleRemoveCollectionItem(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	collID := chi.URLParam(r, "id")
	bookID := chi.URLParam(r, "book_id")
	if err := s.d.Store.RemoveCollectionItem(r.Context(), collID, bookID, id.UserID, profileID(r)); err != nil {
		writeInternal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
