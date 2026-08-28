package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	// time/tzdata embeds the IANA zone database in the binary. Campaign send
	// windows are evaluated in the campaign's own timezone, and the runtime images
	// are bare alpine with no tzdata package — without this, LoadLocation fails in
	// production for every non-UTC zone. Embedding it also keeps behavior identical
	// across Alpine, a developer's machine, and CI.
	_ "time/tzdata"

	"github.com/redis/go-redis/v9"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/coreapi/inprocess"
	"github.com/inroad/inroad/internal/platform/config"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/dnsauth"
	"github.com/inroad/inroad/internal/platform/esp"
	"github.com/inroad/inroad/internal/platform/httpx"
	"github.com/inroad/inroad/internal/platform/keys"
	"github.com/inroad/inroad/internal/platform/log"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/metrics"
	"github.com/inroad/inroad/internal/platform/queue"
	platformrealtime "github.com/inroad/inroad/internal/platform/realtime"
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

	// Prometheus /metrics listener. mtx is always constructed (never nil): the
	// campaign/warmup send handlers' finalize points record into it
	// unconditionally below; INROAD_METRICS_ADDR only controls whether the
	// dedicated listener actually opens a port.
	//
	// Shutdown must be a synchronous cancel-THEN-wait, not a bare cancel: a
	// deferred cancelMetrics() alone unblocks httpx.Run's goroutine but
	// nothing then waits for that goroutine to finish its own graceful
	// srv.Shutdown — run() (and the process) can return before it does,
	// which defeats the whole point of a graceful shutdown. metricsWG makes
	// the deferred cleanup below block until the listener has actually
	// stopped, same as the heartbeat's hbCtx/cancelHeartbeat further down
	// (which has no listener to wait for, so it only needs the cancel).
	mtx := metrics.New()
	metricsCtx, cancelMetrics := context.WithCancel(context.Background())
	var metricsWG sync.WaitGroup
	if cfg.MetricsAddr != "" {
		metricsSrv := httpx.NewServer(cfg.MetricsAddr, mtx.Handler())
		metricsWG.Add(1)
		go func() {
			defer metricsWG.Done()
			if err := httpx.Run(metricsCtx, metricsSrv); err != nil {
				logger.Error("metrics server error", "err", err)
			}
		}()
		logger.Info("metrics listening", "addr", cfg.MetricsAddr)
	}
	defer func() {
		cancelMetrics()
		metricsWG.Wait()
	}()

	pool, err := db.ConnectSized(context.Background(), cfg.DatabaseURL, db.PoolSize{Max: cfg.DBMaxConns, Min: cfg.DBMinConns})
	if err != nil {
		logger.Error("db connect failed", "err", err)
		return err
	}
	defer pool.Close()

	// pgx pool saturation, read on scrape. This is how an operator watches the
	// connection budget (INROAD_DB_MAX_CONNS) being approached BEFORE
	// pool.Acquire starts blocking — the worker is the replica that exhausts it
	// first, since concurrency is per-process.
	if err := mtx.RegisterPool(pool); err != nil {
		logger.Error("register pool metrics failed", "err", err)
		return err
	}

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
	// Realtime fan-out. The worker is a SEPARATE PROCESS from the API, so an
	// in-process channel reaches no browser: every worker-originated event goes
	// through Redis, and this hub is that path. It publishes only — the worker
	// holds no sockets, so nothing here subscribes.
	realtimeRedis := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	defer func() { _ = realtimeRedis.Close() }()
	realtimeHub := platformrealtime.New(realtimeRedis)
	defer func() { _ = realtimeHub.Close() }()

	core := inprocess.New(pool, keyring, cfg.JWTSecret, cfg.PublicURL, googleOAuth, msOAuth, cfg.WarmupSecret, warmup.NewStaticLibrary(),
		// The claim-before-send outcome counter (won/reclaimed/lost/…) is
		// emitted from inside the claim, which is the only place every outcome
		// is already distinguished.
		inprocess.WithMetrics(mtx),
		// Enables PublishRealtime. Omitting it would leave every publish a no-op
		// and browsers on their polling fallback — correct, but the point of the
		// slice is that an inbound reply reaches an open tab without one.
		inprocess.WithRealtime(realtimeHub))

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

	// Queue backlog per queue, read on scrape. Wired on the worker (not the
	// API) because the worker is what consumes the queues, so the depth and the
	// consumer's own saturation are scraped from one target. The inspector is
	// read-only — nothing here mutates a task.
	inspector := queue.NewInspector(cfg.RedisAddr)
	defer func() {
		if err := inspector.Close(); err != nil {
			logger.Error("queue inspector close failed", "err", err)
		}
	}()
	if err := mtx.RegisterQueue(inspector, logger); err != nil {
		logger.Error("register queue metrics failed", "err", err)
		return err
	}

	// Heartbeat this worker into the global registry so the control-plane
	// assigner can route mailboxes to it. The ticker is bound to hbCtx, cancelled
	// when run() returns (the server stopped), so the goroutine exits cleanly.
	hbCtx, cancelHeartbeat := context.WithCancel(context.Background())
	defer cancelHeartbeat()
	startHeartbeat(hbCtx, core, cfg.WorkerID, cfg.WorkerEgressIP, logger)

	// Start the periodic scheduler alongside the worker, if this replica is the
	// one that schedules. It enqueues the reconcile sweeps (enrollments, inbox,
	// warmup, …) so work whose live task was lost (launch committed DB rows but
	// Redis enqueue failed) gets retried without operator action.
	stopScheduler, err := startScheduler(cfg, logger)
	if err != nil {
		return err
	}
	defer stopScheduler()

	// Dead-letter capture. Wired by type assertion for the same reason as the
	// cleaner/breaker capabilities in worker.Register: the capability is
	// consumed through a one-method seam rather than by widening
	// coreapi.Client (and its many test fakes). A core without it yields a nil
	// recorder, which disables capture — the pre-existing behaviour where an
	// exhausted task vanishes into asynq's own archive — rather than failing.
	var deadLetters queue.DeadLetterRecorder
	if dl, ok := core.(coreapi.DeadLetterClient); ok {
		deadLetters = deadLetterRecorder{core: dl}
	} else {
		logger.Warn("coreapi has no dead-letter capability; exhausted tasks will not be captured")
	}

	srv := queue.NewServer(cfg.RedisAddr, logger, cfg.WorkerConcurrency, cfg.WorkerQueues, deadLetters)
	mux := queue.NewMux()
	// The DNS resolvers for the two sweeps. The first resolves only domains
	// derived from connected mailboxes and the second only domains derived from
	// contact addresses (coreapi supplies both lists). Neither needs an SSRF vet:
	// a DNS lookup dials the host's configured nameservers, never the name being
	// looked up, so no user-supplied host is ever connected to here.
	worker.Register(mux, core, sndr, engager, reader, dnsauth.NewResolver(), esp.NewResolver(),
		enq, cfg.PublicURL, cfg.TrackingSecret, cfg.WarmupSecret, mtx)

	logger.Info("worker starting", "redis", cfg.RedisAddr, "concurrency", cfg.WorkerConcurrency)
	if err := srv.Run(mux); err != nil {
		logger.Error("worker error", "err", err)
		return err
	}
	return nil
}

// deadLetterRecorder adapts coreapi.DeadLetterClient to the transport-neutral
// queue.DeadLetterRecorder seam. It exists because platform/queue must not
// import coreapi (platform/* never depends on the control⇄execution seam), so
// the translation between the two one-method interfaces happens here at the
// composition root — the same shape as replyLabelAdapter in cmd/inroad.
type deadLetterRecorder struct{ core coreapi.DeadLetterClient }

func (d deadLetterRecorder) RecordDeadLetter(ctx context.Context, in queue.DeadLetter) error {
	return d.core.RecordDeadLetter(ctx, coreapi.DeadLetterInput{
		WorkspaceID:  in.WorkspaceID,
		TaskType:     in.TaskType,
		Payload:      in.Payload,
		LastError:    in.LastError,
		AttemptCount: in.AttemptCount,
	})
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
