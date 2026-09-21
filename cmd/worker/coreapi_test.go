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
//
// INROAD_DATABASE_URL is cleared too, and that one is not cosmetic. Slice 4
// made "was a DSN given to this process" a startup decision for role=send, and
// leaving the variable to whatever the developer's shell or CI happens to
// export would make these tests pass or fail on the machine rather than on the
// code. A test that needs it SET says so explicitly.
func clearCoreAPIEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"INROAD_FLEET_BROKER_ADDR",
		"INROAD_FLEET_BROKER_URL",
		"INROAD_FLEET_BROKER_TOKEN",
		"INROAD_FLEET_BROKER_ALLOW_PLAINTEXT",
		"INROAD_FLEET_COREAPI_REMOTE",
		"INROAD_DATABASE_URL",
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
// of the fleet — which is every self-hosted installation — resolves to the
// in-process client, so cmd/worker opens its pool and inprocess.New builds
// exactly the client it built before any of this existed.
//
// It drives the real config.Load rather than a hand-built Config on purpose: a
// struct literal would keep passing if the environment default flipped, and the
// default is the thing being protected.
//
// role=send is deliberately NOT in this list any more, and that is slice 4's
// change rather than a gap: a send worker from a self-host environment has no
// broker to read from and is refused at startup (see the test below). The two
// roles here are the two a machine with a database actually runs.
func TestSelfHostDefaultsToTheInProcessCoreAPI(t *testing.T) {
	clearCoreAPIEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load(): %v", err)
	}

	for _, role := range []worker.Role{worker.RoleAll, worker.RoleControl} {
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

	// A local-mode wiring builds NOTHING, even when a broker happens to be
	// available: the mode is the decision, not the availability of a dependency.
	// A nil client is what tells the composition root to open a pool and build
	// the in-process client, so this assertion is the self-host guarantee.
	wiring, err := buildCoreAPIWiring(cfg, coreAPILocal, &stubOpener{}, quiet())
	if err != nil {
		t.Fatalf("buildCoreAPIWiring: %v", err)
	}
	if wiring.client != nil {
		t.Error("self-host wiring carries a remote coreapi client, want none")
	}
	if c := wiring.coreClient(); c != nil {
		t.Errorf("coreClient() = %T on the self-host path, want an untyped nil", c)
	}
}

// And the self-host environment's OTHER answer: a role=send worker started
// there refuses, because there is nowhere for it to read from and it will not
// open a pool instead.
//
// Same real config.Load, same cleared environment — the point is that the
// refusal comes from the configuration an operator actually has, not from a
// struct literal assembled to produce it.
func TestASendWorkerWithNoFleetChannelRefusesToStart(t *testing.T) {
	clearCoreAPIEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load(): %v", err)
	}
	if _, err := resolveCoreAPIMode(cfg, worker.RoleSend); !errors.Is(err, ErrCoreAPIRemoteNeedsFleetURL) {
		t.Fatalf("err = %v, want ErrCoreAPIRemoteNeedsFleetURL", err)
	}
}

// A send worker with the fleet channel configured reads remotely — WITHOUT the
// flag, which is slice 4's change. The role decides, because the role is what
// determines whether there is a pool.
func TestTheRemoteCoreAPIResolvesForASendWorkerWithoutTheFlag(t *testing.T) {
	clearCoreAPIEnv(t)
	t.Setenv("INROAD_FLEET_BROKER_URL", brokerURL)
	t.Setenv("INROAD_FLEET_BROKER_TOKEN", brokerToken)
	// A fleet host holds no master key either (F2, #207). Clearing it here keeps
	// this configuration one a real role=send worker could actually boot with.
	t.Setenv("INROAD_MASTER_KEY", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load(): %v", err)
	}
	if cfg.FleetCoreAPIRemote {
		t.Fatal("INROAD_FLEET_COREAPI_REMOTE read as true with nothing set; this test would prove nothing")
	}

	mode, err := resolveCoreAPIMode(cfg, worker.RoleSend)
	if err != nil {
		t.Fatalf("resolveCoreAPIMode: %v", err)
	}
	if mode != coreAPIRemote {
		t.Fatalf("mode = %v, want %v — a role=send worker reads remotely because it has no pool", mode, coreAPIRemote)
	}

	wiring, err := buildCoreAPIWiring(cfg, mode, &stubOpener{}, quiet())
	if err != nil {
		t.Fatalf("buildCoreAPIWiring: %v", err)
	}
	if wiring.client == nil {
		t.Fatal("remote wiring carries no coreapi client")
	}
	// The whole point of the slice: what the composition root gets back IS the
	// coreapi.Client, so there is no in-process client and no pool behind it.
	if wiring.coreClient() == nil {
		t.Fatal("coreClient() = nil for a remote wiring")
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

// THE REFUSAL THIS SLICE EXISTS FOR: a role=send worker handed
// INROAD_DATABASE_URL does not start.
//
// Driven through the real config.Load, because the thing being tested is
// whether the VARIABLE WAS PRESENT — a question a struct literal cannot pose.
// INROAD_DATABASE_URL has a local development default, so cfg.DatabaseURL is
// never empty and a check on its value would pass vacuously for every worker;
// the refusal reads cfg.DatabaseURLSet instead.
//
// It is checked BEFORE the missing-broker refusal, so an operator who has both
// problems hears about the one that is a security problem.
func TestASendWorkerGivenADatabaseURLRefusesToStart(t *testing.T) {
	clearCoreAPIEnv(t)
	t.Setenv("INROAD_FLEET_BROKER_URL", brokerURL)
	t.Setenv("INROAD_FLEET_BROKER_TOKEN", brokerToken)
	t.Setenv("INROAD_MASTER_KEY", "")
	t.Setenv("INROAD_DATABASE_URL", "postgres://inroad:inroad@db.internal:5432/inroad?sslmode=require")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load(): %v", err)
	}
	if !cfg.DatabaseURLSet {
		t.Fatal("config.Load did not record INROAD_DATABASE_URL as set; this test would prove nothing")
	}

	if _, err := resolveCoreAPIMode(cfg, worker.RoleSend); !errors.Is(err, ErrSendRoleHoldsDatabaseURL) {
		t.Fatalf("err = %v, want ErrSendRoleHoldsDatabaseURL", err)
	}

	// And it wins over the missing-broker refusal when both are wrong.
	t.Setenv("INROAD_FLEET_BROKER_URL", "")
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("config.Load(): %v", err)
	}
	if _, err := resolveCoreAPIMode(cfg, worker.RoleSend); !errors.Is(err, ErrSendRoleHoldsDatabaseURL) {
		t.Fatalf("err = %v, want ErrSendRoleHoldsDatabaseURL to win over the missing broker", err)
	}
}

// The OTHER half of that refusal, and the one that keeps self-host working: the
// same DSN on role=all and role=control is entirely normal and changes nothing.
// A refusal that fired on every role would break every compose file, Helm chart
// and Terraform config in the repository, all of which run RoleAll.
func TestADatabaseURLIsFineOnEveryOtherRole(t *testing.T) {
	clearCoreAPIEnv(t)
	t.Setenv("INROAD_DATABASE_URL", "postgres://inroad:inroad@db.internal:5432/inroad?sslmode=require")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load(): %v", err)
	}
	for _, role := range []worker.Role{worker.RoleAll, worker.RoleControl} {
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
}

// Fail closed at startup, not at the first send: a role=send worker with
// nowhere to read from refuses rather than starting and failing every job —
// and never falls back to opening a pool, which is the one outcome that would
// make the whole boundary a lie.
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
	for _, tc := range []struct {
		err  error
		name string
	}{
		{ErrCoreAPIRemoteNeedsSendRole, "INROAD_FLEET_COREAPI_REMOTE"},
		{ErrCoreAPIRemoteNeedsFleetURL, "INROAD_FLEET_BROKER_URL"},
		{ErrCoreAPIRemoteNeedsBroker, "INROAD_FLEET_BROKER_URL"},
		{ErrSendRoleHoldsDatabaseURL, "INROAD_DATABASE_URL"},
	} {
		if !strings.Contains(tc.err.Error(), tc.name) {
			t.Errorf("%v does not name %s", tc.err, tc.name)
		}
	}
	// And the DSN refusal says what to do about it, not just that it is wrong.
	// It is the one an operator is most likely to hit while migrating a worker
	// from role=all, where the variable was correct.
	if !strings.Contains(ErrSendRoleHoldsDatabaseURL.Error(), "unset it") {
		t.Errorf("%v does not tell the operator what to do", ErrSendRoleHoldsDatabaseURL)
	}
}
