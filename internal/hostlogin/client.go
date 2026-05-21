// Package hostlogin validates a username/password pair against the Continuum
// host's public `POST /api/v1/auth/login` endpoint and returns the validated
// user id. It exists so the audiobooks plugin's standalone-port `/abs/api/login`
// can accept Audiobookshelf-client-style body credentials without ever
// touching a password itself.
//
// The host's local provider gates on user.LocalPasswordLoginEnabled, so users
// who exist only as OIDC accounts without a local password fail closed here.
package hostlogin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrInvalidCredentials is returned when the host responds with 401 or 403 to
// the login attempt. Callers map this to a 401 toward the ABS client.
var ErrInvalidCredentials = errors.New("invalid credentials")

// ErrUpstream is wrapped for any host failure that is not an explicit
// credential rejection (timeouts, 5xx, malformed JSON). Callers map this to
// 502 so the ABS client can distinguish "bad password" from "service down".
var ErrUpstream = errors.New("upstream host login failure")

// Client posts credentials to the host's auth endpoint. One Client is meant
// to be shared across the plugin process.
type Client struct {
	base string
	hc   *http.Client
}

// New builds a Client targeting the given host base URL (e.g.
// "http://localhost:8080"). Trailing slashes are tolerated.
func New(hostBaseURL string) *Client {
	return &Client{
		base: strings.TrimRight(hostBaseURL, "/"),
		hc:   &http.Client{Timeout: 10 * time.Second},
	}
}

// WithHTTPClient swaps the underlying http.Client. Useful for tests against
// httptest.Server.
func (c *Client) WithHTTPClient(hc *http.Client) *Client {
	c.hc = hc
	return c
}

// Result is the validated identity returned by the host.
type Result struct {
	UserID      string
	DisplayName string
}

// Validate posts {username, password, provider: "local"} to
// `<base>/api/v1/auth/login`. Returns ErrInvalidCredentials on host 401/403,
// ErrUpstream wrapping the underlying error otherwise.
//
// deviceName populates the host's session device-name field (sent in the
// User-Agent header). ip is forwarded as X-Forwarded-For so the host's
// rate limiter and audit log see the listener's real IP, not the plugin's.
func (c *Client) Validate(ctx context.Context, username, password, deviceName, ip string) (Result, error) {
	if username == "" || password == "" {
		return Result{}, ErrInvalidCredentials
	}
	body, err := json.Marshal(map[string]string{
		"username": username,
		"password": password,
		"provider": "local",
	})
	if err != nil {
		return Result{}, fmt.Errorf("%w: marshal: %v", ErrUpstream, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/v1/auth/login", bytes.NewReader(body))
	if err != nil {
		return Result{}, fmt.Errorf("%w: new request: %v", ErrUpstream, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if deviceName != "" {
		req.Header.Set("User-Agent", deviceName)
	}
	if ip != "" {
		req.Header.Set("X-Forwarded-For", ip)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("%w: do: %v", ErrUpstream, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		_, _ = io.Copy(io.Discard, resp.Body)
		return Result{}, ErrInvalidCredentials
	}
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return Result{}, fmt.Errorf("%w: status %d: %s", ErrUpstream, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	// The host emits user.id as a JSON number (int). The audiobooks plugin
	// keys everything by the stringified user id the host stamps into
	// X-Continuum-User-Id (continuum/internal/plugins/http_proxy.go:155 —
	// strconv.Itoa(userID)). Match that here so the validated identity drops
	// into the existing handler path unchanged.
	var payload struct {
		User struct {
			ID          json.Number `json:"id"`
			Username    string      `json:"username"`
			DisplayName string      `json:"display_name"`
		} `json:"user"`
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		return Result{}, fmt.Errorf("%w: decode: %v", ErrUpstream, err)
	}
	userID := strings.TrimSpace(string(payload.User.ID))
	if userID == "" {
		return Result{}, fmt.Errorf("%w: empty user id in host response", ErrUpstream)
	}
	// Reject anything that isn't a positive integer — the host always emits
	// an int and the plugin's stored ids must match the header form exactly.
	if n, err := strconv.ParseInt(userID, 10, 64); err != nil || n <= 0 {
		return Result{}, fmt.Errorf("%w: non-integer user id %q", ErrUpstream, userID)
	}
	display := payload.User.DisplayName
	if display == "" {
		display = payload.User.Username
	}
	return Result{UserID: userID, DisplayName: display}, nil
}
