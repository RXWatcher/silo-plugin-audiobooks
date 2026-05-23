package backend_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/backend"
)

func TestHostClient_BuildsURLAndAddsBearer(t *testing.T) {
	var gotURL, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := backend.NewHostClient(srv.URL)
	_, _ = c.Get(context.Background(), "tok-abc", "inst-7", "/api/v1/catalog?limit=10")
	if gotURL != "/api/v1/plugins/inst-7/api/v1/catalog" {
		t.Errorf("URL = %q", gotURL)
	}
	if gotAuth != "Bearer tok-abc" {
		t.Errorf("Auth = %q", gotAuth)
	}
}

func TestHostClient_UsesServiceTokenWhenBearerMissing(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := backend.NewHostClient(srv.URL).WithServiceToken("svc-token")
	if _, err := c.Get(context.Background(), "", "inst-7", "/api/v1/catalog"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if gotAuth != "Bearer svc-token" {
		t.Fatalf("Authorization = %q, want service token fallback", gotAuth)
	}
}

func TestClient_ListCatalog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/plugins/inst-7/api/v1/catalog" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"items":[{"id":"a","title":"A"}],"total":1}`))
	}))
	defer srv.Close()
	c := backend.NewClient(backend.NewHostClient(srv.URL))
	out, err := c.ListCatalog(context.Background(), "tok", "inst-7", backend.ListParams{Limit: 10})
	if err != nil {
		t.Fatalf("ListCatalog: %v", err)
	}
	if len(out.Items) != 1 || out.Items[0].ID != "a" {
		t.Errorf("env = %+v", out)
	}
}

// TestClient_ListCatalog_FilterPushdown verifies that ListParams.Filter +
// FilterValue land on the backend request as filter= / filter_value=
// query params. Backends that understand the contract apply the filter
// with an index hit; backends that don't are documented to ignore the
// params (the plugin always re-applies the filter locally).
func TestClient_ListCatalog_FilterPushdown(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"items":[],"total":0}`))
	}))
	defer srv.Close()
	c := backend.NewClient(backend.NewHostClient(srv.URL))
	_, err := c.ListCatalog(context.Background(), "tok", "inst-7", backend.ListParams{
		Limit:       50,
		LibraryID:   3,
		Filter:      "authors",
		FilterValue: "Brandon Sanderson",
	})
	if err != nil {
		t.Fatalf("ListCatalog: %v", err)
	}
	// Use url.ParseQuery for ordering-independent assertion — encoded
	// param order isn't part of the contract.
	got, err := url.ParseQuery(gotQuery)
	if err != nil {
		t.Fatalf("parse query: %v", err)
	}
	if got.Get("filter") != "authors" {
		t.Errorf("filter = %q, want authors", got.Get("filter"))
	}
	if got.Get("filter_value") != "Brandon Sanderson" {
		t.Errorf("filter_value = %q, want Brandon Sanderson", got.Get("filter_value"))
	}
	if got.Get("limit") != "50" {
		t.Errorf("limit = %q, want 50", got.Get("limit"))
	}
	if got.Get("library_id") != "3" {
		t.Errorf("library_id = %q, want 3", got.Get("library_id"))
	}
}

// TestClient_ListCatalog_FilterRequiresBothFields confirms that a Filter
// kind without a FilterValue is dropped (and vice versa) — partial
// filters would otherwise quietly nuke catalog responses on backends
// that DO honor them.
func TestClient_ListCatalog_FilterRequiresBothFields(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"items":[],"total":0}`))
	}))
	defer srv.Close()
	c := backend.NewClient(backend.NewHostClient(srv.URL))

	// Kind set but value empty → no filter forwarded.
	_, _ = c.ListCatalog(context.Background(), "tok", "inst-7", backend.ListParams{
		Limit:  10,
		Filter: "authors",
	})
	if strings.Contains(gotQuery, "filter=") {
		t.Errorf("filter forwarded with empty value: %q", gotQuery)
	}

	// Value set but kind empty → no filter forwarded either.
	_, _ = c.ListCatalog(context.Background(), "tok", "inst-7", backend.ListParams{
		Limit:       10,
		FilterValue: "Sanderson",
	})
	if strings.Contains(gotQuery, "filter=") {
		t.Errorf("filter forwarded with empty kind: %q", gotQuery)
	}
}

func TestClient_GetDetail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/plugins/inst-7/api/v1/catalog/bw-1" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":"bw-1","title":"X","files":[{"index":0,"format":"m4b"}]}`))
	}))
	defer srv.Close()
	c := backend.NewClient(backend.NewHostClient(srv.URL))
	d, err := c.GetDetail(context.Background(), "tok", "inst-7", "bw-1")
	if err != nil {
		t.Fatalf("GetDetail: %v", err)
	}
	if d.ID != "bw-1" || len(d.Files) != 1 || d.Files[0].Format != "m4b" {
		t.Errorf("detail = %+v", d)
	}
}

func TestClient_StreamURL(t *testing.T) {
	c := backend.NewClient(backend.NewHostClient("http://host.example"))
	got := c.StreamURL("inst-7", "bw-3", 0)
	want := "/api/v1/plugins/inst-7/api/v1/stream/bw-3/0"
	if got != want {
		t.Errorf("StreamURL = %q want %q", got, want)
	}
}

// bookID flows from backend-provided catalog ids into the redirect URL; a
// value with path/query metacharacters must be percent-escaped so it can't
// rewrite the host-proxy path (path injection / wrong-route).
func TestClient_StreamCoverURL_EscapeBookID(t *testing.T) {
	c := backend.NewClient(backend.NewHostClient("http://host.example"))
	st := c.StreamURL("inst-7", "a/../b?x", 3)
	if strings.Contains(st, "a/../b?x") {
		t.Errorf("StreamURL did not escape bookID: %s", st)
	}
	if !strings.Contains(st, "/api/v1/stream/a%2F..%2Fb%3Fx/3") {
		t.Errorf("StreamURL = %s", st)
	}
	cv := c.CoverURL("inst-7", "a/../b?x", "large")
	if strings.Contains(cv, "a/../b?x") {
		t.Errorf("CoverURL did not escape bookID: %s", cv)
	}
}

func TestClient_GetRequestSnapshot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/plugins/inst-7/api/v1/requests/ext-42" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"request_id":"req-1","external_id":"ext-42","status":"imported"}`))
	}))
	defer srv.Close()
	c := backend.NewClient(backend.NewHostClient(srv.URL))
	s, err := c.GetRequestSnapshot(context.Background(), "t", "inst-7", "ext-42")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if s.Status != "imported" {
		t.Errorf("status = %q", s.Status)
	}
}
