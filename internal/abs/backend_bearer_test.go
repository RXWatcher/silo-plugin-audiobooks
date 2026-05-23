package abs_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/abs"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/backend"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/migrate"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/testutil"
)

// TestBackendCalls_DoNotForwardABSBearer guards the standalone-port flow.
//
// The ABS access JWT is signed with the plugin's own ABSJWTSecret; the
// silo host can't validate it and will 401 every backend call routed
// through the host's plugin proxy. Read-side handlers (catalog/browse/
// personalized/etc.) MUST call the backend with an empty bearer so the
// HostClient's service-token fallback fires. Reintroducing `a.Token` here
// silently reverts the symptom this test reproduces: library tiles render
// from the local store but authors / series / items / personalized shelves
// all come back empty.
func TestBackendCalls_DoNotForwardABSBearer(t *testing.T) {
	dsn := testutil.StartPG(t)
	if err := migrate.Run(context.Background(), dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(func() { pool.Close() })
	st := store.New(pool)
	if _, err := st.EnsureBackendConfig(context.Background(), jwtSecret); err != nil {
		t.Fatalf("ensure cfg: %v", err)
	}

	// Seed a single enabled portal library so handleLibrary* handlers find
	// a target row to call the backend against.
	if err := st.ReplacePortalLibraries(context.Background(), []store.PortalLibrary{{
		Name:            "Audiobooks",
		MediaType:       "audiobook",
		BackendPluginID: "99",
		Enabled:         true,
	}}); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	libs, err := st.ListPortalLibraries(context.Background(), true)
	if err != nil || len(libs) != 1 {
		t.Fatalf("list libraries: %v len=%d", err, len(libs))
	}
	libID := libs[0].ID

	const svcToken = "service-token-for-test"
	var (
		mu      sync.Mutex
		seenBy  = map[string][]string{} // path → Authorization headers observed
		jsonOK  = []byte(`{"items":[],"total":0}`)
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenBy[r.URL.Path] = append(seenBy[r.URL.Path], r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jsonOK)
	}))
	t.Cleanup(upstream.Close)

	hc := backend.NewHostClient(upstream.URL).WithServiceToken(svcToken)
	h := abs.NewHandler(abs.Deps{
		Store:   st,
		Backend: backend.NewClient(hc),
		TargetFn: func(ctx context.Context) (string, store.BackendConfig, error) {
			cfg, err := st.GetBackendConfig(ctx)
			return cfg.TargetBackendPluginID, cfg, err
		},
		HostBaseFn: func() string { return upstream.URL },
		InstallID:  func() string { return "test-install" },
	})
	r := chi.NewRouter()
	h.Mount(r)

	// Mint an ABS access JWT the way real /login does, then register it in
	// the token store so bearerAuth accepts it (it looks up the JTI and
	// rejects anything not present or revoked).
	const userID = "u-1"
	const jti = "jti-test"
	access, err := abs.IssueAccessToken(jwtSecret, userID, "", jti, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if err := st.InsertABSToken(context.Background(), store.ABSToken{
		ID: jti, UserID: userID, JTI: jti,
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("insert token: %v", err)
	}

	// One read-side endpoint per backend method we forward (browse /
	// catalog / detail / search). If any handler reverts to passing
	// a.Token, the upstream server records the ABS JWT instead of the
	// service token and we catch it below.
	id := strconv.FormatInt(libID, 10)
	paths := []string{
		"/api/libraries/" + id + "/authors",
		"/api/libraries/" + id + "/series",
		"/api/libraries/" + id + "/items",
		"/api/libraries/" + id + "/search?q=anything",
		"/api/libraries/" + id + "/personalized",
	}
	for _, p := range paths {
		req := httptest.NewRequest("GET", p, nil)
		req.Header.Set("Authorization", "Bearer "+access)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Result().StatusCode >= 500 {
			t.Fatalf("%s: status=%d body=%s", p, w.Result().StatusCode, w.Body.String())
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seenBy) == 0 {
		t.Fatalf("upstream backend was never called; routes may have changed")
	}
	for path, auths := range seenBy {
		for _, got := range auths {
			if got == "Bearer "+access {
				t.Fatalf("backend call to %s forwarded the ABS bearer; "+
					"call sites must pass \"\" so the service token fallback fires", path)
			}
			if !strings.EqualFold(got, "Bearer "+svcToken) {
				t.Fatalf("backend call to %s got Authorization=%q, want %q",
					path, got, "Bearer "+svcToken)
			}
		}
	}
}

