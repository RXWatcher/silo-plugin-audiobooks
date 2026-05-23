package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/auth"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
)

// Custom metadata provider admin surface + proxied search. The
// provider contract follows /opt/audiobookshelf/custom-metadata-
// provider-specification.yaml — GET /search?query=&author=&
// AUTHORIZATION: <header> → {matches: [BookMetadata, ...]}.

func (s *Server) mountCustomMetadataProviderRoutes(r chi.Router) {
	r.Get("/admin/custom-metadata-providers", s.handleListProviders)
	r.Post("/admin/custom-metadata-providers", s.handleCreateProvider)
	r.Patch("/admin/custom-metadata-providers/{id}", s.handleUpdateProvider)
	r.Delete("/admin/custom-metadata-providers/{id}", s.handleDeleteProvider)
	// Public-ish: any authenticated user can search via a configured
	// provider, but the auth_header never leaves the server.
	r.Get("/search/providers", s.handleListProvidersPublic)
	r.Get("/search/providers/{id}", s.handleProviderSearch)
}

type providerBody struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	AuthHeader string `json:"auth_header"`
	Enabled    bool   `json:"enabled"`
}

func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	rows, err := s.d.Store.ListCustomMetadataProviders(r.Context(), false)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
}

func (s *Server) handleCreateProvider(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	var body providerBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Name == "" || body.URL == "" {
		writeError(w, http.StatusBadRequest, "name and url required")
		return
	}
	p := store.CustomMetadataProvider{
		ID:         ulid.Make().String(),
		Name:       body.Name,
		URL:        body.URL,
		AuthHeader: body.AuthHeader,
		Enabled:    body.Enabled,
	}
	if err := s.d.Store.UpsertCustomMetadataProvider(r.Context(), p); err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) handleUpdateProvider(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	existing, err := s.d.Store.GetCustomMetadataProvider(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	var body providerBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Name != "" {
		existing.Name = body.Name
	}
	if body.URL != "" {
		existing.URL = body.URL
	}
	existing.AuthHeader = body.AuthHeader
	existing.Enabled = body.Enabled
	if err := s.d.Store.UpsertCustomMetadataProvider(r.Context(), existing); err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, existing)
}

func (s *Server) handleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	if err := s.d.Store.DeleteCustomMetadataProvider(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeInternal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListProvidersPublic returns just the enabled providers'
// {id, name} so clients can render the picker. auth_header is
// never returned on this route — it'd leak the upstream secret.
func (s *Server) handleListProvidersPublic(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireUser(w, r); !ok {
		return
	}
	rows, err := s.d.Store.ListCustomMetadataProviders(r.Context(), true)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, p := range rows {
		out = append(out, map[string]any{"id": p.ID, "name": p.Name})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// handleProviderSearch proxies the user's ?query=&author= search to
// the configured provider. Response body passes through verbatim
// so callers see whatever the provider returns (per spec, a
// {matches: BookMetadata[]} object).
func (s *Server) handleProviderSearch(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireUser(w, r); !ok {
		return
	}
	id := chi.URLParam(r, "id")
	p, err := s.d.Store.GetCustomMetadataProvider(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) || !p.Enabled {
		writeError(w, http.StatusNotFound, "provider not available")
		return
	}
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("query"))
	if query == "" {
		writeError(w, http.StatusBadRequest, "query required")
		return
	}

	target, err := url.Parse(p.URL)
	if err != nil {
		writeError(w, http.StatusBadGateway, "invalid provider url: "+err.Error())
		return
	}
	// Append /search if the provider URL is the bare base; tolerate
	// admins who include it already.
	if !strings.HasSuffix(target.Path, "/search") {
		target.Path = strings.TrimRight(target.Path, "/") + "/search"
	}
	q := target.Query()
	q.Set("query", query)
	if author := r.URL.Query().Get("author"); author != "" {
		q.Set("author", author)
	}
	target.RawQuery = q.Encode()

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	if p.AuthHeader != "" {
		// Per the spec the header is "AUTHORIZATION" — case-insensitive
		// in HTTP so either form works. We set the standard
		// Authorization for compatibility with other providers.
		req.Header.Set("Authorization", p.AuthHeader)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "provider request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 10<<20))
}

