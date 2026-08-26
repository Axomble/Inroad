package main

import (
	"fmt"
	"log/slog"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/platform/config"
	"github.com/inroad/inroad/internal/platform/queue"
)

// sweepRegistrar names one periodic reconcile so a registration failure can say
// WHICH sweep failed. The registrars all come from platform/queue; the list here
// is the worker's composition root deciding which sweeps this deployment runs.
type sweepRegistrar struct {
	name     string
	register func(*asynq.Scheduler) error
}

// sweepRegistrars is every periodic reconcile the scheduler enqueues.
func sweepRegistrars() []sweepRegistrar {
	return []sweepRegistrar{
		{"enrollments", queue.RegisterSweepEnrollments},
		{"inbox sweep", queue.RegisterInboxSweep},
		{"warmup sweep", queue.RegisterWarmupSweep},
		{"maintenance cleanup", queue.RegisterMaintenanceCleanup},
		{"domain auth sweep", queue.RegisterDomainAuthSweep},
		{"recipient esp sweep", queue.RegisterRecipientESPSweep},
	}
}

// registerSweeps registers every periodic reconcile on sch. Split from
// startScheduler so the registration set can be exercised without Redis.
func registerSweeps(sch *asynq.Scheduler) error {
	return registerSweepList(sch, sweepRegistrars())
}

// registerSweepList registers the given sweeps, naming the one that failed so the
// error identifies the misconfigured periodic task rather than just "it failed".
func registerSweepList(sch *asynq.Scheduler, sweeps []sweepRegistrar) error {
	for _, r := range sweeps {
		if err := r.register(sch); err != nil {
			return fmt.Errorf("scheduler register (%s): %w", r.name, err)
		}
	}
	return nil
}

// startScheduler runs the asynq periodic scheduler in this process when
// cfg.RunScheduler is set, and returns the shutdown func the caller defers.
//
// asynq elects no leader: every process with this enabled registers every
// periodic task, so N replicas fire each sweep N times. The handlers are
// idempotent, so that is cost (N× the sweep scans) rather than corruption — but
// it scales silently with replica count, which is why it is a flag. Exactly one
// replica should run it when scaling out. Zero is degraded but not dangerous:
// live work is unaffected and only the reconciles stop, because every sweep is a
// reconcile rather than a deadline.
//
// The returned stop func is always non-nil, so the caller can defer it
// unconditionally.
func startScheduler(cfg *config.Config, logger *slog.Logger) (stop func(), err error) {
	return startSchedulerWith(cfg, logger, func() (periodicScheduler, error) {
		sch := queue.NewScheduler(cfg.RedisAddr, logger)
		if err := registerSweeps(sch); err != nil {
			return nil, err
		}
		return sch, nil
	})
}

// periodicScheduler is the two-method slice of *asynq.Scheduler this file drives.
// It exists so a test can assert the flag GATES the whole thing — the point of
// the flag is that a disabled replica builds nothing and registers no periodic
// task, which is only observable by watching whether the seam is used at all.
type periodicScheduler interface {
	Run() error
	Shutdown()
}

func startSchedulerWith(cfg *config.Config, logger *slog.Logger, build func() (periodicScheduler, error)) (stop func(), err error) {
	if !cfg.RunScheduler {
		// Logged at INFO, not DEBUG: "does this replica schedule?" is the first
		// question when a sweep stops running, and it must be answerable from
		// a default-level log.
		logger.Info("scheduler disabled for this replica", "run_scheduler", false,
			"note", "another replica must run the scheduler (INROAD_RUN_SCHEDULER=true)")
		return func() {}, nil
	}

	sch, err := build()
	if err != nil {
		logger.Error("scheduler registration failed", "err", err)
		return func() {}, err
	}
	go func() {
		if err := sch.Run(); err != nil {
			logger.Error("scheduler exited", "err", err)
		}
	}()
	logger.Info("scheduler enabled for this replica", "run_scheduler", true,
		"sweeps", len(sweepRegistrars()),
		"note", "exactly one replica should run the scheduler when scaling out")
	return sch.Shutdown, nil
}
