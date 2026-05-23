package abs

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/bookref"
)

// handleSimilarItems — GET /api/items/{id}/similar?limit=N
// Returns a paged list of similar audiobooks based on the source
// book's embedding. The shape mirrors handleLibraryItems: a paged
// envelope with results[], total, limit, page, plus the sortBy =
// "relevance" hint that real ABS uses for embedding-driven shelves.
//
// Behaviour when the recommender isn't configured (no
// EMBEDDING_BASE_URL): returns 200 with an empty results list and
// total=0 rather than 404. The mobile client renders the "similar"
// shelf as empty rather than showing an error.
func (h *Handler) handleSimilarItems(w http.ResponseWriter, r *http.Request) {
	encoded := chi.URLParam(r, "id")
	libID, backendBookID, _ := bookref.Decode(encoded)
	if libID == 0 || backendBookID == "" {
		http.Error(w, "invalid item id", http.StatusBadRequest)
		return
	}
	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 50 {
			limit = n
		}
	}

	empty := pagedEnvelope([]any{}, 0, limit, 0, "relevance", true, "", false, "")
	if h.recommender == nil {
		writeJSON(w, http.StatusOK, empty)
		return
	}

	candidates, err := h.recommender.Similar(r.Context(), libID, backendBookID, limit)
	if err != nil {
		h.logger.Warn("similar: engine failed",
			"book_id", backendBookID, "library_id", libID, "err", err.Error())
		writeJSON(w, http.StatusOK, empty)
		return
	}
	if len(candidates) == 0 {
		writeJSON(w, http.StatusOK, empty)
		return
	}

	// Resolve each candidate to a LibraryItem summary. Group by
	// library to amortise GetDetail latency across the page.
	lib, err := h.store.GetPortalLibrary(r.Context(), libID)
	if err != nil || lib.BackendPluginID == "" {
		writeJSON(w, http.StatusOK, empty)
		return
	}
	items := make([]any, 0, len(candidates))
	for _, c := range candidates {
		// Same-library only for now — cross-library similarity
		// requires the SPA to surface library labels which it doesn't
		// yet handle.
		if c.LibraryID != libID {
			continue
		}
		summary, err := h.backend.GetDetail(r.Context(), "", lib.BackendPluginID, c.BookID)
		if err != nil {
			continue
		}
		items = append(items, ToLibrarySummary(withPortalLibrarySummary(summary.AudiobookSummary, lib)))
	}

	writeJSON(w, http.StatusOK, pagedEnvelope(items, len(items), limit, 0, "relevance", true, "", false, ""))
}
