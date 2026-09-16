package main

import (
	"errors"
	"log/slog"

	"github.com/inroad/inroad/internal/coreapi/inprocess"
	"github.com/inroad/inroad/internal/coreapi/remote"
	"github.com/inroad/inroad/internal/platform/config"
	"github.com/inroad/inroad/internal/worker"
)

// coreAPIMode is where this worker's relational reads come from. It is the
// second of the two security-relevant decisions in this binary — the first is
// credentialMode next door — and it is separated from the wiring for the same
// reason: a decision worth testing is a decision worth being able to test
// without a database, a Redis or a network.
type coreAPIMode int

const (
	// coreAPILocal: the process reads through its own pgxpool, in-process.
	// This is EXACTLY the behaviour that shipped before the remote transport
	// existed, it is what every self-hosted installation runs, and it is what a
	// worker that sets no new variable gets.
	coreAPILocal coreAPIMode = iota
	// coreAPIRemote: the process asks the control plane over the fleet channel
	// for the methods the transport has taken over — in slice 1, IsSuppressed
	// and nothing else. The pool is still open for everything else; see
	// internal/coreapi/remote's package doc for what that does and does not buy
	// yet.
	coreAPIRemote
)

func (m coreAPIMode) String() string {
	if m == coreAPIRemote {
		return "remote"
	}
	return "in-process"
}

// ErrCoreAPIRemoteNeedsSendRole refuses the flag on any role but send. A
// control-role worker runs beside the API and a role=all worker IS the
// single-process self-host topology — in both, the database is right there, so
// a network hop to reach it would be pure latency. Refusing is not pedantry:
// an operator who sets this believes their worker stopped reading the tenant
// database, and silently ignoring it would leave that belief uncorrected.
var ErrCoreAPIRemoteNeedsSendRole = errors.New(
	"INROAD_FLEET_COREAPI_REMOTE applies only to a role=send worker: a control or all-role worker runs beside the database it would be asking the control plane about")

// ErrCoreAPIRemoteNeedsFleetURL is the fail-closed half: a worker told to read
// remotely with nowhere to read from refuses at startup rather than starting
// and failing every check — and never falls back to the local pool, which is
// the one outcome that would make the flag a lie.
var ErrCoreAPIRemoteNeedsFleetURL = errors.New(
	"INROAD_FLEET_COREAPI_REMOTE needs INROAD_FLEET_BROKER_URL (and INROAD_FLEET_BROKER_TOKEN): the coreapi transport shares the credential broker's listener and token")

// resolveCoreAPIMode decides where this process reads from, from the role and
// the configuration alone.
//
// The matrix, stated once:
//
//	role     flag  url  →  outcome
//	any      no    any  →  in-process   (the default; self-host, unchanged)
//	send     yes   yes  →  remote
//	send     yes   no   →  ERROR (nowhere to read from — fail closed)
//	control  yes   any  →  ERROR (the flag does nothing there)
//	all      yes   any  →  ERROR (same)
func resolveCoreAPIMode(cfg *config.Config, role worker.Role) (coreAPIMode, error) {
	if !cfg.FleetCoreAPIRemote {
		return coreAPILocal, nil
	}
	if role != worker.RoleSend {
		return coreAPILocal, ErrCoreAPIRemoteNeedsSendRole
	}
	if cfg.FleetBrokerURL == "" {
		return coreAPILocal, ErrCoreAPIRemoteNeedsFleetURL
	}
	return coreAPIRemote, nil
}

// coreAPIWiring is what the composition root got back. suppression is non-nil
// only in coreAPIRemote.
type coreAPIWiring struct {
	mode        coreAPIMode
	suppression inprocess.SuppressionSource
}

// buildCoreAPIWiring turns the resolved mode into the concrete dependency. It
// is the only place cmd/worker builds a remote coreapi client.
func buildCoreAPIWiring(cfg *config.Config, role worker.Role, logger *slog.Logger) (coreAPIWiring, error) {
	mode, err := resolveCoreAPIMode(cfg, role)
	if err != nil {
		return coreAPIWiring{}, err
	}
	if mode != coreAPIRemote {
		return coreAPIWiring{mode: mode}, nil
	}
	client, err := remote.NewClient(cfg.FleetBrokerURL, cfg.FleetBrokerToken, cfg.FleetBrokerAllowPlaintext)
	if err != nil {
		return coreAPIWiring{}, err
	}
	logger.Info("coreapi source", "mode", mode.String(), "control_plane", cfg.FleetBrokerURL,
		"methods", "IsSuppressed",
		"note", "this worker asks the control plane for the methods the remote transport carries; it still opens a pool for the rest")
	return coreAPIWiring{mode: mode, suppression: client}, nil
}

// coreOptions returns the inprocess options this wiring implies. The local mode
// returns NONE, so inprocess.New builds exactly the client it built before this
// slice existed — that emptiness is the self-host guarantee, not an oversight.
func (w coreAPIWiring) coreOptions() []inprocess.Option {
	if w.suppression == nil {
		return nil
	}
	return []inprocess.Option{inprocess.WithRemoteSuppression(w.suppression)}
}
