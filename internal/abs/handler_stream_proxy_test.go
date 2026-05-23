package abs_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/abs"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/backend"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/bookref"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/migrate"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/testutil"
)

// streamFixture wires the ABS handler against a real Postgres + a fake
// backend httptest.Server. The fake stands in for the bookwarehouse-audio
// stream route and lets us assert what bytes the plugin proxies through.
type streamFixture struct {
	t      *testing.T
	router chi.Router
	store  *store.Store
	pool   *pgxpool.Pool
	host   *httptest.Server
	// gotReq holds the last request received by the fake host. Tests assert
	// on path, method, Range header, and token query.
	gotReq *http.Request
}

func newStreamFixture(t *testing.T) *streamFixture {
	t.Helper()
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
	// Seed a media_signing_secret so the handler can mint a backend token.
	cfg, _ := st.GetBackendConfig(context.Background())
	cfg.MediaSigningSecret = "this-is-32-bytes-of-raw-secret!!"
	if err := st.UpdateBackendConfig(context.Background(), cfg); err != nil {
		t.Fatalf("update cfg: %v", err)
	}

	f := &streamFixture{t: t, store: st, pool: pool}
	f.host = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capture for assertions in the test thread. httptest.Server.Close
		// joins the handler goroutines, so this is safe to read after
		// the test's GET returns.
		f.gotReq = r.Clone(r.Context())

		if r.URL.Query().Get("token") == "" {
			http.Error(w, "media token required", http.StatusUnauthorized)
			return
		}
		// Range-aware happy path. If the caller sent Range: bytes=N-, return
		// 206 with Content-Range; otherwise return the full body as 200.
		body := []byte("audio-bytes-0123456789")
		if rng := r.Header.Get("Range"); strings.HasPrefix(rng, "bytes=") {
			// Tests only exercise the "no-skip" range form (bytes=0-).
			// A real backend would slice the body.
			w.Header().Set("Content-Type", "audio/mp4")
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Range", "bytes 0-21/22")
			w.Header().Set("Content-Length", "22")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(body)
			return
		}
		w.Header().Set("Content-Type", "audio/mp4")
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", "22")
		_, _ = w.Write(body)
	}))
	t.Cleanup(f.host.Close)

	// Wire a portal library that points its backend ID at the path the
	// fake host accepts. The HostClient is built against f.host.URL so it
	// targets our fake; we deliberately don't set a runtimeHost so the
	// streaming path takes the HTTP fallback (which is what GetStream uses
	// regardless of runtimeHost).
	if err := st.ReplacePortalLibraries(context.Background(), []store.PortalLibrary{{
		ID: 0, Name: "Library", MediaType: "audiobook", BackendPluginID: "11", Enabled: true,
	}}); err != nil {
		t.Fatalf("seed libraries: %v", err)
	}

	h := abs.NewHandler(abs.Deps{
		Store:   st,
		Backend: backend.NewClient(backend.NewHostClient(f.host.URL)),
		TargetFn: func(ctx context.Context) (string, store.BackendConfig, error) {
			cfg, err := st.GetBackendConfig(ctx)
			return cfg.TargetBackendPluginID, cfg, err
		},
		HostBaseFn: func() string { return f.host.URL },
		InstallID:  func() string { return "test-install" },
	})
	r := chi.NewRouter()
	h.Mount(r)
	f.router = r
	return f
}

// TestPublicTrack_ProxiesAudioBytes confirms that hitting the session-scoped
// public track URL streams bytes back to the caller (no redirect) and
// preserves the upstream Content-Type / Content-Length. This is the path
// ABS clients connected to the standalone listener actually take.
func TestPublicTrack_ProxiesAudioBytes(t *testing.T) {
	f := newStreamFixture(t)

	// Seed a session that handlePublicTrack can resolve. The book id format
	// matches what portalLibraryForBookRef decodes — "libID:base64(bookID)".
	sess := store.ABSSession{
		ID: "sess-1", UserID: "u-1",
		BookID:   encodeBookRef("book-1", 1), // helper below
		DeviceID: "dev",
	}
	if err := f.store.InsertABSSession(context.Background(), sess); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	// Mint a session JWT the same way handlePlay would. ABS uses 1-based
	// wire indexing for audio tracks (LibraryItemController.js:500); the
	// handler translates back to the backend's 0-based file_idx in the
	// upstream URL, so the URL+claim pair below uses 1 here and the
	// backend assertion later expects file_idx=0 in the upstream path.
	cfg, _ := f.store.GetBackendConfig(context.Background())
	tok, err := abs.IssueSessionToken(cfg.ABSJWTSecret, "u-1", "sess-1", sess.BookID, 1, time.Hour)
	if err != nil {
		t.Fatalf("issue session token: %v", err)
	}

	req := httptest.NewRequest("GET",
		"/abs/public/session/sess-1/track/1?token="+tok, nil)
	req.Header.Set("Range", "bytes=0-")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)

	if w.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206; body=%q", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "audio/mp4" {
		t.Errorf("Content-Type = %q, want audio/mp4", got)
	}
	if got := w.Header().Get("Content-Range"); got != "bytes 0-21/22" {
		t.Errorf("Content-Range = %q", got)
	}
	body, _ := io.ReadAll(w.Result().Body)
	if string(body) != "audio-bytes-0123456789" {
		t.Errorf("body = %q", body)
	}

	// Confirm we forwarded the inbound Range to the backend.
	if f.gotReq == nil {
		t.Fatal("backend never received a request")
	}
	if rng := f.gotReq.Header.Get("Range"); rng != "bytes=0-" {
		t.Errorf("backend Range = %q, want bytes=0-", rng)
	}
	// And that we passed through the media token in the backend URL.
	if f.gotReq.URL.Query().Get("token") == "" {
		t.Errorf("backend received no media token; URL=%q", f.gotReq.URL.String())
	}
	// Wire idx 1 (what the mobile app sends per ABS convention) must hit
	// the backend at file_idx 0 (the backend's own indexing). Spinner-
	// forever bug: emitting 0 on the wire makes the mobile player do
	// `0 || 1` and fetch /track/1, which then asked the backend for a
	// file that doesn't exist. Lock the translation here so a future
	// edit can't quietly re-emit the wire index 1-to-1 to the backend.
	if got := f.gotReq.URL.Path; !strings.HasSuffix(got, "/0") {
		t.Errorf("backend path = %q, want it to end with /0 (wire idx 1 → backend idx 0)", got)
	}
}

// TestPublicTrack_RejectsSessionForOtherUser is a regression guard around
// the IDOR check that the proxy must not bypass. Even when the upstream
// would happily serve bytes for a valid media token, the session-token
// claims must agree with the session row.
func TestPublicTrack_RejectsSessionForOtherUser(t *testing.T) {
	f := newStreamFixture(t)
	sess := store.ABSSession{
		ID: "sess-mallory", UserID: "u-alice",
		BookID:   encodeBookRef("book-x", 1),
		DeviceID: "dev",
	}
	if err := f.store.InsertABSSession(context.Background(), sess); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	cfg, _ := f.store.GetBackendConfig(context.Background())
	// Token says u-mallory; session row says u-alice. URL/claim use the
	// 1-based wire index that real ABS clients send.
	tok, _ := abs.IssueSessionToken(cfg.ABSJWTSecret, "u-mallory", "sess-mallory", sess.BookID, 1, time.Hour)
	req := httptest.NewRequest("GET",
		"/abs/public/session/sess-mallory/track/1?token="+tok, nil)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if f.gotReq != nil {
		t.Errorf("backend should not have been called on IDOR rejection")
	}
}

// encodeBookRef shortens bookref.Encode for the call sites above.
func encodeBookRef(backendID string, libID int64) string {
	return bookref.Encode(libID, backendID)
}
