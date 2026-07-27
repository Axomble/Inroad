package redisbus

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/platform/bus"
)

// fakeEnqueuer captures the task and options handed to it and returns a
// programmable error, so the Job/Options -> asynq translation can be asserted
// without a live Redis.
type fakeEnqueuer struct {
	task *asynq.Task
	opts []asynq.Option
	err  error
	// calls counts how many times Enqueue was invoked.
	calls int
}

func (f *fakeEnqueuer) EnqueueContext(_ context.Context, t *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error) {
	f.calls++
	f.task = t
	f.opts = opts
	if f.err != nil {
		return nil, f.err
	}
	return &asynq.TaskInfo{}, nil
}

// optByType returns the value of the first option of the given type, and whether
// one was present.
func optByType(opts []asynq.Option, typ asynq.OptionType) (any, bool) {
	for _, o := range opts {
		if o.Type() == typ {
			return o.Value(), true
		}
	}
	return nil, false
}

func TestPublishTranslatesJobAndOptions(t *testing.T) {
	at := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	fake := &fakeEnqueuer{}
	d := NewDispatcher(fake)

	err := d.Publish(context.Background(), bus.Job{
		Kind:    "warmup:tick",
		Payload: []byte(`{"mailbox_id":"mb-1"}`),
		Key:     "warmup:mb-1:99",
		Dest:    "w:node-a",
	}, bus.Options{At: at, MaxRetry: 7})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if fake.task.Type() != "warmup:tick" {
		t.Errorf("task type = %q, want warmup:tick", fake.task.Type())
	}
	if string(fake.task.Payload()) != `{"mailbox_id":"mb-1"}` {
		t.Errorf("payload = %q", fake.task.Payload())
	}
	if v, ok := optByType(fake.opts, asynq.TaskIDOpt); !ok || v != "warmup:mb-1:99" {
		t.Errorf("TaskID opt = %v (present=%v), want warmup:mb-1:99", v, ok)
	}
	if v, ok := optByType(fake.opts, asynq.QueueOpt); !ok || v != "w:node-a" {
		t.Errorf("Queue opt = %v (present=%v), want w:node-a", v, ok)
	}
	if v, ok := optByType(fake.opts, asynq.MaxRetryOpt); !ok || v != 7 {
		t.Errorf("MaxRetry opt = %v (present=%v), want 7", v, ok)
	}
	if v, ok := optByType(fake.opts, asynq.ProcessAtOpt); !ok || !v.(time.Time).Equal(at) {
		t.Errorf("ProcessAt opt = %v (present=%v), want %v", v, ok, at)
	}
	// At was set, so ProcessIn must NOT be present.
	if _, ok := optByType(fake.opts, asynq.ProcessInOpt); ok {
		t.Errorf("ProcessIn opt should be absent when At is set")
	}
}

func TestPublishInMapsToProcessIn(t *testing.T) {
	fake := &fakeEnqueuer{}
	d := NewDispatcher(fake)

	if err := d.Publish(context.Background(), bus.Job{Kind: "warmup:engage"}, bus.Options{In: 90 * time.Second}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if v, ok := optByType(fake.opts, asynq.ProcessInOpt); !ok || v != 90*time.Second {
		t.Errorf("ProcessIn opt = %v (present=%v), want 90s", v, ok)
	}
	if _, ok := optByType(fake.opts, asynq.ProcessAtOpt); ok {
		t.Errorf("ProcessAt opt should be absent when only In is set")
	}
}

func TestPublishAtTakesPrecedenceOverIn(t *testing.T) {
	at := time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC)
	fake := &fakeEnqueuer{}
	d := NewDispatcher(fake)

	if err := d.Publish(context.Background(), bus.Job{Kind: "k"}, bus.Options{At: at, In: time.Minute}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if _, ok := optByType(fake.opts, asynq.ProcessAtOpt); !ok {
		t.Errorf("ProcessAt opt should be present when both At and In are set")
	}
	if _, ok := optByType(fake.opts, asynq.ProcessInOpt); ok {
		t.Errorf("ProcessIn opt should be absent when At is also set")
	}
}

func TestPublishOmitsUnsetOptions(t *testing.T) {
	fake := &fakeEnqueuer{}
	d := NewDispatcher(fake)

	// Empty Key/Dest and zero At/In/MaxRetry => no options at all.
	if err := d.Publish(context.Background(), bus.Job{Kind: "k", Payload: []byte("{}")}, bus.Options{}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(fake.opts) != 0 {
		got := make([]asynq.OptionType, 0, len(fake.opts))
		for _, o := range fake.opts {
			got = append(got, o.Type())
		}
		t.Errorf("expected no options, got %v", got)
	}
}

func TestPublishTreatsTaskIDConflictAsSuccess(t *testing.T) {
	fake := &fakeEnqueuer{err: asynq.ErrTaskIDConflict}
	d := NewDispatcher(fake)

	if err := d.Publish(context.Background(), bus.Job{Kind: "k", Key: "dup"}, bus.Options{}); err != nil {
		t.Errorf("duplicate enqueue should be a no-op success, got %v", err)
	}
	if fake.calls != 1 {
		t.Errorf("calls = %d, want 1", fake.calls)
	}
}

func TestPublishPropagatesOtherErrors(t *testing.T) {
	sentinel := errors.New("redis down")
	fake := &fakeEnqueuer{err: sentinel}
	d := NewDispatcher(fake)

	err := d.Publish(context.Background(), bus.Job{Kind: "k"}, bus.Options{})
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want wrapped %v", err, sentinel)
	}
}

// fakeRegistrar captures RegisterPeriodic calls.
type fakeRegistrar struct {
	spec string
	task *asynq.Task
	err  error
}

func (f *fakeRegistrar) Register(cronspec string, task *asynq.Task, _ ...asynq.Option) (string, error) {
	f.spec = cronspec
	f.task = task
	return "entry-1", f.err
}

func TestRegisterPeriodicRegistersTask(t *testing.T) {
	fake := &fakeRegistrar{}
	s := NewScheduler(fake)

	if err := s.RegisterPeriodic("@every 5m", "warmup:sweep"); err != nil {
		t.Fatalf("RegisterPeriodic: %v", err)
	}
	if fake.spec != "@every 5m" {
		t.Errorf("spec = %q, want @every 5m", fake.spec)
	}
	if fake.task.Type() != "warmup:sweep" {
		t.Errorf("task type = %q, want warmup:sweep", fake.task.Type())
	}
}

func TestRegisterPeriodicPropagatesError(t *testing.T) {
	sentinel := errors.New("bad cron")
	fake := &fakeRegistrar{err: sentinel}
	s := NewScheduler(fake)

	if err := s.RegisterPeriodic("nope", "k"); !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want wrapped %v", err, sentinel)
	}
}

// Interface-satisfaction guards.
var (
	_ bus.Dispatcher        = (*Dispatcher)(nil)
	_ bus.PeriodicScheduler = (*Scheduler)(nil)
)
