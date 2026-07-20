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

// SendEmailPayload is the body of a send:email task.
type SendEmailPayload struct {
	SendID string `json:"send_id"`
}

// TaskSweepStuck is the periodic reconcile task that re-enqueues sends left
// in 'queued' longer than the reconcile window. Scheduled by the worker's
// asynq.Scheduler every 2 minutes.
const TaskSweepStuck = "send:sweep_stuck"

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

// EnqueueSend enqueues a send:email task for immediate processing.
func (c *Client) EnqueueSend(sendID string) error {
	b, err := json.Marshal(SendEmailPayload{SendID: sendID})
	if err != nil {
		return err
	}
	_, err = c.inner.Enqueue(asynq.NewTask(TaskSendEmail, b))
	return err
}

// EnqueueSendIn enqueues a send:email task to be processed after delay d.
func (c *Client) EnqueueSendIn(sendID string, d time.Duration) error {
	b, err := json.Marshal(SendEmailPayload{SendID: sendID})
	if err != nil {
		return err
	}
	_, err = c.inner.Enqueue(asynq.NewTask(TaskSendEmail, b), asynq.ProcessIn(d))
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

// asynqLogger adapts *slog.Logger to asynq.Logger.
type asynqLogger struct{ l *slog.Logger }

func newAsynqLogger(l *slog.Logger) *asynqLogger { return &asynqLogger{l: l} }

func (a *asynqLogger) Debug(args ...any) { a.l.Debug("asynq", "msg", args) }
func (a *asynqLogger) Info(args ...any)  { a.l.Info("asynq", "msg", args) }
func (a *asynqLogger) Warn(args ...any)  { a.l.Warn("asynq", "msg", args) }
func (a *asynqLogger) Error(args ...any) { a.l.Error("asynq", "msg", args) }
func (a *asynqLogger) Fatal(args ...any) { a.l.Error("asynq-fatal", "msg", args) }
