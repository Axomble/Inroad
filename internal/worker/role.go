package worker

import (
	"fmt"
	"strings"
)

// Role is what a worker process does. It exists because a sending host may run
// on hardware we do not control, and the periodic sweeps are control-plane
// scans: sequence:sweep_stuck_enrollments enumerates every workspace's due
// enrollments, and maintenance:cleanup purges across all tenants. Dispatching
// those to an untrusted host would hand it the tenant surface the plane split
// exists to withhold (see the fleet design doc, §1.2).
type Role string

const (
	// RoleAll registers everything in one process. This is the DEFAULT and the
	// self-host topology: an operator running Inroad for their own mailboxes has
	// one trust domain and should never have to learn this setting exists.
	RoleAll Role = "all"
	// RoleControl runs the scheduler, the six periodic sweeps and the purges.
	// It stays on trusted infrastructure beside the API.
	RoleControl Role = "control"
	// RoleSend runs per-message work only: sends, warmup ticks, inbox polls,
	// webhook deliveries. This is the role intended for a fleet host.
	RoleSend Role = "send"
)

// ParseRole reads the operator-supplied value. An empty string is RoleAll so an
// unset variable means today's behaviour; an unrecognised one is an ERROR
// rather than a default, because a typo that silently degraded to RoleAll would
// leave a host the operator believed was send-only running every sweep.
func ParseRole(s string) (Role, error) {
	switch Role(strings.ToLower(strings.TrimSpace(s))) {
	case "":
		return RoleAll, nil
	case RoleAll:
		return RoleAll, nil
	case RoleControl:
		return RoleControl, nil
	case RoleSend:
		return RoleSend, nil
	default:
		return "", fmt.Errorf("worker role %q: must be one of %q, %q, %q (or unset for %q)",
			s, RoleAll, RoleControl, RoleSend, RoleAll)
	}
}

// RunsScheduledWork reports whether this role registers the periodic sweeps and
// purges — the handlers that scan or delete across tenants.
func (r Role) RunsScheduledWork() bool { return r == RoleAll || r == RoleControl }

// RunsPerMessageWork reports whether this role registers the per-message
// handlers: sends, warmup ticks and engagement, inbox polls, manual replies,
// test sends and webhook deliveries.
func (r Role) RunsPerMessageWork() bool { return r == RoleAll || r == RoleSend }
