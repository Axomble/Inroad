package auth

import (
	"testing"
	"time"
)

func TestIssueAndParseToken(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	tok, err := IssueToken(secret, Claims{UserID: "user-1", WorkspaceID: "ws-1"}, time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	claims, err := ParseToken(secret, tok)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if claims.UserID != "user-1" || claims.WorkspaceID != "ws-1" {
		t.Fatalf("claims = %+v", claims)
	}
}

func TestParseTokenRejectsWrongSecret(t *testing.T) {
	tok, _ := IssueToken([]byte("0123456789abcdef0123456789abcdef"), Claims{UserID: "u", WorkspaceID: "w"}, time.Hour)
	if _, err := ParseToken([]byte("different-secret-different-secret"), tok); err == nil {
		t.Fatal("expected error for wrong secret")
	}
}

func TestIssueParseRoundTripWithRoleAndSession(t *testing.T) {
	secret := []byte("0123456789abcdef")
	in := Claims{UserID: "u1", WorkspaceID: "w1", Role: "admin", SessionID: "s1"}
	tok, err := IssueToken(secret, in, time.Minute)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	out, err := ParseToken(secret, tok)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if out != in {
		t.Fatalf("round-trip mismatch: %+v != %+v", out, in)
	}
}

func TestParseRejectsExpired(t *testing.T) {
	secret := []byte("0123456789abcdef")
	tok, _ := IssueToken(secret, Claims{UserID: "u1"}, -time.Minute)
	if _, err := ParseToken(secret, tok); err == nil {
		t.Fatal("expected expired token to be rejected")
	}
}
