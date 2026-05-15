package backend

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxResponseBytes caps response bodies read from upstream plugin backends
// via the host's plugin proxy. Backend JSON responses are well under this
// in normal operation; the cap defends against memory exhaustion if an
// upstream returns a runaway body.
const maxResponseBytes = 10 << 20 // 10 MiB

// HostClient issues HTTP requests to other plugins via the continuum host's
// plugin proxy. The portal uses one HostClient per installed backend. The
// bearer token is provided per-request from the caller (typically the user
// header forwarded from the inbound request).
type HostClient struct {
	base string
	hc   *http.Client
}

// NewHostClient builds a HostClient bound to the host base URL (e.g.
// "http://continuum:8080"). Trailing slash is tolerated.
func NewHostClient(hostBaseURL string) *HostClient {
	return &HostClient{
		base: strings.TrimRight(hostBaseURL, "/"),
		hc:   &http.Client{Timeout: 30 * time.Second},
	}
}

// pluginURL builds the host proxy URL.
//
//	<base>/api/v1/plugins/<installID>/<pathAndQuery>
func (c *HostClient) pluginURL(installID, pathAndQuery string) string {
	if !strings.HasPrefix(pathAndQuery, "/") {
		pathAndQuery = "/" + pathAndQuery
	}
	return fmt.Sprintf("%s/api/v1/plugins/%s%s", c.base, installID, pathAndQuery)
}

// Get issues a GET against the named plugin's proxy path. bearerToken is
// passed in the Authorization header (empty token sends no Authorization).
func (c *HostClient) Get(ctx context.Context, bearerToken, installID, pathAndQuery string) ([]byte, error) {
	return c.do(ctx, "GET", bearerToken, installID, pathAndQuery, nil)
}

// PostJSON issues a POST with a JSON body.
func (c *HostClient) PostJSON(ctx context.Context, bearerToken, installID, pathAndQuery string, body []byte) ([]byte, error) {
	return c.do(ctx, "POST", bearerToken, installID, pathAndQuery, body)
}

// PluginURL returns the public host URL the SPA / ABS apps can fetch via
// (e.g. for <audio src=...>). Useful for streaming proxy that simply
// redirects the client.
func (c *HostClient) PluginURL(installID, pathAndQuery string) string {
	return c.pluginURL(installID, pathAndQuery)
}

func (c *HostClient) do(ctx context.Context, method, bearer, installID, pathAndQuery string, body []byte) ([]byte, error) {
	url := c.pluginURL(installID, pathAndQuery)
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
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
		return nil, fmt.Errorf("backend %d: %s", resp.StatusCode, string(out))
	}
	return out, nil
}
