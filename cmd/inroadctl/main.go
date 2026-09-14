// Command inroadctl is the operator CLI. It talks to Postgres directly (never
// through the HTTP API), so it is the one tool that still works when sign-in
// itself is broken — a self-hoster who locked themselves out, lost their only
// owner account, or misconfigured auth recovers through here.
//
// It reuses the existing password-hashing and user-creation paths
// (internal/app/identity, internal/app/auth) rather than reimplementing them:
// a second hashing implementation that drifts from the one the API uses is a
// security bug, not a convenience.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/identity"
	"github.com/inroad/inroad/internal/platform/config"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/notify"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "inroadctl:", err)
		os.Exit(1)
	}
}

// identityService is the subset of *identity.Service the CLI depends on,
// defined here at the seam it is consumed from (dependency inversion — the
// same rule the app domains follow) so unit tests can inject a fake instead
// of a live Postgres-backed Service.
type identityService interface {
	Register(ctx context.Context, in identity.RegisterInput) (identity.Session, error)
	CreateMember(ctx context.Context, workspace uuid.UUID, email, password, role string) (uuid.UUID, error)
	SetPassword(ctx context.Context, email, newPassword string) ([]uuid.UUID, error)
	GrantRole(ctx context.Context, email string, workspace uuid.UUID, role string) (identity.Membership, error)
	ListWorkspaces(ctx context.Context) ([]gen.Workspace, error)
	ListUsers(ctx context.Context) ([]gen.User, error)
}

// env bundles what every subcommand except status needs: the identity
// service, where to read confirmation answers from and write output to, and
// how to obtain a new password. readPW is a seam (not always os.Stdin) so
// tests can drive create-user/set-password without a real terminal.
type env struct {
	ctx    context.Context
	svc    identityService
	in     io.Reader
	out    io.Writer
	readPW func(label string) (string, error)
}

func run(args []string) error {
	if len(args) == 0 {
		printUsage(os.Stderr)
		return errors.New("missing command")
	}
	cmd, rest := args[0], args[1:]
	if cmd == "help" || cmd == "-h" || cmd == "--help" {
		printUsage(os.Stdout)
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	// status must still run - and say WHAT is broken - when the very things it
	// is diagnosing (Postgres, Redis) are down, so it makes its own non-fatal
	// connection attempts instead of sharing the fail-fast connect below.
	if cmd == "status" {
		return runStatus(context.Background(), cfg, os.Stdout)
	}

	ctx := context.Background()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()

	// The REAL configured transactional driver, not a forced "console": an
	// operator-created account should get a working verification email in a
	// deployment that has SMTP configured, exactly like self-serve signup
	// does. Config.TransactionalDriver defaults to "console" (log only) when
	// unset, so a dev instance behaves the same as cmd/seed's fixed console
	// sender without this binary needing its own fallback.
	sender, err := notify.New(notify.Config{
		Driver: cfg.TransactionalDriver, SMTPHost: cfg.SystemSMTPHost, SMTPPort: cfg.SystemSMTPPort,
		SMTPUsername: cfg.SystemSMTPUsername, SMTPPassword: cfg.SystemSMTPPassword, From: cfg.SystemEmailFrom,
		AllowPlaintext: cfg.SystemSMTPAllowPlaintext,
	})
	if err != nil {
		return fmt.Errorf("transactional sender: %w", err)
	}

	store := identity.NewStore(pool)
	svc := identity.NewService(store, cfg.RefreshTokenTTL, sender, cfg.AppBaseURL,
		cfg.EmailVerifyTTL, cfg.PasswordResetTTL, cfg.InviteTTL)

	e := &env{ctx: ctx, svc: svc, in: os.Stdin, out: os.Stdout, readPW: readPassword}

	switch cmd {
	case "create-user":
		return e.createUser(rest)
	case "set-password":
		return e.setPassword(rest)
	case "grant-role":
		return e.grantRole(rest)
	case "workspaces":
		return e.listWorkspaces(rest)
	case "users":
		return e.listUsers(rest)
	default:
		printUsage(os.Stderr)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `inroadctl — Inroad operator CLI. Talks to Postgres directly, so it works
even when sign-in itself is broken.

Usage:
  inroadctl <command> [flags]

Commands:
  create-user   --email E [--workspace ID] [--role owner|admin|member] [--yes]
                Bootstrap a new user. Without --workspace, creates a brand new
                workspace owned by this user (like self-serve signup). With
                --workspace, adds the user to that EXISTING workspace at
                --role (default owner). The password is read from stdin (if
                piped) or an interactive prompt — never a flag.

  set-password  --email E [--yes]
                Reset a forgotten password and sign the account out of every
                existing session. Password read the same way as create-user.

  grant-role    --email E --workspace ID --role owner|admin|member [--yes]
                Grant (or restore) a role for a user in a workspace — adds the
                membership if they are not already a member. The primitive
                for recovering a lost owner.

  workspaces    List every workspace on this instance.
  users         List every user on this instance.
  status        Instance health: database, migrations, redis, workers.

Flags common to the credential/role-changing commands:
  --yes         Skip the confirmation prompt.

Environment: the same INROAD_* variables cmd/inroad reads (INROAD_DATABASE_URL,
INROAD_JWT_SECRET, INROAD_MASTER_KEY, INROAD_REDIS_ADDR, ...).
`)
}
