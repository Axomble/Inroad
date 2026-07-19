// Package queue wraps asynq: task-type constants, typed enqueue helpers,
// and server/mux constructors. This is the only place asynq is imported.
package queue

import (
	"encoding/json"
	"log/slog"

	"github.com/hibiken/asynq"
)

const TaskWarmupTick = "warmup:tick"

// WarmupTickPayload is the body of a warmup:tick task.
type WarmupTickPayload struct {
	MailboxID string `json:"mailbox_id"`
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
