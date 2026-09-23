package config

import (
	"strings"
	"testing"
)

// The defaults are the policy decision this feature is built around: every
// recipient-identifying table is DISABLED unless an operator turns it on, and
// dead letters keep the 90 days they were purged at before the window became
// configurable. A default that drifted to a number would start deleting data
// about people on the next deploy of every installation that never asked for it.
func TestRetentionDefaults(t *testing.T) {
	setRequiredSecrets(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	for name, got := range map[string]int{
		"INROAD_RETENTION_SENDS_DAYS":                 cfg.RetentionSendsDays,
		"INROAD_RETENTION_INBOX_DAYS":                 cfg.RetentionInboxDays,
		"INROAD_RETENTION_TRACKING_EVENTS_DAYS":       cfg.RetentionTrackingEventsDays,
		"INROAD_RETENTION_DELIVERABILITY_EVENTS_DAYS": cfg.RetentionDeliverabilityEventsDays,
	} {
		if got != 0 {
			t.Errorf("%s defaults to %d, want 0 (disabled — a Privacy/Legal decision, not a code default)", name, got)
		}
	}
	if cfg.RetentionDeadLettersDays != 90 {
		t.Errorf("INROAD_RETENTION_DEAD_LETTERS_DAYS defaults to %d, want 90 (the pre-existing hard-coded window)", cfg.RetentionDeadLettersDays)
	}
}

// Each variable reaches its own field — a transposed pair would apply one
// table's window to another and pass every "is it parsed" test.
func TestRetentionOverridesReachTheirOwnField(t *testing.T) {
	setRequiredSecrets(t)
	t.Setenv("INROAD_RETENTION_SENDS_DAYS", "401")
	t.Setenv("INROAD_RETENTION_INBOX_DAYS", "402")
	t.Setenv("INROAD_RETENTION_TRACKING_EVENTS_DAYS", "403")
	t.Setenv("INROAD_RETENTION_DELIVERABILITY_EVENTS_DAYS", "404")
	t.Setenv("INROAD_RETENTION_DEAD_LETTERS_DAYS", "0")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	got := [5]int{cfg.RetentionSendsDays, cfg.RetentionInboxDays, cfg.RetentionTrackingEventsDays,
		cfg.RetentionDeliverabilityEventsDays, cfg.RetentionDeadLettersDays}
	if want := [5]int{401, 402, 403, 404, 0}; got != want {
		t.Fatalf("retention days = %v, want %v (0 must be honoured as 'disabled', not replaced by the default)", got, want)
	}
}

// Every variable here controls a DELETE, so a malformed value must stop the
// process rather than be read as something the operator did not write (#224).
// Negative is refused, not clamped to "disabled"; a unit is refused because the
// unit is fixed; an absurd window is refused because it would overflow a
// time.Duration downstream and wrap negative.
func TestRetentionRejectsMalformedDays(t *testing.T) {
	for _, tc := range []struct {
		name, key, value string
	}{
		{"negative", "INROAD_RETENTION_SENDS_DAYS", "-30"},
		{"duration syntax", "INROAD_RETENTION_INBOX_DAYS", "720h"},
		{"unit suffix", "INROAD_RETENTION_TRACKING_EVENTS_DAYS", "90d"},
		{"word", "INROAD_RETENTION_DELIVERABILITY_EVENTS_DAYS", "forever"},
		{"fraction", "INROAD_RETENTION_DEAD_LETTERS_DAYS", "7.5"},
		{"beyond a century", "INROAD_RETENTION_SENDS_DAYS", "36501"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setRequiredSecrets(t)
			t.Setenv(tc.key, tc.value)
			_, err := Load()
			if err == nil {
				t.Fatalf("Load() with %s=%q returned nil error", tc.key, tc.value)
			}
			if !strings.Contains(err.Error(), tc.key) || !strings.Contains(err.Error(), tc.value) {
				t.Fatalf("Load() error = %q, want it to name %s and quote %q", err, tc.key, tc.value)
			}
		})
	}
}

// The ceiling itself is accepted — the boundary is inclusive.
func TestRetentionAcceptsTheCeiling(t *testing.T) {
	setRequiredSecrets(t)
	t.Setenv("INROAD_RETENTION_SENDS_DAYS", "36500")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.RetentionSendsDays != 36500 {
		t.Fatalf("RetentionSendsDays = %d, want 36500", cfg.RetentionSendsDays)
	}
}
