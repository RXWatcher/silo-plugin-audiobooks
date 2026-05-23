package abs

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/bookref"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
)

// ABS-compat Playlists surface. Distinct from Collections — items
// can reference podcast episodes in addition to library items, and
// the API shape matches upstream ABS's /api/playlists* family.
//
// Routes mounted via mountPlaylistsRoutes alongside the collections
// passthrough; both ride the existing dual-mount (/api + /abs/api).

func (h *Handler) mountPlaylistsRoutes(prefix string, r chi.Router) {
	r.Get(prefix+"/playlists", h.handleListPlaylists)
	r.Post(prefix+"/playlists", h.handleCreatePlaylist)
	r.Get(prefix+"/playlists/{id}", h.handleGetPlaylist)
	r.Patch(prefix+"/playlists/{id}", h.handleUpdatePlaylist)
	r.Delete(prefix+"/playlists/{id}", h.handleDeletePlaylist)
	r.Post(prefix+"/playlists/{id}/item", h.handleAddPlaylistItem)
	r.Post(prefix+"/playlists/{id}/batch/add", h.handleBatchAddPlaylistItems)
	r.Post(prefix+"/playlists/{id}/batch/remove", h.handleBatchRemovePlaylistItems)
	r.Delete(prefix+"/playlists/{id}/item/{libraryItemId}", h.handleRemovePlaylistItem)
	r.Delete(prefix+"/playlists/{id}/item/{libraryItemId}/{episodeId}", h.handleRemovePlaylistEpisode)
}

type playlistBody struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	CoverItem   string `json:"cover_item"`
	IsPublic    bool   `json:"is_public"`
}

type playlistItemRef struct {
	LibraryItemID string `json:"libraryItemId"`
	EpisodeID     string `json:"episodeId"`
}

func (h *Handler) handleListPlaylists(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	rows, err := h.store.ListUserPlaylists(r.Context(), a.UserID, a.ProfileID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, p := range rows {
		out = append(out, h.playlistToABSMap(r, a.UserID, p, false))
	}
	writeJSON(w, http.StatusOK, map[string]any{"playlists": out})
}

func (h *Handler) handleGetPlaylist(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	p, err := h.store.GetPlaylist(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "playlist not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if p.UserID != a.UserID && !p.IsPublic {
		http.Error(w, "not visible", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, h.playlistToABSMap(r, a.UserID, p, true))
}

func (h *Handler) handleCreatePlaylist(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	var body playlistBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	p := store.Playlist{
		ID:          ulid.Make().String(),
		UserID:      a.UserID,
		Name:        body.Name,
		Description: body.Description,
		CoverItem:   body.CoverItem,
		IsPublic:    body.IsPublic,
	}
	if err := h.store.CreatePlaylist(r.Context(), p); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	persisted, _ := h.store.GetPlaylist(r.Context(), p.ID)
	h.publish(a.UserID, "playlist_added", map[string]any{"id": p.ID})
	writeJSON(w, http.StatusCreated, h.playlistToABSMap(r, a.UserID, persisted, true))
}

func (h *Handler) handleUpdatePlaylist(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	p, err := h.store.GetPlaylist(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "playlist not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if p.UserID != a.UserID {
		http.Error(w, "not owned", http.StatusForbidden)
		return
	}
	var body playlistBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Name != "" {
		p.Name = body.Name
	}
	p.Description = body.Description
	p.CoverItem = body.CoverItem
	p.IsPublic = body.IsPublic
	if err := h.store.UpdatePlaylist(r.Context(), p, a.UserID, a.ProfileID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.publish(a.UserID, "playlist_updated", map[string]any{"id": p.ID})
	writeJSON(w, http.StatusOK, h.playlistToABSMap(r, a.UserID, p, true))
}

func (h *Handler) handleDeletePlaylist(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	id := chi.URLParam(r, "id")
	if err := h.store.DeletePlaylist(r.Context(), id, a.UserID, a.ProfileID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.publish(a.UserID, "playlist_removed", map[string]any{"id": id})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleAddPlaylistItem(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	plID := chi.URLParam(r, "id")
	var body playlistItemRef
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.LibraryItemID == "" {
		http.Error(w, "libraryItemId required", http.StatusBadRequest)
		return
	}
	if err := h.store.AddPlaylistItem(r.Context(), plID, body.LibraryItemID, body.EpisodeID, a.UserID, a.ProfileID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	p, _ := h.store.GetPlaylist(r.Context(), plID)
	h.publish(a.UserID, "playlist_updated", map[string]any{"id": plID})
	writeJSON(w, http.StatusOK, h.playlistToABSMap(r, a.UserID, p, true))
}

func (h *Handler) handleBatchAddPlaylistItems(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	plID := chi.URLParam(r, "id")
	var body struct {
		Items []playlistItemRef `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	for _, it := range body.Items {
		if it.LibraryItemID == "" {
			continue
		}
		_ = h.store.AddPlaylistItem(r.Context(), plID, it.LibraryItemID, it.EpisodeID, a.UserID, a.ProfileID)
	}
	p, _ := h.store.GetPlaylist(r.Context(), plID)
	h.publish(a.UserID, "playlist_updated", map[string]any{"id": plID})
	writeJSON(w, http.StatusOK, h.playlistToABSMap(r, a.UserID, p, true))
}

func (h *Handler) handleBatchRemovePlaylistItems(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	plID := chi.URLParam(r, "id")
	var body struct {
		Items []playlistItemRef `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	for _, it := range body.Items {
		_ = h.store.RemovePlaylistItem(r.Context(), plID, it.LibraryItemID, it.EpisodeID, a.UserID, a.ProfileID)
	}
	p, _ := h.store.GetPlaylist(r.Context(), plID)
	h.publish(a.UserID, "playlist_updated", map[string]any{"id": plID})
	writeJSON(w, http.StatusOK, h.playlistToABSMap(r, a.UserID, p, true))
}

func (h *Handler) handleRemovePlaylistItem(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	plID := chi.URLParam(r, "id")
	libItem := chi.URLParam(r, "libraryItemId")
	if err := h.store.RemovePlaylistItem(r.Context(), plID, libItem, "", a.UserID, a.ProfileID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	p, _ := h.store.GetPlaylist(r.Context(), plID)
	h.publish(a.UserID, "playlist_updated", map[string]any{"id": plID})
	writeJSON(w, http.StatusOK, h.playlistToABSMap(r, a.UserID, p, true))
}

func (h *Handler) handleRemovePlaylistEpisode(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	plID := chi.URLParam(r, "id")
	libItem := chi.URLParam(r, "libraryItemId")
	episode := chi.URLParam(r, "episodeId")
	if err := h.store.RemovePlaylistItem(r.Context(), plID, libItem, episode, a.UserID, a.ProfileID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	p, _ := h.store.GetPlaylist(r.Context(), plID)
	h.publish(a.UserID, "playlist_updated", map[string]any{"id": plID})
	writeJSON(w, http.StatusOK, h.playlistToABSMap(r, a.UserID, p, true))
}

// playlistToABSMap renders one playlist in the upstream ABS shape.
// items: include the books[] array — list view sends false to keep
// response sizes down.
func (h *Handler) playlistToABSMap(r *http.Request, viewerID string, p store.Playlist, items bool) map[string]any {
	a, _ := absAuthFrom(r)
	out := map[string]any{
		"id":          p.ID,
		"userId":      p.UserID,
		"name":        p.Name,
		"description": p.Description,
		"isPublic":    p.IsPublic,
		"createdAt":   p.CreatedAt.UnixMilli(),
		"lastUpdate":  p.UpdatedAt.UnixMilli(),
	}
	if p.CoverItem != "" {
		out["coverPath"] = p.CoverItem
	}
	if !items {
		return out
	}
	rows, err := h.store.ListPlaylistItems(r.Context(), p.ID, viewerID, a.ProfileID)
	if err != nil {
		out["items"] = []any{}
		return out
	}
	entries := make([]map[string]any, 0, len(rows))
	for _, it := range rows {
		entry := map[string]any{
			"libraryItemId": it.LibraryItemID,
			"position":      it.Position,
		}
		if it.EpisodeID != "" {
			entry["episodeId"] = it.EpisodeID
		}
		// Best-effort hydrate the library item metadata so the ABS
		// client can render title + cover without a follow-up fetch.
		libID, backendBookID, _ := bookref.Decode(it.LibraryItemID)
		if libID != 0 && backendBookID != "" {
			if lib, err := h.store.GetPortalLibrary(r.Context(), libID); err == nil && lib.BackendPluginID != "" {
				if d, derr := h.backend.GetDetail(r.Context(), "", lib.BackendPluginID, backendBookID); derr == nil {
					entry["libraryId"] = absLibraryID(lib)
					entry["title"] = d.Title
				}
			}
		}
		entries = append(entries, entry)
	}
	out["items"] = entries
	return out
}
