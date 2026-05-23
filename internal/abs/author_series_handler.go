package abs

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/backend"
	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/store"
)

// handleAuthorDetail serves /api/authors/{id} (and /api/authors/{id}?include=items,series).
// Audiobookshelf-app fetches this when the user taps an author chip on the
// item-detail page. We synthesize from BrowseAuthors + a catalog filter
// because the audiobook_backend.v1 contract has no per-author detail
// endpoint — see backend/client.go.
//
// Shape mirrors what AuthorPage.vue reads:
//   - id, asin, name, description, imagePath, addedAt, updatedAt, numBooks
//   - libraryItems[] (when ?include=items)
//   - series[] each with {id, name, items[]} (when ?include=series)
//
// We fan out across all enabled portal libraries because the author id
// surfaces from any of them. First-match wins for description fields
// (which we don't carry today — they're "").
func (h *Handler) handleAuthorDetail(w http.ResponseWriter, r *http.Request) {
	authorID := chi.URLParam(r, "id")
	if authorID == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	include := r.URL.Query().Get("include")
	wantItems := includeHas(include, "items")
	wantSeries := includeHas(include, "series")

	var (
		matched   backend.AuthorSummary
		matchLib  store.PortalLibrary
		foundAuthor bool
	)
	for _, lib := range h.portalLibraries(r.Context(), true) {
		if lib.BackendPluginID == "" {
			continue
		}
		authors, err := h.backend.BrowseAuthors(r.Context(), "", lib.BackendPluginID,
			backend.ListParams{Limit: 5000, LibraryID: backendLibraryID(lib)})
		if err != nil {
			h.logger.Warn("author detail: browse authors", "err", err.Error())
			continue
		}
		for _, a := range authors.Items {
			if a.ID == authorID {
				matched = a
				matchLib = lib
				foundAuthor = true
				break
			}
		}
		if foundAuthor {
			break
		}
	}
	if !foundAuthor {
		http.Error(w, "author not found", http.StatusNotFound)
		return
	}

	out := map[string]any{
		"id":          matched.ID,
		"asin":        nil,
		"name":        matched.Name,
		"description": nil,
		"imagePath":   nil,
		"addedAt":     0,
		"updatedAt":   0,
		"numBooks":    matched.Count,
		"libraryId":   absLibraryID(matchLib),
	}

	// libraryItems[] and series[] both need the catalog filtered by
	// author. ListCatalog supports filter=authors (per backend
	// contract); single call covers both since each item carries
	// SeriesRefs we can dedupe out.
	var libraryItems []LibraryItem
	if wantItems || wantSeries {
		page, err := h.backend.ListCatalog(r.Context(), "", matchLib.BackendPluginID,
			backend.ListParams{
				Filter: "authors", FilterValue: matched.Name,
				LibraryID: backendLibraryID(matchLib), Limit: 500,
			})
		if err != nil {
			h.logger.Warn("author detail: filter by author", "err", err.Error())
		} else {
			for _, s := range page.Items {
				summary := withPortalLibrarySummary(s, matchLib)
				libraryItems = append(libraryItems, ToLibrarySummary(summary))
			}
		}
	}
	if wantItems {
		out["libraryItems"] = libraryItems
	}
	if wantSeries {
		seriesByID := map[string]map[string]any{}
		for i, s := range libraryItems {
			_ = i
			for _, sr := range s.Media.Metadata.Series {
				if sr.ID == "" {
					continue
				}
				existing, ok := seriesByID[sr.ID]
				if !ok {
					existing = map[string]any{
						"id":    sr.ID,
						"name":  sr.Name,
						"items": []LibraryItem{},
					}
					seriesByID[sr.ID] = existing
				}
				items := existing["items"].([]LibraryItem)
				existing["items"] = append(items, s)
			}
		}
		seriesList := make([]map[string]any, 0, len(seriesByID))
		for _, v := range seriesByID {
			seriesList = append(seriesList, v)
		}
		out["series"] = seriesList
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSeriesDetail serves /api/series/{id}. Mobile renders the series
// detail screen from {id, name, description, addedAt, updatedAt,
// libraryId, numBooks, totalDuration, books[]}. Synthesized from
// BrowseSeries (to resolve name from id) + catalog filter=series.
func (h *Handler) handleSeriesDetail(w http.ResponseWriter, r *http.Request) {
	seriesID := chi.URLParam(r, "id")
	if seriesID == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}

	var (
		matched   backend.SeriesSummary
		matchLib  store.PortalLibrary
		found     bool
	)
	for _, lib := range h.portalLibraries(r.Context(), true) {
		if lib.BackendPluginID == "" {
			continue
		}
		series, err := h.backend.BrowseSeries(r.Context(), "", lib.BackendPluginID,
			backend.ListParams{Limit: 5000, LibraryID: backendLibraryID(lib)})
		if err != nil {
			h.logger.Warn("series detail: browse series", "err", err.Error())
			continue
		}
		for _, s := range series.Items {
			if s.ID == seriesID {
				matched = s
				matchLib = lib
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		http.Error(w, "series not found", http.StatusNotFound)
		return
	}

	books := []LibraryItem{}
	totalDuration := 0.0
	page, err := h.backend.ListCatalog(r.Context(), "", matchLib.BackendPluginID,
		backend.ListParams{
			Filter: "series", FilterValue: matched.Name,
			LibraryID: backendLibraryID(matchLib), Limit: 500,
		})
	if err != nil {
		h.logger.Warn("series detail: filter by series", "err", err.Error())
	} else {
		for _, s := range page.Items {
			summary := withPortalLibrarySummary(s, matchLib)
			it := ToLibrarySummary(summary)
			books = append(books, it)
			totalDuration += it.Media.Duration
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":              matched.ID,
		"name":            matched.Name,
		"nameIgnorePrefix": strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(matched.Name, "The "), "A "), "An "),
		"description":     nil,
		"addedAt":         0,
		"updatedAt":       0,
		"libraryId":       absLibraryID(matchLib),
		"numBooks":        matched.Count,
		"totalDuration":   totalDuration,
		"books":           books,
	})
}

// handleAuthorImage serves /api/authors/{id}/image. Audiobookshelf-app
// builds <serverAddress>/api/authors/{id}/image at AuthorImage.vue:65-67
// and falls back to a placeholder on any non-2xx. The audiobook_backend.v1
// contract doesn't expose author images, so we return a clean 404 from
// outside bearerAuth — keeping the mobile fallback fast (no 401 round
// trip the way the cover route used to behave).
//
// If a future backend version adds GET /api/v1/author/{id}/image we
// proxy it here, same shape as handleItemCover.
func (h *Handler) handleAuthorImage(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "author image not available", http.StatusNotFound)
}

