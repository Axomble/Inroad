package main

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/inroad/inroad/internal/platform/config"
	"github.com/inroad/inroad/internal/platform/credbroker"
	"github.com/inroad/inroad/internal/worker"
)

const (
	brokerURL   = "https://control.internal:8090"
	brokerToken = "0123456789abcdef0123456789abcdef" // credbroker.MinTokenLen
)

func masterKey() []byte { return []byte("0123456789abcdef0123456789abcdef") }

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// THE boundary test. A role=send worker — the role role.go describes as "the
// role intended for a fleet host" — must refuse to start while INROAD_MASTER_KEY
// is set, because that key unwraps every workspace's DEK and therefore every
// stored SMTP password and OAuth refresh token in the installation.
//
// This fails against the code before credential brokering existed, where
// cmd/worker called keys.BuildKeyring(cfg, …) for every role and started
// happily. That is the whole point of the change: the refusal IS the boundary.
func TestASendRoleWorkerRefusesToHoldTheMasterKey(t *testing.T) {
	// With a broker configured as well — the "I added the broker but forgot to
	// remove the key" misconfiguration, which would otherwise be silent.
	t.Run("even with a broker configured", func(t *testing.T) {
		cfg := &config.Config{MasterKey: masterKey(), FleetBrokerURL: brokerURL, FleetBrokerToken: brokerToken}
		_, err := resolveCredentialMode(cfg, worker.RoleSend)
		if !errors.Is(err, ErrSendRoleHoldsMasterKey) {
			t.Fatalf("err = %v, want ErrSendRoleHoldsMasterKey", err)
		}
	})
	// And with no broker — the pre-change configuration exactly.
	t.Run("and with no broker at all", func(t *testing.T) {
		cfg := &config.Config{MasterKey: masterKey()}
		_, err := resolveCredentialMode(cfg, worker.RoleSend)
		if !errors.Is(err, ErrSendRoleHoldsMasterKey) {
			t.Fatalf("err = %v, want ErrSendRoleHoldsMasterKey", err)
		}
	})
}

// The other half of failing closed: a send-role worker that holds no key AND
// has no broker must refuse at startup rather than start and fail every job —
// and must never fall back to anything weaker.
func TestASendRoleWorkerWithoutABrokerRefusesToStart(t *testing.T) {
	_, err := resolveCredentialMode(&config.Config{}, worker.RoleSend)
	if !errors.Is(err, ErrSendRoleNeedsBroker) {
		t.Fatalf("err = %v, want ErrSendRoleNeedsBroker", err)
	}
}

// The configuration this change exists to make possible.
func TestASendRoleWorkerWithABrokerAndNoKeyIsBrokered(t *testing.T) {
	cfg := &config.Config{FleetBrokerURL: brokerURL, FleetBrokerToken: brokerToken}
	mode, err := resolveCredentialMode(cfg, worker.RoleSend)
	if err != nil {
		t.Fatalf("resolveCredentialMode: %v", err)
	}
	if mode != credentialsBrokered {
		t.Fatalf("mode = %v, want brokered", mode)
	}
}

// Self-host must not regress. A RoleAll worker that sets INROAD_MASTER_KEY and
// nothing else — which is every single-process install — keeps the local
// keyring. No new variable, no new concept, no behaviour change.
func TestSelfHostRoleAllIsUnchanged(t *testing.T) {
	for _, role := range []worker.Role{worker.RoleAll, ""} {
		mode, err := resolveCredentialMode(&config.Config{MasterKey: masterKey()}, role)
		if err != nil {
			t.Fatalf("role %q: %v", role, err)
		}
		if mode != credentialsLocal {
			t.Errorf("role %q: mode = %v, want local", role, mode)
		}
	}
}

// RoleAll with neither a key nor a broker cannot open anything, and the
// single-process topology definitely needs to. Refuse, do not start degraded.
func TestRoleAllWithNoCredentialSourceRefusesToStart(t *testing.T) {
	_, err := resolveCredentialMode(&config.Config{}, worker.RoleAll)
	if !errors.Is(err, ErrNoCredentialSource) {
		t.Fatalf("err = %v, want ErrNoCredentialSource", err)
	}
}

// A control-role worker registers only the periodic sweeps
// (internal/worker.registerScheduled), none of which opens a mailbox credential
// or a webhook secret — so neither a key nor a broker is required. It keeps the
// local keyring when one is set (no upgrade break for an existing control host)
// and otherwise runs with no credential source at all.
func TestAControlRoleWorkerNeedsNoCredentialSource(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  *config.Config
		want credentialMode
	}{
		{"neither", &config.Config{}, credentialsNone},
		{"key only, unchanged", &config.Config{MasterKey: masterKey()}, credentialsLocal},
		{"broker only", &config.Config{FleetBrokerURL: brokerURL, FleetBrokerToken: brokerToken}, credentialsBrokered},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode, err := resolveCredentialMode(tc.cfg, worker.RoleControl)
			if err != nil {
				t.Fatalf("resolveCredentialMode: %v", err)
			}
			if mode != tc.want {
				t.Errorf("mode = %v, want %v", mode, tc.want)
			}
		})
	}
}

// Holding the key while brokering is refused for EVERY role, not only send: a
// process configured to broker has no business also being able to decrypt
// everything itself. Without this, "I added the broker" would look done while
// the blast radius was untouched.
func TestHoldingTheKeyWhileBrokeringIsRefusedForEveryRole(t *testing.T) {
	for _, role := range []worker.Role{worker.RoleAll, worker.RoleControl, worker.RoleSend} {
		cfg := &config.Config{MasterKey: masterKey(), FleetBrokerURL: brokerURL, FleetBrokerToken: brokerToken}
		if _, err := resolveCredentialMode(cfg, role); err == nil {
			t.Errorf("role %s: resolveCredentialMode succeeded, want a refusal", role)
		}
	}
}

// buildCredentialWiring is what main() actually calls. A brokered worker must
// come back with an opener and NO keyring — the property that makes
// "this process cannot decrypt anything" true rather than merely intended.
func TestBrokeredWiringCarriesNoKeyring(t *testing.T) {
	cfg := &config.Config{FleetBrokerURL: brokerURL, FleetBrokerToken: brokerToken}
	w, err := buildCredentialWiring(cfg, worker.RoleSend, nil, quiet())
	if err != nil {
		t.Fatalf("buildCredentialWiring: %v", err)
	}
	if w.keyring != nil {
		t.Error("a brokered worker was handed a keyring")
	}
	if w.broker == nil {
		t.Fatal("a brokered worker was handed no opener")
	}
	if len(w.coreOptions()) != 1 {
		t.Errorf("coreOptions() = %d options, want exactly the broker option", len(w.coreOptions()))
	}
}

// A worker with no credential source installs no broker option, so the
// inprocess client keeps its (nil-keyring) local opener and every credential
// path fails closed with credbroker.ErrNotConfigured.
func TestNoCredentialSourceInstallsNoOpenerAndFailsClosed(t *testing.T) {
	w, err := buildCredentialWiring(&config.Config{}, worker.RoleControl, nil, quiet())
	if err != nil {
		t.Fatalf("buildCredentialWiring: %v", err)
	}
	if w.keyring != nil || w.broker != nil || len(w.coreOptions()) != 0 {
		t.Fatalf("wiring = %+v with %d options, want an empty wiring", w, len(w.coreOptions()))
	}
	// And the fallback really does refuse rather than panic.
	if _, err := (credbroker.Unconfigured{}).OpenMailbox(t.Context(), credbroker.MailboxRef{}); !errors.Is(err, credbroker.ErrNotConfigured) {
		t.Errorf("err = %v, want ErrNotConfigured", err)
	}
}

// A plaintext broker URL is refused at startup, not at the first send. The
// channel carries the bearer token and the decrypted credential.
func TestAPlaintextBrokerURLIsRefusedAtStartup(t *testing.T) {
	cfg := &config.Config{FleetBrokerURL: "http://control.internal:8090", FleetBrokerToken: brokerToken}
	_, err := buildCredentialWiring(cfg, worker.RoleSend, nil, quiet())
	if !errors.Is(err, credbroker.ErrInsecureURL) {
		t.Fatalf("err = %v, want ErrInsecureURL", err)
	}
	cfg.FleetBrokerAllowPlaintext = true
	if _, err := buildCredentialWiring(cfg, worker.RoleSend, nil, quiet()); err != nil {
		t.Fatalf("with the opt-out chosen: %v", err)
	}
}

// The refusal messages have to tell an operator what to change. A security
// failure nobody can act on gets worked around.
func TestTheRefusalsNameTheVariableToChange(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{ErrSendRoleHoldsMasterKey, "INROAD_MASTER_KEY"},
		{ErrSendRoleNeedsBroker, "INROAD_FLEET_BROKER_URL"},
		{ErrNoCredentialSource, "INROAD_MASTER_KEY"},
	} {
		if !strings.Contains(tc.err.Error(), tc.want) {
			t.Errorf("%v does not mention %s", tc.err, tc.want)
		}
	}
}
