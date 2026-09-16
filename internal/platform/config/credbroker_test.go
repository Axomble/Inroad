package config

import (
	"os"
	"strings"
	"testing"

	"github.com/inroad/inroad/internal/platform/credbroker"
)

// clearKeyAndBroker puts the process into the state a fleet worker's
// environment is in: no master key, no broker settings. t.Setenv cannot unset,
// so this restores explicitly.
func clearKeyAndBroker(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"INROAD_MASTER_KEY",
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
}

// An ABSENT master key loads to a nil MasterKey rather than an error. That is
// the whole point of the change: a fleet worker must be able to start without
// one. Each binary asserts what it needs (cmd/inroad unconditionally,
// cmd/worker per role) — config no longer decides for all four.
func TestAnAbsentMasterKeyLoadsAsNil(t *testing.T) {
	clearKeyAndBroker(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.MasterKey != nil {
		t.Errorf("MasterKey = %v, want nil when INROAD_MASTER_KEY is unset", cfg.MasterKey)
	}
}

// A SET-BUT-BROKEN master key is still a hard error. Relaxing "absent" must not
// relax "mistyped": a truncated or non-base64 value that silently became "this
// process has no key" would turn a typo into a fleet-wide behaviour change.
func TestAMalformedMasterKeyIsStillRefused(t *testing.T) {
	for _, tc := range []struct{ name, value, wantSubstr string }{
		{"not base64", "not-base64-!!!", "valid base64"},
		{"wrong length", "MDEyMzQ1Njc4OQ==", "32 bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearKeyAndBroker(t)
			t.Setenv("INROAD_MASTER_KEY", tc.value)

			_, err := Load()
			if err == nil {
				t.Fatalf("Load() succeeded with INROAD_MASTER_KEY=%q, want an error", tc.value)
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("err = %v, want it to mention %q", err, tc.wantSubstr)
			}
		})
	}
}

// Configuring either side of the broker without a strong token fails at
// startup, naming the variable — not at the first send, as a 401.
func TestTheBrokerTokenFloorIsEnforcedOnBothSides(t *testing.T) {
	weak := strings.Repeat("a", credbroker.MinTokenLen-1)
	for _, tc := range []struct{ name, key, value string }{
		{"server side", "INROAD_FLEET_BROKER_ADDR", ":8090"},
		{"client side", "INROAD_FLEET_BROKER_URL", "https://control.internal:8090"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("missing token", func(t *testing.T) {
				clearKeyAndBroker(t)
				t.Setenv(tc.key, tc.value)
				if _, err := Load(); err == nil {
					t.Fatal("Load() succeeded with no INROAD_FLEET_BROKER_TOKEN, want an error")
				}
			})
			t.Run("weak token", func(t *testing.T) {
				clearKeyAndBroker(t)
				t.Setenv(tc.key, tc.value)
				t.Setenv("INROAD_FLEET_BROKER_TOKEN", weak)
				_, err := Load()
				if err == nil {
					t.Fatal("Load() succeeded with a short token, want an error")
				}
				if !strings.Contains(err.Error(), "INROAD_FLEET_BROKER_TOKEN") {
					t.Errorf("err = %v, want it to name INROAD_FLEET_BROKER_TOKEN", err)
				}
			})
		})
	}
}

// An installation that sets none of the broker variables gets zero values and
// no error — the self-host path must not acquire a new required setting.
func TestNoBrokerSettingsIsNotAnError(t *testing.T) {
	clearKeyAndBroker(t)
	t.Setenv("INROAD_MASTER_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.FleetBrokerAddr != "" || cfg.FleetBrokerURL != "" || cfg.FleetBrokerToken != "" {
		t.Errorf("broker settings = %q/%q/%q, want all empty", cfg.FleetBrokerAddr, cfg.FleetBrokerURL, cfg.FleetBrokerToken)
	}
	if cfg.FleetBrokerAllowPlaintext {
		t.Error("FleetBrokerAllowPlaintext = true, want false by default")
	}
}

// The plaintext opt-out fails closed exactly like the system-SMTP and S3 ones:
// only an explicitly truthy value relaxes https.
func TestFleetBrokerAllowPlaintextFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		want        bool
	}{
		{"unset", "", false},
		{"false", "false", false},
		{"garbage", "sure-why-not", false},
		{"true", "true", true},
		{"one", "1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearKeyAndBroker(t)
			t.Setenv("INROAD_MASTER_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
			if tc.value != "" {
				t.Setenv("INROAD_FLEET_BROKER_ALLOW_PLAINTEXT", tc.value)
			}
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			if cfg.FleetBrokerAllowPlaintext != tc.want {
				t.Errorf("FleetBrokerAllowPlaintext with %q = %v, want %v", tc.value, cfg.FleetBrokerAllowPlaintext, tc.want)
			}
		})
	}
}
