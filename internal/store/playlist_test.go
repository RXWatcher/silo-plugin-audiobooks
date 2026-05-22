package store_test

import (
	"testing"

	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/store"
)

func TestPlaylistsIsolatedByProfile(t *testing.T) {
	st, ctx := newStore(t)
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}

	must(st.CreatePlaylist(ctx, store.Playlist{ID: "p1", UserID: "u1", ProfileID: "", Name: "Primary"}))
	must(st.CreatePlaylist(ctx, store.Playlist{ID: "p2", UserID: "u1", ProfileID: "kids", Name: "Kids"}))

	primary, err := st.ListUserPlaylists(ctx, "u1", "")
	must(err)
	if len(primary) != 1 || primary[0].ID != "p1" {
		t.Fatalf("primary profile sees %d playlists, want [p1]", len(primary))
	}

	kids, err := st.ListUserPlaylists(ctx, "u1", "kids")
	must(err)
	if len(kids) != 1 || kids[0].ID != "p2" {
		t.Fatalf("kids profile sees %d playlists, want [p2]", len(kids))
	}
}
