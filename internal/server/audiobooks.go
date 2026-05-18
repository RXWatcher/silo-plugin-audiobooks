package server

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/auth"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/backend"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/bookref"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
)

// mountAudiobookRoutes wires the read-side catalog + per-book detail routes.
func (s *Server) mountAudiobookRoutes(r chi.Router) {
	r.Get("/audiobooks", s.handleListAudiobooks)
	r.Get("/audiobooks/search", s.handleSearchAudiobooks)
	r.Get("/audiobooks/{id}", s.handleGetAudiobookDetail)
	r.Get("/libraries", s.handleListLibraries)
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
	installID := cfg.BackendInstallID()
	if installID == "" {
		writeError(w, http.StatusPreconditionFailed, "no backend configured; admin must set one in /admin/settings")
		return "", cfg, false
	}
	return installID, cfg, true
}

func (s *Server) targetLibrary(r *http.Request, libraryID int64) (store.PortalLibrary, error) {
	if libraryID > 0 {
		return s.d.Store.GetPortalLibrary(r.Context(), libraryID)
	}
	lib, err := s.d.Store.DefaultPortalLibrary(r.Context())
	if err == nil {
		return lib, nil
	}
	cfg, err := s.d.Store.GetBackendConfig(r.Context())
	if err != nil || cfg.BackendInstallID() == "" {
		return store.PortalLibrary{}, fmt.Errorf("no backend configured")
	}
	return store.PortalLibrary{
		Name:            "Audiobooks",
		MediaType:       "audiobook",
		BackendPluginID: cfg.BackendInstallID(),
		Enabled:         true,
	}, nil
}

func queryLibraryID(r *http.Request) int64 {
	raw := r.URL.Query().Get("library_id")
	if raw == "" {
		return 0
	}
	n, _ := strconv.ParseInt(raw, 10, 64)
	return n
}

func backendLibraryID(lib store.PortalLibrary) int64 {
	if lib.BackendLibraryID == nil {
		return 0
	}
	return *lib.BackendLibraryID
}

func wrapCatalogItems(env backend.PageEnvelope[backend.AudiobookSummary], lib store.PortalLibrary) backend.PageEnvelope[backend.AudiobookSummary] {
	for i := range env.Items {
		env.Items[i].ID = bookref.Encode(lib.ID, env.Items[i].ID)
		env.Items[i].LibraryID = lib.ID
		env.Items[i].LibraryName = lib.Name
		env.Items[i].MediaType = lib.MediaType
	}
	return env
}

func emptyPageEnvelope[T any]() backend.PageEnvelope[T] {
	return backend.PageEnvelope[T]{Items: []T{}}
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
	lib, err := s.targetLibrary(r, queryLibraryID(r))
	if err != nil {
		writeJSON(w, http.StatusOK, emptyPageEnvelope[backend.AudiobookSummary]())
		return
	}
	params := parseListParams(r)
	params.LibraryID = backendLibraryID(lib)
	out, err := s.d.Backend.ListCatalog(r.Context(), id.Token, lib.BackendPluginID, params)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, wrapCatalogItems(out, lib))
}

func (s *Server) handleSearchAudiobooks(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	lib, err := s.targetLibrary(r, queryLibraryID(r))
	if err != nil {
		writeJSON(w, http.StatusOK, emptyPageEnvelope[backend.AudiobookSummary]())
		return
	}
	p := parseListParams(r)
	p.Query = r.URL.Query().Get("q")
	p.LibraryID = backendLibraryID(lib)
	out, err := s.d.Backend.ListCatalog(r.Context(), id.Token, lib.BackendPluginID, p)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, wrapCatalogItems(out, lib))
}

func (s *Server) handleGetAudiobookDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	bookRef := chi.URLParam(r, "id")
	libraryID, bookID, _ := bookref.Decode(bookRef)
	lib, err := s.targetLibrary(r, libraryID)
	if err != nil {
		writeError(w, http.StatusPreconditionFailed, err.Error())
		return
	}
	detail, err := s.d.Backend.GetDetail(r.Context(), id.Token, lib.BackendPluginID, bookID)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	detail.ID = bookref.Encode(lib.ID, detail.ID)
	detail.LibraryID = lib.ID
	detail.LibraryName = lib.Name
	detail.MediaType = lib.MediaType
	// Merge user state (progress, bookmarks, rating).
	resp := map[string]any{"audiobook": detail}
	if p, perr := s.d.Store.GetProgress(r.Context(), id.UserID, bookRef); perr == nil {
		resp["progress"] = p
	}
	if bks, berr := s.d.Store.ListBookmarks(r.Context(), id.UserID, bookRef); berr == nil {
		resp["bookmarks"] = bks
	}
	if rt, rerr := s.d.Store.GetRating(r.Context(), id.UserID, bookRef); rerr == nil {
		resp["rating"] = rt
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleListLibraries(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireUser(w, r); !ok {
		return
	}
	libs, err := s.d.Store.ListPortalLibraries(r.Context(), true)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	if len(libs) == 0 {
		cfg, err := s.d.Store.GetBackendConfig(r.Context())
		if err == nil && cfg.BackendInstallID() != "" {
			libs = []store.PortalLibrary{{
				Name:            "Audiobooks",
				MediaType:       "audiobook",
				BackendPluginID: cfg.BackendInstallID(),
				Enabled:         true,
			}}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": libs})
}

func (s *Server) handleBrowseAuthors(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	lib, err := s.targetLibrary(r, queryLibraryID(r))
	if err != nil {
		writeJSON(w, http.StatusOK, emptyPageEnvelope[backend.AuthorSummary]())
		return
	}
	params := parseListParams(r)
	params.LibraryID = backendLibraryID(lib)
	out, err := s.d.Backend.BrowseAuthors(r.Context(), id.Token, lib.BackendPluginID, params)
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
	lib, err := s.targetLibrary(r, queryLibraryID(r))
	if err != nil {
		writeJSON(w, http.StatusOK, emptyPageEnvelope[backend.SeriesSummary]())
		return
	}
	params := parseListParams(r)
	params.LibraryID = backendLibraryID(lib)
	out, err := s.d.Backend.BrowseSeries(r.Context(), id.Token, lib.BackendPluginID, params)
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
	lib, err := s.targetLibrary(r, queryLibraryID(r))
	if err != nil {
		writeJSON(w, http.StatusOK, emptyPageEnvelope[backend.NarratorSummary]())
		return
	}
	params := parseListParams(r)
	params.LibraryID = backendLibraryID(lib)
	out, err := s.d.Backend.BrowseNarrators(r.Context(), id.Token, lib.BackendPluginID, params)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}
