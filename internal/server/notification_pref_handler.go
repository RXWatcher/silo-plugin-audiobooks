package server

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/auth"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
)

// Per-user notification preferences. Categories + delivery channels
// are server-side enumerated; the SPA renders the matrix the user
// toggles. Defaults to enabled when no row exists ("opt-out" so
// new categories surface for existing users).

func (s *Server) mountNotificationPrefRoutes(r chi.Router) {
	r.Get("/me/notification-prefs", s.handleListNotificationPrefs)
	r.Get("/me/notification-prefs/catalog", s.handleNotificationCatalog)
	r.Put("/me/notification-prefs", s.handlePutNotificationPref)
}

func (s *Server) handleListNotificationPrefs(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	rows, err := s.d.Store.ListNotificationPrefs(r.Context(), id.UserID)
	if err != nil {
		writeInternal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
}

// handleNotificationCatalog returns the SupportedCategories +
// SupportedDeliveries lists. The SPA renders a checkbox matrix
// from these; new categories appear automatically as the server
// adds them.
func (s *Server) handleNotificationCatalog(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.RequireUser(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"categories": store.SupportedCategories,
		"deliveries": store.SupportedDeliveries,
	})
}

func (s *Server) handlePutNotificationPref(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.RequireUser(w, r)
	if !ok {
		return
	}
	var body struct {
		Category string `json:"category"`
		Delivery string `json:"delivery"`
		Enabled  bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.d.Store.UpsertNotificationPref(r.Context(), store.NotificationPref{
		UserID:   id.UserID,
		Category: body.Category,
		Delivery: body.Delivery,
		Enabled:  body.Enabled,
	}); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
