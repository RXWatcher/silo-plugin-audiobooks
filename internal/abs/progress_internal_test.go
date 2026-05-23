package abs

import (
	"testing"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
)

func TestProgressToABSEmitsDuration(t *testing.T) {
	out := progressToABS("u1", store.Progress{
		BookID: "b1", CurrentSeconds: 30, DurationSeconds: 3600, ProgressPct: 0.0083,
	})
	if out["duration"] != float64(3600) {
		t.Errorf("duration = %v, want 3600", out["duration"])
	}
}
