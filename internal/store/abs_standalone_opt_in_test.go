package store_test

import (
	"testing"
)

// TestStandaloneOptIn_RoundTrip verifies the three helpers compose correctly:
// HasStandaloneOptIn is false before insert, true after Enable, idempotent
// across repeated Enable calls, and false again after Disable.
func TestStandaloneOptIn_RoundTrip(t *testing.T) {
	st, ctx := newStore(t)

	ok, err := st.HasStandaloneOptIn(ctx, "u-42")
	if err != nil || ok {
		t.Fatalf("HasStandaloneOptIn pre-insert: ok=%v err=%v want false/nil", ok, err)
	}

	if err := st.EnableStandaloneOptIn(ctx, "u-42"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if err := st.EnableStandaloneOptIn(ctx, "u-42"); err != nil {
		t.Fatalf("Enable (idempotent): %v", err)
	}

	ok, err = st.HasStandaloneOptIn(ctx, "u-42")
	if err != nil || !ok {
		t.Fatalf("HasStandaloneOptIn post-insert: ok=%v err=%v want true/nil", ok, err)
	}

	if err := st.DisableStandaloneOptIn(ctx, "u-42"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if err := st.DisableStandaloneOptIn(ctx, "u-42"); err != nil {
		t.Fatalf("Disable (idempotent): %v", err)
	}

	ok, err = st.HasStandaloneOptIn(ctx, "u-42")
	if err != nil || ok {
		t.Fatalf("HasStandaloneOptIn post-disable: ok=%v err=%v want false/nil", ok, err)
	}
}

// TestStandaloneOptIn_EmptyUser rejects empty user ids — the upstream
// handlers should never call with an empty id, but a defensive check keeps a
// stray call from silently inserting a row keyed by "".
func TestStandaloneOptIn_EmptyUser(t *testing.T) {
	st, ctx := newStore(t)

	if err := st.EnableStandaloneOptIn(ctx, ""); err == nil {
		t.Errorf("Enable with empty user should error")
	}
	if err := st.DisableStandaloneOptIn(ctx, ""); err == nil {
		t.Errorf("Disable with empty user should error")
	}
	ok, err := st.HasStandaloneOptIn(ctx, "")
	if err != nil || ok {
		t.Errorf("Has with empty user: ok=%v err=%v want false/nil", ok, err)
	}
}
