package cdn_test

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ContinuumApp/continuum-plugin-audiobooks/internal/cdn"
)

func TestMintStreamToken_Verifiable(t *testing.T) {
	secret := []byte("test-secret-32-bytes-please-aaaaa")
	tok, err := cdn.MintStreamToken(secret, "user-1", "book-abc", 0, 5*time.Minute)
	if err != nil {
		t.Fatalf("MintStreamToken: %v", err)
	}
	parsed, err := jwt.Parse(tok, func(_ *jwt.Token) (any, error) { return secret, nil },
		jwt.WithAudience("local_audiobooks"), jwt.WithExpirationRequired())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	claims := parsed.Claims.(jwt.MapClaims)
	if claims["sub"].(string) != "user-1" {
		t.Errorf("sub = %v", claims["sub"])
	}
	if claims["book_id"].(string) != "book-abc" {
		t.Errorf("book_id = %v", claims["book_id"])
	}
}

func TestMintStreamToken_EmptySecret_Errors(t *testing.T) {
	if _, err := cdn.MintStreamToken(nil, "u", "b", 0, time.Minute); err == nil {
		t.Fatal("expected error on empty secret")
	}
}

func TestPresignedURL_Shape(t *testing.T) {
	u := cdn.PresignedURL("audiobooks-cdn.example.com", "abc", 0, "TOK")
	want := "https://audiobooks-cdn.example.com/api/v1/file/abc?token=TOK"
	if u != want {
		t.Errorf("u = %q, want %q", u, want)
	}
}

// bookID must be percent-escaped so a crafted catalog id can't inject a
// path/query into the presigned redirect URL.
func TestPresignedURL_EscapesBookID(t *testing.T) {
	u := cdn.PresignedURL("cdn.example.com", "a/../b?x", 0, "TOK")
	want := "https://cdn.example.com/api/v1/file/a%2F..%2Fb%3Fx?token=TOK"
	if u != want {
		t.Errorf("u = %q, want %q", u, want)
	}
}
