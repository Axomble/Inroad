package coreapi_test

import (
	"testing"
	"time"

	"github.com/inroad/inroad/internal/coreapi"
)

// TestStepSendJobNotYetDue pins the claim-time guard's rule. It is the whole
// defence against an out-of-office deferral being ignored: pushing next_due_at
// out cannot cancel the asynq advance task already queued for the old time, so
// this predicate — read by ClaimStepSend and by the worker's reschedule — is
// what stops the step firing into a stated absence.
func TestStepSendJobNotYetDue(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		due  time.Time
		want bool
	}{
		{"no recorded due time is always due", time.Time{}, false},
		{"due in the future is not yet due", now.Add(time.Hour), true},
		{"due exactly now is due", now, false},
		{"due in the past is due", now.Add(-time.Hour), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			job := coreapi.StepSendJob{NotDueUntil: tc.due}
			if got := job.NotYetDue(now); got != tc.want {
				t.Fatalf("NotYetDue(%v) = %v, want %v", tc.due, got, tc.want)
			}
		})
	}
}
