package config

import "testing"

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
// truthy value moves a worker's reads onto the network. Unset, empty, false or
// a typo all keep the in-process path, because a configuration mistake must
// never be able to change where a worker gets its data.
func TestTheRemoteCoreAPIFlagFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		want        bool
	}{
		{"unset", "", false},
		{"false", "false", false},
		{"typo", "ture", false},
		{"garbage", "sure-why-not", false},
		{"true", "true", true},
		{"one", "1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearKeyAndBroker(t)
			t.Setenv("INROAD_MASTER_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
			if tc.value != "" {
				t.Setenv("INROAD_FLEET_COREAPI_REMOTE", tc.value)
			}
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			if cfg.FleetCoreAPIRemote != tc.want {
				t.Errorf("FleetCoreAPIRemote with %q = %v, want %v", tc.value, cfg.FleetCoreAPIRemote, tc.want)
			}
		})
	}
}
