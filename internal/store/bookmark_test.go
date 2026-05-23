package store_test

import (
	"testing"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
)

func TestBookmarksIsolatedByProfile(t *testing.T) {
	st, ctx := newStore(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	const bookID = "book1"
	must(st.InsertBookmark(ctx, store.Bookmark{
		ID: "bm1", UserID: "u1", ProfileID: "", BookID: bookID, PositionSeconds: 60,
	}))
	must(st.InsertBookmark(ctx, store.Bookmark{
		ID: "bm2", UserID: "u1", ProfileID: "kids", BookID: bookID, PositionSeconds: 120,
	}))

	primary, err := st.ListBookmarks(ctx, "u1", "", bookID)
	must(err)
	if len(primary) != 1 || primary[0].ID != "bm1" {
		t.Fatalf("primary profile sees %d bookmarks, want [bm1]; got %+v", len(primary), primary)
	}

	kids, err := st.ListBookmarks(ctx, "u1", "kids", bookID)
	must(err)
	if len(kids) != 1 || kids[0].ID != "bm2" {
		t.Fatalf("kids profile sees %d bookmarks, want [bm2]; got %+v", len(kids), kids)
	}
}
