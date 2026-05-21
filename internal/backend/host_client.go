package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginsdk/runtimehost"
)

// maxResponseBytes caps response bodies read from upstream plugin backends
// via the host's plugin proxy. Backend JSON responses are well under this
// in normal operation; the cap defends against memory exhaustion if an
// upstream returns a runaway body.
const maxResponseBytes = 10 << 20 // 10 MiB

// errBodySnippet caps how much of an upstream error body is inlined into an
// error string (errors propagate to logs and HTTP responses).
const errBodySnippet = 512

func truncForError(b []byte) string {
	if len(b) <= errBodySnippet {
		return string(b)
	}
	return string(b[:errBodySnippet]) + "…(truncated)"
}

// HostClient issues HTTP requests to other plugins via the continuum host's
// plugin proxy. The portal uses one HostClient per installed backend. Calls
// prefer the per-request bearer from the caller and fall back to the plugin's
// own service token when the inbound request was cookie-authenticated.
type HostClient struct {
	base        string
	token       string
	hc          *http.Client
	runtimeHost *runtimehost.Client
}

// NewHostClient builds a HostClient bound to the host base URL (e.g.
// "http://continuum:8080"). Trailing slash is tolerated.
func NewHostClient(hostBaseURL string) *HostClient {
	return &HostClient{
		base: strings.TrimRight(hostBaseURL, "/"),
		hc:   &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *HostClient) WithRuntimeHost(host *runtimehost.Client) *HostClient {
	c.runtimeHost = host
	return c
}

func (c *HostClient) WithServiceToken(token string) *HostClient {
	c.token = strings.TrimSpace(token)
	return c
}

// pluginURL builds the host proxy URL.
//
//	<base>/api/v1/plugins/<installID>/<pathAndQuery>
func (c *HostClient) pluginURL(installID, pathAndQuery string) string {
	if !strings.HasPrefix(pathAndQuery, "/") {
		pathAndQuery = "/" + pathAndQuery
	}
	return fmt.Sprintf("%s/api/v1/plugins/%s%s", c.base, url.PathEscape(installID), pathAndQuery)
}

// Get issues a GET against the named plugin's proxy path. bearerToken is
// passed in the Authorization header (empty token sends no Authorization).
func (c *HostClient) Get(ctx context.Context, bearerToken, installID, pathAndQuery string) ([]byte, error) {
	return c.do(ctx, "GET", bearerToken, installID, pathAndQuery, nil)
}

// GetStream issues a GET against the plugin proxy and returns the live
// *http.Response so callers can stream bytes to a downstream client without
// buffering the whole body in memory. Use this for any payload that may be
// larger than maxResponseBytes — audio files in particular.
//
// Forwards the supplied extraHeaders verbatim (call sites use this to pass
// Range, If-Match, etc. through from the inbound request). Bearer falls
// back to the plugin's service token when empty.
//
// Always uses the host's HTTP-proxy path (not the SDK CallPluginHTTP RPC) —
// CallPluginHTTP returns a single buffered []byte and can't stream. The
// host's HTTP plugin proxy IS a real reverse-proxy and streams.
//
// Caller MUST resp.Body.Close() when done. Non-2xx responses are surfaced
// as a normal *http.Response (no error) so callers can pass status codes
// like 416 (Range Not Satisfiable) and 206 (Partial Content) through.
func (c *HostClient) GetStream(ctx context.Context, bearerToken, installID, pathAndQuery string, extraHeaders map[string]string) (*http.Response, error) {
	token := c.authToken(bearerToken)
	target := c.pluginURL(installID, pathAndQuery)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	// Use a no-timeout client for the stream path; the audio body can take
	// hours for a single audiobook session. The default c.hc has a 30s
	// timeout which would cut every playback short of finishing a chapter.
	resp, err := streamClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do: %w", err)
	}
	return resp, nil
}

// streamClient is the no-timeout http.Client for GetStream. Per-request
// cancellation comes from the ctx the caller supplies (request handlers
// thread their r.Context() through), so we don't need a top-level deadline.
var streamClient = &http.Client{
	// No Timeout — see GetStream docstring.
}

// GetBinary issues a GET like Get but also returns the upstream Content-Type
// header, so callers proxying binary payloads (cover images, etc) can preserve
// it. Body is buffered into memory — only call this for resources expected to
// fit comfortably under maxResponseBytes (covers are typically &lt; 1 MiB).
func (c *HostClient) GetBinary(ctx context.Context, bearerToken, installID, pathAndQuery string) (body []byte, contentType string, err error) {
	token := c.authToken(bearerToken)
	if c.runtimeHost != nil {
		if id, err := strconv.Atoi(installID); err == nil && id > 0 {
			path, query := splitPluginPath(pathAndQuery)
			headers := map[string]string{}
			if token != "" {
				headers["Authorization"] = "Bearer " + token
			}
			resp, err := c.runtimeHost.CallPluginHTTP(ctx, runtimehost.CallPluginHTTPRequest{
				InstallationID: id,
				Method:         "GET",
				Path:           path,
				Headers:        headers,
				Query:          query,
			})
			if err != nil {
				return nil, "", err
			}
			if resp.StatusCode >= 400 {
				return nil, "", fmt.Errorf("backend %d: %s", resp.StatusCode, truncForError(resp.Body))
			}
			if len(resp.Body) > maxResponseBytes {
				return nil, "", fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
			}
			ct := ""
			for k, v := range resp.Headers {
				if strings.EqualFold(k, "Content-Type") {
					ct = v
					break
				}
			}
			return resp.Body, ct, nil
		}
	}
	url := c.pluginURL(installID, pathAndQuery)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("new request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("do: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, errBodySnippet))
		return nil, "", fmt.Errorf("backend %d: %s", resp.StatusCode, truncForError(snippet))
	}
	out, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, "", fmt.Errorf("read body: %w", err)
	}
	return out, resp.Header.Get("Content-Type"), nil
}

// GetJSON issues a GET and decodes the JSON response. When the SDK
// RuntimeHost is available this uses its typed JSON helper; otherwise it
// falls back to the host HTTP proxy path.
func (c *HostClient) GetJSON(ctx context.Context, bearerToken, installID, pathAndQuery string, out any) error {
	token := c.authToken(bearerToken)
	if c.runtimeHost != nil {
		if id, err := strconv.Atoi(installID); err == nil && id > 0 {
			path, query := splitPluginPath(pathAndQuery)
			headers := map[string]string{}
			if token != "" {
				headers["Authorization"] = "Bearer " + token
			}
			err := c.runtimeHost.CallPluginJSON(ctx, runtimehost.CallPluginJSONRequest{
				InstallationID:   id,
				Path:             path,
				Headers:          headers,
				Query:            query,
				Response:         out,
				MaxResponseBytes: maxResponseBytes,
			})
			var statusErr *runtimehost.HTTPStatusError
			if errors.As(err, &statusErr) {
				return fmt.Errorf("backend %d: %s", statusErr.StatusCode, string(statusErr.Body))
			}
			return err
		}
	}
	body, err := c.Get(ctx, token, installID, pathAndQuery)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// PostJSON issues a POST with a JSON body.
func (c *HostClient) PostJSON(ctx context.Context, bearerToken, installID, pathAndQuery string, body []byte) ([]byte, error) {
	return c.do(ctx, "POST", bearerToken, installID, pathAndQuery, body)
}

// PluginURL returns an origin-relative path the SPA / ABS apps can fetch via
// (e.g. for <audio src=...>). Useful for streaming proxy that simply
// redirects the client.
//
// The returned value is intentionally path-only (no scheme or host) so the
// browser resolves it against its current origin. The internal hostBase
// (typically http://localhost:8080) is not reachable from a public client.
func (c *HostClient) PluginURL(installID, pathAndQuery string) string {
	if !strings.HasPrefix(pathAndQuery, "/") {
		pathAndQuery = "/" + pathAndQuery
	}
	return fmt.Sprintf("/api/v1/plugins/%s%s", url.PathEscape(installID), pathAndQuery)
}

func (c *HostClient) authToken(requestBearer string) string {
	if token := strings.TrimSpace(requestBearer); token != "" {
		return token
	}
	return c.token
}

func (c *HostClient) do(ctx context.Context, method, bearer, installID, pathAndQuery string, body []byte) ([]byte, error) {
	token := c.authToken(bearer)
	if c.runtimeHost != nil {
		if id, err := strconv.Atoi(installID); err == nil && id > 0 {
			path, query := splitPluginPath(pathAndQuery)
			headers := map[string]string{"Accept": "application/json"}
			if token != "" {
				headers["Authorization"] = "Bearer " + token
			}
			if body != nil {
				headers["Content-Type"] = "application/json"
			}
			resp, err := c.runtimeHost.CallPluginHTTP(ctx, runtimehost.CallPluginHTTPRequest{
				InstallationID: id,
				Method:         method,
				Path:           path,
				Headers:        headers,
				Body:           body,
				Query:          query,
			})
			if err != nil {
				return nil, err
			}
			if resp.StatusCode >= 400 {
				return nil, fmt.Errorf("backend %d: %s", resp.StatusCode, truncForError(resp.Body))
			}
			if len(resp.Body) > maxResponseBytes {
				return nil, fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
			}
			return resp.Body, nil
		}
	}
	url := c.pluginURL(installID, pathAndQuery)
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do: %w", err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("backend %d: %s", resp.StatusCode, truncForError(out))
	}
	return out, nil
}

func splitPluginPath(pathAndQuery string) (string, map[string]any) {
	u, err := url.Parse(pathAndQuery)
	if err != nil || u.RawQuery == "" {
		return pathAndQuery, nil
	}
	query := make(map[string]any)
	for key, values := range u.Query() {
		if len(values) == 1 {
			query[key] = values[0]
			continue
		}
		items := make([]any, 0, len(values))
		for _, value := range values {
			items = append(items, value)
		}
		query[key] = items
	}
	return u.Path, query
}
