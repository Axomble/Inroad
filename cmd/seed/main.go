package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/app/identity"
	"github.com/inroad/inroad/internal/platform/config"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/notify"
	"github.com/inroad/inroad/internal/sandbox"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// options are the command's flags.
type options struct {
	// sandboxMode swaps the small static fixture for the simulation harness:
	// hundreds of personas, a campaign that has already run, engagement
	// history, and a populated inbox. Opt-in, and additionally gated on a
	// non-production environment (see sandbox.Guard).
	sandboxMode bool
	contacts    int
	// deliver additionally puts every simulated message on a real SMTP hop
	// (Mailpit in the dev compose stack), so the mail can be read as mail.
	deliver     bool
	mailpitHost string
	mailpitPort int
	windowDays  int
}

func parseFlags() options {
	var o options
	flag.BoolVar(&o.sandboxMode, "sandbox", false,
		"seed a full simulated workspace (personas, campaign history, engagement, inbox threads) instead of the small static fixture; refuses to run in production")
	flag.IntVar(&o.contacts, "contacts", sandbox.DefaultContacts, "with -sandbox: how many personas to generate")
	flag.IntVar(&o.windowDays, "window-days", int(sandbox.DefaultWindow/(24*time.Hour)), "with -sandbox: how many days of history to simulate")
	flag.BoolVar(&o.deliver, "deliver", false, "with -sandbox: also deliver every simulated message to a local SMTP catcher (Mailpit)")
	flag.StringVar(&o.mailpitHost, "mailpit-host", sandbox.DefaultMailpitHost, "with -deliver: SMTP catcher host")
	flag.IntVar(&o.mailpitPort, "mailpit-port", sandbox.DefaultMailpitSMTPPort, "with -deliver: SMTP catcher port")
	flag.Parse()
	return o
}

// run holds the seed logic so deferred cleanup (pool.Close) executes before the
// process exits; main translates a returned error into a non-zero exit code.
func run() error {
	opts := parseFlags()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	// The sandbox gate is checked BEFORE any connection is opened, so a run
	// that must not happen never touches the database at all.
	if opts.sandboxMode {
		if err := (sandbox.Guard{Env: cfg.Env, Acknowledged: true}).Check(); err != nil {
			return err
		}
	}

	ctx := context.Background()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}
	defer pool.Close()

	sender, err := notify.New(notify.Config{}) // console driver: seed doesn't need real delivery
	if err != nil {
		return fmt.Errorf("notify: %w", err)
	}
	svc := identity.NewService(identity.NewStore(pool), time.Hour, sender, cfg.AppBaseURL,
		cfg.EmailVerifyTTL, cfg.PasswordResetTTL, cfg.InviteTTL)
	sess, err := svc.Register(ctx, identity.RegisterInput{
		WorkspaceName: workspaceName(opts),
		Email:         seedEmail,
		Password:      seedPassword,
		UserAgent:     "seed",
		IP:            "",
	})
	if err != nil {
		return fmt.Errorf("seed: %w", err)
	}

	// Fixtures come second and deliberately do NOT roll the registration back on
	// failure: a workspace you can log into is worth more than an all-or-nothing
	// seed, and a re-run fails at Register anyway since the user already exists.
	summary, err := seed(ctx, pool, sess.WorkspaceID, sess.UserID, opts)
	if err != nil {
		return fmt.Errorf("fixtures: %w", err)
	}

	fmt.Printf("seeded workspace=%s user=%s\n", sess.WorkspaceID, sess.UserID)
	fmt.Printf("  %s\n", summary)
	fmt.Printf("  login %s / %s\n", seedEmail, seedPassword)
	if opts.sandboxMode && opts.deliver {
		fmt.Printf("  mail  delivered to %s:%d (Mailpit UI: http://localhost:8025)\n", opts.mailpitHost, opts.mailpitPort)
	}
	return nil
}

// The credentials the seeded workspace is reachable with. Fixed and printed,
// because the whole point is to be able to log straight in.
const (
	seedEmail    = "demo@inroad.test"
	seedPassword = "demodemo"
)

func workspaceName(o options) string {
	if o.sandboxMode {
		return "Sandbox Workspace"
	}
	return "Demo Workspace"
}

// seed dispatches to the static fixture or the simulation harness. The user id
// is only the static fixture's concern (its seeded snoozes record who snoozed);
// the harness generates its own actors.
func seed(ctx context.Context, pool *pgxpool.Pool, ws, user uuid.UUID, o options) (string, error) {
	if !o.sandboxMode {
		return seedFixtures(ctx, pool, ws, user)
	}

	q := gen.New(pool)
	store := sandbox.NewPgStore(pool)
	simOpts := sandbox.Options{Window: time.Duration(o.windowDays) * 24 * time.Hour}
	if o.deliver {
		// One connection for the whole run, closed here rather than deferred
		// into the seeder: the deliverer is the caller's resource.
		d := sandbox.NewSMTPDeliverer(o.mailpitHost, o.mailpitPort)
		defer func() {
			if cerr := d.Close(); cerr != nil {
				fmt.Fprintln(os.Stderr, cerr)
			}
		}()
		simOpts.Deliverer = d
	}
	res, err := sandbox.NewSeeder(q, store, store).Seed(ctx, sandbox.SeedInput{
		WorkspaceID: ws, Contacts: o.contacts, Options: simOpts,
	})
	if err != nil {
		return "", err
	}
	return res.String(), nil
}
