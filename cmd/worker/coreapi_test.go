package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/config"
	"github.com/inroad/inroad/internal/platform/credbroker"
	"github.com/inroad/inroad/internal/worker"
)

// stubOpener stands in for the credential broker the remote coreapi client
// takes. It opens nothing: these tests are about which wiring a configuration
// resolves to, and no call reaches it.
type stubOpener struct{}

func (stubOpener) OpenMailbox(context.Context, credbroker.MailboxRef) (credbroker.MailboxSecret, error) {
	return credbroker.MailboxSecret{}, errors.New("stub opener")
}

func (stubOpener) OpenWebhookEndpointSecret(context.Context, uuid.UUID, uuid.UUID, []byte) ([]byte, error) {
	return nil, errors.New("stub opener")
}

// clearCoreAPIEnv puts the process into the environment a self-hosted install
// actually has: none of the fleet variables set. t.Setenv cannot unset, so this
// restores explicitly. The JWT secret is the one thing config.Load requires.
func clearCoreAPIEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"INROAD_FLEET_BROKER_ADDR",
		"INROAD_FLEET_BROKER_URL",
		"INROAD_FLEET_BROKER_TOKEN",
		"INROAD_FLEET_BROKER_ALLOW_PLAINTEXT",
		"INROAD_FLEET_COREAPI_REMOTE",
	} {
		prev, had := os.LookupEnv(k)
		if had {
			t.Cleanup(func() { _ = os.Setenv(k, prev) })
		} else {
			t.Cleanup(func() { _ = os.Unsetenv(k) })
		}
		_ = os.Unsetenv(k)
	}
	t.Setenv("INROAD_JWT_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("INROAD_MASTER_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
}

// THE self-host test. A worker started from an environment that has never heard
// of the remote coreapi — which is every self-hosted installation — resolves to
// the in-process client and installs NO option, so inprocess.New builds exactly
// the client it built before this slice existed.
//
// It drives the real config.Load rather than a hand-built Config on purpose: a
// struct literal would keep passing if the environment default flipped, and the
// default is the thing being protected.
func TestSelfHostDefaultsToTheInProcessCoreAPI(t *testing.T) {
	clearCoreAPIEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load(): %v", err)
	}

	for _, role := range []worker.Role{worker.RoleAll, worker.RoleControl, worker.RoleSend} {
		t.Run(string(role), func(t *testing.T) {
			mode, err := resolveCoreAPIMode(cfg, role)
			if err != nil {
				t.Fatalf("resolveCoreAPIMode: %v", err)
			}
			if mode != coreAPILocal {
				t.Errorf("mode = %v, want %v", mode, coreAPILocal)
			}
		})
	}

	// A local-mode wiring installs NOTHING, even when a broker happens to be
	// available: the mode is the decision, not the availability of a dependency.
	wiring, err := buildCoreAPIWiring(cfg, coreAPILocal, &stubOpener{}, quiet())
	if err != nil {
		t.Fatalf("buildCoreAPIWiring: %v", err)
	}
	if opts := wiring.coreOptions(); len(opts) != 0 {
		t.Errorf("coreOptions() returned %d options for the self-host path, want 0", len(opts))
	}
	if wiring.client != nil {
		t.Error("self-host wiring carries a remote coreapi client, want none")
	}
}

// Turned on for a send worker with the fleet channel configured, this is the
// only combination that moves a read onto the network.
func TestTheRemoteCoreAPIResolvesForAConfiguredSendWorker(t *testing.T) {
	cfg := &config.Config{
		FleetCoreAPIRemote: true,
		FleetBrokerURL:     brokerURL,
		FleetBrokerToken:   brokerToken,
	}
	mode, err := resolveCoreAPIMode(cfg, worker.RoleSend)
	if err != nil {
		t.Fatalf("resolveCoreAPIMode: %v", err)
	}
	if mode != coreAPIRemote {
		t.Fatalf("mode = %v, want %v", mode, coreAPIRemote)
	}

	wiring, err := buildCoreAPIWiring(cfg, mode, &stubOpener{}, quiet())
	if err != nil {
		t.Fatalf("buildCoreAPIWiring: %v", err)
	}
	if wiring.client == nil {
		t.Fatal("remote wiring carries no coreapi client")
	}
	// Two options after slice 2: the suppression source and the job source, both
	// satisfied by the one client.
	if got := len(wiring.coreOptions()); got != 2 {
		t.Errorf("coreOptions() returned %d options, want 2", got)
	}
}

// The remote coreapi transport depends on the credential broker, because the
// job responses it reads carry no credential. Without one a worker would fetch
// work it cannot do, and the failure would surface at a mailbox dial rather
// than at startup.
func TestTheRemoteCoreAPIWithoutACredentialBrokerRefusesToStart(t *testing.T) {
	cfg := &config.Config{
		FleetCoreAPIRemote: true,
		FleetBrokerURL:     brokerURL,
		FleetBrokerToken:   brokerToken,
	}
	if _, err := buildCoreAPIWiring(cfg, coreAPIRemote, nil, quiet()); !errors.Is(err, ErrCoreAPIRemoteNeedsBroker) {
		t.Fatalf("err = %v, want ErrCoreAPIRemoteNeedsBroker", err)
	}
}

// The two decisions this binary makes agree by construction: every role/config
// combination that resolves to remote coreapi ALSO resolves to brokered
// credentials, so the refusal above is unreachable through resolve* rather than
// merely unlikely. Driven through both real resolvers, not asserted by reading.
func TestRemoteCoreAPIAlwaysImpliesBrokeredCredentials(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  *config.Config
	}{
		{"send worker with the fleet channel", &config.Config{
			FleetCoreAPIRemote: true, FleetBrokerURL: brokerURL, FleetBrokerToken: brokerToken,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, role := range []worker.Role{worker.RoleAll, worker.RoleControl, worker.RoleSend} {
				coreMode, coreErr := resolveCoreAPIMode(tc.cfg, role)
				if coreErr != nil || coreMode != coreAPIRemote {
					continue // this role cannot read remotely; nothing to imply
				}
				credMode, credErr := resolveCredentialMode(tc.cfg, role)
				if credErr != nil {
					t.Fatalf("%s reads coreapi remotely but has no credential mode: %v", role, credErr)
				}
				if credMode != credentialsBrokered {
					t.Errorf("%s reads coreapi remotely with credential mode %v, want brokered", role, credMode)
				}
			}
		})
	}
}

// The flag is meaningful only for role=send: a control worker runs beside the
// API and a role=all worker IS the single-process self-host topology, so in
// both the pool is right there. Setting it on either is a misconfiguration and
// must be refused, not silently ignored — an operator who set it believes their
// worker stopped reading the database.
func TestTheRemoteCoreAPIIsRefusedForEveryRoleButSend(t *testing.T) {
	for _, role := range []worker.Role{worker.RoleAll, worker.RoleControl} {
		t.Run(string(role), func(t *testing.T) {
			cfg := &config.Config{
				FleetCoreAPIRemote: true,
				FleetBrokerURL:     brokerURL,
				FleetBrokerToken:   brokerToken,
			}
			if _, err := resolveCoreAPIMode(cfg, role); !errors.Is(err, ErrCoreAPIRemoteNeedsSendRole) {
				t.Fatalf("err = %v, want ErrCoreAPIRemoteNeedsSendRole", err)
			}
		})
	}
}

// Fail closed at startup, not at the first send: a worker told to read remotely
// with nowhere to read from refuses rather than starting and failing every
// suppression check — and never falls back to the local pool, which is the one
// outcome that would make the flag a lie.
func TestTheRemoteCoreAPIWithoutAFleetURLRefusesToStart(t *testing.T) {
	cfg := &config.Config{FleetCoreAPIRemote: true}
	if _, err := resolveCoreAPIMode(cfg, worker.RoleSend); !errors.Is(err, ErrCoreAPIRemoteNeedsFleetURL) {
		t.Fatalf("err = %v, want ErrCoreAPIRemoteNeedsFleetURL", err)
	}
}

// The channel's https rule applies here identically, because it is the same
// channel: the client refuses a plaintext URL at startup rather than sending a
// tenant's contact addresses in the clear.
func TestARemoteCoreAPIRefusesAPlaintextFleetURL(t *testing.T) {
	cfg := &config.Config{
		FleetCoreAPIRemote: true,
		FleetBrokerURL:     "http://control.internal:8090",
		FleetBrokerToken:   brokerToken,
	}
	if _, err := buildCoreAPIWiring(cfg, coreAPIRemote, &stubOpener{}, quiet()); !errors.Is(err, credbroker.ErrInsecureURL) {
		t.Fatalf("err = %v, want credbroker.ErrInsecureURL", err)
	}
}

// Every refusal names the variable an operator has to change. A startup error
// that says "misconfigured" costs an hour; one that says which line of the unit
// file is wrong costs a minute.
func TestTheCoreAPIRefusalsNameTheVariableToChange(t *testing.T) {
	for _, err := range []error{ErrCoreAPIRemoteNeedsSendRole, ErrCoreAPIRemoteNeedsFleetURL} {
		if !strings.Contains(err.Error(), "INROAD_FLEET_COREAPI_REMOTE") {
			t.Errorf("%v does not name INROAD_FLEET_COREAPI_REMOTE", err)
		}
	}
	if !strings.Contains(ErrCoreAPIRemoteNeedsFleetURL.Error(), "INROAD_FLEET_BROKER_URL") {
		t.Errorf("%v does not name INROAD_FLEET_BROKER_URL", ErrCoreAPIRemoteNeedsFleetURL)
	}
}
