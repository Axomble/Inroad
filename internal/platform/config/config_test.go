package config

import (
	"encoding/base64"
	"os"
	"testing"
	"time"
)

func TestLoadDefaultsAndOverrides(t *testing.T) {
	t.Setenv("INROAD_ENV", "production")
	t.Setenv("INROAD_JWT_SECRET", "0123456789abcdef0123456789abcdef")
	// 32 raw bytes, base64-encoded:
	t.Setenv("INROAD_MASTER_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	os.Unsetenv("INROAD_HTTP_ADDR")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Env != "production" {
		t.Errorf("Env = %q, want production", cfg.Env)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want default :8080", cfg.HTTPAddr)
	}
	if len(cfg.MasterKey) != 32 {
		t.Errorf("MasterKey len = %d, want 32", len(cfg.MasterKey))
	}
}

// TestSystemSMTPAllowPlaintextFailsClosed pins the default for the
// transactional cleartext opt-out. Only an explicitly truthy value may relax
// TLS; unset, empty, false, and anything unparseable must all stay false, so a
// typo in production configuration cannot downgrade system email to cleartext.
func TestSystemSMTPAllowPlaintextFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		want        bool
	}{
		{"unset", "", false},
		{"false", "false", false},
		{"zero", "0", false},
		{"garbage", "sure-why-not", false},
		{"true", "true", true},
		{"one", "1", true},
		{"yes", "yes", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("INROAD_JWT_SECRET", "0123456789abcdef0123456789abcdef")
			t.Setenv("INROAD_MASTER_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
			t.Setenv("INROAD_SYSTEM_SMTP_ALLOW_PLAINTEXT", tc.value)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load(): %v", err)
			}
			if cfg.SystemSMTPAllowPlaintext != tc.want {
				t.Fatalf("SystemSMTPAllowPlaintext with %q = %v, want %v", tc.value, cfg.SystemSMTPAllowPlaintext, tc.want)
			}
		})
	}
}

func TestWebauthnDefaults(t *testing.T) {
	cases := []struct {
		name       string
		publicURL  string
		wantID     string
		wantOrigin string
	}{
		{"host with port", "http://localhost:8080", "localhost", "http://localhost:8080"},
		{"https no port", "https://app.example.com", "app.example.com", "https://app.example.com"},
		{"https with port", "https://app.example.com:9443", "app.example.com", "https://app.example.com:9443"},
		{"empty stays empty", "", "", ""},
		{"no scheme stays empty", "app.example.com", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, origin := webauthnDefaults(tc.publicURL)
			if id != tc.wantID || origin != tc.wantOrigin {
				t.Fatalf("webauthnDefaults(%q) = (%q, %q), want (%q, %q)",
					tc.publicURL, id, origin, tc.wantID, tc.wantOrigin)
			}
		})
	}
}

func TestLoadDerivesRPFromPublicURL(t *testing.T) {
	t.Setenv("INROAD_JWT_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("INROAD_MASTER_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	t.Setenv("INROAD_PUBLIC_URL", "https://app.example.com")
	os.Unsetenv("INROAD_RP_ID")
	os.Unsetenv("INROAD_RP_ORIGIN")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.RPID != "app.example.com" {
		t.Errorf("RPID = %q, want app.example.com", cfg.RPID)
	}
	if cfg.RPOrigin != "https://app.example.com" {
		t.Errorf("RPOrigin = %q, want https://app.example.com", cfg.RPOrigin)
	}
}

func TestLoadRejectsMissingSecret(t *testing.T) {
	t.Setenv("INROAD_JWT_SECRET", "")
	t.Setenv("INROAD_MASTER_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for empty JWT secret, got nil")
	}
}

// TestLoadTrackingSecretFallsBackToJWTSecret proves that with
// INROAD_TRACKING_SECRET unset, TrackingSecret inherits JWTSecret (which
// already met the 16-byte floor) rather than being left empty.
func TestLoadTrackingSecretFallsBackToJWTSecret(t *testing.T) {
	t.Setenv("INROAD_JWT_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("INROAD_MASTER_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	os.Unsetenv("INROAD_TRACKING_SECRET")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if string(cfg.TrackingSecret) != string(cfg.JWTSecret) {
		t.Fatalf("TrackingSecret = %q, want it to fall back to JWTSecret %q", cfg.TrackingSecret, cfg.JWTSecret)
	}
}

// TestLoadTrackingSecretExplicitOverridesJWTSecret proves an explicitly-set
// (and sufficiently long) INROAD_TRACKING_SECRET is used as-is, not the
// JWTSecret fallback.
func TestLoadTrackingSecretExplicitOverridesJWTSecret(t *testing.T) {
	t.Setenv("INROAD_JWT_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("INROAD_MASTER_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	t.Setenv("INROAD_TRACKING_SECRET", "fedcba9876543210fedcba9876543210")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if string(cfg.TrackingSecret) != "fedcba9876543210fedcba9876543210" {
		t.Fatalf("TrackingSecret = %q, want the explicit value", cfg.TrackingSecret)
	}
}

// TestLoadRejectsWeakTrackingSecret proves an explicitly-set but too-short
// INROAD_TRACKING_SECRET fails closed, mirroring the JWT secret's floor,
// rather than silently signing tracking tokens with a guessable key.
func TestLoadRejectsWeakTrackingSecret(t *testing.T) {
	t.Setenv("INROAD_JWT_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("INROAD_MASTER_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	t.Setenv("INROAD_TRACKING_SECRET", "tooshort")

	if _, err := Load(); err == nil {
		t.Fatal("expected error for a too-short tracking secret, got nil")
	}
}

func TestLoadTokenDefaults(t *testing.T) {
	t.Setenv("INROAD_JWT_SECRET", "0123456789abcdef")
	t.Setenv("INROAD_MASTER_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	// Pin (clear) every env var whose DEFAULT this test asserts, so an ambient .env
	// (e.g. INROAD_ACCESS_TOKEN_TTL=15m) can't make the defaults check fail. An empty
	// value is treated as unset by the getenv helpers, yielding the compiled default.
	t.Setenv("INROAD_ACCESS_TOKEN_TTL", "")
	t.Setenv("INROAD_REFRESH_TOKEN_TTL", "")
	t.Setenv("INROAD_SESSION_CACHE_TTL", "")
	t.Setenv("INROAD_COOKIE_SECURE", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.AccessTokenTTL != 5*time.Minute {
		t.Fatalf("access ttl = %v", cfg.AccessTokenTTL)
	}
	if cfg.RefreshTokenTTL != 720*time.Hour {
		t.Fatalf("refresh ttl = %v", cfg.RefreshTokenTTL)
	}
	if cfg.SessionCacheTTL != 5*time.Second {
		t.Fatalf("session cache ttl = %v", cfg.SessionCacheTTL)
	}
	if !cfg.CookieSecure {
		t.Fatal("cookie secure should default true")
	}
}

// TestWorkerQueueDefaults proves the worker's queue set defaults to its own
// per-IP queue plus the shared default when INROAD_WORKER_QUEUES is unset, and
// that an explicit id override is honored.
func TestWorkerQueueDefaults(t *testing.T) {
	t.Setenv("INROAD_JWT_SECRET", "0123456789abcdef")
	t.Setenv("INROAD_MASTER_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("INROAD_WORKER_ID", "node-a")
	t.Setenv("INROAD_WORKER_QUEUES", "")
	t.Setenv("INROAD_WORKER_EGRESS_IP", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.WorkerID != "node-a" {
		t.Fatalf("WorkerID = %q, want node-a", cfg.WorkerID)
	}
	want := []string{"w:node-a", "default"}
	if len(cfg.WorkerQueues) != len(want) {
		t.Fatalf("WorkerQueues = %v, want %v", cfg.WorkerQueues, want)
	}
	for i := range want {
		if cfg.WorkerQueues[i] != want[i] {
			t.Fatalf("WorkerQueues[%d] = %q, want %q", i, cfg.WorkerQueues[i], want[i])
		}
	}
}

// TestWorkerQueuesOverride proves INROAD_WORKER_QUEUES is parsed as a trimmed,
// ordered CSV that fully replaces the default set.
func TestWorkerQueuesOverride(t *testing.T) {
	t.Setenv("INROAD_JWT_SECRET", "0123456789abcdef")
	t.Setenv("INROAD_MASTER_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("INROAD_WORKER_QUEUES", " w:node-a , default , critical ")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := []string{"w:node-a", "default", "critical"}
	if len(cfg.WorkerQueues) != len(want) {
		t.Fatalf("WorkerQueues = %v, want %v", cfg.WorkerQueues, want)
	}
	for i := range want {
		if cfg.WorkerQueues[i] != want[i] {
			t.Fatalf("WorkerQueues[%d] = %q, want %q", i, cfg.WorkerQueues[i], want[i])
		}
	}
}

// TestDefaultWorkerQueuesEmptyID proves a worker with no resolvable id falls
// back to the shared default queue only (it can't own a stable per-IP queue).
func TestDefaultWorkerQueuesEmptyID(t *testing.T) {
	got := defaultWorkerQueues("")
	if len(got) != 1 || got[0] != "default" {
		t.Fatalf("defaultWorkerQueues(\"\") = %v, want [default]", got)
	}
}

// TestLoadMetricsAddrDefaultsToDisabled proves the Prometheus listener is off
// by default (empty INROAD_METRICS_ADDR) — self-hosters who don't run
// Prometheus get no extra open port.
func TestLoadMetricsAddrDefaultsToDisabled(t *testing.T) {
	t.Setenv("INROAD_JWT_SECRET", "0123456789abcdef")
	t.Setenv("INROAD_MASTER_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("INROAD_METRICS_ADDR", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.MetricsAddr != "" {
		t.Fatalf("MetricsAddr = %q, want empty (disabled) by default", cfg.MetricsAddr)
	}
}

// TestLoadMetricsAddrOverride proves an explicit INROAD_METRICS_ADDR is
// honored verbatim.
func TestLoadMetricsAddrOverride(t *testing.T) {
	t.Setenv("INROAD_JWT_SECRET", "0123456789abcdef")
	t.Setenv("INROAD_MASTER_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("INROAD_METRICS_ADDR", ":9091")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.MetricsAddr != ":9091" {
		t.Fatalf("MetricsAddr = %q, want :9091", cfg.MetricsAddr)
	}
}
