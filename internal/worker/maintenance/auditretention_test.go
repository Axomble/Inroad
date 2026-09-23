package maintenance

import (
	"context"
	"errors"
	"testing"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/platform/queue"
)

type fakeAuditPurger struct {
	calls int
	days  int
	err   error
}

func (f *fakeAuditPurger) PurgeAuditEvents(_ context.Context, days int) (int64, error) {
	f.calls++
	f.days = days
	return 3, f.err
}

func runCleanup(t *testing.T, opts ...CleanupOption) error {
	t.Helper()
	return CleanupHandler(&cleanupCore{}, opts...)(context.Background(), asynq.NewTask(queue.TaskMaintenanceCleanup, nil))
}

func TestAuditRetentionRunsOnlyWhenConfigured(t *testing.T) {
	for _, tc := range []struct {
		name      string
		days      int
		wantCalls int
	}{
		{"unset keeps forever", 0, 0},
		{"negative is treated as unset", -5, 0},
		{"configured", 400, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakeAuditPurger{}
			if err := runCleanup(t, WithAuditRetention(p, tc.days)); err != nil {
				t.Fatalf("handler: %v", err)
			}
			if p.calls != tc.wantCalls {
				t.Fatalf("PurgeAuditEvents calls = %d, want %d", p.calls, tc.wantCalls)
			}
			if tc.wantCalls == 1 && p.days != tc.days {
				t.Fatalf("purged with %d days, want %d", p.days, tc.days)
			}
		})
	}
}

func TestAuditRetentionWithoutOptionNeverPurges(t *testing.T) {
	// The default registration passes no option at all: the audit table must
	// be untouched by a deployment that never configured retention.
	if err := runCleanup(t); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if err := runCleanup(t, WithAuditRetention(nil, 30)); err != nil {
		t.Fatalf("handler with nil purger: %v", err)
	}
}

func TestAuditRetentionFailureFailsTheRun(t *testing.T) {
	boom := errors.New("db down")
	if err := runCleanup(t, WithAuditRetention(&fakeAuditPurger{err: boom}, 30)); !errors.Is(err, boom) {
		t.Fatalf("handler err = %v, want the purge error so asynq retries", err)
	}
}
