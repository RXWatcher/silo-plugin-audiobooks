package abs_test

import (
	"testing"
	"time"

	"github.com/RXWatcher/silo-plugin-audiobooks/internal/abs"
)

func TestIssueAndParseAccess(t *testing.T) {
	secret := []byte("test-secret-32-bytes-long-aaaaaa")
	tok, err := abs.IssueAccessToken(secret, "u-1", "", "j-1", time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	c, err := abs.ParseToken(secret, tok)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.UserID != "u-1" || c.JTI != "j-1" || c.Type != "access" {
		t.Errorf("claims = %+v", c)
	}
}

func TestParse_RejectsBadSignature(t *testing.T) {
	good := []byte("a-secret-that-is-32-bytes-long!!")
	bad := []byte("b-secret-that-is-32-bytes-long!!")
	tok, _ := abs.IssueAccessToken(good, "u", "", "j", time.Hour)
	if _, err := abs.ParseToken(bad, tok); err == nil {
		t.Error("expected signature error")
	}
}

func TestParse_RejectsExpired(t *testing.T) {
	secret := []byte("a-secret-that-is-32-bytes-long!!")
	tok, _ := abs.IssueAccessToken(secret, "u", "", "j", -time.Hour)
	if _, err := abs.ParseToken(secret, tok); err == nil {
		t.Error("expected expiry error")
	}
}

func TestAccessTokenCarriesProfileID(t *testing.T) {
	secret := []byte("test-secret-test-secret-test-123")
	tok, err := abs.IssueAccessToken(secret, "u1", "p1", "jti1", time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	claims, err := abs.ParseToken(secret, tok)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if claims.ProfileID != "p1" {
		t.Errorf("ProfileID = %q, want p1", claims.ProfileID)
	}
}

func TestIssueSessionToken_CarriesBookAndFile(t *testing.T) {
	secret := []byte("a-secret-that-is-32-bytes-long!!")
	tok, err := abs.IssueSessionToken(secret, "u", "s", "bw-1", 3, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	c, err := abs.ParseToken(secret, tok)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.Type != "session" || c.SessionID != "s" || c.BookID != "bw-1" || c.FileIdx != 3 {
		t.Errorf("claims = %+v", c)
	}
}
