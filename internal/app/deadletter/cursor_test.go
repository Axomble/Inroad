package deadletter

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// A cursor is opaque to the client but must be exact to the server: it names one
// row's position in ONE listing, so it has to round-trip byte-identically and
// refuse anything it did not mint.

func TestCursorRoundTrips(t *testing.T) {
	id := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	cases := map[string]struct {
		status string
		at     time.Time
	}{
		// The unfiltered "All" tab. Its empty status must survive the frame,
		// because that is what distinguishes it from a filtered listing.
		"any status":               {"", time.Date(2026, 8, 28, 9, 50, 15, 0, time.UTC)},
		"pending":                  {StatusPending, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
		"replayed":                 {StatusReplayed, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
		"discarded":                {StatusDiscarded, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)},
		"microsecond precision":    {StatusPending, time.Date(2026, 8, 28, 9, 50, 15, 123456000, time.UTC)},
		"non-UTC input normalises": {StatusPending, time.Date(2026, 8, 28, 9, 50, 15, 0, time.FixedZone("x", 5*3600))},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			raw := encodeCursor(tc.status, Cursor{CreatedAt: tc.at, ID: id})
			got, err := decodeCursor(tc.status, raw)
			if err != nil {
				t.Fatalf("decodeCursor: %v", err)
			}
			if !got.CreatedAt.Equal(tc.at) {
				t.Errorf("created_at = %v, want %v", got.CreatedAt, tc.at)
			}
			if got.ID != id {
				t.Errorf("id = %v, want %v", got.ID, id)
			}
		})
	}
}

// Every rejection path, because a cursor the server cannot trust must be a loud
// 400 rather than a silent reset to page one.
func TestDecodeCursorRejects(t *testing.T) {
	id := uuid.New()
	at := time.Date(2026, 8, 28, 9, 50, 15, 0, time.UTC)
	frame := func(parts ...string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(strings.Join(parts, "|")))
	}

	cases := map[string]struct {
		status string
		raw    string
	}{
		"not base64":       {StatusPending, "not-base64!!"},
		"not a frame":      {StatusPending, base64.RawURLEncoding.EncodeToString([]byte("hello"))},
		"too few fields":   {StatusPending, frame(cursorVersion, cursorKind, StatusPending, at.Format(time.RFC3339Nano))},
		"too many fields":  {StatusPending, frame(cursorVersion, cursorKind, StatusPending, at.Format(time.RFC3339Nano), id.String(), "extra")},
		"future version":   {StatusPending, frame("2", cursorKind, StatusPending, at.Format(time.RFC3339Nano), id.String())},
		"another listing":  {StatusPending, frame(cursorVersion, "contacts", StatusPending, at.Format(time.RFC3339Nano), id.String())},
		"unparseable time": {StatusPending, frame(cursorVersion, cursorKind, StatusPending, "yesterday", id.String())},
		"unparseable id":   {StatusPending, frame(cursorVersion, cursorKind, StatusPending, at.Format(time.RFC3339Nano), "not-a-uuid")},
		"empty string":     {StatusPending, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeCursor(tc.status, tc.raw); !errors.Is(err, ErrBadCursor) {
				t.Fatalf("decodeCursor error = %v, want ErrBadCursor", err)
			}
		})
	}
}

// THE reason the status is inside the frame. A cursor minted while paging "All"
// names a position in the all-statuses ordering; replayed against status=pending
// it would land mid-list and quietly hide every pending row above it. That is
// indistinguishable from data loss, so it is refused instead.
func TestACursorIsRejectedByEveryOtherStatusFilter(t *testing.T) {
	at := time.Date(2026, 8, 28, 9, 50, 15, 0, time.UTC)
	id := uuid.New()
	statuses := []string{"", StatusPending, StatusReplayed, StatusDiscarded}

	for _, minted := range statuses {
		raw := encodeCursor(minted, Cursor{CreatedAt: at, ID: id})
		for _, replayed := range statuses {
			if minted == replayed {
				if _, err := decodeCursor(replayed, raw); err != nil {
					t.Errorf("cursor minted on %q rejected by its own filter: %v", minted, err)
				}
				continue
			}
			if _, err := decodeCursor(replayed, raw); !errors.Is(err, ErrBadCursor) {
				t.Errorf("cursor minted on status %q was accepted by status %q (error = %v), "+
					"want ErrBadCursor", minted, replayed, err)
			}
		}
	}
}

// The cursor must be URL-safe and free of padding: it travels in a query string
// and a client round-trips it verbatim.
func TestCursorIsURLSafe(t *testing.T) {
	raw := encodeCursor(StatusPending, Cursor{CreatedAt: time.Now(), ID: uuid.New()})
	if raw == "" {
		t.Fatal("encodeCursor produced an empty token")
	}
	if strings.ContainsAny(raw, "+/=") {
		t.Errorf("cursor %q contains characters that need URL escaping", raw)
	}
}

// Encoding is deterministic: the same row always yields the same token, so a
// client comparing cursors (or a cache keyed on one) is not defeated by noise.
func TestEncodeCursorIsDeterministic(t *testing.T) {
	c := Cursor{CreatedAt: time.Date(2026, 8, 28, 9, 50, 15, 0, time.UTC), ID: uuid.New()}
	first, second := encodeCursor(StatusPending, c), encodeCursor(StatusPending, c)
	if first != second {
		t.Errorf("encodeCursor is not deterministic: %q then %q", first, second)
	}
	if replayed := encodeCursor(StatusReplayed, c); first == replayed {
		t.Error("two status filters produced the same cursor; the filter is not in the frame")
	}
}
