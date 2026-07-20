// Package config loads runtime configuration from environment variables.
package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Env         string
	HTTPAddr    string
	DatabaseURL string
	RedisAddr   string
	JWTSecret   []byte
	MasterKey   []byte

	// MailAllowPrivateHosts permits mailbox SMTP/IMAP hosts on RFC1918/ULA
	// private ranges. Default true for self-hosted operators reaching internal
	// mail servers; set false for multi-tenant Cloud. Loopback, link-local
	// (incl. cloud metadata), and multicast are always blocked regardless.
	MailAllowPrivateHosts bool

	// PublicURL is the externally-reachable base URL used to build links
	// (e.g. unsubscribe) embedded in outbound email.
	PublicURL string

	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	CookieSecure    bool
	CookieDomain    string

	// WorkerConcurrency caps how many asynq tasks the worker processes
	// simultaneously. Default 10; tune per SMTP throughput.
	WorkerConcurrency int

	// LogLevel is one of debug/info/warn/error. When empty, the logger
	// falls back to env-based defaults (debug in development, info elsewhere).
	LogLevel string

	// TrustedProxies is a list of CIDRs whose X-Forwarded-For / X-Real-IP
	// headers the app will trust. Empty = trust none (default). Only the
	// leftmost IP of X-Forwarded-For is consumed.
	TrustedProxies []string
}

func Load() (*Config, error) {
	cfg := &Config{
		Env:         getenv("INROAD_ENV", "development"),
		HTTPAddr:    getenv("INROAD_HTTP_ADDR", ":8080"),
		DatabaseURL: getenv("INROAD_DATABASE_URL", "postgres://inroad:inroad@localhost:5432/inroad?sslmode=disable"),
		RedisAddr:   getenv("INROAD_REDIS_ADDR", "localhost:6379"),
	}

	secret := os.Getenv("INROAD_JWT_SECRET")
	if len(secret) < 16 {
		return nil, fmt.Errorf("INROAD_JWT_SECRET must be set and at least 16 bytes")
	}
	cfg.JWTSecret = []byte(secret)

	rawKey, err := base64.StdEncoding.DecodeString(os.Getenv("INROAD_MASTER_KEY"))
	if err != nil {
		return nil, fmt.Errorf("INROAD_MASTER_KEY must be valid base64: %w", err)
	}
	if len(rawKey) != 32 {
		return nil, fmt.Errorf("INROAD_MASTER_KEY must decode to 32 bytes, got %d", len(rawKey))
	}
	cfg.MasterKey = rawKey

	cfg.MailAllowPrivateHosts = getenvBool("INROAD_MAIL_ALLOW_PRIVATE_HOSTS", true)
	cfg.PublicURL = getenv("INROAD_PUBLIC_URL", "http://localhost:8080")

	cfg.AccessTokenTTL = getenvDuration("INROAD_ACCESS_TOKEN_TTL", 15*time.Minute)
	cfg.RefreshTokenTTL = getenvDuration("INROAD_REFRESH_TOKEN_TTL", 720*time.Hour)
	cfg.CookieSecure = getenvBool("INROAD_COOKIE_SECURE", true)
	cfg.CookieDomain = getenv("INROAD_COOKIE_DOMAIN", "")
	cfg.WorkerConcurrency = getenvInt("INROAD_WORKER_CONCURRENCY", 10)
	cfg.LogLevel = strings.ToLower(getenv("INROAD_LOG_LEVEL", ""))
	if raw := os.Getenv("INROAD_TRUSTED_PROXIES"); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			if s = strings.TrimSpace(s); s != "" {
				cfg.TrustedProxies = append(cfg.TrustedProxies, s)
			}
		}
	}

	return cfg, nil
}

func getenvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getenvBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
}

func getenvDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
