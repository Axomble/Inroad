package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// minPasswordLen mirrors identity/handler.go's `validate:"required,min=8"` tag
// on the HTTP register/reset-password bodies. inroadctl calls identity.Service
// directly and never goes through that handler, so this is the one remaining
// place enforcing the same floor for the CLI's credential-changing commands.
const minPasswordLen = 8

// validatePassword rejects a password shorter than the floor every other
// account-creation path in the app enforces (see minPasswordLen).
func validatePassword(pw string) error {
	if len(pw) < minPasswordLen {
		return fmt.Errorf("password must be at least %d characters", minPasswordLen)
	}
	return nil
}

// readPipedPassword reads a single line as a password — the shape
// `printf '%s\n' "$PW" | inroadctl set-password --email ...` needs for
// scripted/non-interactive use. Split out from readPassword (rather than
// folded into it) so it is unit-testable against a plain io.Reader; the
// interactive terminal path below needs a real file descriptor for echo
// suppression and is not.
func readPipedPassword(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	pw := strings.TrimRight(line, "\r\n")
	if err := validatePassword(pw); err != nil {
		return "", err
	}
	return pw, nil
}

// readPassword gets a new password for a credential-changing command. It
// NEVER accepts one via a command-line flag: argv is visible to every other
// process on the host (ps, /proc) and lands in shell history, and a password
// leaked that way survives long after the command that typed it exits.
//
// stdin piped (not a terminal): the first line is read verbatim via
// readPipedPassword, no prompt — the automation-friendly path.
// stdin a terminal: the operator is prompted twice with input hidden, and a
// mismatch is rejected before any database write happens.
func readPassword(label string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return readPipedPassword(os.Stdin)
	}
	return promptPassword(label)
}

// promptPassword prompts twice on stderr (so the password itself, echoed by
// neither prompt, never mixes with stdout a caller might be capturing) with
// terminal echo suppressed, requiring both entries to match.
func promptPassword(label string) (string, error) {
	fmt.Fprintf(os.Stderr, "%s: ", label)
	pw1, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	if err := validatePassword(string(pw1)); err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "Confirm password: ")
	pw2, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password confirmation: %w", err)
	}
	if string(pw1) != string(pw2) {
		return "", errors.New("passwords did not match")
	}
	return string(pw1), nil
}
