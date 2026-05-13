// Package runtime implements the audiobooks portal's Runtime gRPC server.
package runtime

import (
	"context"
	"fmt"
	"sync"

	pluginv1 "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginproto/continuum/plugin/v1"
	"github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginsdk/runtimedefault"
)

// Config is the parsed plugin global config. Per the spec, only DatabaseURL
// is a host-managed global config; all other settings live in the portal's
// backend_config table and are written via the admin SPA.
type Config struct {
	DatabaseURL          string `json:"database_url"`
	StandaloneHTTPListen string `json:"standalone_http_listen"`
	CDNHostname          string `json:"cdn_hostname"`
	CDNSigningSecret     string `json:"cdn_signing_secret"`
}

// Configured reports whether the required fields are set.
func (c Config) Configured() bool { return c.DatabaseURL != "" }

// Server implements the plugin's Runtime service.
type Server struct {
	runtimedefault.Server
	manifest *pluginv1.PluginManifest
	onCfg    func(Config) error

	mu  sync.RWMutex
	cfg Config
}

// New constructs a runtime server.
func New(manifest *pluginv1.PluginManifest, onConfig func(Config) error) *Server {
	return &Server{manifest: manifest, onCfg: onConfig}
}

func (s *Server) GetManifest(_ context.Context, _ *pluginv1.GetManifestRequest) (*pluginv1.GetManifestResponse, error) {
	return &pluginv1.GetManifestResponse{Manifest: s.manifest}, nil
}

func (s *Server) Configure(_ context.Context, req *pluginv1.ConfigureRequest) (*pluginv1.ConfigureResponse, error) {
	cfg := Config{}
	for _, e := range req.GetConfig() {
		v := e.GetValue()
		if v == nil {
			continue
		}
		m := v.AsMap()
		switch e.GetKey() {
		case "database_url":
			cfg.DatabaseURL = stringFromValue(m["value"], firstString(m))
		case "standalone_http_listen":
			cfg.StandaloneHTTPListen = stringFromValue(m["value"], firstString(m))
		case "cdn_hostname":
			cfg.CDNHostname = stringFromValue(m["value"], firstString(m))
		case "cdn_signing_secret":
			cfg.CDNSigningSecret = stringFromValue(m["value"], firstString(m))
		}
	}
	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("database_url is required")
	}
	if s.onCfg != nil {
		if err := s.onCfg(cfg); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	return &pluginv1.ConfigureResponse{}, nil
}

// Snapshot returns a copy of the current config.
func (s *Server) Snapshot() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func stringFromValue(candidates ...any) string {
	for _, c := range candidates {
		if s, ok := c.(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func firstString(m map[string]any) any {
	for _, v := range m {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return nil
}
