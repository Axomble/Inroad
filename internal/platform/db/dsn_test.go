package db

import "testing"

func TestWithoutPoolParamsLeavesEverythingElseAlone(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			name: "strips a pool key and keeps the rest verbatim",
			in:   "postgres://u:p@h:5432/app?sslmode=disable&pool_max_conns=8",
			want: "postgres://u:p@h:5432/app?sslmode=disable",
		},
		{
			name: "strips every pool key",
			in:   "postgres://u:p@h:5432/app?pool_max_conns=8&sslmode=disable&pool_min_conns=0",
			want: "postgres://u:p@h:5432/app?sslmode=disable",
		},
		{
			name: "drops the whole query when only pool keys were in it",
			in:   "postgres://u:p@h:5432/app?pool_max_conns=8",
			want: "postgres://u:p@h:5432/app",
		},
		{
			// Byte-for-byte identity matters: a DSN with no pool keys must not be
			// re-encoded, or a search_path with punctuation drifts.
			name: "no pool keys is returned unchanged",
			in:   "postgres://u:p@h:5432/app?sslmode=disable&search_path=public,extensions",
			want: "postgres://u:p@h:5432/app?sslmode=disable&search_path=public,extensions",
		},
		{
			name: "no query string at all",
			in:   "postgres://u:p@h:5432/app",
			want: "postgres://u:p@h:5432/app",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WithoutPoolParams(tc.in); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPinsPoolParamMatchesOnlyRealQueryKeys(t *testing.T) {
	cases := []struct {
		name, dsn, key string
		want           bool
	}{
		{name: "present", dsn: "postgres://h/app?pool_max_conns=8", key: "pool_max_conns", want: true},
		{name: "present after another param", dsn: "postgres://h/app?sslmode=disable&pool_min_conns=0", key: "pool_min_conns", want: true},
		{name: "absent", dsn: "postgres://h/app?sslmode=disable", key: "pool_max_conns", want: false},
		{
			// A password happening to contain the key name is not a pin — the old
			// strings.Contains check would have read it as one.
			name: "a value containing the key name is not a pin",
			dsn:  "postgres://u:pool_max_conns@h/app?sslmode=disable",
			key:  "pool_max_conns",
			want: false,
		},
		{name: "no query string", dsn: "postgres://h/app", key: "pool_max_conns", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pinsPoolParam(tc.dsn, tc.key); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
