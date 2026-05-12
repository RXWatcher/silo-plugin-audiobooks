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
			if sv := v.GetStringValue(); sv != "" {
				vals.Set(k, sv)
				continue
			}
			vals.Set(k, v.String())
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
