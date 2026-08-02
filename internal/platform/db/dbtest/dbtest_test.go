package dbtest

import "testing"

func TestWithTestSuffixKeepsEverythingButTheDatabaseName(t *testing.T) {
	cases := []struct {
		name, in, want string
		ok             bool
	}{
		{
			name: "adds the suffix and preserves query params",
			in:   "postgres://inroad:inroad@localhost:5433/inroad?sslmode=disable",
			want: "postgres://inroad:inroad@localhost:5433/inroad_test?sslmode=disable",
			ok:   true,
		},
		{
			// Deriving twice must be stable, or a re-derived DSN drifts to
			// inroad_test_test and silently uses a third database.
			name: "an already-suffixed name is left alone",
			in:   "postgres://inroad:inroad@localhost:5433/inroad_test?sslmode=disable",
			want: "postgres://inroad:inroad@localhost:5433/inroad_test?sslmode=disable",
			ok:   true,
		},
		{
			name: "no query params",
			in:   "postgres://u:p@db:5432/app",
			want: "postgres://u:p@db:5432/app_test",
			ok:   true,
		},
		{name: "not a url", in: "nonsense", ok: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := withTestSuffix(tc.in)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReplaceDatabaseKeepsCredentialsAndParams(t *testing.T) {
	got := replaceDatabase("postgres://inroad:inroad@localhost:5433/inroad_test?sslmode=disable", "postgres")
	want := "postgres://inroad:inroad@localhost:5433/postgres?sslmode=disable"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A failure message must not put the password in CI logs.
func TestRedactHidesThePassword(t *testing.T) {
	got := redact("postgres://inroad:hunter2@localhost:5433/inroad_test?sslmode=disable")
	if want := "postgres://inroad:***@localhost:5433/inroad_test?sslmode=disable"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolvePrefersTheExplicitOverride(t *testing.T) {
	t.Setenv("INROAD_TEST_DATABASE_URL", "postgres://x:y@h:1/explicit")
	t.Setenv("INROAD_DATABASE_URL", "postgres://x:y@h:1/derived")
	if got := resolve(); got != "postgres://x:y@h:1/explicit" {
		t.Errorf("got %q, want the explicit override", got)
	}
}

// The whole point: with only the app's DSN set, the suite must NOT run against
// the app's own database.
func TestResolveNeverReturnsTheAppDatabase(t *testing.T) {
	t.Setenv("INROAD_TEST_DATABASE_URL", "")
	t.Setenv("INROAD_DATABASE_URL", "postgres://inroad:inroad@localhost:5433/inroad?sslmode=disable")
	got := resolve()
	if got == "postgres://inroad:inroad@localhost:5433/inroad?sslmode=disable" {
		t.Fatal("integration tests resolved to the app's own database — this is the pollution bug")
	}
	if want := "postgres://inroad:inroad@localhost:5433/inroad_test?sslmode=disable"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveFallsBackToASeparateDatabase(t *testing.T) {
	t.Setenv("INROAD_TEST_DATABASE_URL", "")
	t.Setenv("INROAD_DATABASE_URL", "")
	if got := resolve(); got != defaultDSN {
		t.Errorf("got %q, want the default test DSN", got)
	}
}
