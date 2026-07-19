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

// NewServer builds an asynq processing server.
func NewServer(redisAddr string, logger *slog.Logger) *asynq.Server {
	return asynq.NewServer(
		asynq.RedisClientOpt{Addr: redisAddr},
		asynq.Config{Concurrency: 10},
	)
}

// NewMux returns an empty task router for worker handlers to register on.
func NewMux() *asynq.ServeMux { return asynq.NewServeMux() }
