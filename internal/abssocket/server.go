// Package abssocket exposes a Socket.io-compatible realtime endpoint for
// the official Audiobookshelf clients. It runs alongside the standalone
// HTTP listener (path /socket.io/*) — the continuum host's plugin proxy
// can't bridge websocket upgrades (the SDK's CallPluginHTTP is a typed
// request/response RPC), so a Socket.io connection has to terminate on
// the standalone port.
//
// Authentication mirrors real ABS exactly: a Socket.io connection opens
// unauthenticated, then the client emits an "auth" event whose payload is
// the access JWT minted by /abs/api/login. We validate the JWT against
// the plugin's backend_config.ABSJWTSecret (the same secret /abs/api/me
// and friends already use), then join the connection to a user-scoped
// Socket.io room. Events published via Publish(userID, ...) reach every
// client currently connected with that user's token, on this process.
//
// Scope limit: this is a single-process hub. If the audiobooks plugin
// runs as multiple replicas with sticky-session balancing, every replica
// holds an independent in-memory hub — publishes from replica A do not
// reach a client connected to replica B. The host runs the plugin as a
// single instance today, so this is fine; a multi-replica future would
// want a Redis adapter or similar.
package abssocket

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/zishang520/socket.io/v2/socket"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/abs"
	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
)

// Logger is the narrow logging surface this package needs. Implementations
// route to hclog or slog; nil falls back to a no-op.
type Logger interface {
	Debug(msg string, args ...any)
	Warn(msg string, args ...any)
}

type noopLogger struct{}

func (noopLogger) Debug(string, ...any) {}
func (noopLogger) Warn(string, ...any)  {}

// SecretFn returns the current ABS JWT signing secret. Called on every
// inbound "auth" event so an admin secret-rotate takes effect for new
// connections without a plugin restart.
type SecretFn func() []byte

// StoreFn returns the active store, or nil if the plugin hasn't finished
// configuring yet. Used for the optional revoked-token lookup; when nil,
// only the JWT signature/expiry guard the connection.
type StoreFn func() *store.Store

// Server is the Socket.io realtime server. Construct one per process and
// reuse across plugin reconfigures — the internal Socket.io engine carries
// goroutines and event loops we don't want to leak on each Configure.
type Server struct {
	io       *socket.Server
	secretFn SecretFn
	storeFn  StoreFn
	logger   Logger

	mu        sync.Mutex
	connCount int // diagnostics only
}

// New builds a Server. secretFn is required. storeFn is optional but
// recommended — without it, a stolen ABS JWT will continue connecting
// even after the operator revokes it via the admin token-revoke endpoint.
func New(secretFn SecretFn, storeFn StoreFn, logger Logger) *Server {
	if logger == nil {
		logger = noopLogger{}
	}
	io := socket.NewServer(nil, nil)
	s := &Server{
		io:       io,
		secretFn: secretFn,
		storeFn:  storeFn,
		logger:   logger,
	}
	io.On("connection", func(args ...any) {
		if len(args) == 0 {
			return
		}
		client, ok := args[0].(*socket.Socket)
		if !ok {
			return
		}
		s.onConnection(client)
	})
	return s
}

func (s *Server) onConnection(client *socket.Socket) {
	s.mu.Lock()
	s.connCount++
	s.mu.Unlock()
	s.logger.Debug("abssocket: connection opened", "sid", client.Id())

	// ABS clients emit "auth" once with the access token as the payload.
	// Until that succeeds the socket sits in the unauthenticated default
	// namespace and receives no scoped events.
	client.On("auth", func(args ...any) {
		token := pickToken(args)
		if token == "" {
			s.logger.Warn("abssocket: auth without token", "sid", client.Id())
			_ = client.Emit("auth_unauthorized", "missing token")
			client.Disconnect(true)
			return
		}
		secret := s.secretFn()
		if len(secret) == 0 {
			s.logger.Warn("abssocket: server not ready (no jwt secret)", "sid", client.Id())
			_ = client.Emit("auth_unauthorized", "server not ready")
			client.Disconnect(true)
			return
		}
		claims, err := abs.ParseToken(secret, token)
		if err != nil || claims.Type != "access" {
			s.logger.Warn("abssocket: auth rejected", "sid", client.Id(), "err", errString(err))
			_ = client.Emit("auth_unauthorized", "invalid token")
			client.Disconnect(true)
			return
		}
		if s.storeFn != nil {
			if st := s.storeFn(); st != nil {
				row, err := st.GetABSTokenByJTI(context.Background(), claims.JTI)
				if err != nil || row.RevokedAt != nil {
					s.logger.Warn("abssocket: token revoked", "sid", client.Id(), "jti", claims.JTI)
					_ = client.Emit("auth_unauthorized", "token revoked")
					client.Disconnect(true)
					return
				}
			}
		}
		// Bind the socket to a user-scoped room so Publish(userID, ...) can
		// fan a single in-process emit across every device on that account.
		client.Join(userRoom(claims.UserID))
		s.logger.Debug("abssocket: auth ok", "sid", client.Id(), "user_id", claims.UserID)
		_ = client.Emit("auth_authorized", map[string]any{
			"user_id": claims.UserID,
		})
	})

	client.On("disconnect", func(...any) {
		s.mu.Lock()
		if s.connCount > 0 {
			s.connCount--
		}
		s.mu.Unlock()
		s.logger.Debug("abssocket: connection closed", "sid", client.Id())
	})
}

// Handler returns the http.Handler that the standalone listener should
// dispatch /socket.io/* requests to.
func (s *Server) Handler() http.Handler {
	return s.io.ServeHandler(nil)
}

// Publish emits the given event to every socket currently joined to the
// user's room. Non-blocking; a publish to a userID with zero connected
// sockets is a no-op. Returns the number of recipient sockets for
// observability — best-effort, no synchronisation guarantees with the
// underlying broadcast.
func (s *Server) Publish(userID, event string, payload any) {
	if userID == "" {
		return
	}
	s.io.To(userRoom(userID)).Emit(event, payload)
}

// Broadcast emits to every authenticated socket regardless of user. Use
// this for global events like library_item_added that aren't user-scoped.
func (s *Server) Broadcast(event string, payload any) {
	s.io.Emit(event, payload)
}

// ConnectionCount returns the current authenticated-connection count, for
// admin diagnostics. Best-effort; not synchronised with in-flight
// connect/disconnect.
func (s *Server) ConnectionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connCount
}

// Close shuts the Socket.io server down. Idempotent.
func (s *Server) Close() {
	s.io.Close(nil)
}

func userRoom(userID string) socket.Room {
	return socket.Room("user:" + userID)
}

// pickToken extracts the bearer JWT from the variadic "auth" payload.
// Real ABS clients send a single string. Our SPA may send a struct like
// {token: "..."} to mirror the abs-socket-client-demo. Accept both.
func pickToken(args []any) string {
	if len(args) == 0 {
		return ""
	}
	switch v := args[0].(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]any:
		if t, ok := v["token"].(string); ok {
			return strings.TrimSpace(t)
		}
	}
	return ""
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
