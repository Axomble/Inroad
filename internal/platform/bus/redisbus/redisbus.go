// Package redisbus implements the bus seam over asynq/Redis: it maps a
// transport-neutral bus.Job/bus.Options onto asynq task options and the
// asynq client/scheduler. It is the only shipped bus implementation.
//
// Mapping:
//
//	Job.Key      -> asynq.TaskID   (dedup / idempotency)
//	Job.Dest     -> asynq.Queue    (routing destination)
//	Options.At   -> asynq.ProcessAt (delayed delivery; precedence over In)
//	Options.In   -> asynq.ProcessIn
//	Options.MaxRetry -> asynq.MaxRetry (0 = asynq default)
//	RegisterPeriodic -> asynq.Scheduler.Register
//
// The "ErrTaskIDConflict is success" dedup rule (a duplicate enqueue of an
// already-pending task is a deliberate no-op) lives here.
package redisbus

import (
	"context"
	"errors"
	"fmt"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/platform/bus"
)

// Enqueuer is the slice of *asynq.Client the dispatcher needs. Kept minimal so
// tests can inject a fake without a live Redis (accept-interfaces).
type Enqueuer interface {
	EnqueueContext(ctx context.Context, t *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error)
}

// Dispatcher publishes bus.Jobs onto asynq/Redis. It satisfies bus.Dispatcher.
type Dispatcher struct {
	client Enqueuer
}

// NewDispatcher wraps an asynq enqueuer (typically *asynq.Client) as a
// bus.Dispatcher.
func NewDispatcher(client Enqueuer) *Dispatcher { return &Dispatcher{client: client} }

// Publish translates the job/options into an asynq enqueue. A TaskID conflict —
// a duplicate enqueue of an already-pending task (e.g. a sweeper re-enqueue
// racing a live task) — is treated as a deliberate no-op success, matching the
// existing send/advance dedup behavior. The row-claim in the handlers remains
// the real delivery-idempotency guarantee; this only cuts wasted retries.
func (d *Dispatcher) Publish(ctx context.Context, j bus.Job, o bus.Options) error {
	task := asynq.NewTask(j.Kind, j.Payload)
	if _, err := d.client.EnqueueContext(ctx, task, asynqOptions(j, o)...); err != nil {
		if errors.Is(err, asynq.ErrTaskIDConflict) {
			return nil
		}
		return fmt.Errorf("enqueue %q: %w", j.Kind, err)
	}
	return nil
}

// asynqOptions maps a bus.Job/bus.Options to asynq options, emitting an option
// only when the corresponding field is set so zero values fall through to
// asynq's defaults.
func asynqOptions(j bus.Job, o bus.Options) []asynq.Option {
	opts := make([]asynq.Option, 0, 4)
	if j.Key != "" {
		opts = append(opts, asynq.TaskID(j.Key))
	}
	if j.Dest != "" {
		opts = append(opts, asynq.Queue(j.Dest))
	}
	// At takes precedence over In: both set is caller misuse, and an explicit
	// absolute time is the more specific intent.
	switch {
	case !o.At.IsZero():
		opts = append(opts, asynq.ProcessAt(o.At))
	case o.In > 0:
		opts = append(opts, asynq.ProcessIn(o.In))
	}
	if o.MaxRetry != 0 {
		opts = append(opts, asynq.MaxRetry(o.MaxRetry))
	}
	return opts
}

// Registrar is the slice of *asynq.Scheduler the periodic scheduler needs.
type Registrar interface {
	Register(cronspec string, task *asynq.Task, opts ...asynq.Option) (string, error)
}

// Scheduler registers recurring jobs on an asynq scheduler. It satisfies
// bus.PeriodicScheduler.
type Scheduler struct {
	registrar Registrar
}

// NewScheduler wraps an asynq scheduler (typically *asynq.Scheduler) as a
// bus.PeriodicScheduler.
func NewScheduler(registrar Registrar) *Scheduler { return &Scheduler{registrar: registrar} }

// RegisterPeriodic registers a recurring job of the given kind on the cron spec.
func (s *Scheduler) RegisterPeriodic(spec, kind string) error {
	// asynq's Register returns an entry ID for later Unregister; we discard it
	// deliberately. The bus seam's RegisterPeriodic(spec, kind) error signature
	// can't surface that id, so an entry registered through the bus can't later be
	// Unregister'd — fine for boot-time sweep registration (register once at boot,
	// never dynamically torn down).
	if _, err := s.registrar.Register(spec, asynq.NewTask(kind, nil)); err != nil {
		return fmt.Errorf("register periodic %q: %w", kind, err)
	}
	return nil
}
