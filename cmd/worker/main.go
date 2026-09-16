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

	"github.com/inroad/inroad/internal/app/webhook"
	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/coreapi/inprocess"
	"github.com/inroad/inroad/internal/platform/config"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/dnsauth"
	"github.com/inroad/inroad/internal/platform/esp"
	"github.com/inroad/inroad/internal/platform/httpx"
	"github.com/inroad/inroad/internal/platform/log"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/metrics"
	"github.com/inroad/inroad/internal/platform/providersignal"
	"github.com/inroad/inroad/internal/platform/queue"
	platformrealtime "github.com/inroad/inroad/internal/platform/realtime"
	"github.com/inroad/inroad/internal/platform/redisconn"
	"github.com/inroad/inroad/internal/platform/version"
	"github.com/inroad/inroad/internal/platform/warmup"
	"github.com/inroad/inroad/internal/worker"
	"github.com/inroad/inroad/internal/worker/fleetsignal"
)

// workerHeartbeatInterval is how often a worker refreshes its `workers` row. It
// matches the assigner's live-worker window (coreapi workerLiveWindow, 15m) with
// comfortable headroom so a couple of missed ticks don't drop it from routing.
const workerHeartbeatInterval = 5 * time.Minute

// workerSignalFlushInterval is how often a worker reports the provider verdicts
// it accumulated in memory (internal/worker/fleetsignal). Deliberately the same
// cadence as the heartbeat: a worker reports its LIVENESS and its STANDING WITH
// PROVIDERS on one clock, so an operator reading `workers` and
// `worker_provider_signals` side by side sees two series sampled alike.
//
// It is also what a crash costs. Counters live in memory until a flush, so an
// unclean stop loses at most this much — which is the accepted trade (losing a
// window of counts, never a send), and the reason for the graceful final flush
// in Flusher.Run.
const workerSignalFlushInterval = 5 * time.Minute

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

	// Parsed here, immediately after config load and before anything connects
	// (the pool, Redis, the keyring), so a bad INROAD_WORKER_ROLE fails fast
	// with no DB attempt. Config carries only the raw string (see
	// config.Config.WorkerRole) because platform must not import this package.
	// Through the logger, not os.Stderr: the config.Load failure above prints raw
	// because the logger does not exist yet, but by here it does, and log.New
	// emits JSON to stdout. Printing the one line that explains why the worker
	// refused to start onto a different stream in a different format is how it
	// goes missing in whatever collects the container's logs.
	role, err := worker.ParseRole(cfg.WorkerRole)
	if err != nil {
		logger.Error("invalid worker role", "err", err)
		return err
	}
	logger.Info("worker role", "role", role,
		"note", "control runs the scheduler and sweeps; send runs per-message work")
	// Which of the three WorkerID sources fired (see config.Config.WorkerID's
	// doc): "override" (INROAD_WORKER_ID pinned), "hostname" (RoleAll, or no
	// public IP found on a fleet host — NAT/no-egress/CI), or "ipv4"/"ipv6"
	// (derived from the host's public address). Logged unconditionally at
	// INFO, not only on the no-public-IP fallback, because an operator
	// diagnosing "why is this mailbox not moving with the box" needs the
	// SAME id/family pair the `workers` row records (see UpsertWorker) in one
	// place, and every path is equally worth a line, not just the surprising
	// one.
	logger.Info("worker identity", "worker_id", cfg.WorkerID, "id_family", cfg.WorkerIDFamily)

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
		// httpx.MetricsMux additionally mounts /debug/pprof/* when
		// INROAD_PPROF_ENABLED is set (default off) — this fleet's own
		// diagnostic path for a goroutine leak or heap growth on a remote
		// worker, without a debugger attached. Still only on this
		// operator-only listener, never a public port.
		metricsSrv := httpx.NewServer(cfg.MetricsAddr, httpx.MetricsMux(mtx.Handler(), cfg.PprofEnabled))
		metricsWG.Add(1)
		go func() {
			defer metricsWG.Done()
			if err := httpx.Run(metricsCtx, metricsSrv); err != nil {
				logger.Error("metrics server error", "err", err)
			}
		}()
		logger.Info("metrics listening", "addr", cfg.MetricsAddr, "pprof", cfg.PprofEnabled)
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

	// Decide where this worker's decrypted credentials come from, and build it.
	// A fleet (role=send) host gets a credbroker.HTTPOpener and NO keyring, so
	// INROAD_MASTER_KEY is neither needed nor accepted here; the single-process
	// self-host topology (RoleAll) still builds the keyring exactly as before.
	// resolveCredentialMode owns the whole decision and refuses the unsafe
	// combinations — see cmd/worker/credentials.go.
	creds, err := buildCredentialWiring(cfg, role, gen.New(pool), logger)
	if err != nil {
		logger.Error("credential source unusable", "err", err)
		return err
	}
	// nil in every mode but credentialsLocal. inprocess.New treats a nil keyring
	// as "cannot open secrets locally" and fails closed unless the broker option
	// below supplies an opener; webhook.NewService likewise refuses to MINT a
	// secret without one (a path the worker never takes — it only dispatches).
	keyring := creds.keyring

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
	realtimeRedis := redis.NewClient(redisconn.MustOptions(cfg.RedisAddr))
	defer func() { _ = realtimeRedis.Close() }()
	realtimeHub := platformrealtime.New(realtimeRedis)
	defer func() { _ = realtimeHub.Close() }()

	// Created before the coreapi client so the webhook emitter (which enqueues
	// webhook:deliver tasks) can be wired into it. Closed on return.
	enq := queue.NewClient(cfg.RedisAddr)
	defer enq.Close()

	// The outbound-webhook emitter: reply.received / email.bounced /
	// contact.unsubscribed fan out to a workspace's registered endpoints from the
	// inbox poller's coreapi writes.
	webhookEmitter := webhook.NewServiceEmitter(
		webhook.NewService(webhook.NewPgStore(gen.New(pool)), keyring, enq, cfg.WebhookAllowPrivate))

	coreOpts := []inprocess.Option{
		// The claim-before-send outcome counter (won/reclaimed/lost/…) is
		// emitted from inside the claim, which is the only place every outcome
		// is already distinguished.
		inprocess.WithMetrics(mtx),
		// Enables PublishRealtime. Omitting it would leave every publish a no-op
		// and browsers on their polling fallback — correct, but the point of the
		// slice is that an inbound reply reaches an open tab without one.
		inprocess.WithRealtime(realtimeHub),
		// Enables outbound webhook fan-out for the three catalog events.
		inprocess.WithWebhooks(webhookEmitter),
	}
	// Empty unless this worker brokers. When present it REPLACES the (nil)
	// keyring-backed opener, so every credential this process needs is opened
	// by the control plane and none of them by this host.
	coreOpts = append(coreOpts, creds.coreOptions()...)
	core := inprocess.New(pool, keyring, cfg.JWTSecret, cfg.PublicURL, googleOAuth, msOAuth, cfg.WarmupSecret, warmup.NewStaticLibrary(), coreOpts...)

	// Resolve the optional worker egress IP once. When set, every outbound dial
	// this worker makes to a mailbox provider binds its SOURCE address to it
	// (spec §15) so a mailbox's mail egresses from one IP — SMTP and IMAP via
	// net.Dialer.LocalAddr, Gmail and Microsoft Graph via the HTTP transport the
	// API clients below are constructed with. It never relaxes the SSRF
	// destination vet (§17.7).
	egressAddr, err := mail.ParseEgressIP(cfg.WorkerEgressIP)
	if err != nil {
		logger.Error("invalid worker egress ip", "err", err)
		return err
	}
	// Per-worker provider signals. Every send and every poll authenticates to a
	// mailbox PROVIDER from this host's egress IP, and the provider throttles,
	// challenges and rate-limits per source address — the one per-IP risk that
	// recipient-side reputation (warmup lanes) cannot see, because Inroad never
	// dials a recipient's MX at all. The collector accumulates classified
	// verdicts in memory; the flusher below reports the deltas through coreapi.
	signals := providersignal.NewCollector(time.Now)

	// MultiSender dispatches SMTP vs Gmail vs Graph on the job's Provider; the
	// SMTP leg keeps the SSRF-vetted NetSender, the Gmail leg uses the fixed
	// Google host, and the m365 leg uses the fixed Microsoft Graph host.
	//
	// Wrapped in the signal-capturing decorator HERE, at the composition root, so
	// all five send paths (sequence:advance, warmup:tick, warmup:engage's reply,
	// testsend:send, and the inbox package's manual reply/compose sends) are
	// observed by one wrapper. It returns each send's result untouched — a
	// telemetry decorator that could alter a send's outcome would be a delivery
	// bug waiting to happen.
	//
	// All three legs take the same egressAddr. The API legs take it as a
	// constructor argument rather than a field assignment because they build an
	// HTTP transport (and its connection pool) once; see NewGmailSender.
	smtpSender := mail.NewNetSender(cfg.MailAllowPrivateHosts)
	smtpSender.LocalAddr = egressAddr
	sndr := fleetsignal.NewSender(
		mail.NewMultiSender(smtpSender, mail.NewGmailSender(egressAddr), mail.NewGraphSender(egressAddr)), signals)
	// The IMAP reader is wrapped for the same reason: a poll is an
	// authentication from this IP on every tick. LocalAddr is set on the concrete
	// reader BEFORE wrapping — the decorator forwards calls, it does not forward
	// field assignments.
	netReader := mail.NewNetInboxReader(cfg.MailAllowPrivateHosts)
	netReader.LocalAddr = egressAddr
	reader := fleetsignal.NewInboxReader(netReader, signals)
	// Engager runs recipient-side warmup engagement (mark-read/rescue). The IMAP leg
	// dials through the SAME SSRF-vetted, source-IP-bound path as the reader; the
	// Gmail leg uses the fixed Google host; m365 is a documented clean skip.
	imapEngager := mail.NewNetEngager(cfg.MailAllowPrivateHosts)
	imapEngager.LocalAddr = egressAddr
	engager := mail.NewMultiEngager(imapEngager, mail.NewGmailEngager(egressAddr))

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
	startHeartbeat(hbCtx, core, cfg.WorkerID, cfg.WorkerEgressIP, cfg.WorkerIDFamily, role, logger)

	// Report the provider verdicts collected above. Wired by type assertion for
	// the same reason as the dead-letter recorder below and the capabilities in
	// worker.Register: the capability is consumed through a one-method seam
	// rather than by widening coreapi.Client and its many fakes. A core without
	// it collects signals that are never reported — degraded observability, never
	// a failed send.
	//
	// The shutdown is a cancel-THEN-WAIT like the metrics listener above, and for
	// a sharper reason: Flusher.Run performs a FINAL flush after its context is
	// cancelled, so returning from run() without waiting would race that write
	// against pool.Close() and lose the last window on every clean stop.
	//
	// No role gate. A control-role worker registers no per-message handlers, so
	// it sends and polls nothing, so its windows are empty and Flush writes
	// nothing — the gate would be a second statement of a fact the data already
	// makes true.
	if sigClient, ok := core.(coreapi.ProviderSignalClient); ok {
		flusher := fleetsignal.NewFlusher(sigClient, signals, cfg.WorkerID)
		signalCtx, cancelSignals := context.WithCancel(context.Background())
		var signalWG sync.WaitGroup
		signalWG.Add(1)
		go func() {
			defer signalWG.Done()
			flusher.Run(signalCtx, workerSignalFlushInterval)
		}()
		defer func() {
			cancelSignals()
			signalWG.Wait()
		}()
	} else {
		logger.Warn("coreapi has no provider-signal capability; per-worker provider signals will not be recorded")
	}

	// Start the periodic scheduler alongside the worker, if this replica is the
	// one that schedules. It enqueues the reconcile sweeps (enrollments, inbox,
	// warmup, …) so work whose live task was lost (launch committed DB rows but
	// Redis enqueue failed) gets retried without operator action.
	stopScheduler, err := startScheduler(cfg, role, logger)
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

	srv := queue.NewServer(cfg.RedisAddr, logger, cfg.WorkerConcurrency, resolveWorkerQueues(cfg, role, cfg.WorkerID), deadLetters)
	mux := queue.NewMux()
	// The DNS resolvers for the two sweeps. The first resolves only domains
	// derived from connected mailboxes and the second only domains derived from
	// contact addresses (coreapi supplies both lists). Neither needs an SSRF vet:
	// a DNS lookup dials the host's configured nameservers, never the name being
	// looked up, so no user-supplied host is ever connected to here.
	worker.Register(mux, worker.Deps{
		Role:                role,
		Core:                core,
		Sender:              sndr,
		Engager:             engager,
		Reader:              reader,
		Enqueuer:            enq,
		EgressAddr:          egressAddr,
		Resolver:            dnsauth.NewResolver(),
		MXResolver:          esp.NewResolver(),
		PublicURL:           cfg.PublicURL,
		TrackingSecret:      cfg.TrackingSecret,
		WarmupSecret:        cfg.WarmupSecret,
		WebhookAllowPrivate: cfg.WebhookAllowPrivate,
		Metrics:             mtx,
	})

	logger.Info("worker starting", "version", version.String(), "redis", redisconn.Redact(cfg.RedisAddr), "concurrency", cfg.WorkerConcurrency)
	if err := srv.Run(mux); err != nil {
		logger.Error("worker error", "err", err)
		return err
	}
	return nil
}

// resolveWorkerQueues decides the queue set this process consumes. An operator
// override (INROAD_WORKER_QUEUES, surfaced as cfg.WorkerQueues) wins ENTIRELY
// when present — an operator who names queues means exactly those, and
// silently appending the role's defaults would make the override useless for
// isolating a host. Absent an override, the role decides (worker.QueuesFor):
// this is the composition point, because platform/config must not import
// internal/worker, so the role→queues decision cannot live in config itself.
func resolveWorkerQueues(cfg *config.Config, role worker.Role, workerID string) []string {
	if len(cfg.WorkerQueues) > 0 {
		return cfg.WorkerQueues
	}
	return worker.QueuesFor(role, workerID)
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
		Queue:        in.Queue,
	})
}

// heartbeatClient is the one-method slice of coreapi.Client that startHeartbeat
// needs. It exists so a test can assert the ROLE gates registration without a
// fake satisfying coreapi.Client's other methods (it has dozens — see the Deps
// comment in internal/worker/handlers.go on the same tradeoff).
type heartbeatClient interface {
	UpsertWorkerHeartbeat(ctx context.Context, workerID, egressIP, idFamily string) error
}

// startHeartbeat registers this worker immediately, then refreshes its `workers`
// row every workerHeartbeatInterval until ctx is cancelled. The initial beat is
// synchronous so the assigner sees the worker as live before it processes its
// first task. A worker with an empty id (hostname lookup failed AND no
// INROAD_WORKER_ID) can't own a stable "w:<id>" affinity queue, so it skips
// registration and consumes only its ROLE's other shared queues
// (worker.QueuesFor with workerID "" omits just the affinity entry — QueueSend
// and QueueDefault stay, plus QueueControl too for RoleAll). Heartbeat
// failures are logged, not fatal: a transient DB blip must not take the worker
// down.
//
// A worker heartbeats as assignable IF AND ONLY IF it runs per-message work.
// AssignMailboxWorker picks a mailbox's owner from the `workers` table and
// returns a "w:<worker_id>" queue name, which queue.affinityQueue then uses as
// the destination in place of the task type's role queue.
//
// An assignment redirects two task types today: warmup:tick
// (queue.EnqueueWarmupTickAt) and inbox:poll (queue.EnqueueInboxPoll). Both are
// provider authentications from this host's egress IP, and that is the whole
// rule — it is not about warmup.
//
// The rest go to the shared QueueSend whoever owns the mailbox, for reasons
// that differ by task and are worth keeping straight:
//   - sequence:advance is keyed on an ENROLLMENT. Its mailbox does not exist at
//     enqueue time for an initial step: it is chosen inside GetStepSendJob ->
//     resolveSender, which claims a pool member write-once and bumps its
//     rotation counters — deliberately AFTER the campaign-paused, daily-limit
//     and not-due gates, so a send that never happens pins nothing. Routing it
//     per-mailbox therefore needs sender selection to move, which is a design
//     decision, not a routing one. This is a KNOWN GAP: campaign sends do
//     authenticate from an arbitrary worker.
//   - testsend:send DOES name a mailbox in its payload and is a real provider
//     authentication, so the rule applies — but its only producer is
//     campaign.Service in the API server, which holds no coreapi client (no
//     app/* package does), and an operator triggers it a handful of times a day
//     against a per-workspace rate limit. Pinning it means giving that service a
//     worker-resolver seam; worth doing, not worth doing here.
//   - warmup:engage acts on the RECIPIENT's own mailbox, so there is no
//     from-mailbox affinity to honour.
//   - webhook:deliver POSTs to a customer endpoint, not a mailbox provider.
//     There is no authentication and no per-IP provider reputation involved,
//     and its dial does not even bind LocalAddr (mail.GuardedDialContext), so
//     pinning it would buy nothing.
//   - inbox:pending_reply_send / inbox:pending_compose_send name a PENDING ROW,
//     not a mailbox; same shape as sequence:advance, much lower volume.
//
// A control-role host registers no per-message handlers (worker.Register), and
// — since resolveWorkerQueues derives consumption from worker.QueuesFor —
// consumes no per-message queue either: QueuesFor omits "w:<id>" for a role
// that does not run per-message work. So a mailbox STILL assigned to a control
// host (see the stale-assignment paragraph below) goes quiet rather than
// dead-lettering: nothing dequeues its "w:<its-own-id>" queue at all, and the
// task just sits pending until the assignment is cleared or expires out of the
// live window. Declining to heartbeat is what keeps a control host out of the
// assigner for NEW assignments; the operator action described below is what's
// needed for assignments that predate the role flip.
//
// The gate only stops NEW assignments. A host that ran as `send` and comes back
// as `control` keeps its existing mailbox_worker_assignments rows, and
// GetLiveMailboxWorkerAssignment honours them for as long as its last heartbeat
// stays inside coreapi's 15m live window — so flip a host to `control` under a
// NEW INROAD_WORKER_ID, or clear its assignment rows at cutover.
//
// RoleAll still heartbeats (self-host, the only worker there is).
func startHeartbeat(ctx context.Context, core heartbeatClient, workerID, egressIP, idFamily string, role worker.Role, logger *slog.Logger) {
	if !role.RunsPerMessageWork() {
		logger.Info("heartbeat disabled by worker role", "role", role,
			"note", "a control-role host has no per-message handlers; the assigner must never route a mailbox to it")
		return
	}
	if workerID == "" {
		logger.Warn("worker id empty; skipping registration (no per-IP affinity queue; still serves the role's shared queues)")
		return
	}
	beat := func() {
		if err := core.UpsertWorkerHeartbeat(ctx, workerID, egressIP, idFamily); err != nil {
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
