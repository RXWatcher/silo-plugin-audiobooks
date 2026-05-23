package store_test

import (
	"testing"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
)

func TestUpsertProgressPersistsDuration(t *testing.T) {
	st, ctx := newStore(t)
	if err := st.UpsertProgress(ctx, store.Progress{
		UserID: "u1", ProfileID: "", BookID: "b1", CurrentSeconds: 30, DurationSeconds: 3600, ProgressPct: 0.0083,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := st.GetProgress(ctx, "u1", "", "b1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DurationSeconds != 3600 {
		t.Errorf("DurationSeconds = %d, want 3600", got.DurationSeconds)
	}
}

func TestProgressIsolatedByProfile(t *testing.T) {
	st, ctx := newStore(t)

	// Upsert progress for the primary profile (empty string).
	if err := st.UpsertProgress(ctx, store.Progress{
		UserID: "u1", ProfileID: "", BookID: "b1", CurrentSeconds: 100, DurationSeconds: 3600, ProgressPct: 0.027,
	}); err != nil {
		t.Fatalf("upsert primary: %v", err)
	}

	// Upsert progress for the "kids" profile — same user+book, different position.
	if err := st.UpsertProgress(ctx, store.Progress{
		UserID: "u1", ProfileID: "kids", BookID: "b1", CurrentSeconds: 200, DurationSeconds: 3600, ProgressPct: 0.055,
	}); err != nil {
		t.Fatalf("upsert kids: %v", err)
	}

	primary, err := st.GetProgress(ctx, "u1", "", "b1")
	if err != nil {
		t.Fatalf("get primary: %v", err)
	}
	if primary.CurrentSeconds != 100 {
		t.Errorf("primary CurrentSeconds = %d, want 100", primary.CurrentSeconds)
	}

	kids, err := st.GetProgress(ctx, "u1", "kids", "b1")
	if err != nil {
		t.Fatalf("get kids: %v", err)
	}
	if kids.CurrentSeconds != 200 {
		t.Errorf("kids CurrentSeconds = %d, want 200", kids.CurrentSeconds)
	}
}
