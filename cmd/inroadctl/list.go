package main

import (
	"flag"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// listWorkspaces implements `inroadctl workspaces`: every workspace on the
// instance, so an operator can see what exists before targeting one with
// grant-role or create-user --workspace.
func (e *env) listWorkspaces(args []string) error {
	fs := flag.NewFlagSet("workspaces", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	rows, err := e.svc.ListWorkspaces(e.ctx)
	if err != nil {
		return fmt.Errorf("workspaces: %w", err)
	}
	if len(rows) == 0 {
		fmt.Fprintln(e.out, "no workspaces")
		return nil
	}
	tw := tabwriter.NewWriter(e.out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tONBOARDED\tCREATED")
	for _, ws := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", ws.ID, ws.Name, yesIfValid(ws.OnboardingCompletedAt), formatTimestamptz(ws.CreatedAt))
	}
	return tw.Flush()
}

// listUsers implements `inroadctl users`: every user on the instance.
// gen.User carries PasswordHash — this printer deliberately never renders it;
// a hash is still a secret and has no business on an operator's terminal or
// in shell history.
func (e *env) listUsers(args []string) error {
	fs := flag.NewFlagSet("users", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	rows, err := e.svc.ListUsers(e.ctx)
	if err != nil {
		return fmt.Errorf("users: %w", err)
	}
	if len(rows) == 0 {
		fmt.Fprintln(e.out, "no users")
		return nil
	}
	tw := tabwriter.NewWriter(e.out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tEMAIL\tVERIFIED\tCREATED")
	for _, u := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", u.ID, u.Email, yesIfValid(u.EmailVerifiedAt), formatTimestamptz(u.CreatedAt))
	}
	return tw.Flush()
}

// yesIfValid renders a nullable timestamptz as "yes"/"no" for a listing
// column that only cares whether the stamp is set (onboarded, verified).
func yesIfValid(ts pgtype.Timestamptz) string {
	if ts.Valid {
		return "yes"
	}
	return "no"
}

// formatTimestamptz renders a nullable timestamptz as RFC3339, or "-" when
// NULL (a column this package prints unconditionally, e.g. created_at, is
// never actually null in practice, but the zero value must still print
// something rather than panic on a malformed row).
func formatTimestamptz(ts pgtype.Timestamptz) string {
	if !ts.Valid {
		return "-"
	}
	return ts.Time.Format(time.RFC3339)
}
