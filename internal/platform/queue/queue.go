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

// TaskInboxReplySend sends one manual reply queued from the unified inbox
// (POST /inbox/threads/{id}/reply). One task per reply.
const TaskInboxReplySend = "inbox:reply_send"

// TaskInboxPendingReplySend delivers a DEFERRED manual reply — one whose row in
// inbox_pending_replies carries the body and the authoritative send_after.
// Distinct from TaskInboxReplySend, which carries the body in its own payload
// and has no row to cancel: this task is only a POINTER to a row, so the
// operator can undo the send by updating that row while the task is still
// pending in the queue.
const TaskInboxPendingReplySend = "inbox:pending_reply_send"

// TaskInboxPendingComposeSend delivers a deferred COMPOSED email — a new
// message rather than a reply, whose recipients and subject are its own rather
// than derived from a thread. Same pointer-to-a-row design as
// TaskInboxPendingReplySend, and cancellable the same way.
const TaskInboxPendingComposeSend = "inbox:pending_compose_send"

// InboxReplySendPayload is the body of an inbox:reply_send task. WorkspaceID
// travels alongside ThreadID so the worker can pin workspace_id in its
// coreapi lookups (defense in depth on the unguessable thread UUID).
// BodyText is the operator's free-text reply content — never logged by the
// handler that consumes it, like every other piece of business
// correspondence in this domain. TaskID carries the SAME id set as this
// task's asynq.TaskID (below) — stable across retries AND a crash-induced
// lease redelivery of this exact task, which is what makes it usable as the
// claim-before-send key (internal/worker/inbox.ReplySendHandler). Carried in
// the payload rather than read from the handler's context via
// asynq.GetTaskID (which would work identically in production but is opaque
// to construct in a unit test that builds a task directly) — this way the
// SAME value is trivially assertable in tests.
// InboxPendingReplySendPayload names a row in inbox_pending_replies. It
// carries NO body: the row is the single source of truth for what to send and
// whether to send it at all, so a payload copy could go stale the moment the
// operator cancels.
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
		Timeout:  sendTimeout,
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
		asynq.Timeout(sendTimeout),
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

// EnqueuePendingInboxReply schedules a deferred manual reply for delivery at
// sendAfter.
//
// asynq.ProcessAt, not ProcessIn: the moment is already absolute (the row's
// send_after), and converting it to a duration here would introduce a skew
// between what the row says and when the task fires.
//
// The task id is the pending-reply id, which makes the enqueue idempotent for
// free — a retried schedule of the SAME row dedups rather than producing two
// deliveries. asynq's TaskID conflict is swallowed as success by c.enqueue.
//
// Note this task cannot be cancelled through the queue (asynq's Inspector is
// not used in this codebase). Cancellation is a DB status flip that the handler
// re-reads on pickup; the task still fires and no-ops. See
// migrations/000066_inbox_pending_reply.up.sql for why.
func (c *Client) EnqueuePendingInboxReply(pendingID, workspaceID string, sendAfter time.Time) error {
	b, err := json.Marshal(InboxPendingReplySendPayload{
		PendingID: pendingID, WorkspaceID: workspaceID,
	})
	if err != nil {
		return err
	}
	return c.enqueue(asynq.NewTask(TaskInboxPendingReplySend, b),
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
func (c *Client) EnqueuePendingInboxCompose(pendingID, workspaceID string, sendAfter time.Time) error {
	b, err := json.Marshal(InboxPendingComposeSendPayload{
		PendingID: pendingID, WorkspaceID: workspaceID,
	})
	if err != nil {
		return err
	}
	return c.enqueue(asynq.NewTask(TaskInboxPendingComposeSend, b),
		asynq.TaskID("inboxcompose:"+pendingID),
		asynq.ProcessAt(sendAfter),
		asynq.MaxRetry(sendMaxRetry),
		asynq.Timeout(sendTimeout),
		asynq.Retention(taskRetention),
	)
}

// EnqueueInboxReplySend enqueues an inbox:reply_send task for immediate
// processing. The SAME generated id is set as both the payload's TaskID
// field and the task's own asynq.TaskID, so the handler's claim key and
// asynq's own dedup/retry identity are always the one value — see
// InboxReplySendPayload.TaskID's doc for why the payload carries it at all.
func (c *Client) EnqueueInboxReplySend(threadID, bodyText, workspaceID string) error {
	taskID := inboxReplySendTaskID(threadID, time.Now())
	b, err := json.Marshal(InboxReplySendPayload{
		ThreadID: threadID, BodyText: bodyText, WorkspaceID: workspaceID, TaskID: taskID,
	})
	if err != nil {
		return err
	}
	return c.enqueue(asynq.NewTask(TaskInboxReplySend, b),
		asynq.TaskID(taskID),
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
// mailbox in the same interval collapse to one poll.
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
func (c *Client) EnqueueInboxPoll(mailboxID, workspaceID string) error {
	b, err := json.Marshal(InboxPollPayload{MailboxID: mailboxID, WorkspaceID: workspaceID})
	if err != nil {
		return err
	}
	return c.enqueue(asynq.NewTask(TaskInboxPoll, b),
		asynq.TaskID(inboxPollTaskID(mailboxID, time.Now())),
		asynq.MaxRetry(pollMaxRetry),
		asynq.Timeout(pollTimeout),
		asynq.Retention(taskRetention),
	)
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
	_, err := sch.Register("@every 5m", asynq.NewTask(TaskSweepEnrollments, nil), asynq.Timeout(sweepTimeout))
	return err
}

// RegisterInboxSweep registers the periodic inbox:sweep. Runs every
// inboxSweepInterval to fan out inbox:poll tasks for every active mailbox.
func RegisterInboxSweep(sch *asynq.Scheduler) error {
	_, err := sch.Register("@every "+inboxSweepInterval.String(), asynq.NewTask(TaskInboxSweep, nil), asynq.Timeout(sweepTimeout))
	return err
}

// RegisterWarmupSweep registers the periodic warmup:sweep. Runs every 5 minutes
// to fan out a warmup:tick for every due participant and recompute health.
func RegisterWarmupSweep(sch *asynq.Scheduler) error {
	_, err := sch.Register("@every 5m", asynq.NewTask(TaskWarmupSweep, nil), asynq.Timeout(sweepTimeout))
	return err
}

// RegisterMaintenanceCleanup registers the low-frequency retention pass. The
// handler is idempotent, so scheduler restarts and retries are safe.
func RegisterMaintenanceCleanup(sch *asynq.Scheduler) error {
	_, err := sch.Register("@every 24h", asynq.NewTask(TaskMaintenanceCleanup, nil), asynq.Timeout(sweepTimeout))
	return err
}

// RegisterDomainAuthSweep registers the periodic domain-authentication sweep.
// It ticks hourly against a 24-hour staleness window: the tick rate is what
// bounds how long a NEWLY connected mailbox's domain sits unchecked, and how
// soon a domain whose lookup failed is retried, while the window is what stops
// it from re-resolving the same records twelve times a day.
func RegisterDomainAuthSweep(sch *asynq.Scheduler) error {
	_, err := sch.Register("@every 1h", asynq.NewTask(TaskDomainAuthSweep, nil), asynq.Timeout(sweepTimeout))
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
	_, err := sch.Register("@every 5m", asynq.NewTask(TaskRecipientESPSweep, nil), asynq.Timeout(sweepTimeout))
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
