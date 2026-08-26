package main

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/platform/config"
)

// errRegistration stands in for whatever asynq would reject (a malformed cron
// spec, say) so the naming behaviour can be asserted without one.
var errRegistration = errors.New("boom")

// captureLogger returns a logger writing to buf, so a test can assert on what an
// operator would actually see at startup.
func captureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// fakeScheduler stands in for the asynq scheduler so the gate can be tested
// without Redis. Run blocks until Shutdown, mirroring the real one.
type fakeScheduler struct {
	running chan struct{}
	stopped bool
}

func newFakeScheduler() *fakeScheduler {
	return &fakeScheduler{running: make(chan struct{})}
}

func (f *fakeScheduler) Run() error {
	<-f.running
	return nil
}

func (f *fakeScheduler) Shutdown() {
	if !f.stopped {
		f.stopped = true
		close(f.running)
	}
}

// buildSpy counts how many times the scheduler is built, which is the observable
// consequence of the flag.
func buildSpy(calls *int, sch periodicScheduler) func() (periodicScheduler, error) {
	return func() (periodicScheduler, error) {
		*calls++
		return sch, nil
	}
}

// The whole point of the flag: a replica with it off builds no scheduler and so
// registers no periodic task. N replicas would otherwise each register every
// sweep, firing it N times.
func TestStartSchedulerDisabledBuildsNoScheduler(t *testing.T) {
	var logs bytes.Buffer
	var calls int
	cfg := &config.Config{RunScheduler: false, RedisAddr: "127.0.0.1:1"}

	stop, err := startSchedulerWith(cfg, captureLogger(&logs), buildSpy(&calls, newFakeScheduler()))
	if err != nil {
		t.Fatalf("startSchedulerWith: %v", err)
	}
	if calls != 0 {
		t.Errorf("built %d schedulers with the flag off, want 0", calls)
	}
	if stop == nil {
		t.Fatal("stop func is nil; the caller defers it unconditionally")
	}
	stop() // must be safe even though nothing started

	out := logs.String()
	if !strings.Contains(out, "scheduler disabled") {
		t.Errorf("startup log does not say the scheduler is off; an operator cannot tell. got:\n%s", out)
	}
	if strings.Contains(out, "scheduler enabled") {
		t.Errorf("startup log claims the scheduler is enabled while it is off. got:\n%s", out)
	}
}

// With the flag on, the scheduler is built and its mode logged, so a single-worker
// deployment keeps its reconciles with no configuration at all.
func TestStartSchedulerEnabledBuildsOneAndSaysSo(t *testing.T) {
	var logs bytes.Buffer
	var calls int
	cfg := &config.Config{RunScheduler: true, RedisAddr: "127.0.0.1:1"}

	sch := newFakeScheduler()
	stop, err := startSchedulerWith(cfg, captureLogger(&logs), buildSpy(&calls, sch))
	if err != nil {
		t.Fatalf("startSchedulerWith: %v", err)
	}
	stop()

	if calls != 1 {
		t.Errorf("built %d schedulers with the flag on, want exactly 1", calls)
	}
	if !sch.stopped {
		t.Error("the returned stop func did not shut the scheduler down")
	}
	out := logs.String()
	if !strings.Contains(out, "scheduler enabled") {
		t.Errorf("startup log does not say the scheduler is on. got:\n%s", out)
	}
}

// A build failure is surfaced, not swallowed: a worker whose sweeps cannot be
// registered must fail to start rather than run silently without reconciles.
func TestStartSchedulerPropagatesBuildFailure(t *testing.T) {
	var logs bytes.Buffer
	cfg := &config.Config{RunScheduler: true, RedisAddr: "127.0.0.1:1"}

	stop, err := startSchedulerWith(cfg, captureLogger(&logs), func() (periodicScheduler, error) {
		return nil, errRegistration
	})
	if !errors.Is(err, errRegistration) {
		t.Fatalf("startSchedulerWith error = %v, want the build failure", err)
	}
	if stop == nil {
		t.Fatal("stop func is nil on the failure path; the caller defers it unconditionally")
	}
	stop()
}

// Every sweep must actually register: asynq validates each cron spec at Register
// time without dialing Redis, so a malformed spec is caught here rather than as
// a reconcile that silently never runs.
func TestRegisterSweepsRegistersEverySweep(t *testing.T) {
	sweeps := sweepRegistrars()
	if len(sweeps) == 0 {
		t.Fatal("no sweeps registered; the scheduler would be a no-op when enabled")
	}
	sch := asynq.NewScheduler(asynq.RedisClientOpt{Addr: "127.0.0.1:1"}, nil)
	defer sch.Shutdown()
	if err := registerSweeps(sch); err != nil {
		t.Fatalf("registerSweeps: %v", err)
	}
	// Names must be distinct, or a registration error points at the wrong sweep.
	seen := map[string]bool{}
	for _, s := range sweeps {
		if seen[s.name] {
			t.Errorf("duplicate sweep name %q; an error message could not identify it", s.name)
		}
		seen[s.name] = true
	}
}

// A registration failure names the sweep that failed, so the error tells an
// operator which periodic task is misconfigured rather than just "it failed".
func TestRegisterSweepsNamesTheFailingSweep(t *testing.T) {
	sch := asynq.NewScheduler(asynq.RedisClientOpt{Addr: "127.0.0.1:1"}, nil)
	failing := []sweepRegistrar{
		{"inbox sweep", func(*asynq.Scheduler) error { return errRegistration }},
	}
	err := registerSweepList(sch, failing)
	if err == nil {
		t.Fatal("registerSweepList = nil error, want the injected failure")
	}
	if !strings.Contains(err.Error(), "inbox sweep") {
		t.Fatalf("error = %q, want it to name the failing sweep", err)
	}
	if !errors.Is(err, errRegistration) {
		t.Fatalf("error = %q, want the cause preserved through %%w", err)
	}
}
