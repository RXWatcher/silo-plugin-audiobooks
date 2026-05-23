package httproutes

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestServeHTTPBeforeSetHandlerReturns503 confirms a standalone listener
// gracefully returns 503 with the documented body shape before the plugin's
// first Configure call has wired in a handler.
func TestServeHTTPBeforeSetHandlerReturns503(t *testing.T) {
	s := NewServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	if !strings.Contains(rec.Body.String(), `"not_ready"`) {
		t.Fatalf("body = %q, want it to contain \"not_ready\"", rec.Body.String())
	}
}

// TestStandaloneListenerRoutesThroughToHandler binds an actual TCP listener on
// :0, points an http.Client at it, and confirms the listener pipes requests
// through to the same atomic handler that gRPC HandleHTTPRequest serves.
//
// Uses two stub routes that mimic the public/protected pattern the real
// plugin handler tree implements: /public always 200, /protected 401 unless
// the host-injected X-Silo-User-Id header is set. This is the contract
// operators rely on when reverse-proxying a hostname to the standalone port.
func TestStandaloneListenerRoutesThroughToHandler(t *testing.T) {
	srv := NewServer()
	srv.SetHandler(stubAuthHandler())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	httpSrv := &http.Server{
		Handler:           srv,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = httpSrv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(ctx)
	})

	base := "http://" + ln.Addr().String()
	client := &http.Client{Timeout: 3 * time.Second}

	// 1. Public route should respond 200 without any auth header (this is the
	//    client-app path: ABS mobile app hits /abs/api/ping with no Silo
	//    session).
	resp, err := client.Get(base + "/public")
	if err != nil {
		t.Fatalf("GET /public: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("public status = %d, want 200", resp.StatusCode)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	// 2. Protected route without the host-injected header must 401. This is
	//    the explicit design: the standalone port never injects
	//    X-Silo-User-*, so the plugin's own auth.RequireAuth refuses
	//    SPA traffic there.
	resp, err = client.Get(base + "/protected")
	if err != nil {
		t.Fatalf("GET /protected: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("protected (no header) status = %d, want 401", resp.StatusCode)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	// 3. Protected route with the injected header MUST still 401 on the
	//    standalone listener: the listener strips X-Silo-* headers
	//    before reaching the handler, so a forged identity header can never
	//    bypass auth.RequireAuth-style checks. This is the defense against
	//    the auth-bypass class on standalone ports.
	req, _ := http.NewRequest(http.MethodGet, base+"/protected", nil)
	req.Header.Set("X-Silo-User-Id", "42")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("GET /protected (with forged header): %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("protected (with forged header) status = %d, want 401 — standalone listener must strip X-Silo-*", resp.StatusCode)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

// TestServeHTTPStripsSiloHeaders confirms every X-Silo-* header is
// removed before the wrapped handler sees the request, regardless of case.
// These headers are the host trust channel; client-supplied versions are
// always forged.
func TestServeHTTPStripsSiloHeaders(t *testing.T) {
	var seen http.Header
	srv := NewServer()
	srv.SetHandler(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
	}))

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-Silo-User-Id", "forged")
	req.Header.Set("X-Silo-User-Role", "admin")
	req.Header.Set("x-silo-theme", "dark") // lowercase variant
	req.Header.Set("Authorization", "Bearer keep-me")
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	srv.ServeHTTP(httptest.NewRecorder(), req)

	for k := range seen {
		if strings.HasPrefix(strings.ToLower(k), "x-silo-") {
			t.Errorf("header %q leaked through to handler", k)
		}
	}
	if got := seen.Get("Authorization"); got != "Bearer keep-me" {
		t.Errorf("Authorization = %q, want it preserved", got)
	}
	if got := seen.Get("X-Forwarded-For"); got != "1.2.3.4" {
		t.Errorf("X-Forwarded-For = %q, want it preserved", got)
	}
}

// TestSetHandlerSwapPropagates confirms that a Configure-time handler swap
// is reflected on subsequent standalone-port requests, so reconfigures of
// the gRPC-side handler aren't desynced from the standalone listener.
func TestSetHandlerSwapPropagates(t *testing.T) {
	srv := NewServer()
	srv.SetHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("first handler status = %d, want 418", rec.Code)
	}

	srv.SetHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("swapped handler status = %d, want 200", rec.Code)
	}
}

// stubAuthHandler models the plugin's real auth pattern: public routes
// answer for anyone; protected routes require the host-injected user header.
func stubAuthHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/public", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/protected", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Silo-User-Id") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return mux
}
