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
)

// Delivery-idempotency defense in depth (the claim in the send/advance handlers
// is the real correctness guarantee; these just cut wasted retries/concurrency):
//   - sendMaxRetry bounds how many times a permanently-failing task cycles
//     (asynq's default is 25).
//   - taskRetention keeps a finished task briefly for post-run inspection.
const (
	sendMaxRetry  = 5
	taskRetention = 24 * time.Hour
)

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

// TaskInboxReplySend sends one manual reply queued from the unified inbox
// (POST /inbox/threads/{id}/reply). One task per reply.
const TaskInboxReplySend = "inbox:reply_send"

// InboxReplySendPayload is the body of an inbox:reply_send task. WorkspaceID
// travels alongside ThreadID so the worker can pin workspace_id in its
// coreapi lookups (defense in depth on the unguessable thread UUID).
// BodyText is the operator's free-text reply content — never logged by the
// handler that consumes it, like every other piece of business
// correspondence in this domain.
type InboxReplySendPayload struct {
	ThreadID    string `json:"thread_id"`
	BodyText    string `json:"body_text"`
	WorkspaceID string `json:"workspace_id"`
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

// Client enqueues tasks onto Redis.
type Client struct {
	inner *asynq.Client
}

func NewClient(redisAddr string) *Client {
	return &Client{inner: asynq.NewClient(asynq.RedisClientOpt{Addr: redisAddr})}
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
// warmup and campaign mail egress from one IP; "" = shared default queue). It
// goes through the transport seam (bus.Dispatcher): Key→TaskID dedup,
// Dest→Queue routing, At→ProcessAt delayed delivery. dest is always derived
// server-side from the mailbox→worker assignment, never from client input
// (§17.8).
func (c *Client) EnqueueWarmupTickAt(mailboxID, workspaceID string, t time.Time, dest string) error {
	b, err := json.Marshal(WarmupTickPayload{MailboxID: mailboxID, WorkspaceID: workspaceID})
	if err != nil {
		return err
	}
	return c.Publish(context.Background(), bus.Job{
		Kind:    TaskWarmupTick,
		Payload: b,
		Key:     warmupTickTaskID(mailboxID, t),
		Dest:    dest,
	}, bus.Options{
		At:       t,
		MaxRetry: sendMaxRetry,
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
// egress routing applies — it uses the shared default queue (Dest "").
func (c *Client) EnqueueWarmupEngageIn(receiptID, workspaceID string, d time.Duration) error {
	b, err := json.Marshal(WarmupEngagePayload{ReceiptID: receiptID, WorkspaceID: workspaceID})
	if err != nil {
		return err
	}
	return c.Publish(context.Background(), bus.Job{
		Kind:    TaskWarmupEngage,
		Payload: b,
		Key:     warmupEngageTaskID(receiptID),
	}, bus.Options{
		In:       d,
		MaxRetry: sendMaxRetry,
	})
}

// enqueue submits a task and treats an asynq TaskID conflict as success: a
// duplicate enqueue of an already-pending task (sweeper re-enqueue racing a live
// task) is a deliberate no-op, not an error.
func (c *Client) enqueue(t *asynq.Task, opts ...asynq.Option) error {
	if _, err := c.inner.Enqueue(t, opts...); err != nil {
		if errors.Is(err, asynq.ErrTaskIDConflict) {
			return nil
		}
		return err
	}
	return nil
}

// EnqueueAdvance enqueues a sequence:advance task for immediate processing.
func (c *Client) EnqueueAdvance(enrollmentID, workspaceID string) error {
	return c.enqueueAdvance(enrollmentID, workspaceID, time.Now())
}

// EnqueueAdvanceAt enqueues a sequence:advance task to run at time t (used by
// launch stagger and the lazy chain's next-step scheduling).
func (c *Client) EnqueueAdvanceAt(enrollmentID, workspaceID string, t time.Time) error {
	return c.enqueueAdvance(enrollmentID, workspaceID, t, asynq.ProcessAt(t))
}

// EnqueueAdvanceIn enqueues a sequence:advance task after delay d (used by the
// cap-exceeded backoff).
func (c *Client) EnqueueAdvanceIn(enrollmentID, workspaceID string, d time.Duration) error {
	return c.enqueueAdvance(enrollmentID, workspaceID, time.Now().Add(d), asynq.ProcessIn(d))
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
func (c *Client) enqueueAdvance(enrollmentID, workspaceID string, due time.Time, opts ...asynq.Option) error {
	b, err := json.Marshal(AdvancePayload{EnrollmentID: enrollmentID, WorkspaceID: workspaceID})
	if err != nil {
		return err
	}
	taskID := fmt.Sprintf("advance:%s:%d", enrollmentID, due.Unix())
	opts = append(opts,
		asynq.TaskID(taskID),
		asynq.MaxRetry(sendMaxRetry),
		asynq.Retention(taskRetention),
	)
	return c.enqueue(asynq.NewTask(TaskSequenceAdvance, b), opts...)
}

// EnqueueDeliverabilityEvaluate schedules a breaker evaluation for one campaign.
// Keyed on (campaign, dedup bucket) so the many sends finalising inside one
// window collapse to a single evaluation; a TaskID conflict is success (see
// enqueue), so a collapsed duplicate is not an error the caller has to handle.
func (c *Client) EnqueueDeliverabilityEvaluate(campaignID, workspaceID string) error {
	b, err := json.Marshal(DeliverabilityEvaluatePayload{CampaignID: campaignID, WorkspaceID: workspaceID})
	if err != nil {
		return err
	}
	bucket := time.Now().Add(evaluateDedupWindow).Truncate(evaluateDedupWindow)
	return c.enqueue(asynq.NewTask(TaskDeliverabilityEvaluate, b),
		asynq.TaskID(fmt.Sprintf("deliverability:%s:%d", campaignID, bucket.Unix())),
		asynq.ProcessAt(bucket),
		asynq.MaxRetry(sendMaxRetry),
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
func (c *Client) EnqueueTestSend(campaignID, stepID, mailboxID, to, workspaceID string) error {
	b, err := json.Marshal(TestSendPayload{
		CampaignID: campaignID, StepID: stepID, MailboxID: mailboxID, To: to, WorkspaceID: workspaceID,
	})
	if err != nil {
		return err
	}
	return c.enqueue(asynq.NewTask(TaskTestSend, b),
		asynq.TaskID(testSendTaskID(campaignID, stepID, mailboxID, time.Now())),
		asynq.MaxRetry(sendMaxRetry),
		asynq.Retention(taskRetention),
	)
}

// inboxReplySendTaskID keys an inbox:reply_send on (thread, due-second),
// mirroring testSendTaskID's dedup discipline: a double-submitted "send
// reply" click within the same second collapses to one send, while a
// genuinely distinct reply a moment later still enqueues. There is no
// downstream row-claim backing this as a correctness guarantee (unlike
// enqueueAdvance) — this key only cuts an accidental double-click. The reply
// body is deliberately NOT part of the key (free-text business
// correspondence, kept out of a Redis-visible identifier).
func inboxReplySendTaskID(threadID string, now time.Time) string {
	return fmt.Sprintf("inboxreply:%s:%d", threadID, now.Unix())
}

// EnqueueInboxReplySend enqueues an inbox:reply_send task for immediate
// processing.
func (c *Client) EnqueueInboxReplySend(threadID, bodyText, workspaceID string) error {
	b, err := json.Marshal(InboxReplySendPayload{ThreadID: threadID, BodyText: bodyText, WorkspaceID: workspaceID})
	if err != nil {
		return err
	}
	return c.enqueue(asynq.NewTask(TaskInboxReplySend, b),
		asynq.TaskID(inboxReplySendTaskID(threadID, time.Now())),
		asynq.MaxRetry(sendMaxRetry),
		asynq.Retention(taskRetention),
	)
}

// EnqueueInboxPoll enqueues an inbox:poll task for immediate processing.
func (c *Client) EnqueueInboxPoll(mailboxID, workspaceID string) error {
	b, err := json.Marshal(InboxPollPayload{MailboxID: mailboxID, WorkspaceID: workspaceID})
	if err != nil {
		return err
	}
	_, err = c.inner.Enqueue(asynq.NewTask(TaskInboxPoll, b))
	return err
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
// when concurrency <= 0. queues is the ordered set of queues to consume (spec
// §15: a worker serves its own per-IP "w:<id>" queue plus "default"); an empty
// list leaves asynq on its built-in {"default":1}. The provided *slog.Logger is
// adapted to asynq's Logger interface so worker log lines flow through the same
// structured sink as the rest of the app.
func NewServer(redisAddr string, logger *slog.Logger, concurrency int, queues []string) *asynq.Server {
	if concurrency <= 0 {
		concurrency = 10
	}
	cfg := asynq.Config{
		Concurrency: concurrency,
		Logger:      newAsynqLogger(logger),
	}
	if qmap := queuePriorities(queues); len(qmap) > 0 {
		cfg.Queues = qmap
	}
	return asynq.NewServer(asynq.RedisClientOpt{Addr: redisAddr}, cfg)
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
		asynq.RedisClientOpt{Addr: redisAddr},
		&asynq.SchedulerOpts{Logger: newAsynqLogger(logger)},
	)
}

// RegisterSweepEnrollments registers the periodic due-enrollment reconcile.
// Runs every 5 minutes to match the enrollment sweeper's "> 5 minutes" window.
func RegisterSweepEnrollments(sch *asynq.Scheduler) error {
	_, err := sch.Register("@every 5m", asynq.NewTask(TaskSweepEnrollments, nil))
	return err
}

// RegisterInboxSweep registers the periodic inbox:sweep. Runs every 3
// minutes to fan out inbox:poll tasks for every active mailbox.
func RegisterInboxSweep(sch *asynq.Scheduler) error {
	_, err := sch.Register("@every 3m", asynq.NewTask(TaskInboxSweep, nil))
	return err
}

// RegisterWarmupSweep registers the periodic warmup:sweep. Runs every 5 minutes
// to fan out a warmup:tick for every due participant and recompute health.
func RegisterWarmupSweep(sch *asynq.Scheduler) error {
	_, err := sch.Register("@every 5m", asynq.NewTask(TaskWarmupSweep, nil))
	return err
}

// RegisterMaintenanceCleanup registers the low-frequency retention pass. The
// handler is idempotent, so scheduler restarts and retries are safe.
func RegisterMaintenanceCleanup(sch *asynq.Scheduler) error {
	_, err := sch.Register("@every 24h", asynq.NewTask(TaskMaintenanceCleanup, nil))
	return err
}

// RegisterDomainAuthSweep registers the periodic domain-authentication sweep.
// It ticks hourly against a 24-hour staleness window: the tick rate is what
// bounds how long a NEWLY connected mailbox's domain sits unchecked, and how
// soon a domain whose lookup failed is retried, while the window is what stops
// it from re-resolving the same records twelve times a day.
func RegisterDomainAuthSweep(sch *asynq.Scheduler) error {
	_, err := sch.Register("@every 1h", asynq.NewTask(TaskDomainAuthSweep, nil))
	return err
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
	_, err := sch.Register("@every 5m", asynq.NewTask(TaskRecipientESPSweep, nil))
	return err
}

// asynqLogger adapts *slog.Logger to asynq.Logger.
type asynqLogger struct{ l *slog.Logger }

func newAsynqLogger(l *slog.Logger) *asynqLogger { return &asynqLogger{l: l} }

func (a *asynqLogger) Debug(args ...any) { a.l.Debug("asynq", "msg", args) }
func (a *asynqLogger) Info(args ...any)  { a.l.Info("asynq", "msg", args) }
func (a *asynqLogger) Warn(args ...any)  { a.l.Warn("asynq", "msg", args) }
func (a *asynqLogger) Error(args ...any) { a.l.Error("asynq", "msg", args) }
func (a *asynqLogger) Fatal(args ...any) { a.l.Error("asynq-fatal", "msg", args) }
