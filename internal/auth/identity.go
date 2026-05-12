// Package auth extracts the host-injected identity from incoming HTTP
// requests. The continuum plugin proxy stamps X-Continuum-User-Id,
// X-Continuum-User-Role, and X-Continuum-User-Theme headers on every
// authenticated request.
package auth

import (
	"context"
	"net/http"
	"strings"
)

type ctxKey struct{}

// Identity is the per-request user state.
type Identity struct {
	UserID string
	Role   string
	Theme  string
	Token  string
}

// IsAdmin reports whether the user has admin role.
func (i Identity) IsAdmin() bool { return i.Role == "admin" }

// FromHeaders pulls the identity from the request's headers (and any
// inbound Authorization bearer for portal→backend forwarding).
func FromHeaders(r *http.Request) Identity {
	id := Identity{
		UserID: r.Header.Get("X-Continuum-User-Id"),
		Role:   r.Header.Get("X-Continuum-User-Role"),
		Theme:  r.Header.Get("X-Continuum-User-Theme"),
	}
	if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
		id.Token = strings.TrimPrefix(a, "Bearer ")
	}
	return id
}

// Middleware stores the identity in the request context.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), ctxKey{}, FromHeaders(r))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// FromContext retrieves the identity stored by Middleware.
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(ctxKey{}).(Identity)
	return id, ok
}

// RequireUser returns the identity or sets a 401 + ok=false.
func RequireUser(w http.ResponseWriter, r *http.Request) (Identity, bool) {
	id, ok := FromContext(r.Context())
	if !ok || id.UserID == "" {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return Identity{}, false
	}
	return id, true
}

// RequireAdmin returns the identity or sets 403 + ok=false.
func RequireAdmin(w http.ResponseWriter, r *http.Request) (Identity, bool) {
	id, ok := RequireUser(w, r)
	if !ok {
		return Identity{}, false
	}
	if !id.IsAdmin() {
		http.Error(w, "admin required", http.StatusForbidden)
		return Identity{}, false
	}
	return id, true
}
