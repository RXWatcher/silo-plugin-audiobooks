// Package server is the audiobooks portal's chi-mounted HTTP handler. It
// composes the API routes (/api/v1/*), ABS-compat routes (/abs/api/*), and
// the embedded SPA (/* fallback). The httproutes package wraps this handler
// for the SDK's HttpRoutes.v1 RPC.
package server

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/abs"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/auth"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/backend"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/event"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/podcastfeed"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/streaming"
)

// Deps wires the server's collaborators. SPA may be nil during early dev.
type Deps struct {
	Store       *store.Store
	Backend     *backend.Client
	Events      *event.Publisher
	Streaming   *streaming.Router
	ABS         *abs.Handler
	SPA         http.Handler
	HostBaseFn  func() string
	PodcastFeed *podcastfeed.Refresher
	// Broadcaster lets admin mutations push library_* / item_* / etc.
	// realtime events to connected ABS clients without polling. nil
	// is OK — the handlers short-circuit.
	Broadcaster Broadcaster
}

// Broadcaster is the narrow surface admin handlers use to push
// realtime events. Same shape the abssocket hub exposes.
type Broadcaster interface {
	Broadcast(event string, payload any)
}

// Server wraps the chi handler with the configured deps.
type Server struct{ d Deps }

// New constructs a Server from Deps.
func New(d Deps) *Server { return &Server{d: d} }

// Handler returns the fully composed http.Handler. Routes:
//   - /api/v1/*           authenticated REST API
//   - /abs/api/*          ABS-mobile-compat surface (JWT-validated)
//   - /abs/public/*       session-scoped streaming (token in query)
//   - everything else     SPA fallback
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(auth.Middleware)

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/health", s.handleHealth)
		s.mountAudiobookRoutes(r)
		s.mountUserStateRoutes(r)
		s.mountRequestRoutes(r)
		s.mountCollectionRoutes(r)
s.mountPodcastAdminRoutes(r)
		s.mountAdminRoutes(r)
		s.mountContentRestrictionRoutes(r)
		s.mountCustomMetadataProviderRoutes(r)
		s.mountReadingSessionRoutes(r)
		s.mountEnrichRoutes(r)
		s.mountReadingGoalRoutes(r)
		s.mountShareLinkRoutes(r)
		s.mountNotificationPrefRoutes(r)
		s.mountActivityRoutes(r)
		s.mountStreamRoutes(r)
	})
	// Public share resolution — outside the auth group.
	s.MountPublicShare(r)

	if s.d.ABS != nil {
		s.d.ABS.Mount(r)
	}

	if s.d.SPA != nil {
		r.Get("/*", s.d.SPA.ServeHTTP)
	}
	return r
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

// profileID extracts the active profile from the request. The host
// proxy stamps the active profile as the X-Silo-Profile-Id header;
// an empty value means the primary profile.
func profileID(r *http.Request) string {
	return r.Header.Get("X-Silo-Profile-Id")
}

// writeInternal handles an unexpected store/backend error. The underlying
// error may carry SQL text, schema names, or internal paths, so it is logged
// server-side (with the request method+path for triage) and only an opaque
// 500 is returned to the client.
func writeInternal(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("audiobooks: internal error",
		"method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}
