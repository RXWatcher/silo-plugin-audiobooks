// Package consumer implements event_consumer.v1 — receives backend status
// events and updates the corresponding request row.
package consumer

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/go-hclog"

	pluginv1 "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginproto/continuum/plugin/v1"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
)

// Deps wires the consumer's dependencies. Resolved per-event so the store
// can be wired after Configure runs.
type Deps struct {
	Store     *store.Store
	Broadcast EventBroadcaster
}

// EventBroadcaster pushes a global (non-user-scoped) event to every
// authenticated Socket.io connection. The audiobooks plugin uses this for
// catalog mutations (item_added, item_updated, item_removed) so connected
// ABS clients refresh their library view without polling.
//
// Implemented by *abssocket.Server. nil is tolerated — the consumer
// no-ops the broadcast when no realtime hub is wired (host-proxied path,
// tests).
type EventBroadcaster interface {
	Broadcast(event string, payload any)
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
	if req.GetPayload() == nil {
		return &pluginv1.HandleEventResponse{}, nil
	}
	name := req.GetEventName()
	// Event names look like plugin.<backend_id>.<leaf>.
	leaf := lastSegment(name)

	// Decide if this is an event we act on BEFORE resolving deps: a
	// foreign/unknown event must be acked (dropped) even before Configure,
	// otherwise nacking it would make the host redeliver it forever.
	switch leaf {
	case "request_acknowledged", "request_status_changed", "request_fulfilled",
		"request_failed", "audiobook_imported", "audiobook_failed":
		// handled below
	default:
		h.logger.Debug("ignoring unknown event", "name", name)
		return &pluginv1.HandleEventResponse{}, nil
	}

	// Our event. If not configured yet, NACK so the host redelivers once
	// Configure has run — otherwise the request's status update is dropped
	// permanently and the row is stuck (the reconciler can only recover
	// fulfilled/failed, not the acknowledged external_id mapping).
	d := h.depsFn()
	if d == nil || d.Store == nil {
		return nil, fmt.Errorf("plugin not configured yet")
	}
	p := req.GetPayload().AsMap()

	switch leaf {
	case "request_acknowledged":
		h.handleAcknowledged(ctx, d, p)
	case "request_status_changed":
		h.handleStatusChanged(ctx, d, p)
	case "request_fulfilled":
		h.handleFulfilled(ctx, d, p)
	case "request_failed":
		h.handleFailed(ctx, d, p)
	case "audiobook_imported":
		h.handleImported(ctx, d, p)
	case "audiobook_failed":
		h.handleFailed(ctx, d, p)
	}
	return &pluginv1.HandleEventResponse{}, nil
}

func (h *Handler) handleAcknowledged(ctx context.Context, d *Deps, p map[string]any) {
	reqID := requestIDFromPayload(p)
	externalID, _ := p["external_id"].(string)
	if reqID == "" || externalID == "" {
		return
	}
	status, _ := p["status"].(string)
	if status == "" {
		status = "acknowledged"
	}
	if err := d.Store.SetRequestExternal(ctx, reqID, externalID, status); err != nil {
		h.logger.Warn("set request external", "err", err, "request_id", reqID)
	}
}

func (h *Handler) handleStatusChanged(ctx context.Context, d *Deps, p map[string]any) {
	reqID := requestIDFromPayload(p)
	status, _ := p["status"].(string)
	if reqID == "" || status == "" {
		return
	}
	if err := d.Store.UpdateRequestStatus(ctx, reqID, status, "", ""); err != nil {
		h.logger.Warn("update status changed", "err", err, "request_id", reqID)
	}
}

func (h *Handler) handleFulfilled(ctx context.Context, d *Deps, p map[string]any) {
	reqID := requestIDFromPayload(p)
	externalID, _ := p["external_id"].(string)
	if externalID != "" {
		if err := d.Store.MarkRequestFulfilled(ctx, externalID); err == nil {
			return
		}
	}
	if reqID == "" {
		return
	}
	if err := d.Store.UpdateRequestStatus(ctx, reqID, "fulfilled", "", ""); err != nil {
		h.logger.Warn("update fulfilled", "err", err, "request_id", reqID)
	}
}

func (h *Handler) handleFailed(ctx context.Context, d *Deps, p map[string]any) {
	reqID := requestIDFromPayload(p)
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
	// Broadcast item_added so connected ABS clients refresh their library
	// view without polling. We carry the metadata the backend already
	// emitted in the event payload (title/authors/coverPath/...). When the
	// payload is sparse the client falls back to its next browse fetch —
	// the event is a hint, not a state replacement.
	if d.Broadcast != nil {
		payload := map[string]any{
			"libraryItemId": externalID,
			"importedAt":    time.Now().UnixMilli(),
		}
		if title, ok := p["title"].(string); ok && title != "" {
			payload["title"] = title
		}
		if author, ok := p["author"].(string); ok && author != "" {
			payload["authorName"] = author
		}
		if cover, ok := p["cover_path"].(string); ok && cover != "" {
			payload["coverPath"] = cover
		}
		d.Broadcast.Broadcast("item_added", payload)
	}
}

func lastSegment(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i+1:]
	}
	return name
}

func requestIDFromPayload(p map[string]any) string {
	if id, _ := p["request_id"].(string); id != "" {
		return id
	}
	id, _ := p["requestId"].(string)
	return id
}
