package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A malformed number must name itself at startup. Before this was fixed, every
// case below returned a nil error and the compiled default — so an operator who
// wrote "ten" ran on 25 connections and had no way to know, which is exactly the
// failure principle B8 ("validate at startup and fail loud") forbids.
func TestLoadRejectsMalformedInt(t *testing.T) {
	for _, tc := range []struct {
		name, key, value string
	}{
		{"word", "INROAD_DB_MAX_CONNS", "ten"},
		{"float", "INROAD_DB_MIN_CONNS", "4.5"},
		{"trailing unit", "INROAD_WORKER_CONCURRENCY", "10 workers"},
		{"empty-ish sign", "INROAD_SYSTEM_SMTP_PORT", "-"},
		{"thousands separator", "INROAD_RATELIMIT_LOGIN_IP", "1,000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setRequiredSecrets(t)
			t.Setenv(tc.key, tc.value)

			_, err := Load()
			if err == nil {
				t.Fatalf("Load() with %s=%q returned nil error — a malformed number silently became the default", tc.key, tc.value)
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("Load() error = %q, want it to name %s", err, tc.key)
			}
			if !strings.Contains(err.Error(), tc.value) {
				t.Fatalf("Load() error = %q, want it to quote the offending value %q", err, tc.value)
			}
		})
	}
}

// INROAD_ACCESS_TOKEN_TTL=5 is the case that motivated this: a bare 5 is not a
// Go duration, so it used to be discarded in favour of the 5-minute default —
// which happens to look like it worked, and does not for any other number.
func TestLoadRejectsMalformedDuration(t *testing.T) {
	for _, tc := range []struct {
		name, key, value string
	}{
		{"no unit", "INROAD_ACCESS_TOKEN_TTL", "5"},
		{"english", "INROAD_REFRESH_TOKEN_TTL", "30 days"},
		{"unknown unit", "INROAD_SESSION_CACHE_TTL", "5secs"},
		{"word", "INROAD_INVITE_TTL", "forever"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setRequiredSecrets(t)
			t.Setenv(tc.key, tc.value)

			_, err := Load()
			if err == nil {
				t.Fatalf("Load() with %s=%q returned nil error — a malformed duration silently became the default", tc.key, tc.value)
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("Load() error = %q, want it to name %s", err, tc.key)
			}
			if !strings.Contains(err.Error(), tc.value) {
				t.Fatalf("Load() error = %q, want it to quote the offending value %q", err, tc.value)
			}
		})
	}
}

// The dangerous half of the bool bug: three flags DEFAULT TRUE
// (INROAD_MAIL_ALLOW_PRIVATE_HOSTS, INROAD_RUN_SCHEDULER, INROAD_COOKIE_SECURE),
// and an unrecognised value used to read as false — so a typo on
// INROAD_COOKIE_SECURE turned off the Secure attribute on the session cookie
// with no signal whatsoever. An unrecognised value must now stop the process.
func TestLoadRejectsUnrecognisedBool(t *testing.T) {
	for _, tc := range []struct {
		name, key, value string
	}{
		{"typo on a default-true security flag", "INROAD_COOKIE_SECURE", "ture"},
		{"prose on a default-true flag", "INROAD_MAIL_ALLOW_PRIVATE_HOSTS", "sure-why-not"},
		{"typo on the scheduler", "INROAD_RUN_SCHEDULER", "flase"},
		{"garbage on a default-false flag", "INROAD_SYSTEM_SMTP_ALLOW_PLAINTEXT", "maybe"},
		{"number that is not 0 or 1", "INROAD_PPROF_ENABLED", "2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setRequiredSecrets(t)
			t.Setenv(tc.key, tc.value)

			_, err := Load()
			if err == nil {
				t.Fatalf("Load() with %s=%q returned nil error — an unrecognised boolean silently read as false", tc.key, tc.value)
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("Load() error = %q, want it to name %s", err, tc.key)
			}
			if !strings.Contains(err.Error(), tc.value) {
				t.Fatalf("Load() error = %q, want it to quote the offending value %q", err, tc.value)
			}
		})
	}
}

// The accepted spellings, pinned. `1`, `true` and `yes` (case-insensitive) were
// already accepted before this change and MUST keep working — .env.example, the
// compose files and the published docs all promise them. The rest are the
// widening: strconv.ParseBool's single letters plus the on/off and y/n pairs, so
// that a word an operator plainly meant as true is honoured rather than refused.
func TestBoolAcceptedSpellings(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"1", true}, {"0", false},
		{"t", true}, {"f", false},
		{"T", true}, {"F", false},
		{"true", true}, {"false", false},
		{"TRUE", true}, {"FALSE", false},
		{"True", true}, {"False", false},
		{"tRuE", true}, {"fAlSe", false},
		{"yes", true}, {"no", false},
		{"YES", true}, {"NO", false},
		{"y", true}, {"n", false},
		{"on", true}, {"off", false},
		{"ON", true}, {"OFF", false},
		{" true ", true}, {" false ", false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			setRequiredSecrets(t)
			// One default-false flag and one default-true flag, so a spelling
			// that was wrongly ignored cannot pass by agreeing with a default.
			t.Setenv("INROAD_PPROF_ENABLED", tc.value)
			t.Setenv("INROAD_COOKIE_SECURE", tc.value)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() with %q: %v", tc.value, err)
			}
			if cfg.PprofEnabled != tc.want {
				t.Errorf("PprofEnabled with %q = %v, want %v", tc.value, cfg.PprofEnabled, tc.want)
			}
			if cfg.CookieSecure != tc.want {
				t.Errorf("CookieSecure with %q = %v, want %v", tc.value, cfg.CookieSecure, tc.want)
			}
		})
	}
}

// "Empty is the same as unset" is a documented rule for every variable on this
// page, and the defaults tests rely on it to clear an ambient .env. Whitespace
// has to count as empty too, or a stray space in a compose file becomes a
// startup failure for a value the operator never set.
func TestBlankParsedValuesTakeTheirDefault(t *testing.T) {
	for _, tc := range []struct {
		name, blank string
	}{
		{"empty", ""},
		{"space", " "},
		{"tab", "\t"},
		{"newline", "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blank := tc.blank
			setRequiredSecrets(t)
			t.Setenv("INROAD_DB_MAX_CONNS", blank)
			t.Setenv("INROAD_ACCESS_TOKEN_TTL", blank)
			t.Setenv("INROAD_COOKIE_SECURE", blank)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load() with blank values: %v", err)
			}
			if cfg.DBMaxConns != DefaultDBMaxConns {
				t.Errorf("DBMaxConns = %d, want the default %d", cfg.DBMaxConns, DefaultDBMaxConns)
			}
			if cfg.AccessTokenTTL != 5*time.Minute {
				t.Errorf("AccessTokenTTL = %v, want the 5m default", cfg.AccessTokenTTL)
			}
			if !cfg.CookieSecure {
				t.Error("CookieSecure = false, want the true default")
			}
		})
	}
}

// Surrounding whitespace on an otherwise valid number or duration is honoured
// rather than rejected: a quoted compose value with a trailing space is a
// transcription slip, not a different setting.
func TestParsedValuesTolerateSurroundingWhitespace(t *testing.T) {
	setRequiredSecrets(t)
	t.Setenv("INROAD_DB_MAX_CONNS", " 8 ")
	t.Setenv("INROAD_ACCESS_TOKEN_TTL", " 15m ")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.DBMaxConns != 8 {
		t.Errorf("DBMaxConns = %d, want 8", cfg.DBMaxConns)
	}
	if cfg.AccessTokenTTL != 15*time.Minute {
		t.Errorf("AccessTokenTTL = %v, want 15m", cfg.AccessTokenTTL)
	}
}

// Every malformed value is reported, not just the first: an operator fixing a
// bad config should see the whole list in one startup, rather than discovering
// the next one on each restart.
func TestLoadReportsEveryMalformedValue(t *testing.T) {
	setRequiredSecrets(t)
	t.Setenv("INROAD_WORKER_CONCURRENCY", "lots")
	t.Setenv("INROAD_INVITE_TTL", "three days")
	t.Setenv("INROAD_PPROF_ENABLED", "ture")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() = nil error, want a rejection")
	}
	for _, key := range []string{"INROAD_WORKER_CONCURRENCY", "INROAD_INVITE_TTL", "INROAD_PPROF_ENABLED"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("Load() error = %q, want it to name %s too", err, key)
		}
	}
}

// A malformed pool size must be reported AS ITSELF. It must never be replaced
// by its default and then re-reported through the cross-check below it, because
// "INROAD_DB_MAX_CONNS (25) must be at least INROAD_DB_MIN_CONNS (100)" names a
// number the operator never typed and sends them looking in the wrong place.
func TestMalformedPoolSizeIsNotReportedAsTheBudgetCrossCheck(t *testing.T) {
	setRequiredSecrets(t)
	t.Setenv("INROAD_DB_MAX_CONNS", "ten")
	t.Setenv("INROAD_DB_MIN_CONNS", "100")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() = nil error, want a rejection")
	}
	if !strings.Contains(err.Error(), "ten") {
		t.Fatalf("Load() error = %q, want it to quote the malformed value", err)
	}
	if strings.Contains(err.Error(), "must be at least") {
		t.Fatalf("Load() error = %q, want no budget cross-check error derived from the fallback value", err)
	}
}

// Refusing a malformed value is only safe if nothing an operator is already
// running counts as malformed. .env.example is the file self-hosters copy, so
// every value in it has to survive Load — read from the file rather than
// restated here, because a restated copy is what drifts.
//
// Values arrive the way `make dev` delivers them: the Makefile `-include`s .env
// and GNU Make drops a `#` comment while KEEPING the whitespace before it, so
// `INROAD_RATELIMIT_LOGIN_IP=10          # POST /login per IP` is exported as
// "10          ". That trailing run of spaces is the whole point of this test —
// eight variables in the file carry it, and without trimming they would all be
// startup errors on the project's own documented setup path.
//
// The two secrets are placeholders ("replace-me-with-base64-32-bytes" is not
// base64 of 32 bytes) and are overridden with real ones: their validation is
// older than this test and has its own.
func TestEnvExampleStillParses(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "..", ".env.example"))
	if err != nil {
		t.Fatalf("opening .env.example: %v", err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Errorf("closing .env.example: %v", err)
		}
	}()

	var set int
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, value, ok := strings.Cut(strings.TrimSpace(scanner.Text()), "=")
		if !ok || !strings.HasPrefix(key, "INROAD_") {
			continue
		}
		// Make's comment rule, deliberately not trimmed afterwards: the
		// whitespace it leaves behind is what Load has to cope with.
		if comment := strings.Index(value, "#"); comment >= 0 {
			value = value[:comment]
		}
		t.Setenv(key, value)
		set++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading .env.example: %v", err)
	}
	if set == 0 {
		t.Fatal("no INROAD_* assignments found in .env.example — the test is not reading the file it thinks it is")
	}
	setRequiredSecrets(t)

	if _, err := Load(); err != nil {
		t.Fatalf("Load() with every .env.example value (%d of them): %v", set, err)
	}
}
