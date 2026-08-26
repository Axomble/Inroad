package sandbox

import (
	"errors"
	"strings"
	"testing"
)

func TestGuardAllowsAcknowledgedNonProduction(t *testing.T) {
	for _, env := range []string{"development", "dev", "test", "staging", "Development", " local "} {
		if err := (Guard{Env: env, Acknowledged: true}).Check(); err != nil {
			t.Errorf("Check() with env %q = %v, want nil", env, err)
		}
	}
}

// The flag is the operator's explicit request. Without it the harness must
// never write, whatever the environment says.
func TestGuardRefusesWithoutExplicitOptIn(t *testing.T) {
	err := (Guard{Env: "development", Acknowledged: false}).Check()
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("Check() = %v, want an ErrRefused", err)
	}
	if !strings.Contains(err.Error(), "-sandbox") {
		t.Errorf("refusal %q does not tell the operator which flag to pass", err)
	}
}

// The whole point of the second key: an acknowledged run still must not touch
// production.
func TestGuardRefusesProduction(t *testing.T) {
	for _, env := range []string{"production", "PRODUCTION", "prod", " Prod ", "live"} {
		err := (Guard{Env: env, Acknowledged: true}).Check()
		if !errors.Is(err, ErrRefused) {
			t.Errorf("Check() with env %q = %v, want an ErrRefused", env, err)
		}
	}
}

// Fail loud when the check cannot be made: an unknown environment has not
// been shown to be safe, and treating unknown as safe is how a guard stops
// guarding.
func TestGuardRefusesUnknownEnvironment(t *testing.T) {
	err := (Guard{Env: "   ", Acknowledged: true}).Check()
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("Check() with a blank env = %v, want an ErrRefused", err)
	}
	if !strings.Contains(err.Error(), "INROAD_ENV") {
		t.Errorf("refusal %q does not name the variable that was missing", err)
	}
}
