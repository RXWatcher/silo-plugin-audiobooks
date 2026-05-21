// Package scheduler implements scheduled_task.v1 — periodic background work:
// request reconciler, ABS session reaper, cache evictor.
package scheduler

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/go-hclog"

	pluginv1 "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginproto/continuum/plugin/v1"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/backend"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/libsync"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
)

// taskID extracts the capability id from a scheduled-task key. The Continuum
// host sends "plugin:<installationID>:<capabilityID>" (task_registry
// pluginTaskKey); bare ids may arrive from host integration tests. This
// plugin's task ids contain no ':'.
func taskID(key string) string {
	if i := strings.LastIndexByte(key, ':'); i >= 0 {
		return key[i+1:]
	}
	return key
}

// Deps wires the scheduler's runtime collaborators.
type Deps struct {
	Store   *store.Store
	Backend *backend.Client
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

// Run dispatches by task key. Known task keys (see manifest):
//
//	request_reconciler  — poll backend for missed status events
//	abs_session_reaper  — close idle ABS playback sessions
//	portal_library_sync — mirror backend library metadata into portal shelves
//
// cache_evictor was removed when the portal's broken streaming cache was
// dropped; backends now serve bytes directly from their filesystem mount.
func (s *Server) Run(ctx context.Context, req *pluginv1.RunScheduledTaskRequest) (*pluginv1.RunScheduledTaskResponse, error) {
	d := s.depsFn()
	if d == nil || d.Store == nil {
		return nil, fmt.Errorf("plugin not configured yet")
	}

	switch taskID(req.GetTaskKey()) {
	case "request_reconciler":
		s.reconcileRequests(ctx, d)
		s.reapSessions(ctx, d)
	case "abs_session_reaper":
		s.reapSessions(ctx, d)
	case "cache_evictor":
		// No-op: cache was removed when streaming was simplified.
	case "portal_library_sync":
		s.syncPortalLibraries(ctx, d)
	default:
		return nil, fmt.Errorf("unknown task key %q", req.GetTaskKey())
	}
	return &pluginv1.RunScheduledTaskResponse{}, nil
}

func (s *Server) reconcileRequests(ctx context.Context, d *Deps) {
	cfg, err := d.Store.GetBackendConfig(ctx)
	if err != nil || cfg.BackendInstallID() == "" {
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
		// Scheduler calls don't have a user bearer; the backend client
		// falls back to the plugin's service token for authenticated
		// plugin-to-plugin reads.
		snap, err := d.Backend.GetRequestSnapshot(ctx, "", cfg.BackendInstallID(), r.ExternalID)
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

func (s *Server) syncPortalLibraries(ctx context.Context, d *Deps) {
	cfg, err := d.Store.GetBackendConfig(ctx)
	if err != nil {
		s.logger.Warn("portal library sync config", "err", err)
		return
	}
	target := cfg.BackendInstallID()
	if target == "" || d.Backend == nil {
		return
	}
	if _, err := libsync.Sync(ctx, d.Store, d.Backend, "", target); err != nil {
		s.logger.Warn("portal library sync", "backend", target, "err", err)
	}
}
