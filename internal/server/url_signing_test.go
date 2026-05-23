package server

import (
	"strings"
	"testing"
)

// TestSignStreamURL_BuildsPluginProxyPath confirms the basic shape of the
// stream URL the SPA puts in <audio src>: it's an origin-relative path
// under the host's plugin-proxy mount, with file index appended.
func TestSignStreamURL_BuildsPluginProxyPath(t *testing.T) {
	got := signStreamURL("silo.bw-audio", "u-1", "book-42", 3, "")
	want := "/api/v1/plugins/silo.bw-audio/api/v1/stream/book-42/3"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestSignStreamURL_AppendsTokenWhenSecretAndUser confirms that with a
// signing secret + userID, the URL grows a ?token= query param. The token
// content itself is opaque (JWT signed with the secret) — we just assert
// the query parameter is present.
func TestSignStreamURL_AppendsTokenWhenSecretAndUser(t *testing.T) {
	got := signStreamURL("inst", "u-1", "book", 0, "this-is-32-bytes-of-raw-secret!!")
	if !strings.Contains(got, "?token=") {
		t.Errorf("URL missing ?token=: %q", got)
	}
	if !strings.HasPrefix(got, "/api/v1/plugins/inst/api/v1/stream/book/0?token=") {
		t.Errorf("URL prefix wrong: %q", got)
	}
}

// TestSignStreamURL_NoTokenWhenSecretEmpty ensures the URL stays usable
// (just unsigned) when an admin hasn't configured a signing secret. The
// backend will refuse the request with 503, but the URL itself remains
// well-formed so clients don't see a malformed link.
func TestSignStreamURL_NoTokenWhenSecretEmpty(t *testing.T) {
	got := signStreamURL("inst", "u-1", "book", 0, "")
	if strings.Contains(got, "?token=") {
		t.Errorf("URL must not carry token when secret is empty: %q", got)
	}
}

// TestSignStreamURL_NoTokenWhenUserEmpty handles the impossible-but-defensive
// case where userID is empty (would be a bug upstream; we don't crash, we
// emit the tokenless URL).
func TestSignStreamURL_NoTokenWhenUserEmpty(t *testing.T) {
	got := signStreamURL("inst", "", "book", 0, "this-is-32-bytes-of-raw-secret!!")
	if strings.Contains(got, "?token=") {
		t.Errorf("URL must not carry token without a user: %q", got)
	}
}

// TestSignStreamURL_EmptyOnMissingBookOrInstall returns "" when either
// installID or backendBookID is empty — handlers branch on this to decide
// whether to surface a "no backend configured" 412 versus an empty stream
// URL in the response payload.
func TestSignStreamURL_EmptyOnMissingBookOrInstall(t *testing.T) {
	if got := signStreamURL("", "u", "b", 0, "s"); got != "" {
		t.Errorf("empty install: got %q, want \"\"", got)
	}
	if got := signStreamURL("inst", "u", "", 0, "s"); got != "" {
		t.Errorf("empty book: got %q, want \"\"", got)
	}
}

// TestRewriteCoverURL_AbsolutePassesThrough mirrors what booklore-ng
// documented as a real-world gotcha: absolute URLs (some backends emit
// external CDN links for covers) must not be wrapped — the plugin proxy
// can't reach external hosts and rewriting would 502.
func TestRewriteCoverURL_AbsolutePassesThrough(t *testing.T) {
	for _, raw := range []string{
		"https://cdn.example.com/covers/book-1.jpg",
		"http://localhost:9000/cover.png",
	} {
		got := rewriteCoverURL(raw, "inst", "u", "book", "secret")
		if got != raw {
			t.Errorf("absolute %q rewritten to %q", raw, got)
		}
	}
}

// TestRewriteCoverURL_RewritesBackendRelative confirms the path-prefix
// machinery: bare paths grow the /api/v1 prefix, /api/v1/* paths grow the
// plugin proxy mount, and already-mounted paths are left alone.
func TestRewriteCoverURL_RewritesBackendRelative(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"/cover/abc/large", "/api/v1/plugins/inst/api/v1/cover/abc/large"},
		{"cover/abc/large", "/api/v1/plugins/inst/api/v1/cover/abc/large"},
		{"/api/v1/cover/abc/large", "/api/v1/plugins/inst/api/v1/cover/abc/large"},
		{"/api/v1/plugins/inst/api/v1/cover/abc/large", "/api/v1/plugins/inst/api/v1/cover/abc/large"},
	}
	for _, c := range cases {
		got := rewriteCoverURL(c.in, "inst", "", "", "") // tokenless to make assertion simple
		if got != c.want {
			t.Errorf("rewriteCoverURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestRewriteCoverURL_AppendsCoverToken adds a ?token= query carrying the
// cover-scoped media token (file_idx=CoverFileIdx). The token itself is
// opaque; we just confirm the query parameter is appended exactly once.
func TestRewriteCoverURL_AppendsCoverToken(t *testing.T) {
	got := rewriteCoverURL("/cover/abc/large", "inst", "u-1", "abc", "this-is-32-bytes-of-raw-secret!!")
	if !strings.Contains(got, "?token=") {
		t.Errorf("URL missing ?token=: %q", got)
	}
	if strings.Count(got, "?token=") != 1 {
		t.Errorf("URL has multiple token params: %q", got)
	}
}

// TestRewriteCoverURL_NoTokenWhenSecretEmpty mirrors the stream-URL
// behaviour: an empty signing secret produces a URL the backend will
// refuse, but the URL itself must remain well-formed.
func TestRewriteCoverURL_NoTokenWhenSecretEmpty(t *testing.T) {
	got := rewriteCoverURL("/cover/abc/large", "inst", "u-1", "abc", "")
	if strings.Contains(got, "?token=") {
		t.Errorf("URL must not carry token without a secret: %q", got)
	}
}

// TestRewriteCoverURL_EmptyInputPassesThrough — defensive: an empty raw
// URL or empty installID returns the input unchanged. The catalog
// rewriter relies on this to leave nil-cover items alone.
func TestRewriteCoverURL_EmptyInputPassesThrough(t *testing.T) {
	if got := rewriteCoverURL("", "inst", "u", "book", "s"); got != "" {
		t.Errorf("empty raw: got %q, want \"\"", got)
	}
	if got := rewriteCoverURL("/cover/x", "", "u", "book", "s"); got != "/cover/x" {
		t.Errorf("empty install: got %q, want %q", got, "/cover/x")
	}
}
