package main

import (
	"strings"
	"testing"
)

func TestReadPipedPasswordReadsFirstLineOnly(t *testing.T) {
	got, err := readPipedPassword(strings.NewReader("s3cret-pw-123\nnot-this-line\n"))
	if err != nil {
		t.Fatalf("readPipedPassword: %v", err)
	}
	if got != "s3cret-pw-123" {
		t.Fatalf("expected %q, got %q", "s3cret-pw-123", got)
	}
}

func TestReadPipedPasswordTrimsTrailingCRLF(t *testing.T) {
	got, err := readPipedPassword(strings.NewReader("s3cret-pw-123\r\n"))
	if err != nil {
		t.Fatalf("readPipedPassword: %v", err)
	}
	if got != "s3cret-pw-123" {
		t.Fatalf("expected trailing CRLF trimmed, got %q", got)
	}
}

func TestReadPipedPasswordWorksWithoutTrailingNewline(t *testing.T) {
	// A caller piping the last line of a file, or `printf` without \n, hits
	// EOF instead of a newline terminator — must not be treated as an error.
	got, err := readPipedPassword(strings.NewReader("s3cret-pw-123"))
	if err != nil {
		t.Fatalf("readPipedPassword: %v", err)
	}
	if got != "s3cret-pw-123" {
		t.Fatalf("expected %q, got %q", "s3cret-pw-123", got)
	}
}

func TestReadPipedPasswordRejectsTooShort(t *testing.T) {
	if _, err := readPipedPassword(strings.NewReader("short\n")); err == nil {
		t.Fatal("expected an error for a password under the minimum length")
	}
}

func TestReadPipedPasswordRejectsEmpty(t *testing.T) {
	if _, err := readPipedPassword(strings.NewReader("\n")); err == nil {
		t.Fatal("expected an error for an empty piped password")
	}
}

func TestValidatePasswordBoundary(t *testing.T) {
	if err := validatePassword(strings.Repeat("a", minPasswordLen)); err != nil {
		t.Fatalf("expected exactly %d characters to be accepted, got %v", minPasswordLen, err)
	}
	if err := validatePassword(strings.Repeat("a", minPasswordLen-1)); err == nil {
		t.Fatalf("expected %d characters (one under the floor) to be rejected", minPasswordLen-1)
	}
}
