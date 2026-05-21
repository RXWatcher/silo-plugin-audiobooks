package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/auth"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/backend"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/bookref"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/mediatoken"
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

// signStreamURL produces the per-file stream URL embedded in the detail
// response. The SPA puts this directly in <audio src> without needing any
// further portal calls — the signed token rides in the query so the host
// plugin proxy can route to the public backend byte route.
func signStreamURL(installID, userID, backendBookID string, fileIdx int, secret string) string {
	if installID == "" || backendBookID == "" {
		return ""
	}
	base := "/api/v1/plugins/" + url.PathEscape(installID) +
		"/api/v1/stream/" + url.PathEscape(backendBookID) +
		"/" + strconv.Itoa(fileIdx)
	if secret == "" || userID == "" {
		return base
	}
	token, err := mediatoken.Mint(secret, userID, backendBookID, fileIdx)
	if err != nil {
		slog.Warn("mint stream token failed", "book_id", backendBookID, "file_idx", fileIdx, "err", err)
		return base
	}
	return base + "?token=" + url.QueryEscape(token)
}

// mediaSigningSecret reads the portal's media signing secret from the
// backend_config row. Returns the empty string on any error — callers then
// emit URLs without tokens, which the backend rejects with a clear 401.
func (s *Server) mediaSigningSecret(r *http.Request) string {
	if s.d.Store == nil {
		return ""
	}
	cfg, err := s.d.Store.GetBackendConfig(r.Context())
	if err != nil {
		return ""
	}
	return cfg.MediaSigningSecret
}

func wrapCatalogItems(env backend.PageEnvelope[backend.AudiobookSummary], lib store.PortalLibrary, userID, secret string) backend.PageEnvelope[backend.AudiobookSummary] {
	for i := range env.Items {
		backendBookID := env.Items[i].ID
		env.Items[i].CoverURL = rewriteCoverURL(env.Items[i].CoverURL, lib.BackendPluginID, userID, backendBookID, secret)
		env.Items[i].CoverPath = env.Items[i].CoverURL
		env.Items[i].ID = bookref.Encode(lib.ID, env.Items[i].ID)
		env.Items[i].LibraryID = lib.ID
		env.Items[i].LibraryName = lib.Name
		env.Items[i].MediaType = lib.MediaType
	}
	return env
}

// rewriteCoverURL turns a backend-relative cover URL (e.g. "/cover/{id}/large"
// or "/api/v1/cover/{id}/large") into a host plugin proxy URL the browser
// can load directly from the SPA, with a signed media token appended as a
// ?token= query parameter so the backend can authenticate the request —
// browsers can't send Authorization headers on <img>-tag requests, so the
// token rides along in the URL. The token is bound to (user, book, exp,
// file_idx=-1) so a leaked URL can't be reused for other resources or
// replayed beyond the TTL.
//
// Returns absolute URLs unchanged. When the signing secret isn't configured
// the URL is rewritten without a token; the backend will return 503 on the
// missing-secret path so the operator sees a clear error.
func rewriteCoverURL(raw, installID, userID, backendBookID, secret string) string {
	if raw == "" || installID == "" {
		return raw
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	out := raw
	if !strings.HasPrefix(out, "/api/v1/plugins/") {
		if !strings.HasPrefix(out, "/api/v1/") {
			if !strings.HasPrefix(out, "/") {
				out = "/" + out
			}
			out = "/api/v1" + out
		}
		out = "/api/v1/plugins/" + url.PathEscape(installID) + out
	}
	if secret == "" || userID == "" || backendBookID == "" {
		return out
	}
	token, err := mediatoken.Mint(secret, userID, backendBookID, mediatoken.CoverFileIdx)
	if err != nil {
		slog.Warn("mint cover token failed", "book_id", backendBookID, "err", err)
		return out
	}
	sep := "?"
	if strings.Contains(out, "?") {
		sep = "&"
	}
	return out + sep + "token=" + url.QueryEscape(token)
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
	writeJSON(w, http.StatusOK, wrapCatalogItems(out, lib, id.UserID, s.mediaSigningSecret(r)))
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
	writeJSON(w, http.StatusOK, wrapCatalogItems(out, lib, id.UserID, s.mediaSigningSecret(r)))
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
	secret := s.mediaSigningSecret(r)
	detail.CoverURL = rewriteCoverURL(detail.CoverURL, lib.BackendPluginID, id.UserID, bookID, secret)
	detail.CoverPath = detail.CoverURL
	for i := range detail.Files {
		detail.Files[i].StreamURL = signStreamURL(lib.BackendPluginID, id.UserID, bookID, detail.Files[i].Index, secret)
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
