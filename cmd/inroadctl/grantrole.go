package main

import (
	"errors"
	"flag"
	"fmt"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/identity"
)

// grantRole implements `inroadctl grant-role`: the primitive for restoring a
// lost owner, whichever way the access was lost — it adds the membership if
// the user is not already one (dropped from the workspace entirely), or
// updates their role in place if they are (merely demoted).
func (e *env) grantRole(args []string) error {
	fs := flag.NewFlagSet("grant-role", flag.ContinueOnError)
	email := fs.String("email", "", "email of the user to grant a role to (required)")
	workspace := fs.String("workspace", "", "workspace UUID (required)")
	role := fs.String("role", "", "role to grant: owner, admin, or member (required)")
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *email == "" || *workspace == "" || *role == "" {
		return errors.New("grant-role: --email, --workspace, and --role are all required")
	}
	wsID, err := uuid.Parse(*workspace)
	if err != nil {
		return fmt.Errorf("grant-role: --workspace: invalid UUID %q", *workspace)
	}

	fmt.Fprintf(e.out, "About to grant %q the role %q in workspace %s.\n", *email, *role, wsID)
	if !confirm(e.in, e.out, "Continue?", *yes) {
		fmt.Fprintln(e.out, "aborted")
		return nil
	}

	m, err := e.svc.GrantRole(e.ctx, *email, wsID, *role)
	if err != nil {
		switch {
		case errors.Is(err, identity.ErrUserNotFound):
			return fmt.Errorf("grant-role: no user with email %q (run `inroadctl users` to see what exists)", *email)
		case errors.Is(err, identity.ErrWorkspaceNotFound):
			return fmt.Errorf("grant-role: no workspace %s (run `inroadctl workspaces` to see what exists)", wsID)
		default:
			return fmt.Errorf("grant-role: %w", err)
		}
	}
	fmt.Fprintf(e.out, "granted %s role %s in workspace %s (%s)\n", *email, m.Role, m.WorkspaceID, m.WorkspaceName)
	return nil
}
