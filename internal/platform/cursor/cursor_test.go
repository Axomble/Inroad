package cursor

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestParseSort(t *testing.T) {
	tests := []struct {
		in      string
		want    Sort
		wantErr bool
	}{
		{in: "", want: SortNewest},
		{in: "newest", want: SortNewest},
		{in: "oldest", want: SortOldest},
		{in: "email", want: SortEmail},
		{in: "Newest", wantErr: true},
		{in: "created_at", wantErr: true},
		{in: "email; DROP TABLE contacts", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseSort(tc.in)
			if tc.wantErr {
				if !errors.Is(err, ErrUnknownSort) {
					t.Fatalf("ParseSort(%q) err = %v, want ErrUnknownSort", tc.in, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSort(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("ParseSort(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRoundTrip(t *testing.T) {
	id := uuid.MustParse("2f9a1d3e-4b5c-4d6e-8f70-112233445566")
	at := time.Date(2026, 8, 1, 12, 34, 56, 123456789, time.UTC)

	tests := []struct {
		name string
		in   Cursor
	}{
		{"newest after", NewTime(SortNewest, After, at, id)},
		{"newest before", NewTime(SortNewest, Before, at, id)},
		{"oldest after", NewTime(SortOldest, After, at, id)},
		{"email after", NewEmail(After, "jo@acme.com", id)},
		// An email may legally contain the payload delimiter; the key is last
		// and unescaped, so this must survive the round trip intact.
		{"email containing the delimiter", NewEmail(Before, `weird|pipe@acme.com`, id)},
		{"empty email key", NewEmail(After, "", id)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Decode(tc.in.Encode(), tc.in.Sort)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if got != tc.in {
				t.Fatalf("round trip = %+v, want %+v", got, tc.in)
			}
		})
	}
}

// A timestamp cursor must survive with sub-second precision: bulk imports give
// thousands of contacts near-identical created_at values, and truncating to the
// second would make the keyset comparison skip or repeat rows.
func TestRoundTripKeepsNanoseconds(t *testing.T) {
	at := time.Date(2026, 8, 1, 12, 34, 56, 987654321, time.UTC)
	in := NewTime(SortNewest, After, at, uuid.New())
	got, err := Decode(in.Encode(), SortNewest)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !got.CreatedAt.Equal(at) {
		t.Fatalf("CreatedAt = %v, want %v", got.CreatedAt, at)
	}
}

// A non-UTC input must normalise, so two cursors naming the same instant encode
// identically and compare equal.
func TestEncodeNormalisesToUTC(t *testing.T) {
	id := uuid.New()
	at := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	east := time.FixedZone("east", 5*3600)
	if a, b := NewTime(SortNewest, After, at, id).Encode(), NewTime(SortNewest, After, at.In(east), id).Encode(); a != b {
		t.Fatalf("same instant encoded differently:\n%s\n%s", a, b)
	}
}

func TestDecodeRejectsMalformed(t *testing.T) {
	id := uuid.New()
	enc := func(payload string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(payload))
	}
	tests := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"not base64url", "!!!not base64!!!"},
		{"standard base64 padding", base64.StdEncoding.EncodeToString([]byte("1|newest|after|" + id.String() + "|x=="))},
		{"plain text", enc("hello")},
		{"too few fields", enc("1|newest|after|" + id.String())},
		{"unknown version", enc("2|newest|after|" + id.String() + "|2026-08-01T00:00:00Z")},
		{"empty sort", enc("1||after|" + id.String() + "|2026-08-01T00:00:00Z")},
		{"unknown sort", enc("1|sideways|after|" + id.String() + "|2026-08-01T00:00:00Z")},
		{"unknown direction", enc("1|newest|sideways|" + id.String() + "|2026-08-01T00:00:00Z")},
		{"bad uuid", enc("1|newest|after|not-a-uuid|2026-08-01T00:00:00Z")},
		{"bad timestamp", enc("1|newest|after|" + id.String() + "|yesterday")},
		{"email key under a timestamp sort", enc("1|newest|after|" + id.String() + "|jo@acme.com")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Decode(tc.raw, SortNewest); !errors.Is(err, ErrMalformed) {
				t.Fatalf("Decode(%q) err = %v, want ErrMalformed", tc.raw, err)
			}
		})
	}
}

// The "empty sort" case above decodes under want=newest; assert the guard is
// really the empty check and not the mismatch check.
func TestDecodeEmptySortIsMalformedNotMismatch(t *testing.T) {
	raw := base64.RawURLEncoding.EncodeToString([]byte("1||after|" + uuid.New().String() + "|2026-08-01T00:00:00Z"))
	_, err := Decode(raw, SortNewest)
	if errors.Is(err, ErrSortMismatch) {
		t.Fatalf("err = %v, want ErrMalformed not ErrSortMismatch", err)
	}
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed", err)
	}
}

func TestDecodeRejectsWrongSort(t *testing.T) {
	at := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	newest := NewTime(SortNewest, After, at, uuid.New()).Encode()
	email := NewEmail(After, "jo@acme.com", uuid.New()).Encode()

	tests := []struct {
		name string
		raw  string
		want Sort
	}{
		{"newest cursor on an oldest page", newest, SortOldest},
		{"newest cursor on an email page", newest, SortEmail},
		{"email cursor on a newest page", email, SortNewest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Decode(tc.raw, tc.want)
			if !errors.Is(err, ErrSortMismatch) {
				t.Fatalf("Decode err = %v, want ErrSortMismatch", err)
			}
			if errors.Is(err, ErrMalformed) {
				t.Fatalf("a well-formed cursor must not also report ErrMalformed: %v", err)
			}
		})
	}
}

// The token must not leak its contents to a client that might be tempted to
// build one by hand — but it also must not pretend to be encrypted. Assert only
// that it is URL-safe, since it travels in a query string.
func TestEncodeIsURLSafe(t *testing.T) {
	raw := NewEmail(After, "a+b/c=d|e@acme.com", uuid.New()).Encode()
	if strings.ContainsAny(raw, "+/=&?#%") {
		t.Fatalf("cursor %q contains characters that need URL escaping", raw)
	}
}
