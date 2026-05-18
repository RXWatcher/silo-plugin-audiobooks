// Package abs implements the Audiobookshelf-mobile-app compatibility surface.
// It mints self-contained JWTs signed with backend_config.abs_jwt_secret and
// serves the /abs/api/* and /abs/public/* routes.
package abs

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims are the unified ABS JWT claim set. Different `Type` values denote
// access, refresh, or session tokens.
type Claims struct {
	Type      string `json:"type"` // access | refresh | session
	UserID    string `json:"sub"`  // user id
	JTI       string `json:"jti"`  // token id (revocable)
	DeviceID  string `json:"device_id,omitempty"`
	SessionID string `json:"sid,omitempty"`
	BookID    string `json:"bid,omitempty"`
	FileIdx   int    `json:"fidx,omitempty"`
	jwt.RegisteredClaims
}

// IssueAccessToken mints a stateless access JWT.
func IssueAccessToken(secret []byte, userID, jti string, ttl time.Duration) (string, error) {
	return issue(secret, Claims{
		Type:   "access",
		UserID: userID,
		JTI:    jti,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	})
}

// IssueRefreshToken mints a refresh JWT.
func IssueRefreshToken(secret []byte, userID, jti string, ttl time.Duration) (string, error) {
	return issue(secret, Claims{
		Type:   "refresh",
		UserID: userID,
		JTI:    jti,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	})
}

// IssueSessionToken mints a streaming-capability JWT used in the public route.
func IssueSessionToken(secret []byte, userID, sessionID, bookID string, fileIdx int, ttl time.Duration) (string, error) {
	return issue(secret, Claims{
		Type:      "session",
		UserID:    userID,
		SessionID: sessionID,
		BookID:    bookID,
		FileIdx:   fileIdx,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	})
}

// ParseToken validates and decodes a JWT. Returns an error on signature
// mismatch or expiry.
func ParseToken(secret []byte, raw string) (*Claims, error) {
	claims := &Claims{}
	tok, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return secret, nil
	})
	if err != nil {
		return nil, err
	}
	if !tok.Valid {
		return nil, errors.New("token invalid")
	}
	return claims, nil
}

func issue(secret []byte, c Claims) (string, error) {
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	return t.SignedString(secret)
}
