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
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.AccessTokenTTL != 15*time.Minute {
		t.Fatalf("access ttl = %v", cfg.AccessTokenTTL)
	}
	if cfg.RefreshTokenTTL != 720*time.Hour {
		t.Fatalf("refresh ttl = %v", cfg.RefreshTokenTTL)
	}
	if !cfg.CookieSecure {
		t.Fatal("cookie secure should default true")
	}
}
