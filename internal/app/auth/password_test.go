package auth

import "testing"

func TestHashAndCheckPassword(t *testing.T) {
	hash, err := HashPassword("s3cret-pw")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "s3cret-pw" {
		t.Fatal("hash equals plaintext")
	}
	if !CheckPassword(hash, "s3cret-pw") {
		t.Error("CheckPassword returned false for correct password")
	}
	if CheckPassword(hash, "wrong") {
		t.Error("CheckPassword returned true for wrong password")
	}
}
