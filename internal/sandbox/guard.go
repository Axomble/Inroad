package sandbox

import (
	"errors"
	"fmt"
	"strings"
)

// ErrRefused is returned when the harness declines to run. Every refusal
// wraps it, so a caller can tell "this environment is not allowed to be
// seeded" apart from a database or network failure and exit accordingly.
var ErrRefused = errors.New("sandbox: refused")

// Guard is the two-key check the harness runs before it writes anything. Both
// keys must be turned for a run to proceed:
//
//  1. Acknowledged — the operator asked for this explicitly (the -sandbox
//     flag). A tool that fabricates data must never be the default path of a
//     command someone runs for another reason.
//  2. Env — the deployment says it is not production.
//
// The two are independent on purpose. A flag alone would let a mistyped
// command destroy a production workspace; an env check alone would let a
// routine seed run fabricate data in staging because the variable happened to
// be right.
type Guard struct {
	// Env is the loaded config's environment string (INROAD_ENV).
	Env string
	// Acknowledged is the explicit opt-in from the command line.
	Acknowledged bool
}

// productionEnvNames are the environment values that mean "real customer
// data lives here". Matching is case-insensitive and covers the common
// spellings, because the cost of missing one is seeding fabricated contacts
// into a production workspace.
var productionEnvNames = map[string]bool{
	"production": true,
	"prod":       true,
	"live":       true,
}

// Check reports whether the harness may run, and explains any refusal in
// terms of what the operator has to change.
//
// An EMPTY env is refused, not assumed safe. This is the "fail loud if the
// check cannot be made" rule: a binary that could not determine its
// environment has, by definition, not established that it is outside
// production, and defaulting an unknown to "safe" is exactly how a guard
// silently stops guarding. config.Load defaults INROAD_ENV to "development",
// so an empty value here means the config was never loaded — a wiring bug
// worth surfacing rather than papering over.
func (g Guard) Check() error {
	if !g.Acknowledged {
		return fmt.Errorf("%w: the sandbox harness writes fabricated data and must be requested explicitly (pass -sandbox)", ErrRefused)
	}
	env := strings.ToLower(strings.TrimSpace(g.Env))
	if env == "" {
		return fmt.Errorf("%w: the environment could not be determined (INROAD_ENV is empty), so it cannot be shown to be non-production", ErrRefused)
	}
	if productionEnvNames[env] {
		return fmt.Errorf("%w: INROAD_ENV is %q; the sandbox harness never runs against production", ErrRefused, g.Env)
	}
	return nil
}
