package abs

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/store"
)

// Bookmark CRUD on the ABS surface.
//
// Real ABS keys bookmarks by (user, item, time-in-seconds) rather than a
// separate id — the client never sees a bookmark id, only the (item,
// time) pair. POST creates, PATCH updates the title at a given time,
// DELETE removes the bookmark at a given time. All three return the
// updated bookmark list for the item (or an empty array) so the mobile
// client can refresh its view without a follow-up GET.
//
// Real ABS emits `user_updated` over Socket.io after every change so
// other devices on the same account refresh their bookmark UI. We do
// the same.

type bookmarkBody struct {
	Title string  `json:"title"`
	Time  float64 `json:"time"`
}

// handleCreateBookmark — POST /abs/api/me/item/{itemId}/bookmark
func (h *Handler) handleCreateBookmark(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	itemID := chi.URLParam(r, "itemId")
	var body bookmarkBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	bm := store.Bookmark{
		ID:              ulid.Make().String(),
		UserID:          a.UserID,
		ProfileID:       a.ProfileID,
		BookID:          itemID,
		PositionSeconds: int(body.Time),
		Note:            body.Title,
	}
	if err := h.store.UpsertBookmarkAt(r.Context(), bm); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.publish(a.UserID, "user_updated", map[string]any{
		"reason": "bookmark_created",
		"itemId": itemID,
	})
	h.writeBookmarkList(w, r, a.UserID, itemID)
}

// handleUpdateBookmark — PATCH /abs/api/me/item/{itemId}/bookmark
// Body carries the (title, time) pair; time identifies the bookmark.
func (h *Handler) handleUpdateBookmark(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	itemID := chi.URLParam(r, "itemId")
	var body bookmarkBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	bm := store.Bookmark{
		ID:              ulid.Make().String(), // only used when row doesn't exist yet
		UserID:          a.UserID,
		ProfileID:       a.ProfileID,
		BookID:          itemID,
		PositionSeconds: int(body.Time),
		Note:            body.Title,
	}
	if err := h.store.UpsertBookmarkAt(r.Context(), bm); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.publish(a.UserID, "user_updated", map[string]any{
		"reason": "bookmark_updated",
		"itemId": itemID,
	})
	h.writeBookmarkList(w, r, a.UserID, itemID)
}

// handleDeleteBookmark — DELETE /abs/api/me/item/{itemId}/bookmark/{time}
// time is the bookmark's position-in-seconds as a stringified integer.
func (h *Handler) handleDeleteBookmark(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	itemID := chi.URLParam(r, "itemId")
	timeStr := chi.URLParam(r, "time")
	t, err := strconv.Atoi(timeStr)
	if err != nil || t < 0 {
		http.Error(w, "time must be a positive integer", http.StatusBadRequest)
		return
	}
	if err := h.store.DeleteBookmarkAt(r.Context(), a.UserID, a.ProfileID, itemID, t); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.publish(a.UserID, "user_updated", map[string]any{
		"reason": "bookmark_deleted",
		"itemId": itemID,
		"time":   t,
	})
	h.writeBookmarkList(w, r, a.UserID, itemID)
}

// writeBookmarkList emits the ABS-shaped bookmark array for one item.
// Each entry: {libraryItemId, title, time, createdAt} — matches the
// shape the mobile BookmarksModal expects to render.
func (h *Handler) writeBookmarkList(w http.ResponseWriter, r *http.Request, userID, itemID string) {
	a, _ := absAuthFrom(r)
	rows, err := h.store.ListBookmarks(r.Context(), userID, a.ProfileID, itemID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, b := range rows {
		out = append(out, map[string]any{
			"libraryItemId": itemID,
			"title":         b.Note,
			"time":          b.PositionSeconds,
			"createdAt":     b.CreatedAt.UnixMilli(),
		})
	}
	writeJSON(w, http.StatusOK, out)
}
