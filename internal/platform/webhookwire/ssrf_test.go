package webhookwire

import (
	"context"
	"errors"
	"testing"
)

// All cases use literal IPs so the resolver short-circuits without a DNS lookup
// — the test needs no network.
func TestVetURLBlocksHostileTargets(t *testing.T) {
	ctx := context.Background()
	blocked := []string{
		"http://127.0.0.1/hook",          // loopback v4
		"https://10.1.2.3/hook",          // RFC1918
		"https://192.168.0.5:9000/hook",  // RFC1918
		"https://172.16.9.9/hook",        // RFC1918
		"https://169.254.169.254/latest", // link-local (cloud metadata)
		"http://[::1]/hook",              // loopback v6
		"https://[fc00::1]/hook",         // ULA
		"https://[fe80::1]/hook",         // link-local v6
		"https://0.0.0.0/hook",           // unspecified
		"https://224.0.0.1/hook",         // multicast
	}
	for _, raw := range blocked {
		if _, err := VetURL(ctx, raw, false); !errors.Is(err, ErrBlockedURL) {
			t.Errorf("VetURL(%q) = %v, want ErrBlockedURL", raw, err)
		}
	}
}

func TestVetURLRejectsNonHTTPSchemes(t *testing.T) {
	ctx := context.Background()
	for _, raw := range []string{"ftp://8.8.8.8/x", "file:///etc/passwd", "gopher://8.8.8.8", "8.8.8.8/no-scheme"} {
		if _, err := VetURL(ctx, raw, false); !errors.Is(err, ErrBlockedURL) {
			t.Errorf("VetURL(%q) = %v, want ErrBlockedURL", raw, err)
		}
	}
}

func TestVetURLAllowsPublicTargets(t *testing.T) {
	ctx := context.Background()
	for _, raw := range []string{
		"https://8.8.8.8/hook",
		"http://1.1.1.1:8080/webhooks/inroad",
		"https://[2606:4700:4700::1111]/hook",
	} {
		if _, err := VetURL(ctx, raw, false); err != nil {
			t.Errorf("VetURL(%q) = %v, want nil", raw, err)
		}
	}
}

// The escape hatch relaxes loopback/private only — a link-local target stays
// blocked even with allowPrivate set, because the cloud metadata endpoint lives
// there.
func TestVetURLAllowPrivateStillBlocksLinkLocal(t *testing.T) {
	ctx := context.Background()
	if _, err := VetURL(ctx, "https://127.0.0.1/hook", true); err != nil {
		t.Errorf("VetURL(loopback, allowPrivate=true) = %v, want nil", err)
	}
	if _, err := VetURL(ctx, "https://10.0.0.1/hook", true); err != nil {
		t.Errorf("VetURL(rfc1918, allowPrivate=true) = %v, want nil", err)
	}
	if _, err := VetURL(ctx, "https://169.254.169.254/latest", true); !errors.Is(err, ErrBlockedURL) {
		t.Errorf("VetURL(metadata, allowPrivate=true) = %v, want ErrBlockedURL", err)
	}
}
