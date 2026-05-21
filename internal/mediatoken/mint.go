// Package mediatoken mints HS256 JWTs the portal embeds in cover and stream
// URLs. Backends verify with the same shared secret (their
// stream_signing_secret), so a token leaked from a URL only grants access to
// one book/file for a short window, instead of granting the user's full
// account access for the life of their bearer.
package mediatoken

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Audience must match the value backend plugins verify against (see
// bw-audio's internal/tokens package and local-audiobooks's
// internal/auth/streamtoken package).
const Audience = "audiobook_backend"

// CoverFileIdx is the sentinel claim value for cover tokens — covers don't
// address a specific file in a multi-file book.
const CoverFileIdx = -1

// DefaultTTL is the time-to-live for minted media tokens. Short enough that
// a leaked URL stops working quickly; long enough that the SPA can hand the
// URL to the browser and the browser can fetch + cache + retry without
// race-condition failures.
const DefaultTTL = 15 * time.Minute

// ErrSecretUnconfigured is returned when minting is attempted with an empty
// signing secret — caller should treat this as misconfigured.
var ErrSecretUnconfigured = errors.New("media signing secret not configured")

// Mint produces a signed token bound to userID + bookID + fileIdx. Use
// CoverFileIdx for cover tokens.
func Mint(secret, userID, bookID string, fileIdx int) (string, error) {
	if strings.TrimSpace(secret) == "" {
		return "", ErrSecretUnconfigured
	}
	if userID == "" {
		return "", errors.New("userID required")
	}
	if bookID == "" {
		return "", errors.New("bookID required")
	}
	key := decodeSecret(secret)
	now := time.Now()
	claims := jwt.MapClaims{
		"aud":      Audience,
		"sub":      userID,
		"book_id":  bookID,
		"file_idx": fileIdx,
		"iat":      now.Unix(),
		"exp":      now.Add(DefaultTTL).Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(key)
	if err != nil {
		return "", fmt.Errorf("sign: %w", err)
	}
	return signed, nil
}

// decodeSecret mirrors the backend-side helper. The decode order is the
// contract — both portal (here) and backend plugins (bw-audio's
// internal/tokens, local-audiobooks's internal/auth/streamtoken) MUST run the
// same try-base64-first-fallback-raw logic so a given configured secret
// produces the same key on both sides.
//
// Ambiguity: a raw string whose bytes happen to be valid base64 (e.g.
// "dGVzdA==") will be decoded as base64 rather than treated as raw bytes.
// This is consistent across portal and backend so HMAC keys match, but
// operators picking a high-entropy ASCII secret should avoid values that
// look like base64 if they want "what I typed" semantics. See
// mediatoken/mint_test.go for the round-trip pinned-behaviour tests.
func decodeSecret(secret string) []byte {
	if b, err := base64.StdEncoding.DecodeString(secret); err == nil && len(b) > 0 {
		return b
	}
	if b, err := base64.RawStdEncoding.DecodeString(secret); err == nil && len(b) > 0 {
		return b
	}
	return []byte(secret)
}
