package main

import (
	"errors"
	"flag"
	"fmt"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/identity"
)

// createUser implements `inroadctl create-user`. Without --workspace it
// bootstraps a brand-new workspace owned by the new user (the same shape
// self-serve signup produces); with --workspace it adds the user to an
// EXISTING workspace at --role, for an instance that already has one.
func (e *env) createUser(args []string) error {
	fs := flag.NewFlagSet("create-user", flag.ContinueOnError)
	email := fs.String("email", "", "email address for the new user (required)")
	workspace := fs.String("workspace", "", "existing workspace UUID to add the user to; omit to bootstrap a NEW workspace owned by this user")
	role := fs.String("role", "owner", "role within --workspace: owner, admin, or member (ignored — and must be owner — when --workspace is omitted)")
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *email == "" {
		return errors.New("create-user: --email is required")
	}
	if *workspace == "" && *role != "owner" {
		return errors.New("create-user: --role is only meaningful with --workspace (a new workspace's first user is always its owner)")
	}

	pw, err := e.readPW(fmt.Sprintf("New password for %s", *email))
	if err != nil {
		return fmt.Errorf("read password: %w", err)
	}

	if *workspace == "" {
		fmt.Fprintf(e.out, "About to create user %q as the owner of a brand-new workspace.\n", *email)
		if !confirm(e.in, e.out, "Continue?", *yes) {
			fmt.Fprintln(e.out, "aborted")
			return nil
		}
		sess, err := e.svc.Register(e.ctx, identity.RegisterInput{
			WorkspaceName: defaultWorkspaceName(*email),
			Email:         *email, Password: pw, UserAgent: "inroadctl", IP: "",
		})
		if err != nil {
			return fmt.Errorf("create-user: %w", describeCreateUserError(*email, err))
		}
		fmt.Fprintf(e.out, "created user %s in NEW workspace %s (owner)\n", sess.UserID, sess.WorkspaceID)
		return nil
	}

	wsID, err := uuid.Parse(*workspace)
	if err != nil {
		return fmt.Errorf("create-user: --workspace: invalid UUID %q", *workspace)
	}
	fmt.Fprintf(e.out, "About to create user %q in workspace %s at role %q.\n", *email, wsID, *role)
	if !confirm(e.in, e.out, "Continue?", *yes) {
		fmt.Fprintln(e.out, "aborted")
		return nil
	}
	uid, err := e.svc.CreateMember(e.ctx, wsID, *email, pw, *role)
	if err != nil {
		switch {
		case errors.Is(err, identity.ErrWorkspaceNotFound):
			return fmt.Errorf("create-user: no workspace %s (run `inroadctl workspaces` to see what exists)", wsID)
		case errors.Is(err, identity.ErrInvalidRole):
			return fmt.Errorf("create-user: %w", err)
		default:
			return fmt.Errorf("create-user: %w", describeCreateUserError(*email, err))
		}
	}
	fmt.Fprintf(e.out, "created user %s in workspace %s (role %s)\n", uid, wsID, *role)
	return nil
}

// defaultWorkspaceName names a bootstrapped workspace after its owner's email
// local-part, matching what a self-serve signup would show a new user before
// they rename it during onboarding — never left blank, since a workspace
// needs a name to display anywhere in the UI.
func defaultWorkspaceName(email string) string {
	for i, r := range email {
		if r == '@' {
			return email[:i]
		}
	}
	return email
}
