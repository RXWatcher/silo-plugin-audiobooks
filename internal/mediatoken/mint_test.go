package mediatoken

import (
	"encoding/base64"
	"errors"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

// TestMint_Success round-trips a minted token through a verifier built with
// the same secret bytes, confirming the embedded claims (aud, sub, book_id,
// file_idx, exp present and roughly in-window).
func TestMint_Success(t *testing.T) {
	secret := "this-is-32-bytes-of-raw-secret!!"
	tok, err := Mint(secret, "u-42", "book-1", 3)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	parsed, err := jwt.Parse(tok, func(_ *jwt.Token) (any, error) {
		return decodeSecret(secret), nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !parsed.Valid {
		t.Fatalf("Parse: %v valid=%v", err, parsed.Valid)
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatalf("claims = %T, want MapClaims", parsed.Claims)
	}
	if got := claims["aud"]; got != Audience {
		t.Errorf("aud = %v, want %q", got, Audience)
	}
	if got := claims["sub"]; got != "u-42" {
		t.Errorf("sub = %v, want u-42", got)
	}
	if got := claims["book_id"]; got != "book-1" {
		t.Errorf("book_id = %v, want book-1", got)
	}
	if got := claims["file_idx"]; got != float64(3) {
		t.Errorf("file_idx = %v, want 3", got)
	}
	if _, ok := claims["exp"].(float64); !ok {
		t.Errorf("exp missing or wrong type: %v", claims["exp"])
	}
}

// TestMint_CoverFileIdx confirms the sentinel file_idx for cover tokens
// survives the round-trip — backends rely on it to distinguish cover vs
// file requests.
func TestMint_CoverFileIdx(t *testing.T) {
	secret := "another-32-bytes-raw-secret-here"
	tok, err := Mint(secret, "u-1", "book-x", CoverFileIdx)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	parsed, _ := jwt.Parse(tok, func(_ *jwt.Token) (any, error) {
		return decodeSecret(secret), nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	claims := parsed.Claims.(jwt.MapClaims)
	if got := claims["file_idx"]; got != float64(CoverFileIdx) {
		t.Errorf("file_idx = %v, want %d (CoverFileIdx)", got, CoverFileIdx)
	}
}

// TestMint_EmptySecret enforces the typed error so callers can map cleanly
// to a 503 instead of accidentally minting against an empty key (which would
// produce a token that backends would refuse with an opaque "invalid
// signature").
func TestMint_EmptySecret(t *testing.T) {
	_, err := Mint("", "u", "b", 0)
	if !errors.Is(err, ErrSecretUnconfigured) {
		t.Fatalf("err = %v, want ErrSecretUnconfigured", err)
	}
	_, err = Mint("   ", "u", "b", 0)
	if !errors.Is(err, ErrSecretUnconfigured) {
		t.Fatalf("whitespace-only secret: err = %v, want ErrSecretUnconfigured", err)
	}
}

// TestMint_EmptyUserOrBook guards against accidentally minting a token bound
// to no user / no book (which would mean the backend can't validate
// scope-of-access from the claims alone).
func TestMint_EmptyUserOrBook(t *testing.T) {
	secret := "x-y-z-x-y-z-x-y-z-x-y-z-x-y-z-x-y"
	if _, err := Mint(secret, "", "b", 0); err == nil {
		t.Error("empty userID should error")
	}
	if _, err := Mint(secret, "u", "", 0); err == nil {
		t.Error("empty bookID should error")
	}
}

// TestDecodeSecret_Base64Preferred pins the documented behavior: a string
// that parses as base64 is decoded; the resulting key is the decoded bytes,
// not the original string. Backends use the same logic, so both sides agree.
func TestDecodeSecret_Base64Preferred(t *testing.T) {
	// "dGVzdA==" base64-decodes to "test" (4 bytes).
	got := decodeSecret("dGVzdA==")
	if string(got) != "test" {
		t.Errorf("got %q, want %q — base64 must be preferred over raw bytes", got, "test")
	}
	// A string that isn't valid base64 falls through to raw bytes.
	got = decodeSecret("not-base64-because-of-dashes!!")
	if string(got) != "not-base64-because-of-dashes!!" {
		t.Errorf("got %q, want raw fall-through", got)
	}
}

// TestDecodeSecret_RawFallback covers the RawStdEncoding branch — base64
// without padding. Defensive: documents that operators can omit padding.
func TestDecodeSecret_RawFallback(t *testing.T) {
	// "dGVzdA" is "test" in raw (no-padding) base64.
	got := decodeSecret("dGVzdA")
	if string(got) != "test" {
		// Either raw-base64 or raw-bytes is acceptable depending on which
		// branch trips first; lock in the actual behavior so future edits
		// must update this test deliberately.
		// In practice StdEncoding rejects unpadded, RawStdEncoding accepts.
		decoded, _ := base64.RawStdEncoding.DecodeString("dGVzdA")
		t.Logf("StdEncoding rejected, RawStdEncoding decoded to %q", decoded)
		t.Errorf("got %q, want %q", got, "test")
	}
}
