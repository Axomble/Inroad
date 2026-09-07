// Package redisconn turns the single INROAD_REDIS_ADDR value into go-redis
// connection options.
//
// It is the one place that understands the value can be either a bare host:port
// ("localhost:6379", the historical form and still the default) or a
// redis:// / rediss:// URL carrying a username, password, database number and
// TLS — which is what a managed Redis (ElastiCache with an auth token, Upstash,
// Redis Cloud) requires and what the address-only form could not express.
//
// The asynq task queue needs the same information in its own option shape; that
// mapping lives in package queue (which already owns the asynq dependency) and
// is built from Parse's result.
package redisconn

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/redis/go-redis/v9"
)

// looksLikeURL reports whether s carries an explicit scheme. A bare host:port
// has no "://" and is passed straight through as an Addr, unchanged from the
// pre-URL behaviour.
func looksLikeURL(s string) bool { return strings.Contains(s, "://") }

// Parse converts addr into go-redis options. A bare host:port never fails; a
// URL fails only when it is malformed or carries an unsupported scheme.
func Parse(addr string) (*redis.Options, error) {
	if !looksLikeURL(addr) {
		return &redis.Options{Addr: addr}, nil
	}
	opt, err := redis.ParseURL(addr)
	if err != nil {
		return nil, fmt.Errorf("redis URL %q: %w", addr, err)
	}
	return opt, nil
}

// Validate returns the error Parse would, and nothing else. config.Load calls
// it so a broken URL fails at startup with a clear message rather than inside a
// constructor after the process is half wired.
func Validate(addr string) error {
	_, err := Parse(addr)
	return err
}

// MustOptions is Parse for callers that cannot return an error — the Redis
// client constructors, which run after config.Load has already validated the
// value. A parse failure here means config was not loaded first: a programming
// error, not an operator one.
func MustOptions(addr string) *redis.Options {
	opt, err := Parse(addr)
	if err != nil {
		panic("redisconn: " + err.Error() + " (config.Load should have rejected this)")
	}
	return opt
}

// Redact returns addr with any userinfo (a password especially) removed, so it
// is safe to put in a log line. A bare host:port is returned unchanged.
func Redact(addr string) string {
	if !looksLikeURL(addr) {
		return addr
	}
	u, err := url.Parse(addr)
	if err != nil {
		return "redis://<unparseable>"
	}
	u.User = nil
	return u.String()
}
