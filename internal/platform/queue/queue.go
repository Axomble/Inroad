// Package queue wraps asynq: task-type constants, typed enqueue helpers,
// and server/mux constructors. This is the only place asynq is imported.
package queue

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"
)

const TaskWarmupTick = "warmup:tick"

// WarmupTickPayload is the body of a warmup:tick task.
type WarmupTickPayload struct {
	MailboxID string `json:"mailbox_id"`
}

const TaskSendEmail = "send:email"

// SendEmailPayload is the body of a send:email task. WorkspaceID is
// included so the worker's DB lookups can pin `workspace_id` in the WHERE
// clause — defense in depth on top of the unguessable SendID UUID.
type SendEmailPayload struct {
	SendID      string `json:"send_id"`
	WorkspaceID string `json:"workspace_id"`
}

// TaskSweepStuck is the periodic reconcile task that re-enqueues sends left
// in 'queued' longer than the reconcile window. Scheduled by the worker's
// asynq.Scheduler every 2 minutes.
const TaskSweepStuck = "send:sweep_stuck"

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

// Client enqueues tasks onto Redis.
type Client struct {
	inner *asynq.Client
}

func NewClient(redisAddr string) *Client {
	return &Client{inner: asynq.NewClient(asynq.RedisClientOpt{Addr: redisAddr})}
}

func (c *Client) EnqueueWarmupTick(mailboxID string) error {
	b, err := json.Marshal(WarmupTickPayload{MailboxID: mailboxID})
	if err != nil {
		return err
	}
	_, err = c.inner.Enqueue(asynq.NewTask(TaskWarmupTick, b))
	return err
}

// EnqueueSend enqueues a send:email task for immediate processing. Both
// ids travel in the payload so the worker can pin workspace_id in its
// DB lookups (defense in depth on top of the UUID sendID).
func (c *Client) EnqueueSend(sendID, workspaceID string) error {
	b, err := json.Marshal(SendEmailPayload{SendID: sendID, WorkspaceID: workspaceID})
	if err != nil {
		return err
	}
	_, err = c.inner.Enqueue(asynq.NewTask(TaskSendEmail, b))
	return err
}

// EnqueueSendIn enqueues a send:email task to be processed after delay d.
func (c *Client) EnqueueSendIn(sendID, workspaceID string, d time.Duration) error {
	b, err := json.Marshal(SendEmailPayload{SendID: sendID, WorkspaceID: workspaceID})
	if err != nil {
		return err
	}
	_, err = c.inner.Enqueue(asynq.NewTask(TaskSendEmail, b), asynq.ProcessIn(d))
	return err
}

// EnqueueAdvance enqueues a sequence:advance task for immediate processing.
func (c *Client) EnqueueAdvance(enrollmentID, workspaceID string) error {
	return c.enqueueAdvance(enrollmentID, workspaceID)
}

// EnqueueAdvanceAt enqueues a sequence:advance task to run at time t (used by
// launch stagger and the lazy chain's next-step scheduling).
func (c *Client) EnqueueAdvanceAt(enrollmentID, workspaceID string, t time.Time) error {
	return c.enqueueAdvance(enrollmentID, workspaceID, asynq.ProcessAt(t))
}

// EnqueueAdvanceIn enqueues a sequence:advance task after delay d (used by the
// cap-exceeded backoff).
func (c *Client) EnqueueAdvanceIn(enrollmentID, workspaceID string, d time.Duration) error {
	return c.enqueueAdvance(enrollmentID, workspaceID, asynq.ProcessIn(d))
}

func (c *Client) enqueueAdvance(enrollmentID, workspaceID string, opts ...asynq.Option) error {
	b, err := json.Marshal(AdvancePayload{EnrollmentID: enrollmentID, WorkspaceID: workspaceID})
	if err != nil {
		return err
	}
	_, err = c.inner.Enqueue(asynq.NewTask(TaskSequenceAdvance, b), opts...)
	return err
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

func (c *Client) Close() error { return c.inner.Close() }

// NewServer builds an asynq processing server. Concurrency defaults to 10
// when concurrency <= 0. The provided *slog.Logger is adapted to asynq's
// Logger interface so worker log lines flow through the same structured
// sink as the rest of the app.
func NewServer(redisAddr string, logger *slog.Logger, concurrency int) *asynq.Server {
	if concurrency <= 0 {
		concurrency = 10
	}
	return asynq.NewServer(
		asynq.RedisClientOpt{Addr: redisAddr},
		asynq.Config{
			Concurrency: concurrency,
			Logger:      newAsynqLogger(logger),
		},
	)
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

// RegisterSweepStuck registers the periodic stuck-send sweep on the
// scheduler. Runs every 2 minutes to match the "stuck > 2 minutes" query.
func RegisterSweepStuck(sch *asynq.Scheduler) error {
	_, err := sch.Register("@every 2m", asynq.NewTask(TaskSweepStuck, nil))
	return err
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

// asynqLogger adapts *slog.Logger to asynq.Logger.
type asynqLogger struct{ l *slog.Logger }

func newAsynqLogger(l *slog.Logger) *asynqLogger { return &asynqLogger{l: l} }

func (a *asynqLogger) Debug(args ...any) { a.l.Debug("asynq", "msg", args) }
func (a *asynqLogger) Info(args ...any)  { a.l.Info("asynq", "msg", args) }
func (a *asynqLogger) Warn(args ...any)  { a.l.Warn("asynq", "msg", args) }
func (a *asynqLogger) Error(args ...any) { a.l.Error("asynq", "msg", args) }
func (a *asynqLogger) Fatal(args ...any) { a.l.Error("asynq-fatal", "msg", args) }
