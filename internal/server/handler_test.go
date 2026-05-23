package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/backend"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/migrate"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/server"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/store"
	"github.com/RXWatcher/silo-plugin-audiobooks/internal/testutil"
)

// liveServer builds a Server backed by a real migrated Postgres.
func liveServer(t *testing.T) (http.Handler, *store.Store) {
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
	return server.New(server.Deps{Store: st}).Handler(), st
}

func liveServerWithBackend(t *testing.T, bk *backend.Client) (http.Handler, *store.Store) {
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
	return server.New(server.Deps{Store: st, Backend: bk}).Handler(), st
}

// brokenServer builds a Server whose store cannot reach its database, so
// every store call errors — exercising the writeInternal path. No docker.
func brokenServer(t *testing.T) http.Handler {
	t.Helper()
	pool, err := pgxpool.New(context.Background(),
		"postgres://x:x@127.0.0.1:1/x?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return server.New(server.Deps{Store: store.New(pool)}).Handler()
}

func req(method, path string, hdr map[string]string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

func jsonReq(method, path string, hdr map[string]string, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

var (
	asUser  = map[string]string{"X-Silo-User-Id": "alice", "X-Silo-User-Role": "user"}
	asAdmin = map[string]string{"X-Silo-User-Id": "root", "X-Silo-User-Role": "admin"}
	// asUserKids is alice acting under a non-primary profile. The host proxy
	// stamps the active profile as X-Silo-Profile-Id; an empty value
	// (asUser) means the primary profile.
	asUserKids = map[string]string{
		"X-Silo-User-Id":    "alice",
		"X-Silo-User-Role":  "user",
		"X-Silo-Profile-Id": "kids",
	}
)

func TestAuthGating(t *testing.T) {
	h, _ := liveServer(t)

	// User route: no identity -> 401, valid user -> 200.
	if w := do(h, req("GET", "/api/v1/me/requests", nil)); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated me/requests = %d, want 401", w.Code)
	}
	if w := do(h, req("GET", "/api/v1/me/requests", asUser)); w.Code != http.StatusOK {
		t.Fatalf("user me/requests = %d body=%s, want 200", w.Code, w.Body)
	}

	// Admin route: no identity -> 401, non-admin user -> 403.
	if w := do(h, req("POST", "/api/v1/admin/tokens/x/revoke", nil)); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated admin = %d, want 401", w.Code)
	}
	if w := do(h, req("POST", "/api/v1/admin/tokens/x/revoke", asUser)); w.Code != http.StatusForbidden {
		t.Fatalf("non-admin admin route = %d, want 403", w.Code)
	}
}

func TestAdminRevokeToken(t *testing.T) {
	h, st := liveServer(t)
	ctx := context.Background()

	// Unknown token id -> 404, not a misleading 204.
	w := do(h, req("POST", "/api/v1/admin/tokens/does-not-exist/revoke", asAdmin))
	if w.Code != http.StatusNotFound {
		t.Fatalf("revoke unknown = %d, want 404", w.Code)
	}

	if err := st.InsertABSToken(ctx, store.ABSToken{
		ID: "tok1", UserID: "alice", JTI: "jti-1",
		ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("insert token: %v", err)
	}
	if w := do(h, req("POST", "/api/v1/admin/tokens/tok1/revoke", asAdmin)); w.Code != http.StatusNoContent {
		t.Fatalf("revoke existing = %d body=%s, want 204", w.Code, w.Body)
	}
}

func TestWebPlaybackSessionLifecycle(t *testing.T) {
	h, st := liveServer(t)
	ctx := context.Background()

	w := do(h, req("POST", "/api/v1/audiobooks/book1/playback-session", asUser))
	if w.Code != http.StatusCreated {
		t.Fatalf("create session = %d body=%s, want 201", w.Code, w.Body)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.ID == "" {
		t.Fatalf("create response missing id")
	}
	sess, err := st.GetABSSession(ctx, created.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.UserID != "alice" || sess.BookID != "book1" || sess.MediaPlayer != "silo-web" {
		t.Fatalf("unexpected session: %+v", sess)
	}

	w = do(h, jsonReq("PATCH", "/api/v1/playback-sessions/"+created.ID, asUser, `{"current_seconds":42,"duration_seconds":100}`))
	if w.Code != http.StatusOK {
		t.Fatalf("sync session = %d body=%s, want 200", w.Code, w.Body)
	}
	if sess, err = st.GetABSSession(ctx, created.ID); err != nil || sess.CurrentTime != 42 {
		t.Fatalf("session current time = %+v err=%v, want 42", sess, err)
	}
	p, err := st.GetProgress(ctx, "alice", "", "book1")
	if err != nil {
		t.Fatalf("get progress: %v", err)
	}
	if p.CurrentSeconds != 42 || p.ProgressPct < 0.41 || p.ProgressPct > 0.43 || p.IsFinished {
		t.Fatalf("unexpected progress after sync: %+v", p)
	}

	w = do(h, jsonReq("POST", "/api/v1/playback-sessions/"+created.ID+"/close", asUser, `{"current_seconds":96,"duration_seconds":100}`))
	if w.Code != http.StatusNoContent {
		t.Fatalf("close session = %d body=%s, want 204", w.Code, w.Body)
	}
	p, err = st.GetProgress(ctx, "alice", "", "book1")
	if err != nil {
		t.Fatalf("get progress after close: %v", err)
	}
	if p.CurrentSeconds != 96 || !p.IsFinished {
		t.Fatalf("final close did not mark finished: %+v", p)
	}
}

func TestWebPlaybackSessionOwnershipAndClosedGuards(t *testing.T) {
	h, st := liveServer(t)
	ctx := context.Background()

	if err := st.InsertABSSession(ctx, store.ABSSession{
		ID: "sess1", UserID: "alice", BookID: "book1", DeviceID: "web", MediaPlayer: "silo-web",
	}); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	mallory := map[string]string{"X-Silo-User-Id": "mallory", "X-Silo-User-Role": "user"}
	w := do(h, jsonReq("PATCH", "/api/v1/playback-sessions/sess1", mallory, `{"current_seconds":10}`))
	if w.Code != http.StatusNotFound {
		t.Fatalf("wrong-owner sync = %d body=%s, want 404", w.Code, w.Body)
	}

	if err := st.CloseABSSession(ctx, "sess1"); err != nil {
		t.Fatalf("close session: %v", err)
	}
	w = do(h, jsonReq("PATCH", "/api/v1/playback-sessions/sess1", asUser, `{"current_seconds":10}`))
	if w.Code != http.StatusConflict {
		t.Fatalf("closed sync = %d body=%s, want 409", w.Code, w.Body)
	}
}

func TestWebPlaybackSessionPositionOnlySyncPreservesFinished(t *testing.T) {
	h, st := liveServer(t)
	ctx := context.Background()

	if err := st.InsertABSSession(ctx, store.ABSSession{
		ID: "sess1", UserID: "alice", BookID: "book1", DeviceID: "web", MediaPlayer: "silo-web",
	}); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if err := st.UpsertProgress(ctx, store.Progress{
		UserID: "alice", BookID: "book1", CurrentSeconds: 98, ProgressPct: 0.98, IsFinished: true,
	}); err != nil {
		t.Fatalf("seed progress: %v", err)
	}

	w := do(h, jsonReq("PATCH", "/api/v1/playback-sessions/sess1", asUser, `{"current_seconds":12}`))
	if w.Code != http.StatusOK {
		t.Fatalf("position-only sync = %d body=%s, want 200", w.Code, w.Body)
	}
	p, err := st.GetProgress(ctx, "alice", "", "book1")
	if err != nil {
		t.Fatalf("get progress: %v", err)
	}
	if p.CurrentSeconds != 12 || p.ProgressPct != 0.98 || !p.IsFinished {
		t.Fatalf("position-only sync changed finished fields: %+v", p)
	}
}

func TestWebPlaybackSessionStatsAndUserSessionList(t *testing.T) {
	h, st := liveServer(t)
	ctx := context.Background()

	if err := st.InsertABSSession(ctx, store.ABSSession{
		ID: "sess1", UserID: "alice", BookID: "book1", DeviceID: "web", MediaPlayer: "silo-web",
	}); err != nil {
		t.Fatalf("insert alice session: %v", err)
	}
	if err := st.InsertABSSession(ctx, store.ABSSession{
		ID: "sess2", UserID: "mallory", BookID: "book2", DeviceID: "web", MediaPlayer: "silo-web",
	}); err != nil {
		t.Fatalf("insert mallory session: %v", err)
	}

	w := do(h, req("GET", "/api/v1/me/playback-sessions", asUser))
	if w.Code != http.StatusOK {
		t.Fatalf("list sessions = %d body=%s, want 200", w.Code, w.Body)
	}
	var listed struct {
		Items []store.ABSSession `json:"items"`
	}
	if err := json.NewDecoder(w.Body).Decode(&listed); err != nil {
		t.Fatalf("decode sessions: %v", err)
	}
	if len(listed.Items) != 1 || listed.Items[0].ID != "sess1" {
		t.Fatalf("listed wrong sessions: %+v", listed.Items)
	}

	w = do(h, jsonReq("PATCH", "/api/v1/playback-sessions/sess1", asUser, `{"current_seconds":30,"time_listened_seconds":7}`))
	if w.Code != http.StatusOK {
		t.Fatalf("sync stats = %d body=%s, want 200", w.Code, w.Body)
	}
	w = do(h, req("GET", "/api/v1/me/listening-stats/book1", asUser))
	if w.Code != http.StatusOK {
		t.Fatalf("get stats = %d body=%s, want 200", w.Code, w.Body)
	}
	var stats store.ListeningStats
	if err := json.NewDecoder(w.Body).Decode(&stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if stats.ListenedSeconds != 7 || stats.LastPosition != 30 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestAdminSyncLibraries(t *testing.T) {
	var (
		mu      sync.Mutex
		gotAuth string
		gotPath string
	)
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		mu.Unlock()
		if r.URL.Path != "/api/v1/plugins/11/api/v1/catalog/libraries" {
			// FailNow inside a server goroutine only stops this goroutine,
			// not the test. Reply 500 and let the test thread observe.
			http.Error(w, "unexpected path", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"id":5,"name":"Fiction","media_type":"audiobook"}]}`))
	}))
	defer host.Close()

	h, st := liveServerWithBackend(t, backend.NewClient(backend.NewHostClient(host.URL)))
	ctx := context.Background()
	if err := st.ReplacePortalLibraries(ctx, []store.PortalLibrary{{
		ID: 0, Name: "Manual shelf", MediaType: "audiobook", BackendPluginID: "other", Enabled: true, SortOrder: 0,
	}}); err != nil {
		t.Fatalf("seed libraries: %v", err)
	}

	headers := map[string]string{
		"X-Silo-User-Id":   "root",
		"X-Silo-User-Role": "admin",
		"Authorization":         "Bearer admin-token",
	}
	w := do(h, req("POST", "/api/v1/admin/libraries/sync?backend_plugin_id=11", headers))
	if w.Code != http.StatusOK {
		mu.Lock()
		path := gotPath
		mu.Unlock()
		t.Fatalf("sync libraries = %d path-seen=%q body=%s, want 200", w.Code, path, w.Body)
	}
	mu.Lock()
	auth := gotAuth
	mu.Unlock()
	if auth != "Bearer admin-token" {
		t.Fatalf("backend auth = %q, want forwarded bearer", auth)
	}
	var stats struct {
		Created int `json:"created"`
		Updated int `json:"updated"`
		Pruned  int `json:"pruned"`
		Kept    int `json:"kept"`
	}
	if err := json.NewDecoder(w.Body).Decode(&stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if stats.Created != 1 || stats.Kept != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	libs, err := st.ListPortalLibraries(ctx, false)
	if err != nil {
		t.Fatalf("list libraries: %v", err)
	}
	if len(libs) != 2 {
		t.Fatalf("libraries count = %d, want 2", len(libs))
	}
}

func TestListAudiobooks_UsesServiceTokenForCookieAuthenticatedUser(t *testing.T) {
	var (
		mu      sync.Mutex
		gotAuth string
		gotPath string
	)
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		mu.Unlock()
		if r.URL.Path != "/api/v1/plugins/47/api/v1/catalog" {
			http.Error(w, "unexpected path", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"id":"bw-1","title":"Isolation"}],"total":1}`))
	}))
	defer host.Close()

	h, st := liveServerWithBackend(t, backend.NewClient(backend.NewHostClient(host.URL).WithServiceToken("svc-token")))
	ctx := context.Background()
	if _, err := st.EnsureBackendConfig(ctx, []byte("0123456789abcdef0123456789abcdef")); err != nil {
		t.Fatalf("ensure backend config: %v", err)
	}
	if err := st.ReplacePortalLibraries(ctx, []store.PortalLibrary{{
		Name:            "Audiobooks",
		MediaType:       "audiobook",
		BackendPluginID: "47",
		Enabled:         true,
		SortOrder:       0,
	}}); err != nil {
		t.Fatalf("seed libraries: %v", err)
	}
	libs, err := st.ListPortalLibraries(ctx, false)
	if err != nil {
		t.Fatalf("list libraries: %v", err)
	}
	if len(libs) != 1 {
		t.Fatalf("libraries count = %d, want 1", len(libs))
	}

	w := do(h, req("GET", "/api/v1/audiobooks?limit=1&library_id="+strconv.FormatInt(libs[0].ID, 10), asUser))
	if w.Code != http.StatusOK {
		mu.Lock()
		path := gotPath
		mu.Unlock()
		t.Fatalf("list audiobooks = %d path-seen=%q body=%s, want 200", w.Code, path, w.Body)
	}
	mu.Lock()
	auth := gotAuth
	mu.Unlock()
	if auth != "Bearer svc-token" {
		t.Fatalf("backend auth = %q, want service token fallback", auth)
	}
	var out struct {
		Items []backend.AudiobookSummary `json:"items"`
	}
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(out.Items) != 1 || out.Items[0].Title != "Isolation" {
		t.Fatalf("unexpected catalog response: %+v", out.Items)
	}
}

func TestAdminSyncLibraries_GuardsBackendFailures(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream failed", http.StatusUnauthorized)
	}))
	defer host.Close()

	h, st := liveServerWithBackend(t, backend.NewClient(backend.NewHostClient(host.URL)))
	ctx := context.Background()
	seed := []store.PortalLibrary{{
		ID: 0, Name: "Existing shelf", MediaType: "audiobook", BackendPluginID: "11", Enabled: true, SortOrder: 0,
	}}
	if err := st.ReplacePortalLibraries(ctx, seed); err != nil {
		t.Fatalf("seed libraries: %v", err)
	}

	headers := map[string]string{
		"X-Silo-User-Id":   "root",
		"X-Silo-User-Role": "admin",
		"Authorization":         "Bearer admin-token",
	}
	w := do(h, req("POST", "/api/v1/admin/libraries/sync?backend_plugin_id=11", headers))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("sync libraries failure = %d body=%s, want 502", w.Code, w.Body)
	}
	libs, err := st.ListPortalLibraries(ctx, false)
	if err != nil {
		t.Fatalf("list libraries: %v", err)
	}
	if len(libs) != 1 || libs[0].Name != "Existing shelf" {
		t.Fatalf("libraries changed after failed sync: %+v", libs)
	}
}

// The fix's core promise: a store/backend failure returns an opaque body to
// the client while the real error is logged server-side.
func TestWriteInternalOpacity(t *testing.T) {
	var logbuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logbuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	h := brokenServer(t)
	w := do(h, req("GET", "/api/v1/me/requests", asUser))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "internal error") {
		t.Fatalf("body not opaque: %s", body)
	}
	for _, leak := range []string{"127.0.0.1", "connection refused", "dial", "pgx", "SQLSTATE", "list user requests"} {
		if strings.Contains(body, leak) {
			t.Fatalf("client body leaked internal detail %q: %s", leak, body)
		}
	}
	// The real error must still be captured server-side for triage.
	log := logbuf.String()
	if !strings.Contains(log, "internal error") || !strings.Contains(log, "/api/v1/me/requests") {
		t.Fatalf("error not logged with request context: %s", log)
	}
	if !strings.Contains(log, "err=") {
		t.Fatalf("underlying error not logged: %s", log)
	}
}

// A write made under a non-primary profile must be stored against that
// profile, so the same profile reads it back and the primary profile does
// not. The store filters every query by profile_id; this guards the handler
// layer, which is what must stamp the active profile onto each insert.
func TestProfileScopedWritesAreIsolatedByProfile(t *testing.T) {
	h, _ := liveServer(t)

	// itemsLen GETs path as the given identity and returns the length of
	// the JSON {"items":[...]} envelope every list endpoint here returns.
	itemsLen := func(path string, who map[string]string) int {
		t.Helper()
		w := do(h, req("GET", path, who))
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s = %d body=%s, want 200", path, w.Code, w.Body)
		}
		var out struct {
			Items []json.RawMessage `json:"items"`
		}
		if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		return len(out.Items)
	}

	t.Run("collections", func(t *testing.T) {
		w := do(h, jsonReq("POST", "/api/v1/me/collections", asUserKids, `{"name":"Bedtime stories"}`))
		if w.Code != http.StatusCreated {
			t.Fatalf("create collection = %d body=%s, want 201", w.Code, w.Body)
		}
		if n := itemsLen("/api/v1/me/collections", asUserKids); n != 1 {
			t.Fatalf("kids profile sees %d collections, want 1 (its own write)", n)
		}
		if n := itemsLen("/api/v1/me/collections", asUser); n != 0 {
			t.Fatalf("primary profile sees %d collections, want 0", n)
		}
	})

	t.Run("progress", func(t *testing.T) {
		w := do(h, jsonReq("PATCH", "/api/v1/audiobooks/book1/progress", asUserKids, `{"current_seconds":30,"progress_pct":0.3}`))
		if w.Code != http.StatusOK {
			t.Fatalf("upsert progress = %d body=%s, want 200", w.Code, w.Body)
		}
		if n := itemsLen("/api/v1/me/progress", asUserKids); n != 1 {
			t.Fatalf("kids profile sees %d progress rows, want 1 (its own write)", n)
		}
		if n := itemsLen("/api/v1/me/progress", asUser); n != 0 {
			t.Fatalf("primary profile sees %d progress rows, want 0", n)
		}
	})

	t.Run("bookmarks", func(t *testing.T) {
		w := do(h, jsonReq("POST", "/api/v1/audiobooks/book1/bookmarks", asUserKids, `{"position_seconds":42}`))
		if w.Code != http.StatusCreated {
			t.Fatalf("create bookmark = %d body=%s, want 201", w.Code, w.Body)
		}
		if n := itemsLen("/api/v1/audiobooks/book1/bookmarks", asUserKids); n != 1 {
			t.Fatalf("kids profile sees %d bookmarks, want 1 (its own write)", n)
		}
		if n := itemsLen("/api/v1/audiobooks/book1/bookmarks", asUser); n != 0 {
			t.Fatalf("primary profile sees %d bookmarks, want 0", n)
		}
	})

	t.Run("playback sessions", func(t *testing.T) {
		w := do(h, req("POST", "/api/v1/audiobooks/book1/playback-session", asUserKids))
		if w.Code != http.StatusCreated {
			t.Fatalf("create playback session = %d body=%s, want 201", w.Code, w.Body)
		}
		if n := itemsLen("/api/v1/me/playback-sessions", asUserKids); n != 1 {
			t.Fatalf("kids profile sees %d playback sessions, want 1 (its own write)", n)
		}
		if n := itemsLen("/api/v1/me/playback-sessions", asUser); n != 0 {
			t.Fatalf("primary profile sees %d playback sessions, want 0", n)
		}
	})
}

func do(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
