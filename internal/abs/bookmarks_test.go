package abs_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/abs"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/backend"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/migrate"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/testutil"
)

// bookmarkFixture mounts the ABS handler against a real Postgres so
// the CRUD calls go through end-to-end (the bookmark store doesn't
// have a useful stub interface; just hit real Postgres).
type bookmarkFixture struct {
	t      *testing.T
	router chi.Router
	store  *store.Store
}

func newBookmarkFixture(t *testing.T) *bookmarkFixture {
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
	st := store.New(pool)
	if _, err := st.EnsureBackendConfig(context.Background(), jwtSecret); err != nil {
		t.Fatalf("ensure cfg: %v", err)
	}
	h := abs.NewHandler(abs.Deps{
		Store:   st,
		Backend: backend.NewClient(backend.NewHostClient("http://host")),
		TargetFn: func(ctx context.Context) (string, store.BackendConfig, error) {
			cfg, err := st.GetBackendConfig(ctx)
			return cfg.TargetBackendPluginID, cfg, err
		},
		HostBaseFn: func() string { return "http://host" },
		InstallID:  func() string { return "test-install" },
	})
	r := chi.NewRouter()
	h.Mount(r)
	return &bookmarkFixture{t: t, router: r, store: st}
}

// TestBookmarks_CreatePatchDelete walks the full lifecycle. Each step
// asserts the response is the updated bookmark list, which is what
// the mobile BookmarksModal reads to refresh.
func TestBookmarks_CreatePatchDelete(t *testing.T) {
	f := newBookmarkFixture(t)
	tok := f.login("u-1")

	// POST — create at time=120s with title "Cliffhanger".
	created := f.callJSON("POST", "/abs/api/me/item/book-x/bookmark", tok,
		`{"title":"Cliffhanger","time":120}`)
	if len(created) != 1 || created[0]["title"] != "Cliffhanger" {
		t.Fatalf("create response = %+v", created)
	}
	if got := int(created[0]["time"].(float64)); got != 120 {
		t.Errorf("time = %d, want 120", got)
	}

	// POST again at a different time — list now has two.
	twoBookmarks := f.callJSON("POST", "/abs/api/me/item/book-x/bookmark", tok,
		`{"title":"Second mark","time":600}`)
	if len(twoBookmarks) != 2 {
		t.Fatalf("after second create: %d bookmarks, want 2", len(twoBookmarks))
	}

	// PATCH — same time=120, new title. Bookmarks key on (user,item,time)
	// so this updates rather than creates.
	updated := f.callJSON("PATCH", "/abs/api/me/item/book-x/bookmark", tok,
		`{"title":"Cliffhanger v2","time":120}`)
	if len(updated) != 2 {
		t.Fatalf("patch grew the list to %d; should have stayed at 2", len(updated))
	}
	// Find the time=120 row and assert its title is the new value.
	var matched bool
	for _, b := range updated {
		if int(b["time"].(float64)) == 120 {
			if b["title"] != "Cliffhanger v2" {
				t.Errorf("PATCH did not update title: %v", b["title"])
			}
			matched = true
		}
	}
	if !matched {
		t.Errorf("PATCH lost the time=120 row: %+v", updated)
	}

	// DELETE — the time=120 row disappears.
	afterDelete := f.callJSON("DELETE", "/abs/api/me/item/book-x/bookmark/120", tok, "")
	if len(afterDelete) != 1 {
		t.Fatalf("after delete: %d bookmarks, want 1", len(afterDelete))
	}
	if int(afterDelete[0]["time"].(float64)) != 600 {
		t.Errorf("wrong bookmark survived delete: %+v", afterDelete)
	}

	// DELETE of a non-existent bookmark is idempotent (matches real ABS).
	if _ = f.callJSON("DELETE", "/abs/api/me/item/book-x/bookmark/999", tok, ""); t.Failed() {
		t.Errorf("idempotent delete returned an error")
	}
}

// TestBookmarks_PerUserIsolation guards against an IDOR — user A's
// POST to /me/item/X/bookmark must not surface in user B's list for
// the same item.
func TestBookmarks_PerUserIsolation(t *testing.T) {
	f := newBookmarkFixture(t)
	tokA := f.login("alice")
	tokB := f.login("bob")
	_ = f.callJSON("POST", "/abs/api/me/item/book-y/bookmark", tokA, `{"title":"A","time":50}`)

	got := f.callJSON("POST", "/abs/api/me/item/book-y/bookmark", tokB, `{"title":"B","time":75}`)
	if len(got) != 1 {
		t.Fatalf("Bob sees %d bookmarks; should only see his own (1)", len(got))
	}
	if got[0]["title"] != "B" {
		t.Errorf("Bob's list leaked Alice's row: %+v", got)
	}
}

// callJSON drives one bookmark route and decodes the ABS-shaped
// response array. Mostly bookkeeping around httptest.
func (f *bookmarkFixture) callJSON(method, path, tok, body string) []map[string]any {
	f.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	if w.Result().StatusCode != http.StatusOK {
		f.t.Fatalf("%s %s = %d body=%s", method, path, w.Result().StatusCode, w.Body.String())
	}
	var out []map[string]any
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		f.t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	return out
}

func (f *bookmarkFixture) login(userID string) string {
	f.t.Helper()
	req := httptest.NewRequest("POST", "/abs/api/login", nil)
	req.Header.Set("X-Silo-User-Id", userID)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	if w.Result().StatusCode != 200 {
		f.t.Fatalf("login: %d body=%s", w.Result().StatusCode, w.Body.String())
	}
	var out struct {
		AccessToken string `json:"accessToken"`
	}
	_ = json.NewDecoder(w.Body).Decode(&out)
	return out.AccessToken
}
