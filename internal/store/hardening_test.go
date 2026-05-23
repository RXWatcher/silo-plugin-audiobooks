package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/migrate"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/testutil"
)

func newStore(t *testing.T) (*store.Store, context.Context) {
	t.Helper()
	dsn := testutil.StartPG(t)
	if err := migrate.Run(context.Background(), dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return store.New(pool), context.Background()
}

// Regression: a request that already reached a terminal state (imported)
// must not be resurrected by a late/replayed backend status event or a
// reconciler tick. Guards UpdateRequestStatus + SetRequestExternal.
func TestRequestTerminalGuard(t *testing.T) {
	st, ctx := newStore(t)

	if err := st.InsertRequest(ctx, store.Request{
		ID: "r1", UserID: "u1", Title: "Book", Status: "pending",
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := st.SetRequestExternal(ctx, "r1", "ext-1", "acknowledged"); err != nil {
		t.Fatalf("set external: %v", err)
	}
	if err := st.MarkRequestFulfilled(ctx, "ext-1"); err != nil {
		t.Fatalf("fulfill: %v", err)
	}

	// Replayed/out-of-order events must not move it off "imported".
	if err := st.UpdateRequestStatus(ctx, "r1", "downloading", "", ""); err != nil {
		t.Fatalf("update (no-op expected): %v", err)
	}
	if err := st.SetRequestExternal(ctx, "r1", "ext-2", "queued"); err != nil {
		t.Fatalf("set external (no-op expected): %v", err)
	}
	got, err := st.GetRequest(ctx, "r1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != "imported" {
		t.Fatalf("terminal request resurrected: status=%q", got.Status)
	}
	if got.ExternalID != "ext-1" {
		t.Fatalf("external_id overwritten on terminal request: %q", got.ExternalID)
	}

	// Positive control: a non-terminal request still transitions.
	if err := st.InsertRequest(ctx, store.Request{
		ID: "r2", UserID: "u1", Title: "Other", Status: "pending",
	}); err != nil {
		t.Fatalf("insert r2: %v", err)
	}
	if err := st.UpdateRequestStatus(ctx, "r2", "acknowledged", "", ""); err != nil {
		t.Fatalf("update r2: %v", err)
	}
	if g, _ := st.GetRequest(ctx, "r2"); g.Status != "acknowledged" {
		t.Fatalf("non-terminal request did not transition: %q", g.Status)
	}
}

// Regression: a user must not be able to read or mutate another user's
// collection by guessing its id (IDOR). Guards AddCollectionItem,
// RemoveCollectionItem, DeleteCollection, ListCollectionItems.
func TestCollectionItemIDOR(t *testing.T) {
	st, ctx := newStore(t)

	const owner, attacker = "alice", "mallory"
	if err := st.CreateCollection(ctx, store.Collection{
		ID: "c1", UserID: owner, Name: "Alice's shelf",
	}); err != nil {
		t.Fatalf("create collection: %v", err)
	}

	// Attacker cannot write into Alice's collection.
	if err := st.AddCollectionItem(ctx, "c1", "bookX", attacker, ""); err != nil {
		t.Fatalf("add (gated, want silent no-op): %v", err)
	}
	if items, _ := st.ListCollectionItems(ctx, "c1", owner, ""); len(items) != 0 {
		t.Fatalf("attacker injected an item: %+v", items)
	}

	// Owner can.
	if err := st.AddCollectionItem(ctx, "c1", "bookX", owner, ""); err != nil {
		t.Fatalf("owner add: %v", err)
	}
	if items, _ := st.ListCollectionItems(ctx, "c1", owner, ""); len(items) != 1 {
		t.Fatalf("owner add did not take: %+v", items)
	}

	// Attacker cannot remove the owner's item.
	if err := st.RemoveCollectionItem(ctx, "c1", "bookX", attacker, ""); err != nil {
		t.Fatalf("remove (gated, want no-op): %v", err)
	}
	if items, _ := st.ListCollectionItems(ctx, "c1", owner, ""); len(items) != 1 {
		t.Fatalf("attacker removed owner's item: %+v", items)
	}

	// Attacker cannot delete the collection; owner can.
	if err := st.DeleteCollection(ctx, "c1", attacker, ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("attacker delete should be ErrNotFound, got %v", err)
	}
	if err := st.DeleteCollection(ctx, "c1", owner, ""); err != nil {
		t.Fatalf("owner delete: %v", err)
	}
}

// Regression: RevokeABSToken is user-scoped — revoking with the wrong owner
// must report not-found rather than silently succeeding. Backs the admin
// revoke handler's 404-on-unknown-token behaviour.
func TestRevokeABSTokenScoped(t *testing.T) {
	st, ctx := newStore(t)

	if err := st.InsertABSToken(ctx, store.ABSToken{
		ID: "tok1", UserID: "alice", JTI: "jti-1",
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("insert token: %v", err)
	}

	if err := st.RevokeABSToken(ctx, "tok1", "mallory"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoke with wrong owner should be ErrNotFound, got %v", err)
	}
	if err := st.RevokeABSToken(ctx, "tok1", "alice"); err != nil {
		t.Fatalf("revoke by owner: %v", err)
	}
	if err := st.RevokeABSToken(ctx, "missing", "alice"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoke of unknown id should be ErrNotFound, got %v", err)
	}
}
