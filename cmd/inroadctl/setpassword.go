package main

import (
	"errors"
	"flag"
	"fmt"

	"github.com/inroad/inroad/internal/app/identity"
)

// setPassword implements `inroadctl set-password`: overwrite a forgotten
// password and revoke every existing session for the account, so a recovery
// can't leave a possibly-compromised session alive.
func (e *env) setPassword(args []string) error {
	fs := flag.NewFlagSet("set-password", flag.ContinueOnError)
	email := fs.String("email", "", "email of the account to reset (required)")
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *email == "" {
		return errors.New("set-password: --email is required")
	}

	pw, err := e.readPW(fmt.Sprintf("New password for %s", *email))
	if err != nil {
		return fmt.Errorf("read password: %w", err)
	}

	fmt.Fprintf(e.out, "About to overwrite the password for %q and sign it out of every existing session.\n", *email)
	if !confirm(e.in, e.out, "Continue?", *yes) {
		fmt.Fprintln(e.out, "aborted")
		return nil
	}

	revoked, err := e.svc.SetPassword(e.ctx, *email, pw)
	if err != nil {
		if errors.Is(err, identity.ErrUserNotFound) {
			return fmt.Errorf("set-password: no user with email %q (run `inroadctl users` to see what exists)", *email)
		}
		return fmt.Errorf("set-password: %w", err)
	}
	fmt.Fprintf(e.out, "password updated for %s (%d session(s) revoked)\n", *email, len(revoked))
	return nil
}
