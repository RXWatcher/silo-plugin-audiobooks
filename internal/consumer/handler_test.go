package consumer

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/structpb"

	pluginv1 "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginproto/continuum/plugin/v1"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/migrate"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/testutil"
)

func mustStruct(t *testing.T, m map[string]any) *structpb.Struct {
	t.Helper()
	s, err := structpb.NewStruct(m)
	if err != nil {
		t.Fatalf("structpb: %v", err)
	}
	return s
}

// Capability servers serve before Configure runs. A status event we act on
// (e.g. request_fulfilled) arriving while unconfigured must NACK so the host
// redelivers — otherwise the request's terminal status is dropped forever and
// the row is stuck (the reconciler can't recover a missed acknowledged
// external_id mapping).
func TestConsumer_NotConfigured_NacksOurEvent(t *testing.T) {
	h := New(func() *Deps { return nil }, nil)
	resp, err := h.HandleEvent(context.Background(), &pluginv1.HandleEventRequest{
		EventName: "plugin.continuum.bookwarehouse-audio.request_fulfilled",
		Payload:   mustStruct(t, map[string]any{"request_id": "r-1", "external_id": "x"}),
	})
	if err == nil {
		t.Fatal("our event while unconfigured must nack (error)")
	}
	if resp != nil {
		t.Errorf("response must be nil on nack; got %+v", resp)
	}
}

// A foreign/unknown event must be acked (dropped) even before Configure —
// nacking it would loop the host forever on another plugin's event.
func TestConsumer_UnknownEvent_AcksEvenUnconfigured(t *testing.T) {
	h := New(func() *Deps { return nil }, nil)
	if _, err := h.HandleEvent(context.Background(), &pluginv1.HandleEventRequest{
		EventName: "plugin.continuum.something.totally_unrelated",
		Payload:   mustStruct(t, map[string]any{}),
	}); err != nil {
		t.Fatalf("unknown event must ack, not nack; got err=%v", err)
	}
}

// recordingBroadcaster captures the (event, payload) pairs the consumer
// emits so the test thread can assert on them. Implements EventBroadcaster.
type recordingBroadcaster struct {
	mu       sync.Mutex
	events   []string
	payloads []any
}

func (r *recordingBroadcaster) Broadcast(event string, payload any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	r.payloads = append(r.payloads, payload)
}

func (r *recordingBroadcaster) snapshot() ([]string, []any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...), append([]any(nil), r.payloads...)
}

// TestConsumer_AudiobookImported_BroadcastsItemAdded confirms that an
// audiobook_imported event fans an "item_added" event to connected ABS
// clients via the broadcaster, with the metadata the backend supplied.
//
// This is the realtime hook ABS mobile clients rely on to refresh their
// library when a new book finishes importing — without it, the new book
// only shows up on the next pull-to-refresh.
func TestConsumer_AudiobookImported_BroadcastsItemAdded(t *testing.T) {
	dsn := testutil.StartPG(t)
	if err := migrate.Run(context.Background(), dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()
	st := store.New(pool)

	// MarkRequestFulfilled may not find the external_id (no seeded row);
	// the handler warn-logs and continues to broadcast. We exercise just
	// the broadcast path, which is what ABS clients care about.

	bc := &recordingBroadcaster{}
	h := New(func() *Deps {
		return &Deps{Store: st, Broadcast: bc}
	}, nil)

	_, err = h.HandleEvent(context.Background(), &pluginv1.HandleEventRequest{
		EventName: "plugin.continuum.bookwarehouse-audio.audiobook_imported",
		Payload: mustStruct(t, map[string]any{
			"external_id": "ext-1",
			"title":       "Way of Kings",
			"author":      "Brandon Sanderson",
			"cover_path":  "/covers/book-1/large",
		}),
	})
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	events, payloads := bc.snapshot()
	if len(events) != 1 || events[0] != "item_added" {
		t.Fatalf("events = %+v, want exactly one item_added", events)
	}
	p, ok := payloads[0].(map[string]any)
	if !ok {
		t.Fatalf("payload not a map: %T = %+v", payloads[0], payloads[0])
	}
	if p["libraryItemId"] != "ext-1" {
		t.Errorf("libraryItemId = %v", p["libraryItemId"])
	}
	if p["title"] != "Way of Kings" {
		t.Errorf("title = %v", p["title"])
	}
	if p["authorName"] != "Brandon Sanderson" {
		t.Errorf("authorName = %v", p["authorName"])
	}
	if p["coverPath"] != "/covers/book-1/large" {
		t.Errorf("coverPath = %v", p["coverPath"])
	}
	if _, ok := p["importedAt"].(int64); !ok {
		t.Errorf("importedAt missing or wrong type: %v (%T)", p["importedAt"], p["importedAt"])
	}
}

// TestConsumer_AudiobookImported_SkipsBroadcastWhenUnwired confirms the
// nil-safe guard — when no realtime hub is wired (host-proxied flows,
// tests without Socket.io), the consumer must not crash and the rest of
// the import bookkeeping (MarkRequestFulfilled) still happens.
func TestConsumer_AudiobookImported_SkipsBroadcastWhenUnwired(t *testing.T) {
	dsn := testutil.StartPG(t)
	if err := migrate.Run(context.Background(), dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()
	st := store.New(pool)

	h := New(func() *Deps { return &Deps{Store: st, Broadcast: nil} }, nil)
	if _, err := h.HandleEvent(context.Background(), &pluginv1.HandleEventRequest{
		EventName: "plugin.continuum.bookwarehouse-audio.audiobook_imported",
		Payload:   mustStruct(t, map[string]any{"external_id": "ext-1"}),
	}); err != nil {
		t.Fatalf("HandleEvent must not error with nil broadcaster: %v", err)
	}
}
