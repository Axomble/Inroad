package main

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/inroad/inroad/internal/coreapi/inprocess"
	"github.com/inroad/inroad/internal/platform/config"
	"github.com/inroad/inroad/internal/platform/credbroker"
	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/keys"
	"github.com/inroad/inroad/internal/worker"
)

// credentialMode is where this worker process gets decrypted credentials from.
type credentialMode int

const (
	// credentialsLocal: the process builds its own crypto.Keyring from
	// INROAD_MASTER_KEY and opens secrets itself. This is the single-process
	// self-host topology and is EXACTLY the behaviour that shipped before
	// brokering existed — a RoleAll worker that sets no new variable takes this
	// path and needs no change at all.
	credentialsLocal credentialMode = iota
	// credentialsBrokered: the process holds no key and asks the control plane
	// to open each credential (internal/platform/credbroker).
	credentialsBrokered
	// credentialsNone: the process opens nothing, because it runs no handler
	// that needs a credential. Only RoleControl reaches this, and only when the
	// operator configured neither a key nor a broker; every credential path
	// then fails closed with credbroker.ErrNotConfigured rather than falling
	// back to anything.
	credentialsNone
)

func (m credentialMode) String() string {
	switch m {
	case credentialsLocal:
		return "local-keyring"
	case credentialsBrokered:
		return "brokered"
	default:
		return "none"
	}
}

// ErrSendRoleHoldsMasterKey is the refusal at the heart of this boundary: a
// send-role worker is the role meant for a host we do not control, and handing
// it INROAD_MASTER_KEY lets it unwrap EVERY workspace's DEK — every stored SMTP
// password and every OAuth refresh token in the installation, offline and
// permanently. Brokering exists so it does not have to, so holding the key
// while brokering is configured is a misconfiguration that must fail loudly
// rather than quietly keep the old blast radius.
var ErrSendRoleHoldsMasterKey = errors.New(
	"INROAD_MASTER_KEY must not be set on a role=send worker: a fleet host brokers credentials through INROAD_FLEET_BROKER_URL and must never hold the key that unwraps every workspace")

// ErrSendRoleNeedsBroker is the other half: a send-role worker with neither a
// key nor a broker could not send at all, so it refuses at startup instead of
// failing every job. Fail closed, never fall back.
var ErrSendRoleNeedsBroker = errors.New(
	"a role=send worker must set INROAD_FLEET_BROKER_URL (and INROAD_FLEET_BROKER_TOKEN): it holds no master key and cannot open a credential without the control plane")

// ErrNoCredentialSource is RoleAll with neither. The single-process topology
// has to be able to open a credential somehow.
var ErrNoCredentialSource = errors.New(
	"set INROAD_MASTER_KEY (single-process self-host) or INROAD_FLEET_BROKER_URL (brokered): this worker can open no credential")

// resolveCredentialMode decides how this process will obtain credentials, from
// the role and the configuration alone. Pure, and separated from the wiring
// below, because it is the one security-relevant decision in this binary and a
// decision worth testing is a decision worth being able to test without a
// database, a Redis or a keyring.
//
// The matrix, stated once:
//
//	role     broker  key  →  outcome
//	any      yes     yes  →  ERROR (never hold a key you are not supposed to use)
//	send     yes     no   →  brokered
//	send     no      any  →  ERROR (a fleet host must broker)
//	control  yes     no   →  brokered
//	control  no      yes  →  local      (unchanged; control runs beside the API)
//	control  no      no   →  none       (it registers no credential-using handler)
//	all      yes     no   →  brokered
//	all      no      yes  →  local      (self-host; today's behaviour, unchanged)
//	all      no      no   →  ERROR
func resolveCredentialMode(cfg *config.Config, role worker.Role) (credentialMode, error) {
	brokered := cfg.FleetBrokerURL != ""
	hasKey := len(cfg.MasterKey) > 0

	// Checked before the role split because it is true for every role: a
	// process configured to broker has no business also holding the key. Left
	// unchecked, an operator who added the broker but forgot to remove the key
	// would get a worker that brokers AND could decrypt everything — all of the
	// new machinery, none of the benefit, and no signal that it was so.
	if brokered && hasKey {
		if role == worker.RoleSend {
			return credentialsNone, ErrSendRoleHoldsMasterKey
		}
		return credentialsNone, fmt.Errorf("INROAD_MASTER_KEY must not be set when INROAD_FLEET_BROKER_URL is: this worker would hold the key it is configured to broker around")
	}

	if role == worker.RoleSend {
		switch {
		case brokered:
			return credentialsBrokered, nil
		case hasKey:
			return credentialsNone, ErrSendRoleHoldsMasterKey
		default:
			return credentialsNone, ErrSendRoleNeedsBroker
		}
	}

	switch {
	case brokered:
		return credentialsBrokered, nil
	case hasKey:
		return credentialsLocal, nil
	case role == worker.RoleControl:
		// A control-role worker registers only the periodic sweeps
		// (internal/worker.registerScheduled), none of which opens a mailbox
		// credential or a webhook secret. Requiring a key or a broker here would
		// be a setting with nothing behind it.
		return credentialsNone, nil
	default:
		return credentialsNone, ErrNoCredentialSource
	}
}

// credentialWiring is what the composition root got back: at most one of the
// two is non-nil, matching the mode.
type credentialWiring struct {
	mode credentialMode
	// keyring is non-nil only in credentialsLocal. It is passed to
	// inprocess.New and to the webhook service; nil everywhere else, which both
	// of those treat as "this process cannot seal or open" rather than
	// panicking.
	keyring *crypto.Keyring
	// broker is non-nil only in credentialsBrokered.
	broker credbroker.Opener
}

// buildCredentialWiring turns the resolved mode into the concrete dependency.
// It is the only place cmd/worker touches a key or a broker.
func buildCredentialWiring(cfg *config.Config, role worker.Role, q *gen.Queries, logger *slog.Logger) (credentialWiring, error) {
	mode, err := resolveCredentialMode(cfg, role)
	if err != nil {
		return credentialWiring{}, err
	}
	switch mode {
	case credentialsLocal:
		kr, err := keys.BuildKeyring(cfg, q)
		if err != nil {
			return credentialWiring{}, err
		}
		logger.Info("credential source", "mode", mode.String(),
			"note", "this worker holds INROAD_MASTER_KEY and opens credentials itself")
		return credentialWiring{mode: mode, keyring: kr}, nil
	case credentialsBrokered:
		opener, err := credbroker.NewHTTPOpener(cfg.FleetBrokerURL, cfg.FleetBrokerToken, cfg.WorkerID, cfg.FleetBrokerAllowPlaintext)
		if err != nil {
			return credentialWiring{}, err
		}
		note := "this worker holds no master key; every credential is opened by the control plane"
		if cfg.WorkerID == "" {
			// Matches the heartbeat's own tolerance for an unset worker id (no
			// per-IP affinity queue either, in that case) — this worker still
			// brokers correctly, it just gets the broker's WIDER answer: any
			// mailbox a valid token names, not only the ones assigned to it.
			note += "; worker id is empty, so the broker cannot narrow answers to this worker's assigned mailboxes"
		}
		logger.Info("credential source", "mode", mode.String(), "broker", cfg.FleetBrokerURL, "note", note)
		return credentialWiring{mode: mode, broker: opener}, nil
	default:
		logger.Warn("credential source", "mode", mode.String(),
			"note", "this worker can open no credential; it registers no handler that needs one, and any path that did would fail closed")
		return credentialWiring{mode: mode}, nil
	}
}

// coreOptions returns the inprocess options this wiring implies. Brokered mode
// installs the remote opener; the other two leave inprocess with the keyring it
// was handed (nil in credentialsNone, which fails closed).
func (w credentialWiring) coreOptions() []inprocess.Option {
	if w.broker == nil {
		return nil
	}
	return []inprocess.Option{inprocess.WithCredentialBroker(w.broker)}
}
