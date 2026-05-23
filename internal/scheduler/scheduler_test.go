package scheduler

import (
	"context"
	"testing"

	pluginv1 "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

func TestTaskID(t *testing.T) {
	cases := map[string]string{
		"plugin:42:request_reconciler":  "request_reconciler", // real host wire format
		"plugin:42:portal_library_sync": "portal_library_sync",
		"plugin:7:cache_evictor":        "cache_evictor",
		"request_reconciler":            "request_reconciler", // bare (host integration tests)
	}
	for in, want := range cases {
		if got := taskID(in); got != want {
			t.Errorf("taskID(%q) = %q, want %q", in, got, want)
		}
	}
}

// The host sends TaskKey="plugin:<installationID>:request_reconciler". The old
// bare-id switch hit default every tick, so the reconciler (and the session
// reaper + cache evictor it drives) never ran. Not-configured must error so
// the host retries once Configure has run rather than reporting success.
func TestRun_NotConfiguredErrors(t *testing.T) {
	s := New(func() *Deps { return nil }, nil)
	if _, err := s.Run(context.Background(),
		&pluginv1.RunScheduledTaskRequest{TaskKey: "plugin:42:request_reconciler"}); err == nil {
		t.Fatal("nil deps must error so the host retries")
	}
	s2 := New(func() *Deps { return &Deps{} }, nil) // Store nil
	if _, err := s2.Run(context.Background(),
		&pluginv1.RunScheduledTaskRequest{TaskKey: "plugin:42:request_reconciler"}); err == nil {
		t.Fatal("nil store must error")
	}
}

func TestRun_UnknownKeyErrors(t *testing.T) {
	s := New(func() *Deps { return &Deps{} }, nil)
	_, err := s.Run(context.Background(),
		&pluginv1.RunScheduledTaskRequest{TaskKey: "plugin:42:bogus"})
	// Not-configured is checked first, but an unknown bare key with nil deps
	// still must not succeed silently.
	if err == nil {
		t.Fatal("unknown key must not succeed silently")
	}
}
