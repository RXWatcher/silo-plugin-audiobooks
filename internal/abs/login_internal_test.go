package abs

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	runtimehost "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginsdk/runtimehost"

	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/migrate"
	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/store"
	"github.com/RXWatcher/continuum-plugin-audiobooks/internal/testutil"
)

// stubValidator is an in-memory ProfileCredentialValidator. Tests set cred
// or err to simulate the host's ValidateProfileCredential RPC response.
type stubValidator struct {
	cred *runtimehost.ProfileCredential
	err  error
}

func (s stubValidator) ValidateProfileCredential(_ context.Context, _, _ string) (*runtimehost.ProfileCredential, error) {
	return s.cred, s.err
}

// loginTestHandler builds the smallest Handler that reaches the
// credential-validator call in handleStandaloneLogin: a targetFn that
// returns the given standalone-login mode, a fresh per-IP limiter, a
// no-op logger and the supplied validator. It deliberately omits a
// *store.Store — every path exercised here returns before completeLogin
// (which is the only thing that touches the store).
func loginTestHandler(mode string, v ProfileCredentialValidator) *Handler {
	return &Handler{
		logger: noopLogger{},
		targetFn: func(context.Context) (string, store.BackendConfig, error) {
			return "", store.BackendConfig{StandaloneLoginMode: mode}, nil
		},
		credValidator: v,
		loginLimiter:  NewLoginLimiter(),
	}
}

// postLogin drives handleLogin with a JSON body and no identity header
// (the standalone path) and returns the status + body.
func postLogin(h *Handler, jsonBody string) (int, string) {
	req := httptest.NewRequest("POST", "/login", strings.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.handleLogin(w, req)
	res := w.Result()
	out, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(out)
}

// TestStandaloneLogin_BadCredentials_Returns401 asserts that a gRPC
// Unauthenticated error from ValidateProfileCredential maps to HTTP 401
// with the invalid-credentials message — the security-critical path.
func TestStandaloneLogin_BadCredentials_Returns401(t *testing.T) {
	h := loginTestHandler(store.StandaloneLoginModeEnabled, stubValidator{
		err: status.Error(codes.Unauthenticated, "bad password"),
	})
	code, body := postLogin(h, `{"username":"alice#kids","password":"wrong#1234"}`)
	if code != 401 {
		t.Fatalf("status = %d body = %s, want 401", code, body)
	}
	if !strings.Contains(body, "invalid username or password") {
		t.Errorf("body = %q, want invalid-credentials message", body)
	}
}

// TestStandaloneLogin_ValidatorError_Returns503 asserts that a non-
// Unauthenticated error (host down / internal) maps to HTTP 503 so the
// client can distinguish "wrong password" from "service unavailable".
func TestStandaloneLogin_ValidatorError_Returns503(t *testing.T) {
	h := loginTestHandler(store.StandaloneLoginModeEnabled, stubValidator{
		err: status.Error(codes.Unavailable, "host unreachable"),
	})
	code, body := postLogin(h, `{"username":"alice","password":"x"}`)
	if code != 503 {
		t.Fatalf("status = %d body = %s, want 503", code, body)
	}
	if !strings.Contains(body, "login service unavailable") {
		t.Errorf("body = %q, want service-unavailable message", body)
	}
}

// TestStandaloneLogin_DisabledMode_Returns401 keeps the security
// guarantee: with standalone login disabled, a body-creds POST is
// rejected before the validator is ever consulted.
func TestStandaloneLogin_DisabledMode_Returns401(t *testing.T) {
	called := false
	h := loginTestHandler(store.StandaloneLoginModeDisabled, stubValidator{
		// If the handler reaches the validator the test below catches it.
		cred: &runtimehost.ProfileCredential{UserID: "42"},
	})
	// Wrap the validator to detect any call.
	h.credValidator = recordingValidator{onCall: func() { called = true }}
	code, body := postLogin(h, `{"username":"alice","password":"x"}`)
	if code != 401 {
		t.Fatalf("status = %d body = %s, want 401", code, body)
	}
	if called {
		t.Error("validator was called in disabled mode; must reject before RPC")
	}
	if !strings.Contains(body, "standalone login is disabled") {
		t.Errorf("body = %q, want disabled message", body)
	}
}

// TestStandaloneLogin_NoValidator_Returns503 covers the deployment that
// enabled standalone login but didn't wire a credential validator.
func TestStandaloneLogin_NoValidator_Returns503(t *testing.T) {
	h := loginTestHandler(store.StandaloneLoginModeEnabled, nil)
	code, body := postLogin(h, `{"username":"alice","password":"x"}`)
	if code != 503 {
		t.Fatalf("status = %d body = %s, want 503", code, body)
	}
	if !strings.Contains(body, "standalone login is unavailable") {
		t.Errorf("body = %q, want unavailable message", body)
	}
}

// recordingValidator records whether ValidateProfileCredential was
// invoked, used to assert the disabled-mode gate fails closed.
type recordingValidator struct{ onCall func() }

func (r recordingValidator) ValidateProfileCredential(context.Context, string, string) (*runtimehost.ProfileCredential, error) {
	if r.onCall != nil {
		r.onCall()
	}
	return &runtimehost.ProfileCredential{}, nil
}

// TestStandaloneLogin_Success_MintsTokensWithProfile drives the full
// happy path end-to-end against a real Postgres: the validator resolves
// a (userID, profileID) pair, completeLogin mints + persists the token
// pair, and the response carries the resolved user id. This is the only
// test here that needs a store — completeLogin writes abs_tokens rows.
func TestStandaloneLogin_Success_MintsTokensWithProfile(t *testing.T) {
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
	if _, err := st.EnsureBackendConfig(context.Background(), []byte("test-secret-32-bytes-long-aaaaaa")); err != nil {
		t.Fatalf("ensure cfg: %v", err)
	}
	h := &Handler{
		store:  st,
		logger: noopLogger{},
		targetFn: func(ctx context.Context) (string, store.BackendConfig, error) {
			cfg, err := st.GetBackendConfig(ctx)
			cfg.StandaloneLoginMode = store.StandaloneLoginModeEnabled
			return "", cfg, err
		},
		hostBaseFn:   func() string { return "http://host" },
		installID:    func() string { return "test-install" },
		loginLimiter: NewLoginLimiter(),
		credValidator: stubValidator{
			cred: &runtimehost.ProfileCredential{UserID: "u-42", ProfileID: "p-kids"},
		},
	}
	code, body := postLogin(h, `{"username":"alice#Kids","password":"pw#1234"}`)
	if code != 200 {
		t.Fatalf("status = %d body = %s, want 200", code, body)
	}
	var out struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		User         struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"user"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	if out.AccessToken == "" || out.RefreshToken == "" {
		t.Errorf("missing tokens: %+v", out)
	}
	if out.User.ID != "u-42" {
		t.Errorf("user.id = %q, want u-42 (host-resolved)", out.User.ID)
	}
	// Display name is the profile portion of the typed username ("Kids").
	if out.User.Username != "Kids" {
		t.Errorf("user.username = %q, want Kids (profile portion of username)", out.User.Username)
	}
	// The access token must carry the resolved profile id.
	claims, err := ParseToken([]byte("test-secret-32-bytes-long-aaaaaa"), out.AccessToken)
	if err != nil {
		t.Fatalf("parse access token: %v", err)
	}
	if claims.ProfileID != "p-kids" {
		t.Errorf("access token ProfileID = %q, want p-kids", claims.ProfileID)
	}
}
