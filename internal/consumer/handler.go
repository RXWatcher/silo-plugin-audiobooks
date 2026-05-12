// Package consumer implements event_consumer.v1 — receives backend status
// events and updates the corresponding request row.
package consumer

import (
	"context"
	"strings"
	"time"

	"github.com/hashicorp/go-hclog"

	pluginv1 "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginproto/continuum/plugin/v1"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
)

// Deps wires the consumer's dependencies. Resolved per-event so the store
// can be wired after Configure runs.
type Deps struct {
	Store *store.Store
}

// Handler implements pluginv1.EventConsumerServer.
type Handler struct {
	pluginv1.UnimplementedEventConsumerServer
	depsFn func() *Deps
	logger hclog.Logger
}

// New constructs a Handler.
func New(depsFn func() *Deps, logger hclog.Logger) *Handler {
	if logger == nil {
		logger = hclog.NewNullLogger()
	}
	return &Handler{depsFn: depsFn, logger: logger}
}

// HandleEvent dispatches one host event by name.
func (h *Handler) HandleEvent(ctx context.Context, req *pluginv1.HandleEventRequest) (*pluginv1.HandleEventResponse, error) {
	d := h.depsFn()
	if d == nil || d.Store == nil {
		return &pluginv1.HandleEventResponse{}, nil
	}
	if req.GetPayload() == nil {
		return &pluginv1.HandleEventResponse{}, nil
	}
	p := req.GetPayload().AsMap()
	name := req.GetEventName()

	// Event names look like plugin.<backend_id>.<leaf>.
	leaf := lastSegment(name)

	switch leaf {
	case "request_acknowledged":
		h.handleAcknowledged(ctx, d, p)
	case "request_failed":
		h.handleFailed(ctx, d, p)
	case "audiobook_imported":
		h.handleImported(ctx, d, p)
	case "audiobook_failed":
		h.handleFailed(ctx, d, p)
	default:
		h.logger.Debug("ignoring unknown event", "name", name)
	}
	return &pluginv1.HandleEventResponse{}, nil
}

func (h *Handler) handleAcknowledged(ctx context.Context, d *Deps, p map[string]any) {
	reqID, _ := p["request_id"].(string)
	externalID, _ := p["external_id"].(string)
	if reqID == "" || externalID == "" {
		return
	}
	if err := d.Store.SetRequestExternal(ctx, reqID, externalID, "acknowledged"); err != nil {
		h.logger.Warn("set request external", "err", err, "request_id", reqID)
	}
}

func (h *Handler) handleFailed(ctx context.Context, d *Deps, p map[string]any) {
	reqID, _ := p["request_id"].(string)
	reason, _ := p["reason"].(string)
	if reqID == "" {
		// Try matching by external_id for audiobook_failed events.
		externalID, _ := p["external_id"].(string)
		if externalID == "" {
			return
		}
		req, err := d.Store.GetByExternalIDStub(ctx, externalID)
		if err != nil {
			return
		}
		reqID = req.ID
	}
	if err := d.Store.UpdateRequestStatus(ctx, reqID, "failed", "", reason); err != nil {
		h.logger.Warn("update status failed", "err", err)
	}
}

func (h *Handler) handleImported(ctx context.Context, d *Deps, p map[string]any) {
	externalID, _ := p["external_id"].(string)
	if externalID == "" {
		return
	}
	if err := d.Store.MarkRequestFulfilled(ctx, externalID); err != nil {
		h.logger.Warn("mark fulfilled", "err", err)
	}
	_ = time.Now() // suppress unused if event doesn't always emit
}

func lastSegment(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return name
}
