package abs_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/abs"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/backend"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/hostlogin"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/migrate"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/testutil"
)

// stubHostLogin is an in-memory replacement for hostlogin.Client used by the
// body-creds tests. Tests configure ValidateFn per case to simulate the
// continuum host's response (200, 401, 502).
type stubHostLogin struct {
	ValidateFn func(username, password string) (hostlogin.Result, error)
	calls      atomic.Int32
	lastIP     atomic.Value // string
}

func (s *stubHostLogin) Validate(_ context.Context, username, password, _, ip string) (hostlogin.Result, error) {
	s.calls.Add(1)
	s.lastIP.Store(ip)
	if s.ValidateFn == nil {
		return hostlogin.Result{}, hostlogin.ErrInvalidCredentials
	}
	return s.ValidateFn(username, password)
}

// standaloneFixture mounts the handler with a configurable host-login stub
// and a configurable backend_config.standalone_login_mode value.
type standaloneFixture struct {
	t       *testing.T
	store   *store.Store
	router  chi.Router
	pool    *pgxpool.Pool
	handler *abs.Handler
	stub    *stubHostLogin
}

func newStandaloneFixture(t *testing.T, mode string) *standaloneFixture {
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
	cfg, err := st.GetBackendConfig(context.Background())
	if err != nil {
		t.Fatalf("get cfg: %v", err)
	}
	cfg.StandaloneLoginMode = mode
	if err := st.UpdateBackendConfig(context.Background(), cfg); err != nil {
		t.Fatalf("update cfg: %v", err)
	}

	stub := &stubHostLogin{}
	h := abs.NewHandler(abs.Deps{
		Store:   st,
		Backend: backend.NewClient(backend.NewHostClient("http://host")),
		TargetFn: func(ctx context.Context) (string, store.BackendConfig, error) {
			cfg, err := st.GetBackendConfig(ctx)
			return cfg.TargetBackendPluginID, cfg, err
		},
		HostBaseFn: func() string { return "http://host" },
		InstallID:  func() string { return "test-install" },
		HostLogin:  stub,
	})
	r := chi.NewRouter()
	h.Mount(r)
	return &standaloneFixture{t: t, store: st, router: r, pool: pool, handler: h, stub: stub}
}

func (f *standaloneFixture) postLogin(body string) (int, string) {
	f.t.Helper()
	var br io.Reader
	if body != "" {
		br = strings.NewReader(body)
	}
	req := httptest.NewRequest("POST", "/abs/api/login", br)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	out, _ := io.ReadAll(w.Result().Body)
	return w.Result().StatusCode, string(out)
}

// TestStandaloneLogin_DisabledMode_Rejects keeps the security guarantee:
// when mode = "disabled", body-creds attempts always 401 with the
// "standalone /login is not accepted" message, identical to today.
func TestStandaloneLogin_DisabledMode_Rejects(t *testing.T) {
	f := newStandaloneFixture(t, store.StandaloneLoginModeDisabled)
	f.stub.ValidateFn = func(_, _ string) (hostlogin.Result, error) {
		t.Fatalf("hostLogin must not be called in disabled mode")
		return hostlogin.Result{}, nil
	}
	status, body := f.postLogin(`{"username":"alice","password":"x"}`)
	if status != 401 {
		t.Fatalf("status=%d body=%s want 401", status, body)
	}
	if !strings.Contains(body, "standalone /login is not accepted") {
		t.Errorf("body=%q must keep legacy rejection message", body)
	}
	if f.stub.calls.Load() != 0 {
		t.Errorf("hostLogin called %d times; should be zero in disabled mode", f.stub.calls.Load())
	}
}

// TestStandaloneLogin_AllAccounts_Success: with mode = "all_accounts" and the
// host returning a validated identity, the handler mints ABS tokens keyed to
// the host-returned user id.
func TestStandaloneLogin_AllAccounts_Success(t *testing.T) {
	f := newStandaloneFixture(t, store.StandaloneLoginModeAllAccounts)
	f.stub.ValidateFn = func(u, p string) (hostlogin.Result, error) {
		if u != "alice" || p != "secret" {
			return hostlogin.Result{}, hostlogin.ErrInvalidCredentials
		}
		return hostlogin.Result{UserID: "42", DisplayName: "Alice"}, nil
	}
	status, body := f.postLogin(`{"username":"alice","password":"secret"}`)
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
	if out.User.ID != "42" {
		t.Errorf("user.id=%q want 42 (host-supplied)", out.User.ID)
	}
}

// TestStandaloneLogin_BadCredentials_Returns401 maps host 401/403 (returned
// by the stub as ErrInvalidCredentials) to a 401 toward the ABS client.
func TestStandaloneLogin_BadCredentials_Returns401(t *testing.T) {
	f := newStandaloneFixture(t, store.StandaloneLoginModeAllAccounts)
	f.stub.ValidateFn = func(_, _ string) (hostlogin.Result, error) {
		return hostlogin.Result{}, hostlogin.ErrInvalidCredentials
	}
	status, body := f.postLogin(`{"username":"alice","password":"wrong"}`)
	if status != 401 {
		t.Fatalf("status=%d body=%s want 401", status, body)
	}
	if !strings.Contains(body, "invalid username or password") {
		t.Errorf("body=%q must surface invalid-credentials message", body)
	}
}

// TestStandaloneLogin_UpstreamError_Returns502: when the host is unreachable
// or returns a 5xx, the plugin returns 502 so the ABS client can distinguish
// "wrong password" from "service down".
func TestStandaloneLogin_UpstreamError_Returns502(t *testing.T) {
	f := newStandaloneFixture(t, store.StandaloneLoginModeAllAccounts)
	f.stub.ValidateFn = func(_, _ string) (hostlogin.Result, error) {
		return hostlogin.Result{}, errors.New("connection refused")
	}
	status, body := f.postLogin(`{"username":"alice","password":"x"}`)
	if status != 502 {
		t.Fatalf("status=%d body=%s want 502", status, body)
	}
	if !strings.Contains(body, "upstream login unavailable") {
		t.Errorf("body=%q must surface upstream message", body)
	}
}

// TestStandaloneLogin_OptInMode_RequiresOptIn: with mode = "opt_in" and no
// row in abs_standalone_opt_ins, even a host-validated identity is rejected.
func TestStandaloneLogin_OptInMode_RequiresOptIn(t *testing.T) {
	f := newStandaloneFixture(t, store.StandaloneLoginModeOptIn)
	f.stub.ValidateFn = func(_, _ string) (hostlogin.Result, error) {
		return hostlogin.Result{UserID: "42"}, nil
	}
	status, body := f.postLogin(`{"username":"alice","password":"secret"}`)
	if status != 401 {
		t.Fatalf("status=%d body=%s want 401", status, body)
	}
	if !strings.Contains(body, "not_enabled_for_mobile_login") {
		t.Errorf("body=%q must surface the opt-in error code", body)
	}
}

// TestStandaloneLogin_OptInMode_AllowsOptedInUser: after inserting an
// opt-in row for the host-validated user id, login succeeds.
func TestStandaloneLogin_OptInMode_AllowsOptedInUser(t *testing.T) {
	f := newStandaloneFixture(t, store.StandaloneLoginModeOptIn)
	if err := f.store.EnableStandaloneOptIn(context.Background(), "42"); err != nil {
		t.Fatalf("enable opt-in: %v", err)
	}
	f.stub.ValidateFn = func(_, _ string) (hostlogin.Result, error) {
		return hostlogin.Result{UserID: "42"}, nil
	}
	status, body := f.postLogin(`{"username":"alice","password":"secret"}`)
	if status != 200 {
		t.Fatalf("status=%d body=%s want 200", status, body)
	}
	var out struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.User.ID != "42" {
		t.Errorf("user.id=%q want 42", out.User.ID)
	}
}

// TestStandaloneLogin_RateLimited: after exhausting the per-IP login bucket,
// further attempts return 429 even if the host would accept the credentials.
// The fixture is recreated per test so the bucket starts empty.
func TestStandaloneLogin_RateLimited(t *testing.T) {
	f := newStandaloneFixture(t, store.StandaloneLoginModeAllAccounts)
	f.stub.ValidateFn = func(_, _ string) (hostlogin.Result, error) {
		return hostlogin.Result{}, hostlogin.ErrInvalidCredentials
	}
	// Burst is 10. The 11th attempt from the same IP should be 429.
	const burst = 10
	for i := 0; i < burst; i++ {
		if status, body := f.postLogin(`{"username":"a","password":"b"}`); status != 401 {
			t.Fatalf("attempt %d status=%d body=%s want 401", i, status, body)
		}
	}
	status, body := f.postLogin(`{"username":"a","password":"b"}`)
	if status != 429 {
		t.Fatalf("status=%d body=%s want 429", status, body)
	}
	if !strings.Contains(body, "too many login attempts") {
		t.Errorf("body=%q must surface the rate-limit message", body)
	}
}

// TestStandaloneLogin_HeaderPath_BypassesLimiter: header-authenticated calls
// must never count against the body-creds limiter. We over-spend the bucket
// using body attempts (which 401), then verify a header-authenticated login
// still succeeds.
func TestStandaloneLogin_HeaderPath_BypassesLimiter(t *testing.T) {
	f := newStandaloneFixture(t, store.StandaloneLoginModeAllAccounts)
	f.stub.ValidateFn = func(_, _ string) (hostlogin.Result, error) {
		return hostlogin.Result{}, hostlogin.ErrInvalidCredentials
	}
	const burst = 11
	for i := 0; i < burst; i++ {
		_, _ = f.postLogin(`{"username":"a","password":"b"}`)
	}
	req := httptest.NewRequest("POST", "/abs/api/login", nil)
	req.Header.Set("X-Continuum-User-Id", "u-bob")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	if w.Result().StatusCode != 200 {
		t.Fatalf("header-auth login status=%d, must not be rate-limited", w.Result().StatusCode)
	}
}
