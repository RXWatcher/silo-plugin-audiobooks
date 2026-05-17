// Package httproutes adapts a stdlib http.Handler to the SDK's HttpRoutes.v1
// gRPC service. The plugin host invokes our gRPC service for each inbound
// HTTP request; we replay against the wrapped handler and return its response.
package httproutes

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"

	pluginv1 "github.com/ContinuumApp/continuum-plugin-sdk/pkg/pluginproto/continuum/plugin/v1"
)

// Server implements pluginv1.HttpRoutesServer with a swappable handler.
type Server struct {
	pluginv1.UnimplementedHttpRoutesServer
	handler atomic.Pointer[http.Handler]
}

// NewServer constructs an unconfigured server; returns 503 until SetHandler.
func NewServer() *Server { return &Server{} }

// SetHandler atomically replaces the active handler. Pass nil to clear.
func (s *Server) SetHandler(h http.Handler) {
	if h == nil {
		s.handler.Store(nil)
		return
	}
	s.handler.Store(&h)
}

// ServeHTTP exposes the active handler to a standalone HTTP listener so
// operators can reverse-proxy a hostname (e.g. abs.example.com) directly to
// this plugin's public routes. Before SetHandler has been called, returns 503
// in the same shape as Handle.
//
// SECURITY: strips inbound X-Continuum-* headers before invoking the handler.
// These headers are the host plane's trust channel (X-Continuum-User-Id,
// X-Continuum-User-Role, etc. — injected by the host's plugin proxy after
// session validation). A client connecting directly to the standalone port
// must never be able to forge them, otherwise auth checks inside handlers
// would accept attacker-supplied identity. Stripping them puts the request
// in the same shape as an anonymous, public-route request; any handler that
// requires authenticated identity will naturally 401.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	hPtr := s.handler.Load()
	if hPtr == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"not_ready","message":"plugin not configured"}}`))
		return
	}
	for k := range r.Header {
		if strings.HasPrefix(strings.ToLower(k), "x-continuum-") {
			r.Header.Del(k)
		}
	}
	(*hPtr).ServeHTTP(w, r)
}

// Handle is the gRPC entrypoint; replays the request against the wrapped
// handler and returns its response.
func (s *Server) Handle(_ context.Context, req *pluginv1.HandleHTTPRequest) (*pluginv1.HandleHTTPResponse, error) {
	hPtr := s.handler.Load()
	if hPtr == nil {
		return &pluginv1.HandleHTTPResponse{
			StatusCode: http.StatusServiceUnavailable,
			Body:       []byte(`{"error":{"code":"not_ready","message":"plugin not configured"}}`),
			Headers:    map[string]string{"Content-Type": "application/json"},
		}, nil
	}
	h := *hPtr

	rawQuery := ""
	if req.GetQuery() != nil {
		vals := url.Values{}
		for k, v := range req.GetQuery().GetFields() {
			// Use the scalar value, not v.String() (which is the protobuf
			// debug form: a number arrives as "number_value:50", corrupting
			// ?limit= / ?library_id= so pagination/scoping silently breaks).
			switch val := v.AsInterface().(type) {
			case string:
				vals.Set(k, val)
			case bool:
				vals.Set(k, strconv.FormatBool(val))
			case float64:
				vals.Set(k, strconv.FormatFloat(val, 'f', -1, 64))
			}
		}
		rawQuery = vals.Encode()
	}

	u := &url.URL{Path: req.GetPath(), RawQuery: rawQuery}
	method := req.GetMethod()
	if method == "" {
		method = http.MethodGet
	}
	httpReq := httptest.NewRequest(method, u.String(), bytes.NewReader(req.GetBody()))
	for k, v := range req.GetHeaders() {
		httpReq.Header.Set(k, v)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httpReq)

	body, _ := io.ReadAll(rec.Result().Body)
	headers := map[string]string{}
	for k, vs := range rec.Header() {
		if len(vs) > 0 {
			headers[k] = vs[0]
		}
	}
	return &pluginv1.HandleHTTPResponse{
		StatusCode: int32(rec.Code),
		Headers:    headers,
		Body:       body,
	}, nil
}
