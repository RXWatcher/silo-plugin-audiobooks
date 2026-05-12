// Package scheduler implements scheduled_task.v1 — periodic background work:
// request reconciler, ABS session reaper, cache evictor.
package scheduler

import (
	"context"
	"time"

	"github.com/hashicorp/go-hclog"

	pluginv1 "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginproto/continuum/plugin/v1"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/backend"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/streaming"
)

// Deps wires the scheduler's runtime collaborators.
type Deps struct {
	Store   *store.Store
	Backend *backend.Client
	Cache   *streaming.Cache
}

// Server implements pluginv1.ScheduledTaskServer.
type Server struct {
	pluginv1.UnimplementedScheduledTaskServer
	depsFn func() *Deps
	logger hclog.Logger
}

// New constructs a scheduler server.
func New(depsFn func() *Deps, logger hclog.Logger) *Server {
	if logger == nil {
		logger = hclog.NewNullLogger()
	}
	return &Server{depsFn: depsFn, logger: logger}
}

// Run dispatches by task key. Three known task keys (see manifest):
//
//	request_reconciler  — poll backend for missed status events
//	abs_session_reaper  — close idle ABS playback sessions
//	cache_evictor       — LRU-evict cached audio files
func (s *Server) Run(ctx context.Context, req *pluginv1.RunScheduledTaskRequest) (*pluginv1.RunScheduledTaskResponse, error) {
	d := s.depsFn()
	if d == nil || d.Store == nil {
		return &pluginv1.RunScheduledTaskResponse{}, nil
	}

	switch req.GetTaskKey() {
	case "request_reconciler":
		s.reconcileRequests(ctx, d)
	case "abs_session_reaper":
		s.reapSessions(ctx, d)
	case "cache_evictor":
		s.evictCache(ctx, d)
	default:
		s.logger.Debug("unknown task", "key", req.GetTaskKey())
	}
	return &pluginv1.RunScheduledTaskResponse{}, nil
}

func (s *Server) reconcileRequests(ctx context.Context, d *Deps) {
	cfg, err := d.Store.GetBackendConfig(ctx)
	if err != nil || cfg.TargetBackendPluginID == "" {
		return
	}
	if d.Backend == nil {
		return
	}
	candidates, err := d.Store.ListReconcileCandidates(ctx, 100)
	if err != nil {
		s.logger.Warn("list reconcile candidates", "err", err)
		return
	}
	for _, r := range candidates {
		// No bearer available in scheduler context; the backend's
		// /requests endpoint is authenticated, so this only works when
		// the host accepts service-token / unauthenticated reads. Pass an
		// empty bearer and let the backend decide.
		snap, err := d.Backend.GetRequestSnapshot(ctx, "", cfg.TargetBackendPluginID, r.ExternalID)
		if err != nil {
			continue
		}
		switch snap.Status {
		case "imported":
			_ = d.Store.MarkRequestFulfilled(ctx, r.ExternalID)
		case "failed":
			_ = d.Store.UpdateRequestStatus(ctx, r.ID, "failed", "", snap.Error)
		}
	}
}

func (s *Server) reapSessions(ctx context.Context, d *Deps) {
	n, err := d.Store.ReapIdleABSSessions(ctx, 10*time.Minute)
	if err != nil {
		s.logger.Warn("reap sessions", "err", err)
		return
	}
	if n > 0 {
		s.logger.Debug("reaped abs sessions", "count", n)
	}
}

func (s *Server) evictCache(ctx context.Context, d *Deps) {
	if d.Cache == nil {
		return
	}
	target := int64(float64(d.Cache.MaxBytes()) * 0.95)
	n, err := d.Cache.Evict(ctx, target)
	if err != nil {
		s.logger.Warn("evict cache", "err", err)
		return
	}
	if n > 0 {
		s.logger.Debug("evicted cache entries", "count", n)
	}
}
