package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/auth"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
)

// Admin audit log query surface. Read-only — append is server-side
// instrumentation, not a user-callable action.

func (s *Server) mountAuditLogRoutes(r chi.Router) {
	r.Get("/admin/audit-log", s.handleListAuditEntries)
}

func (s *Server) handleListAuditEntries(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireAdmin(w, r); !ok {
		return
	}
	f := store.AuditFilters{
		ActorID:    strings.TrimSpace(r.URL.Query().Get("actor_id")),
		Action:     strings.TrimSpace(r.URL.Query().Get("action")),
		EntityType: strings.TrimSpace(r.URL.Query().Get("entity_type")),
		EntityID:   strings.TrimSpace(r.URL.Query().Get("entity_id")),
	}
	if v := r.URL.Query().Get("since"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			f.SinceMs = n
		}
	}
	if v := r.URL.Query().Get("until"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			f.UntilMs = n
		}
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			f.Limit = n
		}
	}
	rows, err := s.d.Store.ListAuditEntries(r.Context(), f)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
}
