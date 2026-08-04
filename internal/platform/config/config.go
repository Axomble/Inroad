// Package config loads runtime configuration from environment variables.
package config

import (
	"encoding/base64"
	"fmt"
	"net/url"
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

	// MetricsAddr is the address the dedicated Prometheus /metrics listener
	// binds to (e.g. ":9091"), started by both cmd/inroad and cmd/worker.
	// Empty (default) disables the listener entirely — self-hosters who don't
	// run Prometheus get no extra open port. This is a SEPARATE listener from
	// HTTPAddr: metrics is never mounted on the public API router, so serving
	// it raises no auth question.
	MetricsAddr string

	// KeyProvider selects the KEK backend that wraps per-workspace DEKs.
	// "local" (default) wraps under INROAD_MASTER_KEY; a cloud KMS is a future
	// drop-in. An unknown value fails closed at binary startup.
	KeyProvider string

	// TrackingSecret signs open/click tracking tokens (internal/platform/track).
	// Dedicated so rotating tracking links doesn't invalidate sessions; falls
	// back to JWTSecret when unset, so self-hosters aren't forced to mint a
	// second secret on upgrade.
	TrackingSecret []byte

	// WarmupSecret signs the X-Inroad-Warmup receipt token (internal/platform/
	// warmup) so the inbox poller can attribute a received warmup message back to
	// its send. Dedicated so rotating warmup tokens doesn't invalidate sessions or
	// tracking links; falls back to JWTSecret when unset, matching TrackingSecret.
	WarmupSecret []byte

	// MailAllowPrivateHosts permits mailbox SMTP/IMAP hosts on RFC1918/ULA
	// private ranges. Default true for self-hosted operators reaching internal
	// mail servers; set false for multi-tenant Cloud. Loopback, link-local
	// (incl. cloud metadata), and multicast are always blocked regardless.
	MailAllowPrivateHosts bool

	// PublicURL is the externally-reachable base URL used to build links
	// (e.g. unsubscribe) embedded in outbound email.
	PublicURL string

	// WebAuthn Relying Party. RPID is the registrable domain (host only, no scheme
	// or port) that passkey ceremonies are bound to; RPOrigin is the fully-qualified
	// origin (scheme://host[:port]) the browser must present. Both default from
	// INROAD_PUBLIC_URL's host/origin. When neither can be derived nor is set, the
	// passkey endpoints fail cleanly (the feature is effectively off) rather than
	// mis-validating a ceremony against a wrong domain.
	RPID     string
	RPOrigin string

	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	CookieSecure    bool
	CookieDomain    string

	// SessionCacheTTL is how long the store-backed access-token verifier caches
	// a session's auth-state (revocation/expiry/token_version) in-process before
	// re-reading it from Postgres. A revoke performed by THIS process busts the
	// entry immediately; an out-of-band change propagates within at most this
	// TTL. Set <= 0 to disable the cache (every request hits the DB). Kept short
	// because it bounds revocation-propagation latency across replicas.
	SessionCacheTTL time.Duration

	// WorkerConcurrency caps how many asynq tasks the worker processes
	// simultaneously. Default 10; tune per SMTP throughput.
	WorkerConcurrency int

	// --- Worker identity + per-IP routing (spec §15) ---

	// WorkerID is this worker's stable id (default: OS hostname). It keys the
	// `workers` heartbeat row and names the worker's dedicated queue ("w:<id>").
	WorkerID string
	// WorkerEgressIP is the optional source IP outbound SMTP/IMAP dials bind to
	// (net.Dialer.LocalAddr). Empty = OS default route (single-node dev). It sets
	// the SOURCE address only and never relaxes the SSRF destination vet.
	WorkerEgressIP string
	// WorkerQueues is the ordered set of asynq queues this worker consumes;
	// default {"w:<WorkerID>", "default"} so it serves its own per-IP queue plus
	// the shared default.
	WorkerQueues []string

	// LogLevel is one of debug/info/warn/error. When empty, the logger
	// falls back to env-based defaults (debug in development, info elsewhere).
	LogLevel string

	// TrustedProxies is a list of CIDRs whose X-Forwarded-For header the app will
	// trust. Empty = trust none (default): the direct peer address is used. When a
	// direct peer falls in this set, the client is the RIGHTMOST XFF entry that is
	// not itself a listed proxy (see platform/httpx.ClientIPResolver).
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
	// WebDir is the optional built SPA directory. When it exists, the API serves
	// static assets and an index fallback; an empty or missing directory keeps
	// local API-only development unchanged.
	WebDir string

	// Google OAuth (mailbox connect via Gmail). Empty client id/secret disables
	// Gmail OAuth: the start endpoint returns 501 and gmail jobs fail cleanly.
	GoogleClientID     string
	GoogleClientSecret string
	GoogleRedirectURL  string

	// Microsoft OAuth (mailbox connect via M365 / Graph). Empty client id/secret
	// disables M365 OAuth: the start endpoint returns 501 and m365 jobs fail
	// cleanly. MSTenant selects the Azure AD authority (default "common").
	MSClientID     string
	MSClientSecret string
	MSRedirectURL  string
	MSTenant       string

	EmailVerifyTTL   time.Duration
	PasswordResetTTL time.Duration
	InviteTTL        time.Duration

	// TurnstileSecret is the Cloudflare Turnstile secret used to server-side
	// validate captcha tokens on register/login/email-OTP-start. Empty (default)
	// disables the captcha gate entirely (a no-op verifier that always passes) —
	// self-hosters without a captcha provider aren't blocked. Never logged.
	TurnstileSecret string

	// Pre-authentication rate limits, in requests per minute (fixed window). Each
	// abusable unauthenticated endpoint is throttled on the client IP and, where
	// its body carries one, the target account (email). A non-positive value means
	// "no cap" for that key. Backed by the shared Redis limiter (fails closed).
	RateLimitLoginIP          int // POST /login per IP
	RateLimitLoginAccount     int // POST /login per email
	RateLimitVerifyIP         int // 2fa/passkey/email-OTP verify per IP
	RateLimitVerifyAccount    int // email-OTP verify per email
	RateLimitSensitiveIP      int // password/forgot + email-OTP start per IP
	RateLimitSensitiveAccount int // password/forgot + email-OTP start per email
}

func Load() (*Config, error) {
	cfg := &Config{
		Env:         getenv("INROAD_ENV", "development"),
		HTTPAddr:    getenv("INROAD_HTTP_ADDR", ":8080"),
		DatabaseURL: getenv("INROAD_DATABASE_URL", "postgres://inroad:inroad@localhost:5432/inroad?sslmode=disable"),
		RedisAddr:   getenv("INROAD_REDIS_ADDR", "localhost:6379"),
	}
	cfg.MetricsAddr = getenv("INROAD_METRICS_ADDR", "")

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

	if warmupSecret := os.Getenv("INROAD_WARMUP_SECRET"); warmupSecret != "" {
		// Same floor as INROAD_JWT_SECRET: an explicitly-set weak secret fails
		// closed rather than signing warmup tokens with a guessable key. The
		// fallback below inherits JWTSecret, which already met this bar.
		if len(warmupSecret) < 16 {
			return nil, fmt.Errorf("INROAD_WARMUP_SECRET must be at least 16 bytes")
		}
		cfg.WarmupSecret = []byte(warmupSecret)
	} else {
		cfg.WarmupSecret = cfg.JWTSecret
	}

	rawKey, err := base64.StdEncoding.DecodeString(os.Getenv("INROAD_MASTER_KEY"))
	if err != nil {
		return nil, fmt.Errorf("INROAD_MASTER_KEY must be valid base64: %w", err)
	}
	if len(rawKey) != 32 {
		return nil, fmt.Errorf("INROAD_MASTER_KEY must decode to 32 bytes, got %d", len(rawKey))
	}
	cfg.MasterKey = rawKey

	cfg.KeyProvider = getenv("INROAD_KEY_PROVIDER", "local")
	cfg.MailAllowPrivateHosts = getenvBool("INROAD_MAIL_ALLOW_PRIVATE_HOSTS", true)
	cfg.PublicURL = getenv("INROAD_PUBLIC_URL", "http://localhost:8080")

	// Derive the WebAuthn Relying Party from the public URL by default: RPID is the
	// bare host (no port), RPOrigin is the scheme+host+port. An unparseable public
	// URL leaves both empty, disabling passkeys rather than binding to a wrong RP.
	defaultRPID, defaultRPOrigin := webauthnDefaults(cfg.PublicURL)
	cfg.RPID = getenv("INROAD_RP_ID", defaultRPID)
	cfg.RPOrigin = getenv("INROAD_RP_ORIGIN", defaultRPOrigin)

	// Short by default: the access token is re-validated against the session
	// store every request, so a ~5-minute TTL plus per-request revocation check
	// is the revocation guarantee (a revoked session is rejected within the
	// session-cache TTL, not the token TTL).
	cfg.AccessTokenTTL = getenvDuration("INROAD_ACCESS_TOKEN_TTL", 5*time.Minute)
	cfg.RefreshTokenTTL = getenvDuration("INROAD_REFRESH_TOKEN_TTL", 720*time.Hour)
	cfg.SessionCacheTTL = getenvDuration("INROAD_SESSION_CACHE_TTL", 5*time.Second)
	cfg.CookieSecure = getenvBool("INROAD_COOKIE_SECURE", true)
	cfg.CookieDomain = getenv("INROAD_COOKIE_DOMAIN", "")
	cfg.WorkerConcurrency = getenvInt("INROAD_WORKER_CONCURRENCY", 10)

	hostname, _ := os.Hostname() // "" on the rare lookup failure; handled below
	cfg.WorkerID = getenv("INROAD_WORKER_ID", hostname)
	cfg.WorkerEgressIP = getenv("INROAD_WORKER_EGRESS_IP", "")
	if raw := os.Getenv("INROAD_WORKER_QUEUES"); raw != "" {
		for _, s := range strings.Split(raw, ",") {
			if s = strings.TrimSpace(s); s != "" {
				cfg.WorkerQueues = append(cfg.WorkerQueues, s)
			}
		}
	}
	if len(cfg.WorkerQueues) == 0 {
		cfg.WorkerQueues = defaultWorkerQueues(cfg.WorkerID)
	}
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
	cfg.WebDir = getenv("INROAD_WEB_DIR", "")
	cfg.GoogleClientID = getenv("INROAD_GOOGLE_CLIENT_ID", "")
	cfg.GoogleClientSecret = getenv("INROAD_GOOGLE_CLIENT_SECRET", "")
	cfg.GoogleRedirectURL = getenv("INROAD_GOOGLE_REDIRECT_URL", cfg.PublicURL+"/oauth/google/callback")
	cfg.MSClientID = getenv("INROAD_MS_CLIENT_ID", "")
	cfg.MSClientSecret = getenv("INROAD_MS_CLIENT_SECRET", "")
	cfg.MSRedirectURL = getenv("INROAD_MS_REDIRECT_URL", cfg.PublicURL+"/oauth/microsoft/callback")
	cfg.MSTenant = getenv("INROAD_MS_TENANT", "common")
	cfg.EmailVerifyTTL = getenvDuration("INROAD_EMAIL_VERIFY_TTL", 24*time.Hour)
	cfg.PasswordResetTTL = getenvDuration("INROAD_PASSWORD_RESET_TTL", time.Hour)
	cfg.InviteTTL = getenvDuration("INROAD_INVITE_TTL", 72*time.Hour)

	cfg.TurnstileSecret = getenv("INROAD_TURNSTILE_SECRET", "")
	cfg.RateLimitLoginIP = getenvInt("INROAD_RATELIMIT_LOGIN_IP", 10)
	cfg.RateLimitLoginAccount = getenvInt("INROAD_RATELIMIT_LOGIN_ACCOUNT", 5)
	cfg.RateLimitVerifyIP = getenvInt("INROAD_RATELIMIT_VERIFY_IP", 10)
	cfg.RateLimitVerifyAccount = getenvInt("INROAD_RATELIMIT_VERIFY_ACCOUNT", 5)
	cfg.RateLimitSensitiveIP = getenvInt("INROAD_RATELIMIT_SENSITIVE_IP", 5)
	cfg.RateLimitSensitiveAccount = getenvInt("INROAD_RATELIMIT_SENSITIVE_ACCOUNT", 3)

	return cfg, nil
}

// webauthnDefaults derives the default RP id (bare host) and RP origin
// (scheme://host[:port]) from the public base URL. An empty or unparseable URL, or
// one without a host, yields two empty strings so passkeys stay disabled instead of
// binding a ceremony to a wrong or empty domain.
func webauthnDefaults(publicURL string) (rpID, rpOrigin string) {
	u, err := url.Parse(publicURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", ""
	}
	return u.Hostname(), u.Scheme + "://" + u.Host
}

// defaultWorkerQueues is the queue set a worker consumes when INROAD_WORKER_QUEUES
// is unset: its own dedicated per-IP queue plus the shared default. An empty
// workerID (hostname lookup failed AND no override) collapses to just the shared
// default, so the worker still processes unrouted traffic.
func defaultWorkerQueues(workerID string) []string {
	if workerID == "" {
		return []string{"default"}
	}
	return []string{"w:" + workerID, "default"}
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
