package config

import (
	"strings"
	"testing"
)

// The whole promise of this slice to a self-hoster: setting nothing changes
// nothing. An installation that has never heard of INROAD_FLEET_COREAPI_REMOTE
// gets the in-process coreapi it has always had.
//
// This test fails the moment the default flips, which is what it is for.
func TestTheRemoteCoreAPIIsOffByDefault(t *testing.T) {
	clearKeyAndBroker(t)
	t.Setenv("INROAD_MASTER_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.FleetCoreAPIRemote {
		t.Error("FleetCoreAPIRemote = true with nothing set, want false")
	}
}

// The flag fails closed exactly like the plaintext opt-outs: only an explicitly
// truthy value moves a worker's reads onto the network. Unset, empty and false
// keep the in-process path, and a typo refuses to start — because a
// configuration mistake must never be able to change where a worker gets its
// data, and a worker that does not start has not changed it.
//
// Refusing beats the silent false this used to assert for the same reason the
// flag exists: an operator who set it and got the in-process path anyway had no
// way to tell, and would have gone looking at the broker.
func TestTheRemoteCoreAPIFlagFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		want        bool
		wantErr     bool
	}{
		{name: "unset", value: "", want: false},
		{name: "false", value: "false", want: false},
		{name: "typo", value: "ture", wantErr: true},
		{name: "garbage", value: "sure-why-not", wantErr: true},
		{name: "true", value: "true", want: true},
		{name: "one", value: "1", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearKeyAndBroker(t)
			t.Setenv("INROAD_MASTER_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
			if tc.value != "" {
				t.Setenv("INROAD_FLEET_COREAPI_REMOTE", tc.value)
			}
			cfg, err := Load()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Load() with %q = nil error (FleetCoreAPIRemote=%v), want a rejection", tc.value, cfg.FleetCoreAPIRemote)
				}
				if !strings.Contains(err.Error(), "INROAD_FLEET_COREAPI_REMOTE") {
					t.Fatalf("Load() error = %q, want it to name the variable", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			if cfg.FleetCoreAPIRemote != tc.want {
				t.Errorf("FleetCoreAPIRemote with %q = %v, want %v", tc.value, cfg.FleetCoreAPIRemote, tc.want)
			}
		})
	}
}
