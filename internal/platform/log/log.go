// Package log builds the application's structured logger.
package log

import (
	"log/slog"
	"os"
)

// New returns a JSON slog logger. In development it also lowers the level to Debug.
func New(env string) *slog.Logger {
	level := slog.LevelInfo
	if env == "development" {
		level = slog.LevelDebug
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	return slog.New(h)
}
