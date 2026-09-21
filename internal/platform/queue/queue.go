// Package queue wraps asynq: task-type constants, typed enqueue helpers,
// and server/mux constructors. This is the only place asynq is imported.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/platform/bus"
	"github.com/inroad/inroad/internal/platform/bus/redisbus"
	"github.com/inroad/inroad/internal/platform/redisconn"
)

// Delivery-idempotency defense in depth (the claim in the send/advance handlers
// is the real correctness guarantee; these just cut wasted retries/concurrency):
//   - sendMaxRetry bounds how many times a permanently-failing task cycles
//     (asynq's default is 25).
//   - taskRetention keeps a finished task briefly for post-run inspection.
const (
	sendMaxRetry = 5
	// pollMaxRetry is deliberately lower than sendMaxRetry. A poll that keeps
	// failing is re-fanned-out by the next sweep minutes later, so giving up
	// early costs one interval of latency and never a lost message — whereas
	// cycling a broken poll 5+ times just holds worker slots.
	pollMaxRetry  = 2
	taskRetention = 24 * time.Hour
)

// Per-task handler ceilings. asynq applies 30 minutes when a task carries no
// Timeout, which is far longer than any handler here should hold a worker slot:
// with WorkerConcurrency at 10, a handful of mailboxes on a slow provider can
// occupy every slot and stall sends and sweeps behind them. When the timeout
// fires asynq cancels the handler's context and retries the task, so these are
// ceilings on one attempt, not on the work overall.
//
// Each is set well clear of the underlying dial timeouts (mail.dialTimeout 15s
// for IMAP, 30s for SMTP) so a slow-but-progressing provider is not cut off
// mid-pass; the point is to bound a wedged handler, not a slow one.
const (
	// A poll pass fetches up to fetchBatchSize messages plus a junk scan, each
	// with its own dial, and makes several coreapi calls per message — so it
	// gets the widest ceiling. A poll cut short loses nothing: the next sweep
	// re-fans it out and the poller resumes from what it has already recorded.
	pollTimeout = 5 * time.Minute
	// One send is one SMTP conversation. Kept tight so a hung provider frees the
	// slot quickly; the row-claim, not this, is what prevents a double send when
	// a timed-out attempt is retried.
	sendTimeout = 2 * time.Minute
	// Fan-out sweeps are DB-plus-Redis work with no provider dial, but they walk
	// the whole active fleet one mailbox at a time, so they need room at scale.
	sweepTimeout = 10 * time.Minute
)

// Queue names. A task's queue decides WHICH WORKER ROLE can claim it, which is
// load-bearing rather than cosmetic: asynq dequeues a task before consulting
// the mux, so a host that consumes a queue it has no handler for claims the
// task, fails handler lookup, and burns its retries into the dead-letter table.
// Routing per-message work and control-plane work to separate queues is what
// stops a control host from silently eating sends.
const (
	// QueueSend carries per-message work: sends, warmup ticks and engagement,
	// inbox polls, manual replies, test sends, webhook deliveries.
	QueueSend = "send"
	// QueueControl carries the periodic reconciles, the purges and the campaign
	// breaker — everything that scans or deletes across tenants.
	QueueControl = "control"
	// QueueDefault is asynq's built-in queue. NOTHING is enqueued here any more:
	// every producer routes by task type through taskQueues, including the
	// dead-letter replay that used to land here. It is consumed transitionally
	// by the send and all roles (never by control), which is what makes a
	// rolling upgrade to this version lossless — an upgraded worker drains both
	// what this version produces and what the previous one left on "default".
	//
	// DRAIN ONLY, and removed in the release after this one — together with the
	// QueuesFor branch that consumes it. The deadline is paired with a signal
	// that says the queue has drained, rather than with a judgement call about
	// whether some deployment might still hold a backlog (which the person
	// deleting the code cannot check): inroad_queue_depth{queue="default"} —
	// scraped per queue by internal/platform/metrics/queue.go — must read 0 in
	// every state and stay there across a window wider than the longest delay a
	// task can carry, on every deployment being upgraded.
	//
	// Deliberately NOT marked Deprecated in the godoc sense: every use of this
	// constant today (QueuesFor, and the tests pinning what each role consumes)
	// is the drain working as intended, and marking it would flag four correct
	// call sites as mistakes in exchange for saying nothing this comment does
	// not. The same distinction is drawn at the other drain site, internal/
	// worker/inbox.RegisterPerMessage: the deprecated identifier there is the
	// TASK TYPE nothing may enqueue, and its one remaining use carries an
	// explained //nolint because it IS the drain.
	QueueDefault = "default"
)

// WorkerQueue names one worker's private affinity queue. Per-mailbox work is
// routed here so that a mailbox keeps authenticating to its provider from one
// egress IP.
//
// The IP is what the PROVIDER sees on every authentication, and providers
// challenge sign-ins, throttle (454 4.7.0) and rate-limit (421 4.7.28) per
// source address. It is NOT what the recipient sees: Inroad never delivers to a
// recipient's MX — every send authenticates to the customer's own provider,
// which delivers from its own pool (docs/security.md invariant 46) — so
// recipient-side reputation cannot transfer between mailboxes sharing a worker.
// See internal/platform/providersignal's package doc, which is where that
// asymmetry is measured.
//
// An earlier version of this comment said affinity existed because "warmup
// reputation is per-IP". That premise was retired by the fleet design's
// 2026-09-14 revision (and by fleetscore's rule 3): warmup lanes are built
// entirely from recipient-side signals, which the worker's IP is invisible to.
// The routing survived the premise because the real reason — provider-side
// sign-in risk — applies to every authentication, not just a warmup one.
func WorkerQueue(workerID string) string { return "w:" + workerID }

// taskQueues is the single source of truth for which role queue a task type
// belongs on. Every producer reads it — the typed helpers through
// enqueueRouted, the periodic sweeps through registerControlSweep, the two
// bus-seam helpers through routeTaskType — and so does the dead-letter replay.
//
// It is a TABLE rather than a constant repeated at each producer because
// replay cannot repeat one: EnqueueReplay re-enqueues a captured task type
// verbatim and has no typed helper to read a queue off, so before this existed
// it set no queue at all and asynq filed the task under its own "default",
// which the control role never consumes. Routing modelled as data on the
// consuming side (internal/worker.QueuesFor) and as twelve literals on the
// producing side is what left that one producer with nothing to consult.
//
// inbox:reply_send is deliberately absent. Nothing enqueues it (drain-only —
// see TaskInboxReplySend) and deadletter.Service.Replay refuses it by task type
// before it could reach this table, so a mapping here would only assert a route
// nothing may take.
var taskQueues = map[string]string{
	// Per-message work. For the two AFFINITY-routed types — warmup:tick and
	// inbox:poll, the two whose payload names a mailbox — the entry here is the
	// FALLBACK: a mailbox with a worker assignment overrides it with that
	// worker's affinity queue, which is what keeps the mailbox authenticating
	// to its provider from one IP (see affinityQueue).
	TaskWarmupTick:              QueueSend,
	TaskWarmupEngage:            QueueSend,
	TaskSequenceAdvance:         QueueSend,
	TaskInboxPoll:               QueueSend,
	TaskInboxPendingReplySend:   QueueSend,
	TaskInboxPendingComposeSend: QueueSend,
	TaskTestSend:                QueueSend,
	TaskWebhookDeliver:          QueueSend,

	// Control-plane work: the periodic reconciles and purges, plus the campaign
	// breaker — which is a cross-campaign decision, not one message's delivery,
	// and is registered by the control role (internal/worker/handlers.go).
	TaskWarmupSweep:            QueueControl,
	TaskInboxSweep:             QueueControl,
	TaskSweepEnrollments:       QueueControl,
	TaskMaintenanceCleanup:     QueueControl,
	TaskDomainAuthSweep:        QueueControl,
	TaskRecipientESPSweep:      QueueControl,
	TaskFleetRotate:            QueueControl,
	TaskInboxPendingSendSweep:  QueueControl,
	TaskDeliverabilityEvaluate: QueueControl,
}

// queueForTaskType reports the role queue a task type belongs on, and whether
// it is routable at all. Comma-ok rather than a "" fallback on purpose: ""
// means "asynq's built-in default queue" everywhere downstream (bus.Job.Dest,
// redisbus.asynqOptions), which is precisely the silent misroute this table
// exists to prevent.
func queueForTaskType(taskType string) (string, bool) {
	q, ok := taskQueues[taskType]
	return q, ok
}

// routeTaskType is queueForTaskType in the form the producers want: the queue,
// or one error message written once. An unroutable task type fails the enqueue
// rather than landing somewhere quiet — the same fail-loud rule as enqueue's
// guard, and the rule that matters most where an ARBITRARY task type enters the
// system (EnqueueReplay hands back whatever was captured).
func routeTaskType(taskType string) (string, error) {
	q, ok := queueForTaskType(taskType)
	if !ok {
		return "", fmt.Errorf("queue: task type %q has no role queue; add it to taskQueues", taskType)
	}
	return q, nil
}

// affinityQueue is the routing rule for PER-MAILBOX work, written once: the
// queue of the worker the mailbox is assigned to, or — with no assignment, so
// no IP to stay on — the task type's own role queue.
//
// dest is always the server-side result of coreapi.AssignMailboxWorker, never
// client input (invariant §17.8); "" is that function's sentinel for NO LIVE
// WORKER AT ALL — a single-node dev stack with no worker registered, or a fleet
// mid-restart — not for "not placed yet", since placement persists an
// assignment on first sight and hands back its queue. A self-host RoleAll
// install therefore takes the affinity queue like any other topology once its
// one worker has heartbeated, and the fallback here is what carries it (and
// every pre-fleet installation) until then.
//
// It exists as a function rather than as an `if dest == ""` at each producer
// because there are two producers and they submit through different funnels —
// warmup:tick through the bus seam (Publish), inbox:poll through
// enqueueAffinity, which is what keeps its asynq.Retention that bus.Options
// cannot express. Two funnels are tolerable; two answers to "which queue does
// this mailbox's work go to" are not.
func affinityQueue(taskType, dest string) (string, error) {
	if dest != "" {
		return dest, nil
	}
	return routeTaskType(taskType)
}

const TaskWarmupTick = "warmup:tick"

// WarmupTickPayload is the body of a warmup:tick task. WorkspaceID travels
// alongside MailboxID so the worker can pin workspace_id in its coreapi lookups
// (defense in depth on the unguessable mailbox UUID), matching every other task
// payload.
type WarmupTickPayload struct {
	MailboxID   string `json:"mailbox_id"`
	WorkspaceID string `json:"workspace_id"`
}

// TaskWarmupEngage drives the recipient-side engagement of one received warmup
// message (rescue-from-spam / mark-read / threaded reply). It is enqueued,
// delayed by a humanized dwell, by the inbox poller when it detects a warmup
// message (spec §7); the handler (C5b) acts on the receipt behind the
// warmup_receipts.engaged idempotency guard.
const TaskWarmupEngage = "warmup:engage"

// WarmupEngagePayload is the body of a warmup:engage task. WorkspaceID travels
// alongside ReceiptID so the worker can pin workspace_id in its coreapi lookups
// (defense in depth on the unguessable receipt UUID), matching every other task
// payload.
type WarmupEngagePayload struct {
	ReceiptID   string `json:"receipt_id"`
	WorkspaceID string `json:"workspace_id"`
}

// TaskWarmupSweep is the periodic fan-out that enqueues a warmup:tick for every
// due participant (routing each to its assigned worker queue) and recomputes
// participant health. Scheduled every 5 minutes.
const TaskWarmupSweep = "warmup:sweep"

// TaskMaintenanceCleanup is the daily retention job for expired security
// artifacts (sessions, challenges, one-time codes, and OAuth credentials).
const TaskMaintenanceCleanup = "maintenance:cleanup"

// TaskDomainAuthSweep re-checks the SPF/DKIM/DMARC records of every sending
// domain whose last completed check is older than the staleness window.
const TaskDomainAuthSweep = "domainauth:sweep"

// TaskFleetRotate is the periodic pass of the rotation gate: the ONLY thing
// that moves an already-placed mailbox off its worker. Placement keeps a live
// incumbent unconditionally, so without this a worker whose IP the provider has
// blocked holds every mailbox assigned to it and each of them keeps failing.
//
// Cross-tenant by nature — it scans assignments across every workspace — so it
// is control-role work like every other reconcile above.
const TaskFleetRotate = "fleet:rotate"

// TaskRecipientESPSweep classifies recipient domains by MX (Google/Microsoft/
// other) and evicts expired rows from that cache. It exists so ESP-matched
// sender selection never resolves DNS on the send path.
const TaskRecipientESPSweep = "recipientesp:sweep"

// TaskSequenceAdvance drives one step of a contact's enrollment: send the due
// step, then (lazy chain) schedule the next. One task per enrollment per step.
const TaskSequenceAdvance = "sequence:advance"

// AdvancePayload is the body of a sequence:advance task. WorkspaceID travels
// alongside EnrollmentID so the worker can pin workspace_id in its DB lookups
// (defense in depth on the unguessable enrollment UUID).
type AdvancePayload struct {
	EnrollmentID string `json:"enrollment_id"`
	WorkspaceID  string `json:"workspace_id"`
}

// TaskSweepEnrollments is the periodic reconcile that re-enqueues active
// enrollments whose next_due_at passed without a live advance task (launch
// committed rows but Redis enqueue failed, or a scheduled task was lost).
const TaskSweepEnrollments = "sequence:sweep_stuck_enrollments"

// TaskInboxPoll polls one mailbox's inbox for replies/bounces via IMAP.
const TaskInboxPoll = "inbox:poll"

// InboxPollPayload is the body of an inbox:poll task. WorkspaceID travels
// alongside MailboxID so the worker can pin workspace_id in its DB lookups
// (defense in depth on the unguessable mailbox UUID).
type InboxPollPayload struct {
	MailboxID   string `json:"mailbox_id"`
	WorkspaceID string `json:"workspace_id"`
}

// TaskInboxSweep is the periodic reconcile that enqueues an inbox:poll task
// for every active mailbox.
const TaskInboxSweep = "inbox:sweep"

// inboxSweepInterval is how often inbox:sweep fans out. It drives both the
// scheduler registration and the inbox:poll dedup bucket, so the two cannot
// drift apart — a bucket shorter than the interval would stop deduplicating.
const inboxSweepInterval = 3 * time.Minute

const TaskTestSend = "testsend:send"

// TestSendPayload is the body of a testsend:send task. It carries only ids --
// the worker (internal/worker/testsend) loads the step content, the preview
// personalization vars, and the resolved mailbox's decrypted transport itself
// through coreapi, so nothing here is a credential or rendered content.
// WorkspaceID travels alongside every other id so the worker's coreapi lookups
// are workspace-pinned (defense in depth on the unguessable ids).
type TestSendPayload struct {
	CampaignID  string `json:"campaign_id"`
	StepID      string `json:"step_id"`
	MailboxID   string `json:"mailbox_id"`
	To          string `json:"to"`
	WorkspaceID string `json:"workspace_id"`
}

// TaskInboxReplySend sent one manual reply whose body travelled in the task
// payload. POST /inbox/threads/{id}/reply now queues an inbox_pending_replies
// row and enqueues TaskInboxPendingReplySend instead, so nothing produces this
// any more; the constant and its handler survive only to drain tasks that were
// already in Redis when this shipped.
//
// Deprecated: drain-only; removed in the release after this one. Nothing
// enqueues this.
const TaskInboxReplySend = "inbox:reply_send"

// TaskInboxPendingReplySend delivers a manual reply — one whose row in
// inbox_pending_replies carries the body and the authoritative send_after. This
// task is only a POINTER to a row, which is what lets the operator undo the send
// by updating that row while the task is still pending in the queue, and what
// keeps the body out of Redis and out of a captured dead letter.
//
// Both the immediate and the deferred send paths use it: an immediate reply is
// a row whose send_after has already passed.
const TaskInboxPendingReplySend = "inbox:pending_reply_send"

// TaskInboxPendingComposeSend delivers a deferred COMPOSED email — a new
// message rather than a reply, whose recipients and subject are its own rather
// than derived from a thread. Same pointer-to-a-row design as
// TaskInboxPendingReplySend, and cancellable the same way.
const TaskInboxPendingComposeSend = "inbox:pending_compose_send"

// TaskInboxPendingSendSweep is the periodic reconcile that finds manual replies
// and composed emails no live task will ever deliver — a row whose enqueue was
// lost, and a row abandoned in 'sending' past its lease — and re-enqueues the
// ordinary send task for each. It never sends: a second send path for mail a
// human already pressed send on is the failure this whole subsystem is built
// around.
//
// Cross-tenant by nature, so control-role work like every other reconcile.
const TaskInboxPendingSendSweep = "inbox:pending_send_sweep"

// pendingSendSweepInterval is how often the stranded-send sweep ticks. It drives
// both the scheduler registration and the rescue tasks' dedup bucket, so the two
// cannot drift — a bucket shorter than the interval would stop deduplicating,
// and one longer would let a tick's rescue suppress the next tick's.
//
// Faster than the 5-minute family, and the reason is what this one sweeps: every
// other reconcile reconciles machine-scheduled work, while this one is the only
// safety net under an email a PERSON wrote and is watching for. A tick is two
// bounded index reads over rows that are transient by nature, and enqueues
// nothing at all when there is nothing stranded, so the cost of ticking often is
// close to zero.
const pendingSendSweepInterval = 2 * time.Minute

// InboxPendingReplySendPayload names a row in inbox_pending_replies. It
// carries NO body: the row is the single source of truth for what to send and
// whether to send it at all, so a payload copy could go stale the moment the
// operator cancels — and, as InboxReplySendPayload's history below shows, a
// payload copy of a reply body is disclosed by GET /dead-letters under a scope
// that is deliberately denied the inbox.
type InboxPendingReplySendPayload struct {
	PendingID   string `json:"pending_id"`
	WorkspaceID string `json:"workspace_id"`
}

// InboxPendingComposeSendPayload names a row in inbox_pending_composes. Carries
// no content, for the same reason InboxPendingReplySendPayload does not: the row
// is the single source of truth for what to send and whether to send it.
type InboxPendingComposeSendPayload struct {
	PendingID   string `json:"pending_id"`
	WorkspaceID string `json:"workspace_id"`
}

// InboxReplySendPayload is the body of an inbox:reply_send task: a thread id, a
// workspace pin, the claim key, and — the reason this type is being retired —
// the operator's free-text reply in BodyText.
//
// A payload is not a private channel between an enqueuer and its handler. On
// terminal failure DeadLetterErrorHandler stores it byte-for-byte in
// task_dead_letters and GET /dead-letters serves it verbatim under
// campaigns:read, which IS OAuth-grantable — while inbox:read is deliberately
// NOT, precisely because reply bodies are correspondence (see
// internal/app/auth/scopes.go). Carrying the body here therefore handed
// correspondence to a scope that is structurally denied it.
//
// The replacement is the pointer shape InboxPendingReplySendPayload already
// had: the body lives in an inbox_pending_replies row and the task names it.
// Nothing constructs this type any more, capture of its task type is suppressed
// (see legacyContentBearingTaskTypes in deadletter.go), and the only consumer
// left is the drain handler internal/worker/inbox.ReplySendHandler.
//
// TaskID carried the SAME id as the task's own asynq.TaskID, which is what made
// it usable as the claim-before-send key across retries and lease redeliveries.
//
// Deprecated: drain-only; removed in the release after this one. Nothing
// enqueues this.
type InboxReplySendPayload struct {
	ThreadID    string `json:"thread_id"`
	BodyText    string `json:"body_text"`
	WorkspaceID string `json:"workspace_id"`
	TaskID      string `json:"task_id"`
}

// TaskDeliverabilityEvaluate re-evaluates one campaign's circuit breaker. It is
// enqueued AFTER a send is finalised, never inside the send transaction, so a
// scoring bug cannot fail a delivery.
const TaskDeliverabilityEvaluate = "deliverability:evaluate"

// DeliverabilityEvaluatePayload is the body of a deliverability:evaluate task.
// WorkspaceID travels alongside CampaignID so every query the evaluation runs is
// workspace-pinned (defense in depth on the unguessable campaign UUID).
type DeliverabilityEvaluatePayload struct {
	CampaignID  string `json:"campaign_id"`
	WorkspaceID string `json:"workspace_id"`
}

// TaskWebhookDeliver delivers one outbound webhook: POST the signed event body
// to a registered endpoint. Enqueued by webhook.Service.Dispatch (control plane)
// after the originating domain operation has committed; retried by the handler
// itself on the app-level backoff schedule.
const TaskWebhookDeliver = "webhook:deliver"

// Per-attempt asynq ceilings for a webhook delivery. These are a BACKSTOP for a
// crashed or lost handler, not the retry policy: the real policy is the handler
// re-enqueuing itself via EnqueueWebhookDeliverIn on a {1m,5m,30m,2h,6h} schedule
// (up to 6 attempts). asynq's own retry only covers one attempt's handler dying
// mid-POST, so a low count is right — and the DB status='pending' guard makes a
// duplicate delivery a no-op regardless.
const (
	webhookMaxRetry = 3
	// Comfortably above the handler's 10s POST timeout so a slow-but-responding
	// receiver is not cut off, while still bounding a wedged handler.
	webhookDeliverTimeout = 30 * time.Second
)

// WebhookDeliverPayload names a row in webhook_deliveries. It is a POINTER to the
// row and carries NO body and NO secret: the row is the single source of truth
// for what to send, the signing secret is resolved control-plane-side through the
// keyring at delivery time, and — as InboxReplySendPayload's history shows — a
// payload copy of content is disclosed by GET /dead-letters under a scope the
// webhook feature never intends to expose it to. WorkspaceID travels alongside so
// every coreapi lookup the handler makes is workspace-pinned (defense in depth on
// the unguessable delivery UUID).
type WebhookDeliverPayload struct {
	DeliveryID  string `json:"delivery_id"`
	WorkspaceID string `json:"workspace_id"`
}

// webhookDeliverTaskID keys a webhook:deliver on (delivery, due-second) so a
// double-enqueue for the SAME instant (a Dispatch racing a reconcile sweep)
// collapses to one task, while the handler's own later re-enqueue for a backoff
// retry — a genuinely different due time — still enqueues. Whole-second
// granularity is safe because the row's status='pending' guard, not this key, is
// the delivery-idempotency guarantee.
func webhookDeliverTaskID(deliveryID string, due time.Time) string {
	return fmt.Sprintf("webhook:%s:%d", deliveryID, due.Unix())
}

// evaluateDedupWindow collapses the per-send fan-out. One evaluation per campaign
// per window is enough: the breaker reads committed state, so a slightly later
// evaluation sees strictly more evidence than the one it replaced, and without
// this a 10,000-contact launch would enqueue 10,000 identical evaluations.
//
// The delay is what makes the collapse effective rather than theoretical — a
// TaskID conflict only dedups while the earlier task is still PENDING, so an
// immediate task would be consumed before the next send finalised and dedup
// nothing. A minute of latency on a safeguard that acts over a 7-day window costs
// nothing.
const evaluateDedupWindow = time.Minute

// asynqConnOpt turns INROAD_REDIS_ADDR (a bare host:port or a redis:// /
// rediss:// URL) into asynq's connection option, carrying any username,
// password, database number and TLS across. redisconn owns the URL parsing;
// this is the one spot that maps its result onto asynq's struct, keeping asynq
// contained to this package. redisconn.MustOptions never fails for the bare
// form, and config.Load has already rejected a malformed URL.
func asynqConnOpt(redisAddr string) asynq.RedisConnOpt {
	opt := redisconn.MustOptions(redisAddr)
	network := opt.Network
	if network == "" {
		network = "tcp"
	}
	return asynq.RedisClientOpt{
		Network:   network,
		Addr:      opt.Addr,
		Username:  opt.Username,
		Password:  opt.Password,
		DB:        opt.DB,
		TLSConfig: opt.TLSConfig,
	}
}

// enqueuer is the raw asynq capability Client needs: submit a task with
// options, and close the connection NewClient opened. It embeds
// redisbus.Enqueuer — the same minimal seam redisbus.Dispatcher already
// depends on — rather than requiring the concrete *asynq.Client, so a test can
// inject a fake and read back exactly which asynq.Option (including
// asynq.Queue) a producer asked for without a live Redis: asynq.Option is a
// public Type()/Value() pair, not an opaque callback only a real client could
// execute (see redisbus_test.go's optByType for the established pattern).
type enqueuer interface {
	redisbus.Enqueuer
	Close() error
}

// Client enqueues tasks onto Redis.
type Client struct {
	inner enqueuer
}

func NewClient(redisAddr string) *Client {
	return &Client{inner: asynq.NewClient(asynqConnOpt(redisAddr))}
}

// warmupTickTaskID keys a warmup:tick on (mailbox, due-second) so duplicate
// enqueues for the SAME due instant — a sweep fan-out racing the send handler's
// lazy chain — dedup to one task, while a genuinely later tick still enqueues.
// Whole-second granularity is safe because ClaimWarmupSend (the row claim), not
// this key, is the delivery-idempotency guarantee; a collapsed duplicate only
// saves wasted work. Mirrors enqueueAdvance's advance:<id>:<sec> key.
func warmupTickTaskID(mailboxID string, due time.Time) string {
	return fmt.Sprintf("warmup:%s:%d", mailboxID, due.Unix())
}

// EnqueueWarmupTickAt schedules a warmup:tick for one mailbox at time t, routed
// to dest (the from-mailbox's assigned worker queue, spec §15 — so a mailbox's
// warmup and campaign mail egress from one IP; "" = no assignment yet, so
// affinityQueue falls back to the shared send queue rather than asynq's
// unconsumed "default"). It goes through the transport seam (bus.Dispatcher):
// Key→TaskID dedup, Dest→Queue routing, At→ProcessAt delayed delivery. dest is
// always derived server-side from the mailbox→worker assignment, never from
// client input (§17.8).
func (c *Client) EnqueueWarmupTickAt(ctx context.Context, mailboxID, workspaceID string, t time.Time, dest string) error {
	b, err := json.Marshal(WarmupTickPayload{MailboxID: mailboxID, WorkspaceID: workspaceID})
	if err != nil {
		return err
	}
	dest, err = affinityQueue(TaskWarmupTick, dest)
	if err != nil {
		return err
	}
	return c.Publish(ctx, bus.Job{
		Kind:    TaskWarmupTick,
		Payload: b,
		Key:     warmupTickTaskID(mailboxID, t),
		Dest:    dest,
	}, bus.Options{
		At:       t,
		MaxRetry: sendMaxRetry,
		Timeout:  sendTimeout,
	})
}

// warmupEngageTaskID keys a warmup:engage on the receipt id so a re-poll that
// re-detects the SAME warmup message (junk scans are stateless and idempotent)
// dedups to one engage task. The warmup_receipts.engaged guard — not this key —
// is the real double-engage guarantee; a collapsed duplicate only saves wasted
// work. Mirrors warmupTickTaskID's dedup discipline.
func warmupEngageTaskID(receiptID string) string {
	return "warmupengage:" + receiptID
}

// EnqueueWarmupEngageIn schedules a warmup:engage for one receipt after delay d
// (the humanized engage dwell from the receipt's plan). It goes through the
// transport seam (bus.Dispatcher): Key→TaskID dedup, In→ProcessIn delayed
// delivery. Engagement acts on the RECIPIENT's own mailbox, so no cross-worker
// egress routing applies — but it is still per-message work, so it belongs on
// the shared send queue (QueueSend), not asynq's unconsumed "default".
func (c *Client) EnqueueWarmupEngageIn(ctx context.Context, receiptID, workspaceID string, d time.Duration) error {
	b, err := json.Marshal(WarmupEngagePayload{ReceiptID: receiptID, WorkspaceID: workspaceID})
	if err != nil {
		return err
	}
	dest, err := routeTaskType(TaskWarmupEngage)
	if err != nil {
		return err
	}
	return c.Publish(ctx, bus.Job{
		Kind:    TaskWarmupEngage,
		Payload: b,
		Key:     warmupEngageTaskID(receiptID),
		Dest:    dest,
	}, bus.Options{
		In:       d,
		MaxRetry: sendMaxRetry,
		Timeout:  sendTimeout,
	})
}

// enqueue submits a task and treats an asynq TaskID conflict as success: a
// duplicate enqueue of an already-pending task (sweeper re-enqueue racing a live
// task) is a deliberate no-op, not an error.
//
// It refuses a task that carries no asynq.Queue option rather than letting
// asynq silently file it under its own built-in "default" queue. QueueDefault
// is consumed only transitionally (see its doc) and NOT by the control role at
// all, so an unrouted task would drain only until that transitional
// consumption is removed, then quietly stop — the exact role-blindness this
// package exists to prevent. Every producer that reaches this reaches it
// through enqueueAffinity (directly, or via enqueueRouted, which is that
// function with no dest), which derives the option from taskQueues; this is the
// backstop for a future one that calls enqueue directly.
//
// NOT every producer reaches this. EnqueueWarmupTickAt, EnqueueWarmupEngageIn
// and EnqueueReplay are bus-seam producers: they go through Publish ->
// redisbus.Dispatcher, which has no equivalent guard, and resolve their own
// queue at their own call sites instead — affinityQueue for the warmup tick,
// routeTaskType for the other two (see taskQueues' doc for the accurate
// enumeration). Scoped to this funnel only: bus.Job.Dest and
// redisbus.asynqOptions keep their own documented "" = shared-queue meaning,
// which this does not touch.
func (c *Client) enqueue(ctx context.Context, t *asynq.Task, opts ...asynq.Option) error {
	if _, ok := queueOption(opts); !ok {
		return fmt.Errorf("queue: enqueue %q: no asynq.Queue option set; every producer must route to a role queue", t.Type())
	}
	if _, err := c.inner.EnqueueContext(ctx, t, opts...); err != nil {
		if errors.Is(err, asynq.ErrTaskIDConflict) {
			return nil
		}
		return err
	}
	return nil
}

// enqueueRouted submits a task routed BY ITS TYPE: the queue comes from
// taskQueues rather than from the caller, so a producer cannot pick the wrong
// one — or forget one — and the queue a REPLAY of that type picks can never
// disagree with the queue its original producer picked. Everything a producer
// legitimately owns (dedup key, delay, retries, timeout, retention) stays the
// caller's.
func (c *Client) enqueueRouted(ctx context.Context, taskType string, payload []byte, opts ...asynq.Option) error {
	return c.enqueueAffinity(ctx, taskType, "", payload, opts...)
}

// enqueueAffinity is enqueueRouted for a task that MAY be pinned to one worker:
// dest wins when the caller resolved an assignment, and the task type's own
// role queue is the fallback (affinityQueue). It is a separate entry point
// rather than a wider enqueueRouted so that the dozen producers with no
// per-mailbox routing cannot pass a queue at all — routing by type stays the
// default, and overriding it stays a deliberate act with a name.
func (c *Client) enqueueAffinity(ctx context.Context, taskType, dest string, payload []byte, opts ...asynq.Option) error {
	q, err := affinityQueue(taskType, dest)
	if err != nil {
		return err
	}
	return c.enqueue(ctx, asynq.NewTask(taskType, payload), append(opts, asynq.Queue(q))...)
}

// queueOption returns the value of opts' asynq.Queue option and whether one
// was present. The one scan behind both enqueue's guard and the test fakes'
// assertions (fakeEnqueuer.queue()/fakeRegistrar.queue() in queue_test.go call
// this directly, same package) — asynq.Option is a public Type()/Value() pair,
// so this needs no cooperation from asynq beyond that.
//
// It returns the LAST matching option, not the first, because that is what
// asynq itself does: composeOptions type-switches over opts in order and
// OVERWRITES res.queue on every queueOption it sees (asynq@v0.26.0/
// client.go:255-264), so the option that survives to be enqueued on is
// whichever one came last in the slice. Returning the first match here would
// make this guard inspect a queue the task does not actually go to the moment
// any caller ever passed two — today nothing does (enqueueRouted appends the
// routed queue last and no producer passes its own), but the guard and the
// runtime must never be able to disagree about which one wins.
//
// The comma-ok assertion is load-bearing, not lint appeasement: this runs on
// the send path (via enqueue's guard), so a bare o.Value().(string) would turn
// a routing check into a production panic at enqueue time if asynq's QueueOpt
// value type ever changed. A QueueOpt whose value ISN'T a string does not
// overwrite a previously found result — it is treated as no usable option at
// this position, exactly as asynq's own type switch would silently not match
// a value of the wrong concrete type — and if nothing else matches, enqueue's
// guard rejects the task the same way it would an entirely missing Queue
// option (fail the enqueue, never silently accept an unusable/malformed one).
func queueOption(opts []asynq.Option) (string, bool) {
	result, found := "", false
	for _, o := range opts {
		if o.Type() != asynq.QueueOpt {
			continue
		}
		if s, ok := o.Value().(string); ok {
			result, found = s, true
		}
	}
	return result, found
}

// EnqueueAdvance enqueues a sequence:advance task for immediate processing.
func (c *Client) EnqueueAdvance(ctx context.Context, enrollmentID, workspaceID string) error {
	return c.enqueueAdvance(ctx, enrollmentID, workspaceID, time.Now())
}

// EnqueueAdvanceAt enqueues a sequence:advance task to run at time t (used by
// launch stagger and the lazy chain's next-step scheduling).
func (c *Client) EnqueueAdvanceAt(ctx context.Context, enrollmentID, workspaceID string, t time.Time) error {
	return c.enqueueAdvance(ctx, enrollmentID, workspaceID, t, asynq.ProcessAt(t))
}

// EnqueueAdvanceIn enqueues a sequence:advance task after delay d (used by the
// cap-exceeded backoff).
func (c *Client) EnqueueAdvanceIn(ctx context.Context, enrollmentID, workspaceID string, d time.Duration) error {
	return c.enqueueAdvance(ctx, enrollmentID, workspaceID, time.Now().Add(d), asynq.ProcessIn(d))
}

// enqueueAdvance submits a sequence:advance keyed on (enrollment, due-second) so
// duplicate enqueues for the SAME due instant (a sweeper re-enqueue racing the
// lazy chain) dedup, while a genuinely new advance (a later due time) still
// enqueues.
//
// Invariant: the TaskID uses whole-second granularity (due.Unix()), so two
// advances whose due times land in the same second COLLAPSE to one task. That is
// safe because (a) the ClaimStepSend row-claim — not this key — is the
// delivery-idempotency guarantee, so a collapsed duplicate only saves wasted
// work, never correctness; and (b) all advance due times are second-granular
// (step delays are whole seconds), so a legitimately-distinct next advance never
// shares a second with the one that scheduled it. The claim in AdvanceHandler
// remains the correctness guarantee; this only cuts wasted concurrent advances.
// due is the scheduled processing time.
func (c *Client) enqueueAdvance(ctx context.Context, enrollmentID, workspaceID string, due time.Time, opts ...asynq.Option) error {
	b, err := json.Marshal(AdvancePayload{EnrollmentID: enrollmentID, WorkspaceID: workspaceID})
	if err != nil {
		return err
	}
	taskID := fmt.Sprintf("advance:%s:%d", enrollmentID, due.Unix())
	opts = append(opts,
		asynq.TaskID(taskID),
		asynq.MaxRetry(sendMaxRetry),
		asynq.Timeout(sendTimeout),
		asynq.Retention(taskRetention),
	)
	return c.enqueueRouted(ctx, TaskSequenceAdvance, b, opts...)
}

// EnqueueDeliverabilityEvaluate schedules a breaker evaluation for one campaign.
// Keyed on (campaign, dedup bucket) so the many sends finalising inside one
// window collapse to a single evaluation; a TaskID conflict is success (see
// enqueue), so a collapsed duplicate is not an error the caller has to handle.
func (c *Client) EnqueueDeliverabilityEvaluate(ctx context.Context, campaignID, workspaceID string) error {
	b, err := json.Marshal(DeliverabilityEvaluatePayload{CampaignID: campaignID, WorkspaceID: workspaceID})
	if err != nil {
		return err
	}
	bucket := time.Now().Add(evaluateDedupWindow).Truncate(evaluateDedupWindow)
	return c.enqueueRouted(ctx, TaskDeliverabilityEvaluate, b,
		asynq.TaskID(fmt.Sprintf("deliverability:%s:%d", campaignID, bucket.Unix())),
		asynq.ProcessAt(bucket),
		asynq.MaxRetry(sendMaxRetry),
		asynq.Timeout(sendTimeout),
		asynq.Retention(taskRetention),
	)
}

// testSendTaskID keys a testsend:send on (campaign, step, mailbox,
// due-second) so a double-submitted form within the same second collapses to
// one send, while a genuinely distinct test-send a moment later still
// enqueues. Unlike enqueueAdvance, there is no downstream row-claim
// backing this as a correctness guarantee (a test-send writes no sends row) --
// the per-workspace rate limiter (campaign.Service.TestSend) is the abuse
// guard; this key only cuts an accidental double-click. The recipient address
// is deliberately NOT part of the key (test-send operator input, kept out of
// a Redis-visible identifier). now is passed in (rather than read from
// time.Now() here) so the dedup window is deterministic under test, mirroring
// warmupTickTaskID.
func testSendTaskID(campaignID, stepID, mailboxID string, now time.Time) string {
	return fmt.Sprintf("testsend:%s:%s:%s:%d", campaignID, stepID, mailboxID, now.Unix())
}

// EnqueueTestSend enqueues a testsend:send task for immediate processing.
func (c *Client) EnqueueTestSend(ctx context.Context, campaignID, stepID, mailboxID, to, workspaceID string) error {
	b, err := json.Marshal(TestSendPayload{
		CampaignID: campaignID, StepID: stepID, MailboxID: mailboxID, To: to, WorkspaceID: workspaceID,
	})
	if err != nil {
		return err
	}
	return c.enqueueRouted(ctx, TaskTestSend, b,
		asynq.TaskID(testSendTaskID(campaignID, stepID, mailboxID, time.Now())),
		asynq.MaxRetry(sendMaxRetry),
		asynq.Retention(taskRetention),
	)
}

// EnqueuePendingInboxReply schedules a manual reply for delivery at sendAfter.
//
// asynq.ProcessAt, not ProcessIn: the moment is already absolute (the row's
// send_after), and converting it to a duration here would introduce a skew
// between what the row says and when the task fires.
//
// The task id is the pending-reply id, which makes the enqueue idempotent for
// free — a retried schedule of the SAME row dedups rather than producing two
// deliveries. asynq's TaskID conflict is swallowed as success by c.enqueue.
//
// Note this task cannot be cancelled through the queue. The only asynq
// Inspector in this codebase is the deliberately READ-ONLY one behind the
// queue-depth metric (see inspect.go) — nothing here mutates a queued task.
// Cancellation is a DB status flip that the handler re-reads on pickup; the
// task still fires and no-ops. See
// migrations/000066_inbox_pending_reply.up.sql for why.
func (c *Client) EnqueuePendingInboxReply(ctx context.Context, pendingID, workspaceID string, sendAfter time.Time) error {
	b, err := json.Marshal(InboxPendingReplySendPayload{
		PendingID: pendingID, WorkspaceID: workspaceID,
	})
	if err != nil {
		return err
	}
	return c.enqueueRouted(ctx, TaskInboxPendingReplySend, b,
		asynq.TaskID("inboxpending:"+pendingID),
		asynq.ProcessAt(sendAfter),
		asynq.MaxRetry(sendMaxRetry),
		asynq.Timeout(sendTimeout),
		asynq.Retention(taskRetention),
	)
}

// EnqueuePendingInboxCompose schedules a composed email for delivery at
// sendAfter. See EnqueuePendingInboxReply for the ProcessAt/TaskID reasoning —
// this is the same design over the compose table.
func (c *Client) EnqueuePendingInboxCompose(ctx context.Context, pendingID, workspaceID string, sendAfter time.Time) error {
	b, err := json.Marshal(InboxPendingComposeSendPayload{
		PendingID: pendingID, WorkspaceID: workspaceID,
	})
	if err != nil {
		return err
	}
	return c.enqueueRouted(ctx, TaskInboxPendingComposeSend, b,
		asynq.TaskID("inboxcompose:"+pendingID),
		asynq.ProcessAt(sendAfter),
		asynq.MaxRetry(sendMaxRetry),
		asynq.Timeout(sendTimeout),
		asynq.Retention(taskRetention),
	)
}

// strandedRescueTaskID keys ONE stranded-send rescue within one sweep interval.
//
// IT MUST NOT BE THE ID THE ORIGINAL ENQUEUE USED, and that is the single detail
// the whole sweep turns on. asynq's uniqueness check is `EXISTS` on the task's
// own Redis key (rdb.enqueueCmd), and that key survives the run: a task that
// completed is held for its asynq.Retention (taskRetention, 24h) and a task that
// exhausted its retries is held in the archive for far longer. Re-enqueuing
// "inboxpending:<id>" for a row whose task has already run would therefore
// conflict — and c.enqueue treats a TaskID conflict as SUCCESS (deliberately, so
// a sweeper racing a live task is a no-op rather than an error). The sweep would
// report a rescue, enqueue nothing, and the reply would still never leave. A
// sweep that silently does nothing is worse than no sweep, because the metric
// and the ledger both say it ran.
//
// The bucket is what keeps it a dedup key rather than a licence to pile up
// tasks: every replica's sweep in the same interval computes the same id, so
// their enqueues collapse to one, while the next interval mints a distinct id
// and can try again. Bucketing (rather than a bare per-row key) is also what
// stops a completed rescue from suppressing every later one — see
// inboxPollTaskID, which makes the same trade for the same reason.
//
// Collapsing is safe here for the reason it is safe everywhere else in this
// file: the ROW's guarded claim, not this key, is the delivery-idempotency
// guarantee. A dropped duplicate only saves work.
func strandedRescueTaskID(prefix, pendingID string, now time.Time) string {
	return fmt.Sprintf("%s:%s:%d", prefix, pendingID, now.Truncate(pendingSendSweepInterval).Unix())
}

// EnqueueStrandedPendingInboxReply re-drives one stranded manual reply: the
// SAME task type, the same payload shape and the same retry budget the original
// schedule used, with no ProcessAt (the row is already overdue by definition)
// and a rescue-scoped TaskID.
//
// It is a separate helper rather than a flag on EnqueuePendingInboxReply because
// the two differ in exactly the properties a flag would hide — the dedup key and
// the delay — and because the sweep must not be able to reschedule a row into
// the future by accident.
func (c *Client) EnqueueStrandedPendingInboxReply(ctx context.Context, pendingID, workspaceID string) error {
	b, err := json.Marshal(InboxPendingReplySendPayload{
		PendingID: pendingID, WorkspaceID: workspaceID,
	})
	if err != nil {
		return err
	}
	return c.enqueueRouted(ctx, TaskInboxPendingReplySend, b,
		asynq.TaskID(strandedRescueTaskID("inboxpending-rescue", pendingID, time.Now())),
		asynq.MaxRetry(sendMaxRetry),
		asynq.Timeout(sendTimeout),
		asynq.Retention(taskRetention),
	)
}

// EnqueueStrandedPendingInboxCompose is the compose half of the rescue. Same
// shape over the compose table's task type; see the reply helper above.
func (c *Client) EnqueueStrandedPendingInboxCompose(ctx context.Context, pendingID, workspaceID string) error {
	b, err := json.Marshal(InboxPendingComposeSendPayload{
		PendingID: pendingID, WorkspaceID: workspaceID,
	})
	if err != nil {
		return err
	}
	return c.enqueueRouted(ctx, TaskInboxPendingComposeSend, b,
		asynq.TaskID(strandedRescueTaskID("inboxcompose-rescue", pendingID, time.Now())),
		asynq.MaxRetry(sendMaxRetry),
		asynq.Timeout(sendTimeout),
		asynq.Retention(taskRetention),
	)
}

// inboxPollTaskID is the dedup key for one mailbox's poll within one sweep
// interval: every replica's sweep in the same interval computes the same bucket,
// so their enqueues collapse to a single task. Split out of EnqueueInboxPoll (as
// warmupTickTaskID and testSendTaskID are) so the bucketing is unit-testable
// without a live Redis — it is the whole correctness argument for the dedup.
func inboxPollTaskID(mailboxID string, now time.Time) string {
	return fmt.Sprintf("inbox-poll:%s:%d", mailboxID, now.Truncate(inboxSweepInterval).Unix())
}

// EnqueueInboxPoll enqueues an inbox:poll task for immediate processing, keyed
// on (mailbox, sweep-interval bucket) so concurrent fan-outs for the same
// mailbox in the same interval collapse to one poll, and routed to dest — the
// polled mailbox's assigned worker queue, so the mailbox authenticates to its
// provider from one egress IP ("" = no assignment, so affinityQueue falls back
// to the shared send queue rather than asynq's unconsumed "default"). dest is
// derived server-side from the mailbox→worker assignment, never from client
// input (§17.8).
//
// Affinity applies here for the SAME reason it applies to warmup:tick, and with
// more force. A poll is a provider authentication — an IMAP LOGIN, or an
// OAuth-bearing call to the fixed Gmail/Graph host — and it happens once per
// mailbox per inboxSweepInterval, which is roughly an order of magnitude more
// often than that mailbox's campaign sends. Left on the shared queue it was the
// largest source of novel-source-IP sign-ins in the system, which is precisely
// what provokes the sign-in challenges and per-IP throttles the fleet exists to
// avoid. Nothing about the RECIPIENT is involved: see WorkerQueue.
//
// Every worker process runs its own scheduler, so inbox:sweep fires once per
// replica per interval. Without a TaskID that meant N replicas each opening a
// real IMAP connection per mailbox per interval — a provider rate-limit problem
// and pure waste, since the extra polls read the same messages. Bucketing by
// interval rather than using a bare mailbox key keeps the dedup window bounded:
// a poll that has already run does not suppress the next interval's.
//
// Collapsing is safe for the same reason it is in enqueueAdvance: nothing here
// is the correctness guarantee. Poll processing is idempotent per message (the
// poller records what it has seen), so a dropped duplicate only saves work.
//
// Careful with the two windows: asynq keeps a finished task's id reserved for
// its Retention, so an id stays blocked well after it ran. That is harmless only
// because each bucket mints a DISTINCT id — the block expires long before the
// same id could recur. Shortening the bucket below taskRetention (or dropping
// the bucket for a bare per-mailbox key) would make a completed poll suppress
// every later one and silently stop polling. TestInboxPollTaskID pins both
// halves: same interval collapses, next interval does not.
// Retries are deliberately bounded lower than a send's — a poll that fails is
// re-fanned-out by the next sweep a few minutes later, so exhausting retries
// costs one interval of latency, not a lost message.
func (c *Client) EnqueueInboxPoll(ctx context.Context, mailboxID, workspaceID, dest string) error {
	b, err := json.Marshal(InboxPollPayload{MailboxID: mailboxID, WorkspaceID: workspaceID})
	if err != nil {
		return err
	}
	return c.enqueueAffinity(ctx, TaskInboxPoll, dest, b,
		asynq.TaskID(inboxPollTaskID(mailboxID, time.Now())),
		asynq.MaxRetry(pollMaxRetry),
		asynq.Timeout(pollTimeout),
		asynq.Retention(taskRetention),
	)
}

// EnqueueWebhookDeliver enqueues a webhook:deliver task for immediate processing.
func (c *Client) EnqueueWebhookDeliver(ctx context.Context, deliveryID, workspaceID string) error {
	return c.enqueueWebhookDeliver(ctx, deliveryID, workspaceID, time.Now())
}

// EnqueueWebhookDeliverIn enqueues a webhook:deliver task after delay d — the
// handler's own backoff-retry path.
func (c *Client) EnqueueWebhookDeliverIn(ctx context.Context, deliveryID, workspaceID string, d time.Duration) error {
	return c.enqueueWebhookDeliver(ctx, deliveryID, workspaceID, time.Now().Add(d), asynq.ProcessIn(d))
}

func (c *Client) enqueueWebhookDeliver(ctx context.Context, deliveryID, workspaceID string, due time.Time, opts ...asynq.Option) error {
	b, err := json.Marshal(WebhookDeliverPayload{DeliveryID: deliveryID, WorkspaceID: workspaceID})
	if err != nil {
		return err
	}
	opts = append(opts,
		asynq.TaskID(webhookDeliverTaskID(deliveryID, due)),
		asynq.MaxRetry(webhookMaxRetry),
		asynq.Timeout(webhookDeliverTimeout),
		asynq.Retention(taskRetention),
	)
	return c.enqueueRouted(ctx, TaskWebhookDeliver, b, opts...)
}

// Publish makes *Client satisfy bus.Dispatcher, so the new warmup and routing
// enqueue paths can depend on the transport seam while sharing this Client's
// live asynq connection. It is a thin adapter over redisbus — the same
// Job/Options -> asynq translation and TaskID-conflict-as-success dedup rule.
//
// The existing typed helpers (EnqueueSend/EnqueueAdvance/…) are intentionally
// left untouched; migrating those call sites onto the seam is a documented
// follow-up, not part of this change.
func (c *Client) Publish(ctx context.Context, j bus.Job, o bus.Options) error {
	return redisbus.NewDispatcher(c.inner).Publish(ctx, j, o)
}

// Compile-time proof the adapter is complete.
var _ bus.Dispatcher = (*Client)(nil)

func (c *Client) Close() error { return c.inner.Close() }

// NewServer builds an asynq processing server. Concurrency defaults to 10
// when concurrency <= 0. queues is the ordered set of queues to consume —
// callers derive it per worker ROLE (cmd/worker.resolveWorkerQueues /
// internal/worker.QueuesFor: control consumes only QueueControl; send
// consumes its own per-IP "w:<id>" queue plus QueueSend and the transitional
// QueueDefault), not a fixed pair every role shares; an empty list leaves
// asynq on its built-in {"default":1}. The provided *slog.Logger is adapted to
// asynq's Logger interface so worker log lines flow through the same
// structured sink as the rest of the app.
//
// recorder receives tasks that have exhausted their retries
// (DeadLetterErrorHandler); nil disables capture, which degrades to asynq's own
// invisible archive rather than to a failing worker.
func NewServer(redisAddr string, logger *slog.Logger, concurrency int, queues []string, recorder DeadLetterRecorder) *asynq.Server {
	if concurrency <= 0 {
		concurrency = 10
	}
	cfg := asynq.Config{
		Concurrency: concurrency,
		Logger:      newAsynqLogger(logger),
		// The dead-letter capture hook. asynq calls this for EVERY failed
		// attempt; the handler itself filters down to the terminal one, which
		// is why exhaustion detection lives in one place rather than in each
		// task handler (see DeadLetterErrorHandler's doc for the full reasoning).
		ErrorHandler: DeadLetterErrorHandler(recorder, logger),
	}
	if qmap := queuePriorities(queues); len(qmap) > 0 {
		cfg.Queues = qmap
	}
	return asynq.NewServer(asynqConnOpt(redisAddr), cfg)
}

// queuePriorities maps an ordered queue list to asynq's weighted-priority map.
// Earlier queues get proportionally higher weight so a worker prefers its own
// per-IP queue over the shared default without starving it (asynq is weighted,
// not strict). Duplicates and empty names are ignored. An empty result leaves
// the caller on asynq's default {"default":1}.
func queuePriorities(queues []string) map[string]int {
	m := make(map[string]int, len(queues))
	weight := len(queues)
	for _, q := range queues {
		if q == "" {
			continue
		}
		if _, ok := m[q]; ok {
			continue
		}
		m[q] = weight
		weight--
	}
	return m
}

// NewMux returns an empty task router for worker handlers to register on.
func NewMux() *asynq.ServeMux { return asynq.NewServeMux() }

// NewScheduler builds an asynq scheduler bound to the same Redis instance
// the worker consumes from. Registered periodic tasks are enqueued on
// their cron interval; the worker picks them up like any other task.
func NewScheduler(redisAddr string, logger *slog.Logger) *asynq.Scheduler {
	return asynq.NewScheduler(
		asynqConnOpt(redisAddr),
		&asynq.SchedulerOpts{Logger: newAsynqLogger(logger)},
	)
}

// registerControlSweep is the shared shape behind every periodic registration
// below: every sweep is control-plane work — a fan-out or a cross-tenant scan —
// so every sweep lands on QueueControl, never on the send role. The queue comes
// from taskQueues like every other producer's rather than being named here, so
// a sweep's scheduled route and a replay of that same sweep cannot disagree.
// Split out so (a) cronspec and task type are the only thing that differs per
// sweep, and (b) the routing decision is testable without a live Redis: sch
// only needs to satisfy redisbus.Registrar — the same seam redisbus.Scheduler
// already depends on — which *asynq.Scheduler already does, and Register merely
// stores the cron entry (it does not touch Redis until the entry fires), so a
// fake registrar can assert on the options without one.
func registerControlSweep(sch redisbus.Registrar, cronspec, taskType string) error {
	q, err := routeTaskType(taskType)
	if err != nil {
		return err
	}
	if _, err := sch.Register(cronspec, asynq.NewTask(taskType, nil), asynq.Timeout(sweepTimeout), asynq.Queue(q)); err != nil {
		return err
	}
	return nil
}

// RegisterSweepEnrollments registers the periodic due-enrollment reconcile.
// Runs every 5 minutes to match the enrollment sweeper's "> 5 minutes" window.
func RegisterSweepEnrollments(sch *asynq.Scheduler) error {
	return registerControlSweep(sch, "@every 5m", TaskSweepEnrollments)
}

// RegisterInboxSweep registers the periodic inbox:sweep. Runs every
// inboxSweepInterval to fan out inbox:poll tasks for every active mailbox.
func RegisterInboxSweep(sch *asynq.Scheduler) error {
	return registerControlSweep(sch, "@every "+inboxSweepInterval.String(), TaskInboxSweep)
}

// RegisterInboxPendingSendSweep registers the periodic stranded-manual-send
// reconcile. Runs every pendingSendSweepInterval — see that constant for why
// this one ticks faster than the rest.
func RegisterInboxPendingSendSweep(sch *asynq.Scheduler) error {
	return registerControlSweep(sch, "@every "+pendingSendSweepInterval.String(), TaskInboxPendingSendSweep)
}

// RegisterWarmupSweep registers the periodic warmup:sweep. Runs every 5 minutes
// to fan out a warmup:tick for every due participant and recompute health.
func RegisterWarmupSweep(sch *asynq.Scheduler) error {
	return registerControlSweep(sch, "@every 5m", TaskWarmupSweep)
}

// RegisterMaintenanceCleanup registers the low-frequency retention pass. The
// handler is idempotent, so scheduler restarts and retries are safe.
func RegisterMaintenanceCleanup(sch *asynq.Scheduler) error {
	return registerControlSweep(sch, "@every 24h", TaskMaintenanceCleanup)
}

// RegisterDomainAuthSweep registers the periodic domain-authentication sweep.
// It ticks hourly against a 24-hour staleness window: the tick rate is what
// bounds how long a NEWLY connected mailbox's domain sits unchecked, and how
// soon a domain whose lookup failed is retried, while the window is what stops
// it from re-resolving the same records twelve times a day.
func RegisterDomainAuthSweep(sch *asynq.Scheduler) error {
	return registerControlSweep(sch, "@every 1h", TaskDomainAuthSweep)
}

// RegisterRecipientESPSweep registers the periodic recipient-domain ESP sweep.
// It ticks every 5 minutes against a 30-day staleness window, and the two rates
// answer different questions: the WINDOW is how often a domain's MX records are
// re-read (rarely, because they rarely change), while the TICK is how quickly a
// NEWLY enrolled contact's domain gets classified before its first send. Each
// tick is bounded by the fan-out query's LIMIT, so a large import costs more
// ticks rather than one unbounded DNS run.
//
// A slow tick can overlap the next one, which is harmless: the classification
// write is an idempotent upsert and eviction is a range delete, so two ticks
// racing over the same domain converge on the same row.
func RegisterRecipientESPSweep(sch *asynq.Scheduler) error {
	return registerControlSweep(sch, "@every 5m", TaskRecipientESPSweep)
}

// RegisterFleetRotate registers the periodic rotation pass.
//
// Every 5 minutes, which is the rate at which the evidence it reads can change:
// a worker flushes its accumulated provider verdicts every 5m (cmd/worker's
// workerSignalFlushInterval), so a provider that starts refusing an egress IP
// becomes visible one flush later and is acted on one tick after that. Ticking
// faster would re-read a picture that had not moved; slower would leave mailboxes
// failing on a blocked address for no reason.
//
// A tick is cheap when there is nothing to do and bounded when there is: a fleet
// with at most one live worker costs one COUNT and stops, and every other tick
// scans a fixed number of assignments and moves at most fleetrotate.Policy's
// budget. Overlapping ticks are harmless — every move is guarded on the worker
// it was decided from, so the loser of a race matches zero rows.
//
// The RATE is also how finely the opportunistic scan samples the fleet. A tick
// reads a fixed-size window from a rotating cursor that crosses the whole
// mailbox-id space in a day (coreapi/inprocess.rotationScanSweep), so slowing
// this schedule does not merely delay each pass — it widens the arc between
// consecutive windows, and past a point leaves settled assignments unread for a
// whole sweep. Read that constant's doc before changing this one.
func RegisterFleetRotate(sch *asynq.Scheduler) error {
	return registerControlSweep(sch, "@every 5m", TaskFleetRotate)
}

// asynqLogger adapts *slog.Logger to asynq.Logger.
type asynqLogger struct{ l *slog.Logger }

func newAsynqLogger(l *slog.Logger) *asynqLogger { return &asynqLogger{l: l} }

func (a *asynqLogger) Debug(args ...any) { a.l.Debug("asynq", "msg", args) }
func (a *asynqLogger) Info(args ...any)  { a.l.Info("asynq", "msg", args) }
func (a *asynqLogger) Warn(args ...any)  { a.l.Warn("asynq", "msg", args) }
func (a *asynqLogger) Error(args ...any) { a.l.Error("asynq", "msg", args) }
func (a *asynqLogger) Fatal(args ...any) { a.l.Error("asynq-fatal", "msg", args) }
