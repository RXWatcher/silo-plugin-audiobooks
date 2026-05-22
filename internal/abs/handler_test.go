package abs_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/abs"
	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/backend"
	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/migrate"
	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/store"
	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/testutil"
)

// jwtSecret is the per-test signing secret for ABS access tokens. Must be at
// least 32 bytes so HS256 is happy.
var jwtSecret = []byte("test-secret-32-bytes-long-aaaaaa")

// authFixture wires a Handler against a fresh Postgres + applied migrations.
// Returns the chi router so tests can exercise the real Mount() surface.
type authFixture struct {
	t       *testing.T
	store   *store.Store
	router  chi.Router
	pool    *pgxpool.Pool
	handler *abs.Handler
}

func newAuthFixture(t *testing.T) *authFixture {
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
	// EnsureBackendConfig stores our test JWT secret. Subsequent reads
	// will surface it on cfg.ABSJWTSecret.
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
	return &authFixture{t: t, store: st, router: r, pool: pool, handler: h}
}

// do drives one request through the mounted router and returns status + body.
func (f *authFixture) do(method, path string, headers map[string]string, body string) (int, string) {
	f.t.Helper()
	var br io.Reader
	if body != "" {
		br = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, br)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	out, _ := io.ReadAll(w.Result().Body)
	return w.Result().StatusCode, string(out)
}

// login returns an access token after a successful header-authenticated login.
func (f *authFixture) login(userID string) string {
	f.t.Helper()
	status, body := f.do("POST", "/abs/api/login", map[string]string{"X-Continuum-User-Id": userID}, "")
	if status != 200 {
		f.t.Fatalf("login: status=%d body=%s", status, body)
	}
	var out struct {
		AccessToken string `json:"accessToken"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		f.t.Fatalf("unmarshal login: %v body=%s", err, body)
	}
	return out.AccessToken
}

// TestHandleLogin_RejectsMissingIdentity verifies the auth-bypass fix:
// POSTing /login without the host-injected X-Continuum-User-Id header must
// 401 when standalone login is disabled (the default backend_config mode).
// This is the security guarantee — any change that allows body-supplied
// identity through the disabled gate reopens the bypass and must fail this
// test.
func TestHandleLogin_RejectsMissingIdentity(t *testing.T) {
	f := newAuthFixture(t)
	status, body := f.do("POST", "/abs/api/login",
		map[string]string{"Content-Type": "application/json"},
		`{"username":"attacker","password":"any"}`)
	if status != 401 {
		t.Fatalf("status = %d, want 401", status)
	}
	if !strings.Contains(body, "standalone login is disabled") {
		t.Errorf("body = %q, want it to mention 'standalone login is disabled'", body)
	}
}

// TestHandleLogin_AcceptsHeaderIdentity verifies the happy path: with the
// header set (as the host proxy does), /login mints a real token pair.
func TestHandleLogin_AcceptsHeaderIdentity(t *testing.T) {
	f := newAuthFixture(t)
	status, body := f.do("POST", "/abs/api/login",
		map[string]string{"X-Continuum-User-Id": "u-alice"}, "")
	if status != 200 {
		t.Fatalf("status=%d body=%s", status, body)
	}
	var out struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		User         struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	if out.AccessToken == "" || out.RefreshToken == "" {
		t.Errorf("missing tokens: %+v", out)
	}
	if out.User.ID != "u-alice" {
		t.Errorf("user.id = %q, want u-alice", out.User.ID)
	}
}

// TestBearerAuth_RejectsMissingToken hits a protected route with no
// Authorization header — must 401.
func TestBearerAuth_RejectsMissingToken(t *testing.T) {
	f := newAuthFixture(t)
	status, _ := f.do("GET", "/abs/api/me", nil, "")
	if status != 401 {
		t.Fatalf("status = %d, want 401", status)
	}
}

// TestBearerAuth_RejectsBadSignature mints a token with a *different* secret
// and verifies the bearer middleware rejects it. Guards against accidentally
// disabling signature verification (e.g., by accepting any well-formed JWT).
func TestBearerAuth_RejectsBadSignature(t *testing.T) {
	f := newAuthFixture(t)
	bad, err := abs.IssueAccessToken([]byte("different-32-byte-key-zzzzzzzzzz"), "u", "", "j-1", time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	status, _ := f.do("GET", "/abs/api/me",
		map[string]string{"Authorization": "Bearer " + bad}, "")
	if status != 401 {
		t.Errorf("status = %d, want 401", status)
	}
}

// TestBearerAuth_RejectsRevokedToken verifies that flipping the DB row's
// revoked_at column blocks subsequent requests — this is the only way an
// operator can lock out a leaked token, so the lookup must run on every
// request.
func TestBearerAuth_RejectsRevokedToken(t *testing.T) {
	f := newAuthFixture(t)
	tok := f.login("u-bob")
	// First request succeeds.
	if s, _ := f.do("GET", "/abs/api/me", map[string]string{"Authorization": "Bearer " + tok}, ""); s != 200 {
		t.Fatalf("pre-revoke /me status = %d, want 200", s)
	}
	// Pull the JTI back out of the token to revoke the row directly.
	claims, err := abs.ParseToken(jwtSecret, tok)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := f.store.RevokeABSTokenByJTI(context.Background(), claims.JTI); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if s, _ := f.do("GET", "/abs/api/me", map[string]string{"Authorization": "Bearer " + tok}, ""); s != 401 {
		t.Errorf("post-revoke status = %d, want 401", s)
	}
}

// TestHandleLogout_RevokesToken ensures POST /auth/logout flips the row's
// revoked_at and subsequent /me requests with the same token 401.
func TestHandleLogout_RevokesToken(t *testing.T) {
	f := newAuthFixture(t)
	tok := f.login("u-carol")
	// Logout
	if s, _ := f.do("POST", "/abs/api/auth/logout",
		map[string]string{"Authorization": "Bearer " + tok}, ""); s != 204 {
		t.Fatalf("logout status = %d, want 204", s)
	}
	if s, _ := f.do("GET", "/abs/api/me", map[string]string{"Authorization": "Bearer " + tok}, ""); s != 401 {
		t.Errorf("post-logout status = %d, want 401", s)
	}
}

func TestHandlePingReturnsSuccess(t *testing.T) {
	f := newAuthFixture(t)
	status, body := f.do("GET", "/ping", nil, "")
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}

	var respBody map[string]any
	if err := json.Unmarshal([]byte(body), &respBody); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if respBody["success"] != true {
		t.Errorf("success = %v, want true", respBody["success"])
	}
}

func TestHandleStatusIdentifiesAsAudiobookshelf(t *testing.T) {
	f := newAuthFixture(t)
	status, body := f.do("GET", "/status", nil, "")
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}

	var respBody map[string]any
	if err := json.Unmarshal([]byte(body), &respBody); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if respBody["app"] != "audiobookshelf" {
		t.Errorf("app = %v, want audiobookshelf", respBody["app"])
	}
	methods, ok := respBody["authMethods"].([]any)
	if !ok || len(methods) != 1 || methods[0] != "local" {
		t.Errorf("authMethods = %v, want [local]", respBody["authMethods"])
	}
}

func TestAbsServerSettingsShape(t *testing.T) {
	s := abs.AbsServerSettings()
	for _, k := range []string{"version", "language", "authActiveAuthMethods", "authOpenIDAutoLaunch"} {
		if _, ok := s[k]; !ok {
			t.Errorf("serverSettings missing %q", k)
		}
	}
	if s["version"] != abs.ServerVersion {
		t.Errorf("version = %v, want %s", s["version"], abs.ServerVersion)
	}
}
