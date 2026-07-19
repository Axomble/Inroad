package inprocess

import "testing"

func TestEffectiveCap(t *testing.T) {
	// disabled ramp -> full cap
	if got := effectiveCap(50, 5, 30, false, 0); got != 50 {
		t.Errorf("disabled: got %d want 50", got)
	}
	// day 0 -> start cap
	if got := effectiveCap(50, 5, 30, true, 0); got != 5 {
		t.Errorf("day0: got %d want 5", got)
	}
	// day >= rampDays -> full cap
	if got := effectiveCap(50, 5, 30, true, 30); got != 50 {
		t.Errorf("dayN: got %d want 50", got)
	}
	// midpoint ~ halfway
	if got := effectiveCap(50, 10, 20, true, 10); got < 28 || got > 32 {
		t.Errorf("mid: got %d want ~30", got)
	}
}
