package coreapi_test

import (
	"testing"
	"time"

	"github.com/inroad/inroad/internal/coreapi"
)

// TestStepSendJobNotYetDue pins the process-side reading of the not-due rule,
// which the worker uses to size its retry after a deferral. The claim itself
// evaluates the same rule on the database's clock (StepSendNotYetDue, covered
// by the inprocess integration tests); the two must agree on the edges pinned
// here — a zero due time is due, and "due exactly now" is due.
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

// TestStepSendJobCarriesTracking pins the one rule both the worker's rewrite and
// the claim's sends.tracked stamp read: tracking needs the campaign flag AND an
// HTML body, because the pixel and the rewritten links exist only in HTML.
func TestStepSendJobCarriesTracking(t *testing.T) {
	cases := []struct {
		name     string
		tracking bool
		html     string
		want     bool
	}{
		{"flag on with an HTML body", true, "<p>Hi</p>", true},
		{"flag on but text only", true, "", false},
		{"flag off with an HTML body", false, "<p>Hi</p>", false},
		{"flag off and text only", false, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			job := coreapi.StepSendJob{TrackingEnabled: tc.tracking, BodyHTML: tc.html}
			if got := job.CarriesTracking(); got != tc.want {
				t.Fatalf("CarriesTracking() = %v, want %v", got, tc.want)
			}
		})
	}
}
