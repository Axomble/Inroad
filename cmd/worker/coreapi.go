package main

import (
	"errors"
	"log/slog"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/coreapi/remote"
	"github.com/inroad/inroad/internal/platform/config"
	"github.com/inroad/inroad/internal/platform/credbroker"
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
	// existed, it is what every self-hosted installation runs, and it is what
	// role=all and role=control get.
	coreAPILocal coreAPIMode = iota
	// coreAPIRemote: the process asks the control plane over the fleet channel
	// for EVERY coreapi method it can reach, and opens no database connection
	// at all. This is role=send, and since slice 4 it is not optional there.
	//
	// The client is a *remote.Client, not an inprocess client with remote
	// pieces installed. That distinction is the security property: the type has
	// no *pgxpool.Pool field, so "this worker cannot reach the tenant database"
	// is a fact about the type rather than a claim about which code paths
	// happen to be unreachable today.
	coreAPIRemote
)

func (m coreAPIMode) String() string {
	if m == coreAPIRemote {
		return "remote"
	}
	return "in-process"
}

// ErrCoreAPIRemoteNeedsSendRole refuses INROAD_FLEET_COREAPI_REMOTE on any role
// but send. A control-role worker runs beside the API and a role=all worker IS
// the single-process self-host topology — in both, the database is right there
// and they need it (the cross-tenant sweeps have no remote transport and never
// will). Refusing is not pedantry: an operator who sets this believes their
// worker stopped reading the tenant database, and it has not.
//
// This is now the variable's ONLY remaining effect. On role=send it selects
// nothing, because that role reads remotely either way; see
// config.Config.FleetCoreAPIRemote.
var ErrCoreAPIRemoteNeedsSendRole = errors.New(
	"INROAD_FLEET_COREAPI_REMOTE applies only to a role=send worker: a control or all-role worker runs beside the database it would be asking the control plane about (and a role=send worker now reads remotely whether or not this is set)")

// ErrCoreAPIRemoteNeedsFleetURL is the fail-closed half: a role=send worker has
// no pool and nowhere to read from, so it refuses at startup rather than
// starting and failing every job. It never falls back to opening one.
var ErrCoreAPIRemoteNeedsFleetURL = errors.New(
	"a role=send worker must set INROAD_FLEET_BROKER_URL (and INROAD_FLEET_BROKER_TOKEN): it opens no database connection and reads coreapi from the control plane")

// ErrSendRoleHoldsDatabaseURL is THE refusal this slice exists for, and it is
// deliberately shaped exactly like ErrSendRoleHoldsMasterKey next door
// (cmd/worker/credentials.go).
//
// A role=send worker is the role meant for a host we do not control. Until
// slice 4 it opened its own pgxpool, so it could read every workspace's
// contacts, message bodies, reply text and secret_ciphertext — the ciphertext
// it could not decrypt since #207, but could still exfiltrate. It now reaches
// all of that through the control plane instead, one named subject at a time.
//
// Being HANDED a DSN is therefore a misconfiguration rather than a preference:
// either the operator believes this host talks to the database (it does not,
// and nothing here will use the value), or they have put production database
// credentials in the environment of a machine that has no use for them. Both
// are worth stopping at startup. That refusal IS the boundary — without it the
// variable would sit there unused, and the next person to add a pool-backed
// call site would find it working.
//
// It fires on the variable being PRESENT, not on the resolved value being
// non-empty: INROAD_DATABASE_URL has a local-development default, so every
// process has one (see config.Config.DatabaseURLSet).
var ErrSendRoleHoldsDatabaseURL = errors.New(
	"INROAD_DATABASE_URL must not be set on a role=send worker: a fleet host opens no database connection and reaches every coreapi method through INROAD_FLEET_BROKER_URL; unset it (this worker will not use it, and a fleet host should not hold tenant database credentials at all)")

// resolveCoreAPIMode decides where this process reads from, from the role and
// the configuration alone. Pure, and called before anything connects, so a
// combination that cannot work fails with no database attempt behind it.
//
// The matrix, stated once:
//
//	role     flag  url  dsn  →  outcome
//	all      no    -    any  →  in-process   (self-host, unchanged)
//	control  no    -    any  →  in-process   (runs the cross-tenant sweeps)
//	all/ctl  yes   -    any  →  ERROR (the flag does nothing there)
//	send     any   yes  no   →  remote, NO POOL
//	send     any   yes  yes  →  ERROR (a fleet host must hold no DSN)
//	send     any   no   any  →  ERROR (nowhere to read from — fail closed)
//
// The flag column is almost inert on purpose. role=send reads remotely because
// it has no pool, not because a variable said so; the only thing the flag can
// still do is be wrong on another role.
func resolveCoreAPIMode(cfg *config.Config, role worker.Role) (coreAPIMode, error) {
	if role != worker.RoleSend {
		if cfg.FleetCoreAPIRemote {
			return coreAPILocal, ErrCoreAPIRemoteNeedsSendRole
		}
		return coreAPILocal, nil
	}
	// Checked BEFORE the broker URL, so an operator who has set both hears
	// about the one that is a security problem rather than the one that is a
	// missing setting.
	if cfg.DatabaseURLSet {
		return coreAPILocal, ErrSendRoleHoldsDatabaseURL
	}
	if cfg.FleetBrokerURL == "" {
		return coreAPILocal, ErrCoreAPIRemoteNeedsFleetURL
	}
	return coreAPIRemote, nil
}

// ErrCoreAPIRemoteNeedsBroker is the third fail-closed refusal, and it names a
// dependency between the two decisions this binary makes. Reading coreapi
// remotely means the job responses carry no credential — a fleet worker gets
// those from the credential broker, so that one channel stays the only thing in
// the installation handing out a plaintext secret. Without a broker there would
// be a job and nothing to send it with.
//
// In practice resolveCredentialMode has already refused a role=send worker
// without a broker (ErrSendRoleNeedsBroker), and role=send is the only role
// that reads remotely — so this is the belt-and-braces half, checked here
// because the ORDER of two independent resolutions is not something the next
// person to touch this file should have to reason about.
var ErrCoreAPIRemoteNeedsBroker = errors.New(
	"a role=send worker needs the credential broker: coreapi job responses carry no credential, and a worker reading them remotely obtains one through INROAD_FLEET_BROKER_URL")

// coreAPIWiring is what the composition root got back. client is non-nil only
// in coreAPIRemote, and it is a COMPLETE coreapi.Client — the compile-time
// assertion lives in internal/coreapi/remote/controlplane.go.
type coreAPIWiring struct {
	mode   coreAPIMode
	client *remote.Client
}

// buildCoreAPIWiring turns an ALREADY-RESOLVED mode into the concrete
// dependency. It is the only place cmd/worker builds a remote coreapi client.
//
// The mode is a parameter rather than resolved here because the two halves have
// to happen at different points in the composition root: resolveCoreAPIMode is
// pure and runs before anything connects, so a role/configuration combination
// that cannot work fails with no database attempt behind it; this half needs
// the credential broker.
//
// creds is that broker. The coreapi client takes it because a job response
// carries no credential and the client fills one in per job; it is nil in every
// mode but credentialsBrokered, which is why remote mode refuses without it.
func buildCoreAPIWiring(cfg *config.Config, mode coreAPIMode, creds credbroker.Opener, logger *slog.Logger) (coreAPIWiring, error) {
	if mode != coreAPIRemote {
		return coreAPIWiring{mode: mode}, nil
	}
	if creds == nil {
		return coreAPIWiring{}, ErrCoreAPIRemoteNeedsBroker
	}
	client, err := remote.NewClient(cfg.FleetBrokerURL, cfg.FleetBrokerToken, cfg.FleetBrokerAllowPlaintext, creds)
	if err != nil {
		return coreAPIWiring{}, err
	}
	logger.Info("coreapi source", "mode", mode.String(), "control_plane", cfg.FleetBrokerURL,
		"note", "this worker opens NO database connection: every coreapi method it can reach is answered by the control plane, and each credential is brokered separately. The cross-tenant sweeps have no remote transport and are not registered on this role.")
	return coreAPIWiring{mode: mode, client: client}, nil
}

// coreClient returns the coreapi.Client this wiring implies, or nil in the
// local mode — where the composition root builds the in-process client from
// the pool it opened, exactly as it did before any of this existed.
//
// Returning coreapi.Client rather than the concrete type is deliberate: the
// caller must not be able to reach past the seam into transport internals, and
// a nil *remote.Client wrapped in a non-nil interface is the classic way a
// "core == nil" check silently stops working. The nil here is an untyped nil
// interface, and the caller branches on the MODE anyway.
func (w coreAPIWiring) coreClient() coreapi.Client {
	if w.client == nil {
		return nil
	}
	return w.client
}
