package sendcap

import "testing"

func TestEffective(t *testing.T) {
	cases := []struct {
		name                              string
		dailyCap, startCap, rampDays, age int
		rampEnabled                       bool
		want                              int
	}{
		{"ramp disabled yields the full cap", 50, 5, 30, 0, false, 50},
		{"day zero yields the start cap", 50, 5, 30, 0, true, 5},
		{"past the ramp yields the full cap", 50, 5, 30, 30, true, 50},
		{"midway is linear", 50, 10, 20, 10, true, 30},
		{"zero ramp days cannot divide", 50, 5, 0, 3, true, 50},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Effective(c.dailyCap, c.startCap, c.rampDays, c.rampEnabled, c.age); got != c.want {
				t.Errorf("Effective = %d, want %d", got, c.want)
			}
		})
	}
}

// TestColdFactor covers every health state the CHECK constraint allows plus the
// two "no signal" cases — a mailbox that is not warming up (empty) and a value
// outside the constraint — both of which must leave cold sending untouched.
func TestColdFactor(t *testing.T) {
	cases := []struct {
		state string
		want  float64
	}{
		{"healthy", 1},
		{HealthUnknown, 0.5},
		{"", 1},
		{"nonsense", 1},
		{HealthWatch, 0.7},
		{HealthThrottled, 0.5},
		{HealthPaused, 0},
	}
	for _, c := range cases {
		t.Run("state="+c.state, func(t *testing.T) {
			if got := ColdFactor(c.state); got != c.want {
				t.Errorf("ColdFactor(%q) = %v, want %v", c.state, got, c.want)
			}
		})
	}
}

func TestCold(t *testing.T) {
	cases := []struct {
		name      string
		effective int
		state     string
		want      int
	}{
		{"healthy is unscaled", 40, "healthy", 40},
		{"unknown reputation is conservative", 40, HealthUnknown, 20},
		{"not warming up is unscaled", 40, "", 40},
		{"watch takes 70 percent", 40, HealthWatch, 28},
		{"throttled takes half", 40, HealthThrottled, 20},
		{"paused cannot send", 40, HealthPaused, 0},
		{"scaling rounds down", 9, HealthThrottled, 4},
		// A cap too small to scale must stay sendable: 'throttled' means slower,
		// not stopped, and a zero here would STOP the enrollment instead of
		// deferring it.
		{"a cap of one stays sendable when throttled", 1, HealthThrottled, 1},
		{"a cap of one stays sendable on watch", 1, HealthWatch, 1},
		{"a cap of one is still stopped when paused", 1, HealthPaused, 0},
		{"a zero cap stays zero", 0, "healthy", 0},
		{"a negative cap cannot become sendable", -5, HealthWatch, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Cold(c.effective, c.state); got != c.want {
				t.Errorf("Cold(%d, %q) = %d, want %d", c.effective, c.state, got, c.want)
			}
		})
	}
}
