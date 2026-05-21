package abs

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/bookref"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
)

// ABS-compat Collections surface. Maps to the existing manual
// collection/collection_item tables shared with the SPA's
// /api/v1/me/collections* routes. Same data, just under the
// upstream-canonical URL family + with the ABS response shape.
//
// Differences from our SPA shape:
//   - "libraryItems" array of full LibraryItem objects rather than
//     just ids — the ABS client renders the cover + title without
//     a follow-up fetch.
//   - "libraryId" pulled from the first item's library; collections
//     in upstream ABS are scoped to one library, ours aren't, so
//     this is a best-effort hint.
//   - "books" + "items" / "lastUpdate" cookie crumbs in the
//     envelope.

func (h *Handler) mountCollectionsRoutes(prefix string, r chi.Router) {
	r.Get(prefix+"/collections", h.handleListCollections)
	r.Post(prefix+"/collections", h.handleCreateCollection)
	r.Get(prefix+"/collections/{id}", h.handleGetCollection)
	r.Patch(prefix+"/collections/{id}", h.handleUpdateCollection)
	r.Delete(prefix+"/collections/{id}", h.handleDeleteCollection)
	r.Post(prefix+"/collections/{id}/book/{bookId}", h.handleAddCollectionBook)
	r.Delete(prefix+"/collections/{id}/book/{bookId}", h.handleRemoveCollectionBook)
}

func (h *Handler) handleListCollections(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	rows, err := h.store.ListUserCollections(r.Context(), a.UserID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, c := range rows {
		out = append(out, h.collectionToABSMap(r, a.UserID, c, false))
	}
	writeJSON(w, http.StatusOK, map[string]any{"collections": out})
}

func (h *Handler) handleGetCollection(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	c, err := h.store.GetCollection(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "collection not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if c.UserID != a.UserID && !c.IsPublic {
		http.Error(w, "not visible", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, h.collectionToABSMap(r, a.UserID, c, true))
}

type collectionBody struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (h *Handler) handleCreateCollection(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	var body collectionBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	c := store.Collection{
		ID:     ulid.Make().String(),
		UserID: a.UserID,
		Name:   body.Name,
	}
	if err := h.store.CreateCollection(r.Context(), c); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	persisted, _ := h.store.GetCollection(r.Context(), c.ID)
	writeJSON(w, http.StatusCreated, h.collectionToABSMap(r, a.UserID, persisted, true))
}

func (h *Handler) handleUpdateCollection(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	c, err := h.store.GetCollection(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "collection not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if c.UserID != a.UserID {
		http.Error(w, "not owned", http.StatusForbidden)
		return
	}
	var body collectionBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Name != "" {
		c.Name = body.Name
	}
	if err := h.store.UpdateCollection(r.Context(), c, a.UserID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, h.collectionToABSMap(r, a.UserID, c, true))
}

func (h *Handler) handleDeleteCollection(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	if err := h.store.DeleteCollection(r.Context(), chi.URLParam(r, "id"), a.UserID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleAddCollectionBook(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	collID := chi.URLParam(r, "id")
	encoded := chi.URLParam(r, "bookId")
	c, err := h.store.GetCollection(r.Context(), collID)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "collection not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if c.UserID != a.UserID {
		http.Error(w, "not owned", http.StatusForbidden)
		return
	}
	if err := h.store.AddCollectionItem(r.Context(), collID, encoded, a.UserID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, h.collectionToABSMap(r, a.UserID, c, true))
}

func (h *Handler) handleRemoveCollectionBook(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	collID := chi.URLParam(r, "id")
	encoded := chi.URLParam(r, "bookId")
	c, err := h.store.GetCollection(r.Context(), collID)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "collection not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if c.UserID != a.UserID {
		http.Error(w, "not owned", http.StatusForbidden)
		return
	}
	if err := h.store.RemoveCollectionItem(r.Context(), collID, encoded, a.UserID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, h.collectionToABSMap(r, a.UserID, c, true))
}

// collectionToABSMap renders a store.Collection in the ABS wire
// shape. Pass items=true to include the books[] array; the list
// view omits it for response-size reasons.
func (h *Handler) collectionToABSMap(r *http.Request, userID string, c store.Collection, items bool) map[string]any {
	out := map[string]any{
		"id":          c.ID,
		"userId":      c.UserID,
		"name":        c.Name,
		"description": "",
		"isPublic":    c.IsPublic,
		"lastUpdate":  c.CreatedAt.UnixMilli(),
		"createdAt":   c.CreatedAt.UnixMilli(),
	}
	if !items {
		return out
	}
	rows, err := h.store.ListCollectionItems(r.Context(), c.ID, userID)
	if err != nil {
		out["books"] = []any{}
		return out
	}
	books := make([]map[string]any, 0, len(rows))
	for _, it := range rows {
		// Try to hydrate a LibraryItem-shape; on backend miss emit
		// just the id so the ABS client at least knows the book is
		// in the collection.
		libID, backendBookID, _ := bookref.Decode(it.BookID)
		entry := map[string]any{"id": it.BookID, "libraryId": libID}
		if libID != 0 && backendBookID != "" {
			if lib, err := h.store.GetPortalLibrary(r.Context(), libID); err == nil && lib.BackendPluginID != "" {
				if d, derr := h.backend.GetDetail(r.Context(), "", lib.BackendPluginID, backendBookID); derr == nil {
					entry = map[string]any{
						"id":        it.BookID,
						"libraryId": absLibraryID(lib),
						"media": map[string]any{
							"metadata": map[string]any{
								"title":   d.Title,
								"authors": d.AuthorRefs,
							},
						},
					}
				}
			}
		}
		books = append(books, entry)
	}
	out["books"] = books
	return out
}
