// Package config loads runtime configuration from environment variables.
package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"
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

	return cfg, nil
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
