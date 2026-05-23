package abs_test

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestMount_RootPaths_Resolve confirms the dual-mount: ABS-canonical
// paths (no /abs/api prefix) reach the same handlers as the legacy
// /abs/api/* paths. Without this the official ABS mobile + web
// clients can't even reach /login because they build URLs as
// ${serverAddress}/login, not ${serverAddress}/abs/api/login.
func TestMount_RootPaths_Resolve(t *testing.T) {
	f := newAuthFixture(t)

	// /ping at server root — was /abs/api/ping.
	req := httptest.NewRequest("GET", "/ping", nil)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	if w.Result().StatusCode != 200 {
		t.Errorf("GET /ping = %d, want 200", w.Result().StatusCode)
	}

	// /status at server root.
	req = httptest.NewRequest("GET", "/status", nil)
	w = httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	if w.Result().StatusCode != 200 {
		t.Errorf("GET /status = %d, want 200", w.Result().StatusCode)
	}

	// /login at server root — header-auth path with X-Silo-User-Id.
	req = httptest.NewRequest("POST", "/login", strings.NewReader(""))
	req.Header.Set("X-Silo-User-Id", "u-test")
	w = httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	if w.Result().StatusCode != 200 {
		t.Errorf("POST /login = %d body=%s, want 200", w.Result().StatusCode, w.Body.String())
	}

	// /api/me — bearer-auth path under /api (not /abs/api).
	tok := f.login("u-test")
	req = httptest.NewRequest("GET", "/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w = httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	if w.Result().StatusCode != 200 {
		t.Errorf("GET /api/me = %d body=%s, want 200", w.Result().StatusCode, w.Body.String())
	}

	// /api/libraries — same group.
	req = httptest.NewRequest("GET", "/api/libraries", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w = httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	if w.Result().StatusCode != 200 {
		t.Errorf("GET /api/libraries = %d body=%s, want 200", w.Result().StatusCode, w.Body.String())
	}
}

// TestMount_LegacyPathsStillWork is the explicit no-regression guard:
// our own SPA, host-proxied callers, and existing third-party tools
// still reach the same handlers via the original /abs/api/* paths.
func TestMount_LegacyPathsStillWork(t *testing.T) {
	f := newAuthFixture(t)

	req := httptest.NewRequest("GET", "/abs/api/ping", nil)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	if w.Result().StatusCode != 200 {
		t.Errorf("legacy /abs/api/ping = %d", w.Result().StatusCode)
	}

	tok := f.login("u-test")
	req = httptest.NewRequest("GET", "/abs/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w = httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	if w.Result().StatusCode != 200 {
		t.Errorf("legacy /abs/api/me = %d body=%s", w.Result().StatusCode, w.Body.String())
	}
}
