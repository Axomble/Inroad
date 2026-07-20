// Package log builds the application's structured logger.
package log

import (
	"log/slog"
	"os"
	"strings"
)

// New returns a JSON slog logger. Level resolution: an explicit
// INROAD_LOG_LEVEL (debug|info|warn|error) always wins; otherwise the
// environment name falls back to debug in development, info elsewhere.
// The explicit env var is the preferred lever now — the env-based fallback
// exists only to keep older deployments working without a config change.
func New(env string) *slog.Logger {
	level := envDefault(env)
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("INROAD_LOG_LEVEL"))); v != "" {
		level = parseLevel(v, level)
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	return slog.New(h)
}

// NewAt returns a JSON slog logger at the explicit level. Level parsing
// mirrors New; unknown levels fall back to slog.LevelInfo.
func NewAt(level string) *slog.Logger {
	lvl := parseLevel(strings.ToLower(strings.TrimSpace(level)), slog.LevelInfo)
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(h)
}

func envDefault(env string) slog.Level {
	if env == "development" {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

func parseLevel(v string, fallback slog.Level) slog.Level {
	switch v {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return fallback
	}
}
