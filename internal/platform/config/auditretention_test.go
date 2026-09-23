package config

import (
	"strings"
	"testing"
)

func TestAuditRetentionDefaultsToKeepForever(t *testing.T) {
	setRequiredSecrets(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AuditRetentionDays != 0 {
		t.Fatalf("AuditRetentionDays = %d by default, want 0 (retention disabled)", cfg.AuditRetentionDays)
	}
}

func TestAuditRetentionAcceptsAWholeNumberOfDays(t *testing.T) {
	setRequiredSecrets(t)
	t.Setenv("INROAD_AUDIT_RETENTION_DAYS", "365")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AuditRetentionDays != 365 {
		t.Fatalf("AuditRetentionDays = %d, want 365", cfg.AuditRetentionDays)
	}
}

// A setting that deletes evidence must fail loud on anything it cannot read
// exactly: a malformed value silently becoming "keep forever" hides a policy
// the operator believed was in force, and one silently becoming a short
// window destroys records.
func TestAuditRetentionRejectsMalformedOrOutOfRange(t *testing.T) {
	for _, v := range []string{"90d", "1y", "-30", "3.5", "36501"} {
		t.Run(v, func(t *testing.T) {
			setRequiredSecrets(t)
			t.Setenv("INROAD_AUDIT_RETENTION_DAYS", v)
			_, err := Load()
			if err == nil {
				t.Fatalf("Load accepted INROAD_AUDIT_RETENTION_DAYS=%q", v)
			}
			if !strings.Contains(err.Error(), "INROAD_AUDIT_RETENTION_DAYS") {
				t.Fatalf("error %q does not name the setting", err)
			}
		})
	}
}
