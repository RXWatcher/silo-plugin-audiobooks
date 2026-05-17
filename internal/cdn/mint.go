// Package cdn signs presigned-URL JWTs for the local audiobooks plugin's
// standalone HTTP listener. The shared HMAC secret is configured by the
// operator (cdn_signing_secret in this plugin's manifest, plus
// stream_signing_secret in local-audiobooks' manifest — same value pasted
// into both).
package cdn

import (
	"errors"
	"net/url"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// audience the verifier on the local audiobooks side requires. Don't change
// without coordinated update.
const audience = "local_audiobooks"

// MintStreamToken returns an HS256-signed JWT scoped to (book_id, file_idx)
// and expiring after ttl. The verifier on the local audiobooks side rejects
// tokens with the wrong audience, mismatched book/file binding, or
// expired exp.
func MintStreamToken(secret []byte, userID, bookID string, fileIdx int, ttl time.Duration) (string, error) {
	if len(secret) == 0 {
		return "", errors.New("empty signing secret")
	}
	claims := jwt.MapClaims{
		"sub":      userID,
		"aud":      audience,
		"book_id":  bookID,
		"file_idx": fileIdx,
		"exp":      time.Now().Add(ttl).Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString(secret)
}

// PresignedURL formats the redirect URL the mobile client follows.
// hostname is e.g. "audiobooks-cdn.example.com" (no scheme, no trailing
// slash). The result includes "https://".
func PresignedURL(hostname, bookID string, fileIdx int, token string) string {
	return "https://" + hostname + "/api/v1/file/" + url.PathEscape(bookID) + "?token=" + url.QueryEscape(token)
}
