package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// listWorkspaces implements `inroadctl workspaces`: every workspace on the
// instance, so an operator can see what exists before targeting one with
// grant-role or create-user --workspace.
func (e *env) listWorkspaces(args []string) error {
	return listEntities(e, args, "workspaces", "no workspaces",
		func() ([]gen.Workspace, error) { return e.svc.ListWorkspaces(e.ctx) },
		[]string{"ID", "NAME", "ONBOARDED", "CREATED"},
		func(ws gen.Workspace) []string {
			return []string{ws.ID.String(), ws.Name, yesIfValid(ws.OnboardingCompletedAt), formatTimestamptz(ws.CreatedAt)}
		},
	)
}

// listUsers implements `inroadctl users`: every user on the instance.
// gen.User carries PasswordHash — the row-builder below never reads that
// field, so it can never reach the printer: a hash is still a secret and has
// no business on an operator's terminal or in shell history.
func (e *env) listUsers(args []string) error {
	return listEntities(e, args, "users", "no users",
		func() ([]gen.User, error) { return e.svc.ListUsers(e.ctx) },
		[]string{"ID", "EMAIL", "VERIFIED", "CREATED"},
		func(u gen.User) []string {
			return []string{u.ID.String(), u.Email, yesIfValid(u.EmailVerifiedAt), formatTimestamptz(u.CreatedAt)}
		},
	)
}

// listEntities is the shared shape every `inroadctl` listing command follows:
// parse flags (none of these take any, but FlagSet still rejects a stray
// argument), fetch, print "no <things>" on an empty result, otherwise render
// a table. Generic over the row type so listWorkspaces/listUsers share this
// one implementation instead of two copies that differ only in field names.
func listEntities[T any](e *env, args []string, cmdName, emptyMsg string, fetch func() ([]T, error), header []string, toRow func(T) []string) error {
	fs := flag.NewFlagSet(cmdName, flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	rows, err := fetch()
	if err != nil {
		return fmt.Errorf("%s: %w", cmdName, err)
	}
	if len(rows) == 0 {
		fmt.Fprintln(e.out, emptyMsg)
		return nil
	}
	body := make([][]string, len(rows))
	for i, r := range rows {
		body[i] = toRow(r)
	}
	return printTable(e.out, header, body)
}

// printTable renders header and rows as a tab-aligned table.
func printTable(out io.Writer, header []string, rows [][]string) error {
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(header, "\t"))
	for _, row := range rows {
		fmt.Fprintln(tw, strings.Join(row, "\t"))
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
