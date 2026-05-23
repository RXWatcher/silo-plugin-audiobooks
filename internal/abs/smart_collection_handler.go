package abs

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/backend"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/bookref"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/smartcoll"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
)

// Smart Collection HTTP surface — wires the smartcoll DSL + store into
// the ABS-style /api/me/smart-collections routes the SPA + future
// rule-builder UI consume. Manual collections (the existing
// /api/me/collections surface) are unaffected.

func (h *Handler) mountSmartCollectionRoutes(prefix string, r chi.Router) {
	r.Get(prefix+"/me/smart-collections", h.handleListSmartCollections)
	r.Post(prefix+"/me/smart-collections", h.handleCreateSmartCollection)
	r.Get(prefix+"/me/smart-collections/{id}", h.handleGetSmartCollection)
	r.Get(prefix+"/me/smart-collections/{id}/items", h.handleSmartCollectionItems)
	r.Patch(prefix+"/me/smart-collections/{id}", h.handleUpdateSmartCollection)
	r.Delete(prefix+"/me/smart-collections/{id}", h.handleDeleteSmartCollection)
}

type smartCollectionBody struct {
	Name        string                    `json:"name"`
	Description string                    `json:"description"`
	Color       string                    `json:"color"`
	IsPublic    bool                      `json:"is_public"`
	IsPinned    bool                      `json:"is_pinned"`
	QueryDef    smartcoll.QueryDefinition `json:"query_def"`
}

func (h *Handler) handleListSmartCollections(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	rows, err := h.store.ListSmartCollections(r.Context(), a.UserID, a.ProfileID, 200)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, c := range rows {
		out = append(out, smartCollectionToMap(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Handler) handleGetSmartCollection(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	c, err := h.store.GetSmartCollection(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "smart collection not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if c.UserID != a.UserID && !c.IsPublic {
		http.Error(w, "not visible to this user", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, smartCollectionToMap(c))
}

func (h *Handler) handleCreateSmartCollection(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	c, err := h.persistSmartCollection(w, r, "")
	if err != nil {
		return
	}
	_ = a
	writeJSON(w, http.StatusCreated, smartCollectionToMap(c))
}

func (h *Handler) handleUpdateSmartCollection(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	existing, err := h.store.GetSmartCollection(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "smart collection not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if existing.UserID != a.UserID {
		http.Error(w, "not owned by this user", http.StatusForbidden)
		return
	}
	c, err := h.persistSmartCollection(w, r, existing.ID)
	if err != nil {
		return
	}
	writeJSON(w, http.StatusOK, smartCollectionToMap(c))
}

func (h *Handler) handleDeleteSmartCollection(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	if err := h.store.DeleteSmartCollection(r.Context(), chi.URLParam(r, "id"), a.UserID, a.ProfileID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSmartCollectionItems evaluates the collection's rules + sort
// against the configured backend's catalog and returns the matching
// LibraryItems as a paged response. ?limit + ?page can override the
// collection's own Limit for browsing.
func (h *Handler) handleSmartCollectionItems(w http.ResponseWriter, r *http.Request) {
	a, _ := absAuthFrom(r)
	c, err := h.store.GetSmartCollection(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "smart collection not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if c.UserID != a.UserID && !c.IsPublic {
		http.Error(w, "not visible to this user", http.StatusNotFound)
		return
	}

	var qd smartcoll.QueryDefinition
	if err := json.Unmarshal(c.QueryDef, &qd); err != nil {
		http.Error(w, "invalid query_def: "+err.Error(), http.StatusInternalServerError)
		return
	}

	limit, page := readPagedQuery(r, 30)
	// Use the collection's own limit when the request doesn't override.
	if r.URL.Query().Get("limit") == "" && qd.Limit != nil && *qd.Limit > 0 {
		limit = *qd.Limit
	}

	// Resolve target libraries. If query_def.library_ids is empty, use
	// every audiobook library the user can see. Podcast libraries are
	// excluded — podcasts have their own surfaces.
	allLibs, err := h.store.ListPortalLibraries(r.Context(), true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	libByID := make(map[int64]store.PortalLibrary, len(allLibs))
	for _, lib := range allLibs {
		libByID[lib.ID] = lib
	}
	var targetLibs []store.PortalLibrary
	if len(qd.LibraryIDs) > 0 {
		for _, id := range qd.LibraryIDs {
			if lib, ok := libByID[id]; ok && lib.MediaType != "podcast" {
				targetLibs = append(targetLibs, lib)
			}
		}
	} else {
		for _, lib := range allLibs {
			if lib.MediaType != "podcast" {
				targetLibs = append(targetLibs, lib)
			}
		}
	}

	// Over-fetch from each library, build Candidate list, evaluate.
	candidates := make([]smartcoll.Candidate, 0, 1024)
	for _, lib := range targetLibs {
		if lib.BackendPluginID == "" {
			continue
		}
		out, err := h.backend.ListCatalog(r.Context(), a.Token, lib.BackendPluginID, backend.ListParams{
			Limit:     5000,
			LibraryID: backendLibraryID(lib),
		})
		if err != nil {
			h.logger.Warn("smart_collection: list catalog", "library_id", lib.ID, "err", err.Error())
			continue
		}
		for _, s := range out.Items {
			s = withPortalLibrarySummary(s, lib)
			cand := smartcoll.Candidate{Item: s}
			// Hydrate per-user state for personalized rules — only
			// for the requesting user's own collections (public
			// collections evaluate without personalization to avoid
			// leaking who's listening to what).
			if c.UserID == a.UserID {
				if prog, perr := h.store.GetProgress(r.Context(), a.UserID, a.ProfileID, bookref.Encode(lib.ID, s.ID)); perr == nil {
					cand.IsFinished = prog.IsFinished
					cand.ProgressPct = prog.ProgressPct
					cand.CurrentSeconds = prog.CurrentSeconds
					cand.LastPlayedAt = prog.UpdatedAt
				}
				bookmarks, _ := h.store.ListBookmarks(r.Context(), a.UserID, a.ProfileID, bookref.Encode(lib.ID, s.ID))
				cand.BookmarkCount = len(bookmarks)
			}
			candidates = append(candidates, cand)
		}
	}

	matched := smartcoll.Evaluate(r.Context(), qd, candidates, smartcoll.EvaluateOptions{
		AllowPersonalized: c.UserID == a.UserID,
		UserSeed:          a.UserID + ":" + c.ID,
		Now:               time.Now(),
	})

	total := len(matched)
	start := page * limit
	if start > len(matched) {
		start = len(matched)
	}
	end := start + limit
	if end > len(matched) {
		end = len(matched)
	}
	pageSlice := matched[start:end]
	results := make([]any, 0, len(pageSlice))
	for _, cand := range pageSlice {
		// Render as ABS LibraryItem summary; the cover/library
		// metadata is already merged in via withPortalLibrarySummary
		// above.
		results = append(results, ToLibrarySummary(cand.Item))
	}

	writeJSON(w, http.StatusOK, pagedEnvelope(results, total, limit, page, qd.Sort.Field, qd.Sort.Order == "desc", "", false, ""))
}

// persistSmartCollection decodes the request body, validates the DSL,
// and upserts. Shared between create + update; mints a new id when the
// caller passed an empty existingID.
func (h *Handler) persistSmartCollection(w http.ResponseWriter, r *http.Request, existingID string) (store.SmartCollection, error) {
	a, _ := absAuthFrom(r)
	var body smartCollectionBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return store.SmartCollection{}, err
	}
	if body.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return store.SmartCollection{}, errors.New("name required")
	}
	normalized := body.QueryDef.Normalize()
	if err := normalized.Validate(true); err != nil {
		http.Error(w, "invalid query_def: "+err.Error(), http.StatusBadRequest)
		return store.SmartCollection{}, err
	}
	defJSON, err := json.Marshal(normalized)
	if err != nil {
		http.Error(w, "encode query_def: "+err.Error(), http.StatusInternalServerError)
		return store.SmartCollection{}, err
	}
	id := existingID
	if id == "" {
		id = ulid.Make().String()
	}
	c := store.SmartCollection{
		ID:          id,
		UserID:      a.UserID,
		Name:        body.Name,
		Description: body.Description,
		Color:       body.Color,
		IsPublic:    body.IsPublic,
		IsPinned:    body.IsPinned,
		QueryDef:    defJSON,
	}
	if err := h.store.UpsertSmartCollection(r.Context(), c); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return store.SmartCollection{}, err
	}
	persisted, err := h.store.GetSmartCollection(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return store.SmartCollection{}, err
	}
	return persisted, nil
}

func smartCollectionToMap(c store.SmartCollection) map[string]any {
	var qd any
	_ = json.Unmarshal(c.QueryDef, &qd)
	return map[string]any{
		"id":          c.ID,
		"userId":      c.UserID,
		"name":        c.Name,
		"description": c.Description,
		"color":       c.Color,
		"isPublic":    c.IsPublic,
		"isPinned":    c.IsPinned,
		"queryDef":    qd,
		"createdAt":   c.CreatedAt.UnixMilli(),
		"updatedAt":   c.UpdatedAt.UnixMilli(),
	}
}

