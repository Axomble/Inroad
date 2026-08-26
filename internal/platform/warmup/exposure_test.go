package warmup

import "testing"

// The ordering is the part that is not a guess: a deteriorating domain must never be
// allowed to carry more of a campaign than a healthy one.
func TestExposureCeilingTightensAsALaneDeteriorates(t *testing.T) {
	watch, recovery := ExposureCeiling(LaneWatch), ExposureCeiling(LaneRecovery)

	if !(recovery < watch) {
		t.Errorf("recovery ceiling %v is not below watch %v; recovery has already failed once "+
			"and needs its clean window on less traffic, not more", recovery, watch)
	}
	for _, c := range []float64{watch, recovery} {
		if c <= 0 || c >= 1 {
			t.Errorf("ceiling %v is outside (0,1); the caller reads anything outside that as "+
				"'no opinion' and silently falls back to the flat cap", c)
		}
	}
}

// Zero means "no opinion", never "may not send". Containment is LaneMaySend's
// decision; a ceiling of 0 here would be a second implementation of it, and two
// things that must agree about whether a mailbox may send is the shape every
// repeated defect in this subsystem has taken.
func TestExposureCeilingHasNoOpinionOnLanesItDoesNotNarrow(t *testing.T) {
	for _, lane := range []string{
		LaneHealthy, LaneProbation, LanePendingAuth, LaneQuarantine, LaneBlocked, "", "nonsense",
	} {
		if got := ExposureCeiling(lane); got != 0 {
			t.Errorf("ExposureCeiling(%q) = %v, want 0 (no opinion)", lane, got)
		}
	}
}

// Every lane that may send is either narrowed or explicitly left alone — so adding a
// sendable lane without deciding its exposure fails here rather than silently
// inheriting the flat cap.
func TestEverySendableLaneHasADecidedCeiling(t *testing.T) {
	narrowed := map[string]bool{LaneWatch: true, LaneRecovery: true}
	unnarrowed := map[string]bool{LaneHealthy: true, LaneProbation: true}

	for _, lane := range []string{LanePendingAuth, LaneProbation, LaneHealthy, LaneWatch, LaneRecovery, LaneQuarantine, LaneBlocked} {
		if !LaneMaySend(lane) {
			continue
		}
		if !narrowed[lane] && !unnarrowed[lane] {
			t.Errorf("lane %q may send but no exposure decision was recorded for it; decide "+
				"whether it is narrowed and add it to this test", lane)
		}
		if narrowed[lane] && ExposureCeiling(lane) == 0 {
			t.Errorf("lane %q is listed as narrowed but has no ceiling", lane)
		}
		if unnarrowed[lane] && ExposureCeiling(lane) != 0 {
			t.Errorf("lane %q is listed as unnarrowed but has ceiling %v", lane, ExposureCeiling(lane))
		}
	}
}
