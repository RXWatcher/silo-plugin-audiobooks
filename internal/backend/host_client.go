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

// HostClient issues HTTP requests to other plugins via the continuum host's
// plugin proxy. The portal uses one HostClient per installed backend. The
// bearer token is provided per-request from the caller (typically the user
// header forwarded from the inbound request).
type HostClient struct {
	base        string
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

// GetJSON issues a GET and decodes the JSON response. When the SDK
// RuntimeHost is available this uses its typed JSON helper; otherwise it
// falls back to the host HTTP proxy path.
func (c *HostClient) GetJSON(ctx context.Context, bearerToken, installID, pathAndQuery string, out any) error {
	if c.runtimeHost != nil {
		if id, err := strconv.Atoi(installID); err == nil && id > 0 {
			path, query := splitPluginPath(pathAndQuery)
			headers := map[string]string{}
			if bearerToken != "" {
				headers["Authorization"] = "Bearer " + bearerToken
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
	body, err := c.Get(ctx, bearerToken, installID, pathAndQuery)
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

// PluginURL returns the public host URL the SPA / ABS apps can fetch via
// (e.g. for <audio src=...>). Useful for streaming proxy that simply
// redirects the client.
func (c *HostClient) PluginURL(installID, pathAndQuery string) string {
	return c.pluginURL(installID, pathAndQuery)
}

func (c *HostClient) do(ctx context.Context, method, bearer, installID, pathAndQuery string, body []byte) ([]byte, error) {
	if c.runtimeHost != nil {
		if id, err := strconv.Atoi(installID); err == nil && id > 0 {
			path, query := splitPluginPath(pathAndQuery)
			headers := map[string]string{"Accept": "application/json"}
			if bearer != "" {
				headers["Authorization"] = "Bearer " + bearer
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
				return nil, fmt.Errorf("backend %d: %s", resp.StatusCode, string(resp.Body))
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
