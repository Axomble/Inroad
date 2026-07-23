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

	// TrackingSecret signs open/click tracking tokens (internal/platform/track).
	// Dedicated so rotating tracking links doesn't invalidate sessions; falls
	// back to JWTSecret when unset, so self-hosters aren't forced to mint a
	// second secret on upgrade.
	TrackingSecret []byte

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

	// TransactionalDriver selects the notify.Sender used for system email:
	// "console" (default, logs only) or "smtp" (dials SystemSMTP*).
	TransactionalDriver string
	SystemSMTPHost      string
	SystemSMTPPort      int
	SystemSMTPUsername  string
	SystemSMTPPassword  string
	SystemEmailFrom     string

	// AppBaseURL is the frontend origin used to build links (verify/reset/
	// invite) embedded in transactional email.
	AppBaseURL string

	// Google OAuth (mailbox connect via Gmail). Empty client id/secret disables
	// Gmail OAuth: the start endpoint returns 501 and gmail jobs fail cleanly.
	GoogleClientID     string
	GoogleClientSecret string
	GoogleRedirectURL  string

	EmailVerifyTTL   time.Duration
	PasswordResetTTL time.Duration
	InviteTTL        time.Duration
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

	if trackingSecret := os.Getenv("INROAD_TRACKING_SECRET"); trackingSecret != "" {
		// Same floor as INROAD_JWT_SECRET: an explicitly-set weak secret fails
		// closed rather than silently signing tracking tokens with a guessable
		// key. The fallback below inherits JWTSecret, which already met this
		// bar, so it needs no separate check.
		if len(trackingSecret) < 16 {
			return nil, fmt.Errorf("INROAD_TRACKING_SECRET must be at least 16 bytes")
		}
		cfg.TrackingSecret = []byte(trackingSecret)
	} else {
		cfg.TrackingSecret = cfg.JWTSecret
	}

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

	cfg.TransactionalDriver = getenv("INROAD_TRANSACTIONAL_DRIVER", "console")
	cfg.SystemSMTPHost = getenv("INROAD_SYSTEM_SMTP_HOST", "")
	cfg.SystemSMTPPort = getenvInt("INROAD_SYSTEM_SMTP_PORT", 587)
	cfg.SystemSMTPUsername = getenv("INROAD_SYSTEM_SMTP_USERNAME", "")
	cfg.SystemSMTPPassword = getenv("INROAD_SYSTEM_SMTP_PASSWORD", "")
	cfg.SystemEmailFrom = getenv("INROAD_SYSTEM_EMAIL_FROM", "")
	cfg.AppBaseURL = getenv("INROAD_APP_BASE_URL", "http://localhost:5173")
	cfg.GoogleClientID = getenv("INROAD_GOOGLE_CLIENT_ID", "")
	cfg.GoogleClientSecret = getenv("INROAD_GOOGLE_CLIENT_SECRET", "")
	cfg.GoogleRedirectURL = getenv("INROAD_GOOGLE_REDIRECT_URL", cfg.PublicURL+"/oauth/google/callback")
	cfg.EmailVerifyTTL = getenvDuration("INROAD_EMAIL_VERIFY_TTL", 24*time.Hour)
	cfg.PasswordResetTTL = getenvDuration("INROAD_PASSWORD_RESET_TTL", time.Hour)
	cfg.InviteTTL = getenvDuration("INROAD_INVITE_TTL", 72*time.Hour)

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
