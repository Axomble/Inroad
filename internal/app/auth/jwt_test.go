package auth

import (
	"testing"
	"time"
)

func TestIssueAndParseToken(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	tok, err := IssueToken(secret, "user-1", "ws-1", time.Hour)
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
	tok, _ := IssueToken([]byte("0123456789abcdef0123456789abcdef"), "u", "w", time.Hour)
	if _, err := ParseToken([]byte("different-secret-different-secret"), tok); err == nil {
		t.Fatal("expected error for wrong secret")
	}
}
