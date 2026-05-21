package libsync

import (
	"context"
	"errors"
	"testing"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/backend"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
)

func i64(v int64) *int64 { return &v }

func TestReconcile_CreateMissing(t *testing.T) {
	out, st := Reconcile(nil,
		[]backend.LibraryInfo{{ID: 5, Name: "Series", MediaType: "podcast"}}, "42")
	if st.Created != 1 || st.Updated != 0 || st.Pruned != 0 || st.Kept != 0 {
		t.Fatalf("stats=%+v", st)
	}
	if len(out) != 1 || out[0].ID != 0 || out[0].Name != "Series" ||
		out[0].MediaType != "podcast" || out[0].BackendPluginID != "42" ||
		out[0].BackendLibraryID == nil || *out[0].BackendLibraryID != 5 ||
		!out[0].Enabled {
		t.Fatalf("created row wrong: %+v", out[0])
	}
}

func TestReconcile_DefaultsEmptyNameAndMediaType(t *testing.T) {
	out, _ := Reconcile(nil, []backend.LibraryInfo{{ID: 9}}, "42")
	if out[0].Name != "Library 9" || out[0].MediaType != "audiobook" {
		t.Fatalf("defaults wrong: %+v", out[0])
	}
}

func TestReconcile_UpdatePreservesOperatorFields(t *testing.T) {
	existing := []store.PortalLibrary{{
		ID: 7, Name: "Old", MediaType: "audiobook", BackendPluginID: "42",
		BackendLibraryID: i64(5), Enabled: false, SortOrder: 3,
	}}
	out, st := Reconcile(existing,
		[]backend.LibraryInfo{{ID: 5, Name: "Series", MediaType: "podcast"}}, "42")
	if st.Updated != 1 || st.Created != 0 || st.Pruned != 0 {
		t.Fatalf("stats=%+v", st)
	}
	g := out[0]
	if g.ID != 7 || g.Name != "Series" || g.MediaType != "podcast" ||
		g.Enabled != false || g.SortOrder != 3 || g.BackendLibraryID == nil || *g.BackendLibraryID != 5 {
		t.Fatalf("update must change only name/media_type: %+v", g)
	}
}

func TestReconcile_PruneGoneBackendDerived(t *testing.T) {
	existing := []store.PortalLibrary{{
		ID: 7, Name: "Gone", MediaType: "audiobook", BackendPluginID: "42",
		BackendLibraryID: i64(99), Enabled: true, SortOrder: 0,
	}}
	out, st := Reconcile(existing, []backend.LibraryInfo{{ID: 5, Name: "Keep"}}, "42")
	if st.Pruned != 1 || st.Created != 1 {
		t.Fatalf("stats=%+v", st)
	}
	for _, l := range out {
		if l.BackendLibraryID != nil && *l.BackendLibraryID == 99 {
			t.Fatal("pruned row must be omitted")
		}
	}
}

func TestReconcile_PassthroughUntouched(t *testing.T) {
	existing := []store.PortalLibrary{
		{ID: 1, Name: "Manual", MediaType: "audiobook", BackendPluginID: "42", BackendLibraryID: nil, Enabled: true, SortOrder: 0},
		{ID: 2, Name: "OtherBackend", MediaType: "podcast", BackendPluginID: "99", BackendLibraryID: i64(5), Enabled: true, SortOrder: 1},
	}
	out, st := Reconcile(existing, []backend.LibraryInfo{{ID: 5, Name: "X"}}, "42")
	if st.Kept != 2 || st.Pruned != 0 || st.Created != 1 {
		t.Fatalf("stats=%+v", st)
	}
	var sawManual, sawOther bool
	for _, l := range out {
		if l.ID == 1 {
			sawManual = true
		}
		if l.ID == 2 {
			sawOther = true
		}
	}
	if !sawManual || !sawOther {
		t.Fatal("non-managed rows must pass through unchanged")
	}
}

func TestReconcile_Idempotent(t *testing.T) {
	bl := []backend.LibraryInfo{{ID: 5, Name: "Series", MediaType: "podcast"}}
	out1, _ := Reconcile(nil, bl, "42")
	out1[0].ID = 7
	out2, st := Reconcile(out1, bl, "42")
	if st.Created != 0 || st.Updated != 0 || st.Pruned != 0 || st.Kept != 1 {
		t.Fatalf("second run must be a no-op: %+v", st)
	}
	if len(out2) != 1 || out2[0].ID != 7 {
		t.Fatalf("idempotent run changed rows: %+v", out2)
	}
}

func TestReconcile_DuplicateManagedNotSilentlyDropped(t *testing.T) {
	existing := []store.PortalLibrary{
		{ID: 1, Name: "A", MediaType: "audiobook", BackendPluginID: "42", BackendLibraryID: i64(5), Enabled: true, SortOrder: 0},
		{ID: 2, Name: "B", MediaType: "audiobook", BackendPluginID: "42", BackendLibraryID: i64(5), Enabled: true, SortOrder: 1},
	}
	out, st := Reconcile(existing, []backend.LibraryInfo{{ID: 5, Name: "Series", MediaType: "podcast"}}, "42")
	if st.Pruned != 1 {
		t.Fatalf("displaced duplicate must be counted as Pruned, stats=%+v", st)
	}
	n := 0
	for _, l := range out {
		if l.BackendLibraryID != nil && *l.BackendLibraryID == 5 {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want exactly 1 surviving row for backend lib 5, got %d (%+v)", n, out)
	}
}

type fakeStore struct {
	existing   []store.PortalLibrary
	replaced   bool
	replacedTo []store.PortalLibrary
}

func (f *fakeStore) ListPortalLibraries(_ context.Context, _ bool) ([]store.PortalLibrary, error) {
	return f.existing, nil
}

func (f *fakeStore) ReplacePortalLibraries(_ context.Context, libs []store.PortalLibrary) error {
	f.replaced = true
	f.replacedTo = libs
	return nil
}

type fakeLister struct {
	libs         []backend.LibraryInfo
	err          error
	gotBearer    string
	gotInstallID string
}

func (f *fakeLister) ListLibraries(_ context.Context, bearer, installID string) ([]backend.LibraryInfo, error) {
	f.gotBearer = bearer
	f.gotInstallID = installID
	return f.libs, f.err
}

func TestSync_GuardBackendErrorNoWrite(t *testing.T) {
	fs := &fakeStore{}
	_, err := Sync(context.Background(), fs, &fakeLister{err: errors.New("upstream down")}, "tok", "42")
	if err == nil {
		t.Fatal("expected error on backend fetch failure")
	}
	if fs.replaced {
		t.Fatal("store must NOT be written when the backend fetch failed")
	}
}

func TestSync_GuardZeroLibrariesNoWrite(t *testing.T) {
	fs := &fakeStore{existing: []store.PortalLibrary{{ID: 1, Name: "X", BackendPluginID: "42"}}}
	_, err := Sync(context.Background(), fs, &fakeLister{libs: nil}, "tok", "42")
	if err == nil {
		t.Fatal("expected error when backend returns zero libraries")
	}
	if fs.replaced {
		t.Fatal("store must NOT be written (catastrophic-prune guard)")
	}
}

func TestSync_HappyPathWritesReconciled(t *testing.T) {
	fs := &fakeStore{}
	fl := &fakeLister{libs: []backend.LibraryInfo{{ID: 5, Name: "Series", MediaType: "podcast"}}}
	stats, err := Sync(context.Background(), fs, fl, "tok-123", "42")
	if err != nil {
		t.Fatal(err)
	}
	if fl.gotBearer != "tok-123" || fl.gotInstallID != "42" {
		t.Fatalf("list libraries called with bearer=%q installID=%q", fl.gotBearer, fl.gotInstallID)
	}
	if !fs.replaced || len(fs.replacedTo) != 1 || stats.Created != 1 {
		t.Fatalf("expected one created row persisted; replaced=%v to=%+v stats=%+v", fs.replaced, fs.replacedTo, stats)
	}
}

func TestSync_EmptyBackendIDErrors(t *testing.T) {
	fs := &fakeStore{}
	if _, err := Sync(context.Background(), fs, &fakeLister{}, "tok", ""); err == nil {
		t.Fatal("empty backendID must error")
	}
	if fs.replaced {
		t.Fatal("no write on empty backendID")
	}
}
