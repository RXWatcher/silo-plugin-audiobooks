package store_test

import (
	"encoding/json"
	"testing"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
)

func TestSmartCollectionsIsolatedByProfile(t *testing.T) {
	st, ctx := newStore(t)
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}

	qd := json.RawMessage(`{"filters":[]}`)

	sc1 := store.SmartCollection{
		ID:        "sc1",
		UserID:    "u1",
		ProfileID: "",
		Name:      "Primary SC",
		QueryDef:  qd,
	}
	sc2 := store.SmartCollection{
		ID:        "sc2",
		UserID:    "u1",
		ProfileID: "kids",
		Name:      "Kids SC",
		QueryDef:  qd,
	}

	must(st.UpsertSmartCollection(ctx, sc1))
	must(st.UpsertSmartCollection(ctx, sc2))

	primary, err := st.ListSmartCollections(ctx, "u1", "", 50)
	must(err)
	if len(primary) != 1 || primary[0].ID != "sc1" {
		t.Fatalf("primary profile sees %d smart collections, want [sc1]; got %v", len(primary), primary)
	}

	kids, err := st.ListSmartCollections(ctx, "u1", "kids", 50)
	must(err)
	if len(kids) != 1 || kids[0].ID != "sc2" {
		t.Fatalf("kids profile sees %d smart collections, want [sc2]; got %v", len(kids), kids)
	}
}
