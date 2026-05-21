package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/oklog/ulid/v2"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/store"
)

// audit is the helper admin handlers call to record their action.
// Best-effort: a failure to write an audit row never blocks the
// request — surface in the log instead. The handler captures actor
// IP + user-agent from the request automatically.
func (s *Server) audit(r *http.Request, actorID, action, entityType, entityID string, payload any) {
	if s.d.Store == nil || actorID == "" {
		return
	}
	var payloadJSON json.RawMessage
	if payload != nil {
		if b, err := json.Marshal(payload); err == nil {
			payloadJSON = b
		}
	}
	if len(payloadJSON) == 0 {
		payloadJSON = json.RawMessage("{}")
	}
	entry := store.AuditLogEntry{
		ID:         ulid.Make().String(),
		ActorID:    actorID,
		Action:     action,
		EntityType: entityType,
		EntityID:   entityID,
		IP:         clientIP(r),
		UserAgent:  r.UserAgent(),
		Payload:    payloadJSON,
	}
	if err := s.d.Store.AppendAuditEntry(r.Context(), entry); err != nil {
		// Log + swallow; never block a write on audit failure.
		slog.Warn("audit append failed", "action", action, "err", err.Error())
	}
}

// clientIP picks the best available client IP. X-Forwarded-For
// when behind a reverse proxy (first entry in the list); falls
// back to r.RemoteAddr otherwise.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if addr := r.RemoteAddr; addr != "" {
		if i := strings.LastIndex(addr, ":"); i > 0 {
			return addr[:i]
		}
		return addr
	}
	return ""
}
