package hostlogin_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/hostlogin"
)

// TestValidate_Success drives the client against a fake host returning the
// real continuum host login shape (user.id as a JSON number) and verifies
// the client stringifies the id to match the X-Continuum-User-Id contract.
func TestValidate_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/login" {
			t.Errorf("path=%q want /api/v1/auth/login", r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body["username"] != "alice" || body["password"] != "secret" || body["provider"] != "local" {
			t.Errorf("body=%v must carry username, password, provider=local", body)
		}
		if r.Header.Get("X-Forwarded-For") != "10.0.0.5" {
			t.Errorf("xff=%q want 10.0.0.5", r.Header.Get("X-Forwarded-For"))
		}
		if r.Header.Get("User-Agent") != "Audiobookshelf" {
			t.Errorf("user-agent=%q want Audiobookshelf", r.Header.Get("User-Agent"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "ignored", "refresh_token": "ignored",
			"user": map[string]any{
				"id": 42, "username": "alice", "display_name": "Alice",
			},
		})
	}))
	defer srv.Close()

	res, err := hostlogin.New(srv.URL).Validate(context.Background(), "alice", "secret", "Audiobookshelf", "10.0.0.5")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if res.UserID != "42" {
		t.Errorf("UserID=%q want 42", res.UserID)
	}
	if res.DisplayName != "Alice" {
		t.Errorf("DisplayName=%q want Alice", res.DisplayName)
	}
}

// TestValidate_HostReturns401 maps the host's invalid-credentials response
// to ErrInvalidCredentials so callers can distinguish auth failure from
// transport failure.
func TestValidate_HostReturns401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"invalid_credentials"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := hostlogin.New(srv.URL).Validate(context.Background(), "a", "b", "ua", "ip")
	if !errors.Is(err, hostlogin.ErrInvalidCredentials) {
		t.Fatalf("err=%v want ErrInvalidCredentials", err)
	}
}

// TestValidate_HostReturns5xx returns the upstream sentinel so callers map
// to 502.
func TestValidate_HostReturns5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := hostlogin.New(srv.URL).Validate(context.Background(), "a", "b", "ua", "ip")
	if !errors.Is(err, hostlogin.ErrUpstream) {
		t.Fatalf("err=%v want ErrUpstream", err)
	}
}

// TestValidate_EmptyUserID protects against a malformed host response: even
// if status is 200, an empty user id must not be accepted because callers
// will key downstream state on it.
func TestValidate_EmptyUserID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"user":{}}`)
	}))
	defer srv.Close()

	_, err := hostlogin.New(srv.URL).Validate(context.Background(), "a", "b", "ua", "ip")
	if !errors.Is(err, hostlogin.ErrUpstream) {
		t.Fatalf("err=%v want ErrUpstream", err)
	}
}

// TestValidate_NonIntegerUserID guards against a host that ever ships a
// string id by accident — the plugin's existing rows are keyed by the
// integer form, so accepting a non-integer here would silently fragment
// state across the same user. The decoder rejects it (json.Number cannot
// hold a string literal) and the failure surfaces as ErrUpstream.
func TestValidate_NonIntegerUserID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"user":{"id":"u-alice"}}`)
	}))
	defer srv.Close()

	_, err := hostlogin.New(srv.URL).Validate(context.Background(), "a", "b", "ua", "ip")
	if !errors.Is(err, hostlogin.ErrUpstream) {
		t.Fatalf("err=%v want ErrUpstream", err)
	}
}

// TestValidate_EmptyInputs short-circuits before hitting the network.
func TestValidate_EmptyInputs(t *testing.T) {
	_, err := hostlogin.New("http://unused").Validate(context.Background(), "", "x", "", "")
	if !errors.Is(err, hostlogin.ErrInvalidCredentials) {
		t.Fatalf("err=%v want ErrInvalidCredentials for empty username", err)
	}
	_, err = hostlogin.New("http://unused").Validate(context.Background(), "x", "", "", "")
	if !errors.Is(err, hostlogin.ErrInvalidCredentials) {
		t.Fatalf("err=%v want ErrInvalidCredentials for empty password", err)
	}
}
