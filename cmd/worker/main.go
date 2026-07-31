package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	// time/tzdata embeds the IANA zone database in the binary. Campaign send
	// windows are evaluated in the campaign's own timezone, and the runtime images
	// are bare alpine with no tzdata package — without this, LoadLocation fails in
	// production for every non-UTC zone. Embedding it also keeps behavior identical
	// across Alpine, a developer's machine, and CI.
	_ "time/tzdata"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/coreapi/inprocess"
	"github.com/inroad/inroad/internal/platform/config"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/keys"
	"github.com/inroad/inroad/internal/platform/log"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/platform/warmup"
	"github.com/inroad/inroad/internal/worker"
)

// workerHeartbeatInterval is how often a worker refreshes its `workers` row. It
// matches the assigner's live-worker window (coreapi workerLiveWindow, 15m) with
// comfortable headroom so a couple of missed ticks don't drop it from routing.
const workerHeartbeatInterval = 5 * time.Minute

func main() {
	if err := run(); err != nil {
		os.Exit(1)
	}
}

// run wires and serves the worker. Keeping the body here (rather than in main)
// guarantees the deferred cleanups (pool.Close, enq.Close, sch.Shutdown) run
// before the process exits; main only maps a returned error to a non-zero
// status code. The error paths log/print their own diagnostics before returning.
func run() error {
	cfg, err := config.Load()
	if err != nil {
		// Report before building the logger: config failure means we may not have
		// the info the logger needs (env/level), and matching cmd/migrate keeps
		// bad-config output uniform across binaries.
		fmt.Fprintln(os.Stderr, "config:", err)
		return err
	}
	logger := log.New(cfg.Env)

	pool, err := db.Connect(context.Background(), cfg.DatabaseURL)
	if err != nil {
		logger.Error("db connect failed", "err", err)
		return err
	}
	defer pool.Close()

	// Build the per-workspace Keyring at the worker's composition root. The
	// DEKStore is the sqlc-backed adapter over the pool; the worker engine
	// packages never see it — they reach data only through coreapi, which holds
	// the Keyring. keys.BuildKeyring owns the fail-closed provider guard.
	keyring, err := keys.BuildKeyring(cfg, gen.New(pool))
	if err != nil {
		logger.Error("keyring init failed", "err", err)
		return err
	}

	// The worker package depends only on coreapi.Client; the DB-backed
	// implementation is wired here at the composition root.
	googleOAuth := mail.GoogleOAuth{
		ClientID:     cfg.GoogleClientID,
		ClientSecret: cfg.GoogleClientSecret,
		RedirectURL:  cfg.GoogleRedirectURL,
	}
	msOAuth := mail.MicrosoftOAuth{
		ClientID:     cfg.MSClientID,
		ClientSecret: cfg.MSClientSecret,
		RedirectURL:  cfg.MSRedirectURL,
		Tenant:       cfg.MSTenant,
	}
	core := inprocess.New(pool, keyring, cfg.JWTSecret, cfg.PublicURL, googleOAuth, msOAuth, cfg.WarmupSecret, warmup.NewStaticLibrary())

	// Resolve the optional worker egress IP once. When set, outbound SMTP/IMAP
	// dials bind their SOURCE address to it (spec §15) so a mailbox's mail
	// egresses from one IP; it never relaxes the SSRF destination vet (§17.7).
	egressAddr, err := mail.ParseEgressIP(cfg.WorkerEgressIP)
	if err != nil {
		logger.Error("invalid worker egress ip", "err", err)
		return err
	}
	// MultiSender dispatches SMTP vs Gmail vs Graph on the job's Provider; the
	// SMTP leg keeps the SSRF-vetted NetSender, the Gmail leg uses the fixed
	// Google host, and the m365 leg uses the fixed Microsoft Graph host.
	smtpSender := mail.NewNetSender(cfg.MailAllowPrivateHosts)
	smtpSender.LocalAddr = egressAddr
	sndr := mail.NewMultiSender(smtpSender, mail.NewGmailSender(), mail.NewGraphSender())
	reader := mail.NewNetInboxReader(cfg.MailAllowPrivateHosts)
	reader.LocalAddr = egressAddr
	// Engager runs recipient-side warmup engagement (mark-read/rescue). The IMAP leg
	// dials through the SAME SSRF-vetted, source-IP-bound path as the reader; the
	// Gmail leg uses the fixed Google host; m365 is a documented clean skip.
	imapEngager := mail.NewNetEngager(cfg.MailAllowPrivateHosts)
	imapEngager.LocalAddr = egressAddr
	engager := mail.NewMultiEngager(imapEngager, mail.NewGmailEngager())
	enq := queue.NewClient(cfg.RedisAddr)
	defer enq.Close()

	// Heartbeat this worker into the global registry so the control-plane
	// assigner can route mailboxes to it. The ticker is bound to hbCtx, cancelled
	// when run() returns (the server stopped), so the goroutine exits cleanly.
	hbCtx, cancelHeartbeat := context.WithCancel(context.Background())
	defer cancelHeartbeat()
	startHeartbeat(hbCtx, core, cfg.WorkerID, cfg.WorkerEgressIP, logger)

	// Start the periodic scheduler alongside the worker. It enqueues
	// send:sweep_stuck every 2 minutes so orphaned sends (launch committed
	// DB rows but Redis enqueue failed) get retried without operator action.
	sch := queue.NewScheduler(cfg.RedisAddr, logger)
	if err := queue.RegisterSweepStuck(sch); err != nil {
		logger.Error("scheduler register failed", "err", err)
		return err
	}
	if err := queue.RegisterSweepEnrollments(sch); err != nil {
		logger.Error("scheduler register (enrollments) failed", "err", err)
		return err
	}
	if err := queue.RegisterInboxSweep(sch); err != nil {
		logger.Error("scheduler register (inbox sweep) failed", "err", err)
		return err
	}
	if err := queue.RegisterWarmupSweep(sch); err != nil {
		logger.Error("scheduler register (warmup sweep) failed", "err", err)
		return err
	}
	if err := queue.RegisterMaintenanceCleanup(sch); err != nil {
		logger.Error("scheduler register (maintenance cleanup) failed", "err", err)
		return err
	}
	go func() {
		if err := sch.Run(); err != nil {
			logger.Error("scheduler exited", "err", err)
		}
	}()
	defer sch.Shutdown()

	srv := queue.NewServer(cfg.RedisAddr, logger, cfg.WorkerConcurrency, cfg.WorkerQueues)
	mux := queue.NewMux()
	worker.Register(mux, core, sndr, engager, reader, enq, cfg.PublicURL, cfg.TrackingSecret, cfg.WarmupSecret)

	logger.Info("worker starting", "redis", cfg.RedisAddr, "concurrency", cfg.WorkerConcurrency)
	if err := srv.Run(mux); err != nil {
		logger.Error("worker error", "err", err)
		return err
	}
	return nil
}

// startHeartbeat registers this worker immediately, then refreshes its `workers`
// row every workerHeartbeatInterval until ctx is cancelled. The initial beat is
// synchronous so the assigner sees the worker as live before it processes its
// first task. A worker with an empty id (hostname lookup failed AND no
// INROAD_WORKER_ID) can't own a stable queue, so it skips registration and runs
// off the shared default queue only. Heartbeat failures are logged, not fatal:
// a transient DB blip must not take the worker down.
func startHeartbeat(ctx context.Context, core coreapi.Client, workerID, egressIP string, logger *slog.Logger) {
	if workerID == "" {
		logger.Warn("worker id empty; skipping registration (serves default queue only)")
		return
	}
	beat := func() {
		if err := core.UpsertWorkerHeartbeat(ctx, workerID, egressIP); err != nil {
			logger.Error("worker heartbeat failed", "worker_id", workerID, "err", err)
		}
	}
	beat() // register now so the assigner routes to us on the first send
	go func() {
		t := time.NewTicker(workerHeartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				beat()
			}
		}
	}()
}
