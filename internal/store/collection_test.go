package store_test

import (
	"testing"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
)

func TestCollectionsIsolatedByProfile(t *testing.T) {
	st, ctx := newStore(t)
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.CreateCollection(ctx, store.Collection{ID: "c1", UserID: "u1", ProfileID: "", Name: "Primary"}))
	must(st.CreateCollection(ctx, store.Collection{ID: "c2", UserID: "u1", ProfileID: "kids", Name: "Kids"}))

	primary, err := st.ListUserCollections(ctx, "u1", "")
	must(err)
	if len(primary) != 1 || primary[0].ID != "c1" {
		t.Fatalf("primary profile sees %d collections, want [c1]", len(primary))
	}
	kids, err := st.ListUserCollections(ctx, "u1", "kids")
	must(err)
	if len(kids) != 1 || kids[0].ID != "c2" {
		t.Fatalf("kids profile sees %d collections, want [c2]", len(kids))
	}
}
