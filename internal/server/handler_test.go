package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/migrate"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/server"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/testutil"
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
	asUser  = map[string]string{"X-Continuum-User-Id": "alice", "X-Continuum-User-Role": "user"}
	asAdmin = map[string]string{"X-Continuum-User-Id": "root", "X-Continuum-User-Role": "admin"}
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
	if sess.UserID != "alice" || sess.BookID != "book1" || sess.MediaPlayer != "continuum-web" {
		t.Fatalf("unexpected session: %+v", sess)
	}

	w = do(h, jsonReq("PATCH", "/api/v1/playback-sessions/"+created.ID, asUser, `{"current_seconds":42,"duration_seconds":100}`))
	if w.Code != http.StatusOK {
		t.Fatalf("sync session = %d body=%s, want 200", w.Code, w.Body)
	}
	if sess, err = st.GetABSSession(ctx, created.ID); err != nil || sess.CurrentTime != 42 {
		t.Fatalf("session current time = %+v err=%v, want 42", sess, err)
	}
	p, err := st.GetProgress(ctx, "alice", "book1")
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
	p, err = st.GetProgress(ctx, "alice", "book1")
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
		ID: "sess1", UserID: "alice", BookID: "book1", DeviceID: "web", MediaPlayer: "continuum-web",
	}); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	mallory := map[string]string{"X-Continuum-User-Id": "mallory", "X-Continuum-User-Role": "user"}
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
		ID: "sess1", UserID: "alice", BookID: "book1", DeviceID: "web", MediaPlayer: "continuum-web",
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
	p, err := st.GetProgress(ctx, "alice", "book1")
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
		ID: "sess1", UserID: "alice", BookID: "book1", DeviceID: "web", MediaPlayer: "continuum-web",
	}); err != nil {
		t.Fatalf("insert alice session: %v", err)
	}
	if err := st.InsertABSSession(ctx, store.ABSSession{
		ID: "sess2", UserID: "mallory", BookID: "book2", DeviceID: "web", MediaPlayer: "continuum-web",
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

func do(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
