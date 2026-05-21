package server

import (
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/auth"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/enrich"
)

// Metadata enrichment surface — calls into OpenLibrary + Google
// Books in parallel and merges the candidate lists. Admin uses it
// to fill in missing fields on a book before importing.
//
// Both providers are unauthenticated; rate limits are generous for
// the few-requests-per-import volume we'll generate. Failures are
// logged + swallowed per-provider so a Google outage doesn't make
// the whole endpoint fail when OpenLibrary still works.

func (s *Server) mountEnrichRoutes(r chi.Router) {
	r.Get("/admin/enrich/search", s.handleEnrichSearch)
}

// handleEnrichSearch — GET /api/v1/admin/enrich/search?q=&limit=N
// Returns merged candidate list from both providers:
//
//	{"matches": [{provider, provider_id, title, ...}, ...]}
//
// OpenLibrary's results come first (typically better for older /
// niche titles); Google Books follows (better for recent + audio-
// book editions). Caller dedupes by ISBN if it cares; the SPA
// usually wants both lists displayed side-by-side.
func (s *Server) handleEnrichSearch(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeError(w, http.StatusBadRequest, "q required")
		return
	}
	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 25 {
			limit = n
		}
	}

	// Parallel fan-out — both providers have ~200ms typical
	// latency; in serial we'd add them. Errors per provider are
	// captured but don't abort the response.
	var (
		wg          sync.WaitGroup
		mu          sync.Mutex
		matches     []enrich.Match
		olErr, gbErr string
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		results, err := enrich.SearchOpenLibrary(r.Context(), query, limit)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			olErr = err.Error()
		} else {
			matches = append(matches, results...)
		}
	}()
	go func() {
		defer wg.Done()
		results, err := enrich.SearchGoogleBooks(r.Context(), query, limit)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			gbErr = err.Error()
		} else {
			matches = append(matches, results...)
		}
	}()
	wg.Wait()

	resp := map[string]any{"matches": matches}
	if olErr != "" {
		resp["openlibrary_error"] = olErr
	}
	if gbErr != "" {
		resp["google_books_error"] = gbErr
	}
	writeJSON(w, http.StatusOK, resp)
}
