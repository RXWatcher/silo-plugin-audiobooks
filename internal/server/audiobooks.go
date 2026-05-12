package server

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/auth"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/backend"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
)

// mountAudiobookRoutes wires the read-side catalog + per-book detail routes.
func (s *Server) mountAudiobookRoutes(r chi.Router) {
	r.Get("/audiobooks", s.handleListAudiobooks)
	r.Get("/audiobooks/search", s.handleSearchAudiobooks)
	r.Get("/audiobooks/{id}", s.handleGetAudiobookDetail)
	r.Get("/browse/authors", s.handleBrowseAuthors)
	r.Get("/browse/series", s.handleBrowseSeries)
	r.Get("/browse/narrators", s.handleBrowseNarrators)
}

func (s *Server) resolveTarget(w http.ResponseWriter, r *http.Request) (string, store.BackendConfig, bool) {
	if s.d.Store == nil {
		writeError(w, http.StatusInternalServerError, "store unavailable")
		return "", store.BackendConfig{}, false
	}
	cfg, err := s.d.Store.GetBackendConfig(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "backend config not initialised")
		return "", store.BackendConfig{}, false
	}
	if cfg.TargetBackendPluginID == "" {
		writeError(w, http.StatusPreconditionFailed, "no backend configured; admin must set one in /admin/settings")
		return "", cfg, false
	}
	return cfg.TargetBackendPluginID, cfg, true
}

func parseListParams(r *http.Request) backend.ListParams {
	p := backend.ListParams{
		Cursor: r.URL.Query().Get("cursor"),
		Sort:   r.URL.Query().Get("sort"),
		Order:  r.URL.Query().Get("order"),
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			p.Limit = n
		}
	}
	return p
}

func (s *Server) handleListAudiobooks(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	target, _, ok := s.resolveTarget(w, r)
	if !ok {
		return
	}
	out, err := s.d.Backend.ListCatalog(r.Context(), id.Token, target, parseListParams(r))
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSearchAudiobooks(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	target, _, ok := s.resolveTarget(w, r)
	if !ok {
		return
	}
	p := parseListParams(r)
	p.Query = r.URL.Query().Get("q")
	out, err := s.d.Backend.ListCatalog(r.Context(), id.Token, target, p)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetAudiobookDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	bookID := chi.URLParam(r, "id")
	target, _, ok := s.resolveTarget(w, r)
	if !ok {
		return
	}
	detail, err := s.d.Backend.GetDetail(r.Context(), id.Token, target, bookID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	// Merge user state (progress, bookmarks, rating).
	resp := map[string]any{"audiobook": detail}
	if p, perr := s.d.Store.GetProgress(r.Context(), id.UserID, bookID); perr == nil {
		resp["progress"] = p
	}
	if bks, berr := s.d.Store.ListBookmarks(r.Context(), id.UserID, bookID); berr == nil {
		resp["bookmarks"] = bks
	}
	if rt, rerr := s.d.Store.GetRating(r.Context(), id.UserID, bookID); rerr == nil {
		resp["rating"] = rt
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleBrowseAuthors(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	target, _, ok := s.resolveTarget(w, r)
	if !ok {
		return
	}
	out, err := s.d.Backend.BrowseAuthors(r.Context(), id.Token, target, parseListParams(r))
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleBrowseSeries(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	target, _, ok := s.resolveTarget(w, r)
	if !ok {
		return
	}
	out, err := s.d.Backend.BrowseSeries(r.Context(), id.Token, target, parseListParams(r))
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleBrowseNarrators(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	target, _, ok := s.resolveTarget(w, r)
	if !ok {
		return
	}
	out, err := s.d.Backend.BrowseNarrators(r.Context(), id.Token, target, parseListParams(r))
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}
