package consumer

import (
	"context"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	pluginv1 "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginproto/continuum/plugin/v1"
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
