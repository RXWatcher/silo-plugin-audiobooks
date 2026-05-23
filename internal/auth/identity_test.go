package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/auth"
)

func TestFromHeaders(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Silo-User-Id", "u-1")
	r.Header.Set("X-Silo-User-Role", "admin")
	r.Header.Set("Authorization", "Bearer abc.def.ghi")
	id := auth.FromHeaders(r)
	if id.UserID != "u-1" || !id.IsAdmin() || id.Token != "abc.def.ghi" {
		t.Errorf("id = %+v", id)
	}
}

func TestMiddleware_StoresIdentity(t *testing.T) {
	called := false
	h := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		id, ok := auth.FromContext(r.Context())
		if !ok || id.UserID != "u-9" {
			t.Errorf("id = %+v ok=%v", id, ok)
		}
	}))
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-Silo-User-Id", "u-9")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if !called {
		t.Errorf("not called")
	}
}

func TestRequireUser_Unauthenticated(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	_, ok := auth.RequireUser(w, r)
	if ok {
		t.Errorf("expected RequireUser to fail without identity")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("code = %d", w.Code)
	}
}
