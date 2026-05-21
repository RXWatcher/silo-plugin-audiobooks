package abs

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/bookref"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
)

// handleItemsInProgress — GET /abs/api/me/items-in-progress
// Returns {libraryItems: [...]} where each entry is a LibraryItem
// summary with the user's progress merged in. The mobile app's home
// tab "Continue Listening" reads from this endpoint.
//
// Filters: non-finished, progress > 0, not hidden-from-continue
// (the user-facing "Remove from Continue Listening" toggle below).
// Limit is capped at 25 — the shelf only renders ~10 entries on
// mobile so over-fetching wastes the backend GetDetail loop below.
func (h *Handler) handleItemsInProgress(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	rows, err := h.store.ListInProgress(r.Context(), a.UserID, 25)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	items := make([]any, 0, len(rows))
	for _, p := range rows {
		libID, backendBookID, encoded := bookref.Decode(p.BookID)
		// Skip rows from libraries that no longer exist or whose backend
		// id is empty (orphaned progress survives a library removal).
		if !encoded || libID == 0 {
			continue
		}
		lib, err := h.store.GetPortalLibrary(r.Context(), libID)
		if err != nil {
			continue
		}
		if item := h.hydrateInProgressItem(r.Context(), a.Token, lib, backendBookID, p); item != nil {
			items = append(items, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"libraryItems": items})
}

// hydrateInProgressItem builds a LibraryItem-shaped row with embedded
// progress for one in-progress book. Returns nil when the backend is
// unreachable or the book has been deleted upstream — the caller skips
// nil entries so the response stays consistent.
func (h *Handler) hydrateInProgressItem(ctx context.Context, bearer string, lib store.PortalLibrary, backendBookID string, p store.Progress) map[string]any {
	if lib.BackendPluginID == "" {
		return nil
	}
	detail, err := h.backend.GetDetail(ctx, bearer, lib.BackendPluginID, backendBookID)
	if err != nil {
		h.logger.Debug("items-in-progress: backend detail failed",
			"book_id", backendBookID, "err", err.Error())
		return nil
	}
	detail = withPortalLibraryDetail(detail, lib)
	item := ToLibraryItem(detail, func(int) string { return "" })
	item.ID = bookref.Encode(lib.ID, backendBookID)
	// Embed the user's progress so the home tab can render the
	// progress bar without a second GET /me/progress per row. Real
	// ABS adds it under "userMediaProgress" on the LibraryItem; we
	// match that key exactly.
	return map[string]any{
		"id":        item.ID,
		"libraryId": item.LibraryID,
		"folderId":  item.FolderID,
		"mediaType": item.MediaType,
		"media":     item.Media,
		"numTracks": item.NumTracks,
		"addedAt":   item.AddedAt,
		"updatedAt": item.UpdatedAt,
		"userMediaProgress": map[string]any{
			"id":            p.UserID + "-" + p.BookID,
			"libraryItemId": item.ID,
			"currentTime":   p.CurrentSeconds,
			"progress":      p.ProgressPct,
			"isFinished":    p.IsFinished,
			"lastUpdate":    p.UpdatedAt.UnixMilli(),
		},
	}
}

// handleHideFromContinue — GET /abs/api/me/progress/{itemId}/remove-from-continue-listening
// Real ABS returns 200 even when nothing matched, so we mirror that.
func (h *Handler) handleHideFromContinue(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	itemID := chi.URLParam(r, "itemId")
	if err := h.store.HideProgressFromContinue(r.Context(), a.UserID, itemID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.publish(a.UserID, "user_updated", map[string]any{
		"reason": "progress_hidden_from_continue",
		"itemId": itemID,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleUnhideFromContinue — GET /abs/api/me/progress/{itemId}/readd-to-continue-listening
func (h *Handler) handleUnhideFromContinue(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	itemID := chi.URLParam(r, "itemId")
	if err := h.store.UnhideProgressFromContinue(r.Context(), a.UserID, itemID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.publish(a.UserID, "user_updated", map[string]any{
		"reason": "progress_readded_to_continue",
		"itemId": itemID,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDeleteProgress — DELETE /abs/api/me/progress/{itemId}
// Removes the progress row entirely. The user's next playback starts
// from the beginning of the book.
func (h *Handler) handleDeleteProgress(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	itemID := chi.URLParam(r, "itemId")
	// Confirm the progress exists so 404-vs-200 semantics match ABS:
	// real ABS 404s when the row was already gone. Our store.Delete is
	// idempotent (no error), so we explicitly check first.
	if _, err := h.store.GetProgress(r.Context(), a.UserID, itemID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "progress not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.store.DeleteProgress(r.Context(), a.UserID, itemID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.publish(a.UserID, "user_updated", map[string]any{
		"reason": "progress_deleted",
		"itemId": itemID,
	})
	w.WriteHeader(http.StatusNoContent)
}
