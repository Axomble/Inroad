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

	"github.com/inroad/inroad/internal/platform/db"
)

// Pool-sizing defaults. Aliased from db so the numbers have ONE home (the
// package that owns the pool) while staying nameable in a config error message.
// db does not import config, so this direction adds no cycle.
const (
	DefaultDBMaxConns = db.DefaultPoolMaxConns
	DefaultDBMinConns = db.DefaultPoolMinConns
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

	// WSTicketSecret signs the realtime WebSocket connect ticket
	// (internal/platform/wsticket), which a browser spends on the Upgrade request
	// because it cannot set an Authorization header there. Dedicated so rotating
	// it doesn't invalidate sessions, tracking links or warmup receipts; falls
	// back to JWTSecret when unset, matching TrackingSecret.
	//
	// The ticket payload carries a domain prefix, so sharing JWTSecret with the
	// other codecs cannot let one of their tokens authenticate a socket.
	WSTicketSecret []byte

	// MailAllowPrivateHosts permits mailbox SMTP/IMAP hosts on RFC1918/ULA
	// private ranges. Default true for self-hosted operators reaching internal
	// mail servers; set false for multi-tenant Cloud. Loopback, link-local
	// (incl. cloud metadata), and multicast are always blocked regardless.
	MailAllowPrivateHosts bool

	// AIAllowPrivateBaseURL permits openai_compatible AI-provider base URLs on
	// private/loopback hosts (a localhost Ollama/vLLM). Default false — self-host
	// convenience is an explicit opt-in, not a default hole (agent-platform spec
	// §3). Link-local (incl. cloud metadata) and multicast stay blocked always.
	AIAllowPrivateBaseURL bool

	// AgentMaxConcurrentRuns caps how many agent runs one API process executes
	// simultaneously. Runs are goroutines in the API binary (never asynq), so
	// without a bound a burst of sends is unbounded concurrency against the
	// provider, the pool, and every tool they call. Default 20.
	AgentMaxConcurrentRuns int

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

	// DBMaxConns / DBMinConns size the pgx pool this process opens. Defaults
	// (25 / 4) are exactly the floors db.Connect applied before these existed, so
	// an upgrade changes no deployment's behaviour. They are per-PROCESS, and the
	// binding budget is cluster-wide: replicas × DBMaxConns + headroom must stay
	// under Postgres max_connections (100 by default), which four stock processes
	// hit exactly. A DSN that pins pool_max_conns/pool_min_conns still wins over
	// both — see db.PoolSize for the full precedence rule.
	DBMaxConns int
	DBMinConns int

	// WorkerConcurrency caps how many asynq tasks the worker processes
	// simultaneously. Default 10; tune per SMTP throughput.
	WorkerConcurrency int

	// RunScheduler decides whether THIS worker process runs the asynq periodic
	// scheduler. Default true so the common single-worker self-host keeps working
	// with no configuration. asynq elects no leader, so N replicas with this on
	// register every periodic task N times and every reconcile sweep runs N
	// times — cost, not corruption (the handlers are idempotent), but it scales
	// with replica count. Scaling out means setting this false on all but one.
	RunScheduler bool

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
	// SystemSMTPAllowPlaintext explicitly opts the transactional SMTP
	// connection out of TLS. Defaults to FALSE so an absent or malformed value
	// keeps TLS mandatory — a misconfiguration can never silently downgrade
	// system email to cleartext (security Invariant 6). Intended solely for a
	// local mail catcher (Mailpit/MailHog) in development.
	SystemSMTPAllowPlaintext bool

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

	// Google sign-in (Inroad LOGIN via Google). A different flow from mailbox
	// connect above -- its own redirect URL, and only the openid/email/profile
	// scopes, never Gmail -- but it may share the same OAuth client: when
	// GoogleSignInClientID is unset it falls back to GoogleClientID/Secret, so one
	// configured Google client makes both features work. With neither set, federated
	// sign-in is off: the start endpoint returns 501 and the SPA hides the button.
	// See Load for why the pair falls back together and the redirect URL does not.
	GoogleSignInClientID     string
	GoogleSignInClientSecret string
	GoogleSignInRedirectURL  string

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
	RateLimitSensitiveIP      int // password/forgot, email-OTP start, OAuth register, Google sign-in start per IP
	RateLimitSensitiveAccount int // password/forgot + email-OTP start per email

	// AI reply drafting, in requests per minute (fixed window). Unlike the caps
	// above this endpoint is AUTHENTICATED — it is throttled not against abuse of
	// an open door but because every call spends real money at an AI provider, so
	// the "account" key is the WORKSPACE (which owns the budget), not an email.
	RateLimitDraftReplyIP        int // POST /inbox/threads/{id}/draft-reply per IP
	RateLimitDraftReplyWorkspace int // POST /inbox/threads/{id}/draft-reply per workspace

	// Realtime connect-ticket minting, in requests per minute. Authenticated, like
	// draft-reply above, and keyed on the WORKSPACE rather than an email — but
	// throttled for a different reason: this endpoint mints a CREDENTIAL, so an
	// unbounded caller could farm tickets. The cap is generous because a normal
	// tab mints one per connect and a reconnect storm after a deploy is legitimate
	// traffic; it exists to bound abuse, not to shape ordinary use.
	RateLimitRealtimeTicketIP        int // POST /realtime/ticket per IP
	RateLimitRealtimeTicketWorkspace int // POST /realtime/ticket per workspace

	// Realtime connection caps, per open socket. Zero takes the package defaults
	// (8 per user, 200 per workspace). An unbounded socket count is a trivial
	// resource-exhaustion vector: each connection costs a goroutine, a buffer and
	// a registry slot.
	RealtimeMaxConnsPerUser      int
	RealtimeMaxConnsPerWorkspace int
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

	if wsTicketSecret := os.Getenv("INROAD_WS_TICKET_SECRET"); wsTicketSecret != "" {
		// Same floor as INROAD_JWT_SECRET: an explicitly-set weak secret fails
		// closed rather than signing connect tickets with a guessable key — and a
		// forgeable ticket is a forgeable workspace, since the channel key comes
		// from the ticket. The fallback below inherits JWTSecret, which already met
		// this bar.
		if len(wsTicketSecret) < 16 {
			return nil, fmt.Errorf("INROAD_WS_TICKET_SECRET must be at least 16 bytes")
		}
		cfg.WSTicketSecret = []byte(wsTicketSecret)
	} else {
		cfg.WSTicketSecret = cfg.JWTSecret
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
	cfg.AIAllowPrivateBaseURL = getenvBool("INROAD_AI_ALLOW_PRIVATE_BASE_URL", false)
	// Zero is passed through and means "use the run manager's own default";
	// platform packages never import app packages, so the number itself lives
	// in agentrun.DefaultMaxConcurrentRuns.
	cfg.AgentMaxConcurrentRuns = getenvInt("INROAD_AGENT_MAX_CONCURRENT_RUNS", 0)
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

	// Pool sizing is validated here, at the env boundary, so a bad budget fails at
	// startup with the offending numbers rather than as pool.Acquire blocking
	// forever on the first request — the failure mode this setting exists to fix.
	cfg.DBMaxConns = getenvInt("INROAD_DB_MAX_CONNS", DefaultDBMaxConns)
	cfg.DBMinConns = getenvInt("INROAD_DB_MIN_CONNS", DefaultDBMinConns)
	if cfg.DBMaxConns <= 0 {
		return nil, fmt.Errorf("INROAD_DB_MAX_CONNS must be greater than 0, got %d", cfg.DBMaxConns)
	}
	if cfg.DBMinConns < 0 {
		return nil, fmt.Errorf("INROAD_DB_MIN_CONNS must not be negative, got %d", cfg.DBMinConns)
	}
	if cfg.DBMaxConns < cfg.DBMinConns {
		return nil, fmt.Errorf("INROAD_DB_MAX_CONNS (%d) must be at least INROAD_DB_MIN_CONNS (%d)", cfg.DBMaxConns, cfg.DBMinConns)
	}

	cfg.RunScheduler = getenvBool("INROAD_RUN_SCHEDULER", true)

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
	cfg.SystemSMTPAllowPlaintext = getenvBool("INROAD_SYSTEM_SMTP_ALLOW_PLAINTEXT", false)
	cfg.AppBaseURL = getenv("INROAD_APP_BASE_URL", "http://localhost:5173")
	cfg.WebDir = getenv("INROAD_WEB_DIR", "")
	cfg.GoogleClientID = getenv("INROAD_GOOGLE_CLIENT_ID", "")
	cfg.GoogleClientSecret = getenv("INROAD_GOOGLE_CLIENT_SECRET", "")
	cfg.GoogleRedirectURL = getenv("INROAD_GOOGLE_REDIRECT_URL", cfg.PublicURL+"/oauth/google/callback")
	cfg.GoogleSignInClientID = getenv("INROAD_GOOGLE_SIGNIN_CLIENT_ID", "")
	cfg.GoogleSignInClientSecret = getenv("INROAD_GOOGLE_SIGNIN_CLIENT_SECRET", "")
	// Fall back to the mailbox-connect client so ONE configured Google client makes
	// both features work: a self-hoster shouldn't have to register two OAuth clients
	// to log in. Operators who WANT the separation set the dedicated pair, and the
	// reason to want it is that the mailbox client requests restricted Gmail scopes
	// subject to Google's verification review — on its own client, a pending review
	// can never block people signing in.
	//
	// Falls back as a PAIR, keyed on the id: a sign-in id with no sign-in secret
	// leaves both empty rather than pairing that id with the MAILBOX secret, which
	// would fail at Google in a way nothing here could explain. (An id without a
	// secret then just leaves sign-in disabled — mail.GoogleOAuth-style Enabled()
	// requires both.)
	//
	// The redirect URL deliberately does NOT fall back: sign-in and mailbox connect
	// have different callback paths even on the same client, so this always defaults
	// to the sign-in callback. Whichever client is used must list that exact URL in
	// its Authorized redirect URIs.
	if cfg.GoogleSignInClientID == "" {
		cfg.GoogleSignInClientID = cfg.GoogleClientID
		cfg.GoogleSignInClientSecret = cfg.GoogleClientSecret
	}
	cfg.GoogleSignInRedirectURL = getenv("INROAD_GOOGLE_SIGNIN_REDIRECT_URL", cfg.PublicURL+"/api/v1/auth/oauth/google/callback")
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
	// Generous enough that a human clicking "Draft a reply" never notices, tight
	// enough that a scripted loop cannot run up a provider bill. The per-IP cap is
	// the higher-tolerance one because a whole office can share one NAT address,
	// while the per-workspace cap is what actually bounds spend.
	cfg.RateLimitDraftReplyIP = getenvInt("INROAD_RATELIMIT_DRAFT_REPLY_IP", 20)
	cfg.RateLimitDraftReplyWorkspace = getenvInt("INROAD_RATELIMIT_DRAFT_REPLY_WORKSPACE", 60)

	// Generous by design: one mint per socket connect, and a rolling deploy has
	// every open tab reconnecting at once. 60/min per IP still covers a shared
	// office NAT; 600/min per workspace bounds a farming loop.
	cfg.RateLimitRealtimeTicketIP = getenvInt("INROAD_RATELIMIT_REALTIME_TICKET_IP", 60)
	cfg.RateLimitRealtimeTicketWorkspace = getenvInt("INROAD_RATELIMIT_REALTIME_TICKET_WORKSPACE", 600)
	cfg.RealtimeMaxConnsPerUser = getenvInt("INROAD_REALTIME_MAX_CONNS_PER_USER", 0)
	cfg.RealtimeMaxConnsPerWorkspace = getenvInt("INROAD_REALTIME_MAX_CONNS_PER_WORKSPACE", 0)

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
