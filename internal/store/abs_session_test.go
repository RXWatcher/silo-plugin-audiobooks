package store_test

import (
	"testing"
	"time"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
)

func TestABSSessionsIsolatedByProfile(t *testing.T) {
	st, ctx := newStore(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	expires := time.Now().Add(time.Hour)
	_ = expires

	// Session for user u1, primary profile ("").
	must(st.InsertABSSession(ctx, store.ABSSession{
		ID:       "sess1",
		UserID:   "u1",
		ProfileID: "",
		BookID:   "book1",
		DeviceID: "dev1",
	}))

	// Session for user u1, "kids" profile.
	must(st.InsertABSSession(ctx, store.ABSSession{
		ID:       "sess2",
		UserID:   "u1",
		ProfileID: "kids",
		BookID:   "book2",
		DeviceID: "dev2",
	}))

	primary, err := st.ListActiveABSSessionsForUser(ctx, "u1", "", 100)
	must(err)
	if len(primary) != 1 || primary[0].ID != "sess1" {
		t.Fatalf("primary profile sees %d sessions, want [sess1]; got %+v", len(primary), primary)
	}

	kids, err := st.ListActiveABSSessionsForUser(ctx, "u1", "kids", 100)
	must(err)
	if len(kids) != 1 || kids[0].ID != "sess2" {
		t.Fatalf("kids profile sees %d sessions, want [sess2]; got %+v", len(kids), kids)
	}
}
