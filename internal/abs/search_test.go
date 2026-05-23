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

// searchFixture mounts the ABS handler against a real Postgres + a fake
// backend httptest.Server. Lets us exercise the multi-bucket search
// response without standing up the full server.go wiring.
type searchFixture struct {
	t       *testing.T
	router  chi.Router
	store   *store.Store
	backend *httptest.Server
}

func newSearchFixture(t *testing.T, mediaType string, handler http.Handler) *searchFixture {
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
	if err := st.ReplacePortalLibraries(context.Background(), []store.PortalLibrary{{
		Name: "Lib", MediaType: mediaType, BackendPluginID: "11", Enabled: true,
	}}); err != nil {
		t.Fatalf("seed lib: %v", err)
	}

	host := httptest.NewServer(handler)
	t.Cleanup(host.Close)

	h := abs.NewHandler(abs.Deps{
		Store:   st,
		Backend: backend.NewClient(backend.NewHostClient(host.URL)),
		TargetFn: func(ctx context.Context) (string, store.BackendConfig, error) {
			cfg, err := st.GetBackendConfig(ctx)
			return cfg.TargetBackendPluginID, cfg, err
		},
		HostBaseFn: func() string { return host.URL },
		InstallID:  func() string { return "test-install" },
	})
	r := chi.NewRouter()
	h.Mount(r)
	return &searchFixture{t: t, router: r, store: st, backend: host}
}

// TestLibrarySearch_MultiBucketShape pins the response shape real ABS
// clients depend on: 5 named buckets (book, podcast, series, authors,
// tags), never null. An empty query returns all-empty buckets without
// hitting the backend.
func TestLibrarySearch_MultiBucketShape(t *testing.T) {
	f := newSearchFixture(t, "audiobook", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Catalog ?q= hit returns one match; series + authors return zero
		// to keep the test focused on the bucket shape.
		if strings.HasPrefix(r.URL.Path, "/api/v1/plugins/11/api/v1/catalog") {
			_, _ = w.Write([]byte(`{"items":[{"id":"b-1","title":"The Way of Kings"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))

	// Empty query — five buckets, never null, no backend hit needed.
	libs, _ := f.store.ListPortalLibraries(context.Background(), false)
	libID := libs[0].ID
	wEmpty := doSearch(f, libID, "")
	assertFiveBuckets(t, wEmpty)

	// Non-empty query — book bucket populated; other buckets remain
	// empty arrays, not nil.
	w := doSearch(f, libID, "way")
	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(w), &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, w)
	}
	for _, key := range []string{"book", "podcast", "series", "authors", "tags"} {
		if _, ok := got[key]; !ok {
			t.Errorf("missing bucket %q in response: %s", key, w)
		}
	}
	var books []map[string]any
	if err := json.Unmarshal(got["book"], &books); err != nil {
		t.Fatalf("unmarshal book bucket: %v", err)
	}
	if len(books) != 1 {
		t.Fatalf("book bucket = %d hits, want 1", len(books))
	}
	hit := books[0]
	if _, ok := hit["libraryItem"]; !ok {
		t.Errorf("book hit missing libraryItem: %+v", hit)
	}
	if hit["matchKey"] != "title" {
		t.Errorf("book hit matchKey = %v, want title", hit["matchKey"])
	}
}

// TestLibrarySearch_PodcastLibraryUsesLocalStore confirms podcast
// libraries don't hit the backend — they search the plugin's own
// podcast table. Operator-seeded podcasts surface in the bucket.
func TestLibrarySearch_PodcastLibraryUsesLocalStore(t *testing.T) {
	f := newSearchFixture(t, "podcast", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("backend hit on podcast library: %s", r.URL.Path)
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))

	libs, _ := f.store.ListPortalLibraries(context.Background(), false)
	libID := libs[0].ID

	// Seed a podcast — matches "show" substring search.
	if err := f.store.UpsertPodcast(context.Background(), store.Podcast{
		ID: "p-1", LibraryID: libID, Title: "Test Show", Author: "Host",
		RefreshIntervalMinutes: 360,
	}); err != nil {
		t.Fatalf("seed podcast: %v", err)
	}

	w := doSearch(f, libID, "show")
	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(w), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var podcasts []map[string]any
	if err := json.Unmarshal(got["podcast"], &podcasts); err != nil {
		t.Fatalf("unmarshal podcast bucket: %v", err)
	}
	if len(podcasts) != 1 {
		t.Fatalf("podcast bucket = %d hits, want 1; body=%s", len(podcasts), w)
	}
	if podcasts[0]["matchText"] != "Test Show" {
		t.Errorf("matchText = %v", podcasts[0]["matchText"])
	}
}

// doSearch issues one GET against the search route and returns the
// response body. Sends X-Silo-User-Id since /abs/api/* routes are
// behind the bearer-auth middleware.
func doSearch(f *searchFixture, libID int64, q string) string {
	tok := f.loginToken("u-test")
	url := "/abs/api/libraries/" + strconvI(libID) + "/search?q=" + q
	req := httptest.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	if w.Result().StatusCode != http.StatusOK {
		f.t.Fatalf("search status = %d body=%s", w.Result().StatusCode, w.Body.String())
	}
	return w.Body.String()
}

// loginToken mints an access token via the header path so search calls
// can authenticate. Mirrors the helper used by other ABS tests.
func (f *searchFixture) loginToken(userID string) string {
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
	if out.AccessToken == "" {
		f.t.Fatalf("login returned no token")
	}
	return out.AccessToken
}

func assertFiveBuckets(t *testing.T, body string) {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	for _, key := range []string{"book", "podcast", "series", "authors", "tags"} {
		v, ok := got[key]
		if !ok {
			t.Errorf("missing bucket %q", key)
			continue
		}
		if v == nil {
			t.Errorf("bucket %q must be empty array, not null", key)
		}
	}
}

// strconvI shortens strconv.FormatInt(_, 10) for in-test URL building.
func strconvI(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	digits := make([]byte, 0, 20)
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}
