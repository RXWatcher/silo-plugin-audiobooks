package abs_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/abs"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/migrate"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/testutil"
)

// The track route must be registered so a real subscriber's enclosure
// URL resolves to a handler rather than chi's 404.
func TestPublicFeedTrackRouteIsRegistered(t *testing.T) {
	h := &abs.Handler{}
	r := chi.NewRouter()
	h.MountPublicFeed(r)

	rctx := chi.NewRouteContext()
	if !r.Match(rctx, http.MethodGet, "/feed/abc/track/li_5:somebook/0.mp3") {
		t.Fatal("GET /feed/{slug}/track/{ref}/{idx} is not routed")
	}
}

// A collection RSS feed's track route must verify the requested book is a
// member of the feed's collection — otherwise a leaked slug could be used to
// stream any book in the owner's library. Non-members are rejected outright;
// members clear the gate (and only then fail later for unrelated reasons).
func TestPublicFeedTrack_CollectionMembershipGate(t *testing.T) {
	dsn := testutil.StartPG(t)
	if err := migrate.Run(context.Background(), dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	st := store.New(pool)
	ctx := context.Background()

	// A collection owned by alice holding exactly one member book.
	const owner, collID, memberBook = "alice", "coll-1", "book-in-collection"
	if err := st.CreateCollection(ctx, store.Collection{ID: collID, UserID: owner, Name: "Roadtrip"}); err != nil {
		t.Fatalf("create collection: %v", err)
	}
	if err := st.AddCollectionItem(ctx, collID, memberBook, owner, ""); err != nil {
		t.Fatalf("add collection item: %v", err)
	}
	if err := st.UpsertRSSFeed(ctx, store.RSSFeed{
		ID: "feed-1", UserID: owner, Slug: "secret-slug",
		EntityType: "collection", EntityID: collID, Title: "Roadtrip",
	}); err != nil {
		t.Fatalf("upsert feed: %v", err)
	}

	h := abs.NewHandler(abs.Deps{Store: st})
	r := chi.NewRouter()
	h.MountPublicFeed(r)

	get := func(ref string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/feed/secret-slug/track/"+ref+"/0.mp3", nil))
		return w
	}

	// A book that is NOT in the collection is rejected by the gate.
	if w := get("book-not-in-collection"); w.Code != http.StatusNotFound ||
		!strings.Contains(w.Body.String(), "track not part of this feed") {
		t.Fatalf("non-member ref = %d body=%q, want 404 'track not part of this feed'", w.Code, w.Body.String())
	}

	// The member book clears the membership gate; it fails later only
	// because this test wires no backend — proving the gate let it through.
	if w := get(memberBook); w.Code == http.StatusNotFound &&
		strings.Contains(w.Body.String(), "track not part of this feed") {
		t.Fatalf("member ref wrongly rejected by membership gate: %d body=%q", w.Code, w.Body.String())
	}
}
