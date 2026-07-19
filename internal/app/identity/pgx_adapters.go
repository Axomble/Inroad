package identity

import (
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// pgxTimestamp converts a Go time.Time into the pgx nullable timestamptz
// representation used by generated params, marking it valid (non-null).
func pgxTimestamp(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// pgxTime extracts a Go time.Time from a pgx timestamptz. A NULL value
// (Valid == false) yields the zero time.
func pgxTime(ts pgtype.Timestamptz) time.Time {
	if !ts.Valid {
		return time.Time{}
	}
	return ts.Time
}

// ptr returns a pointer to s, or nil if s is empty (matches the *string
// column representation for optional fields like user_agent).
func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// parseIP parses s into a *netip.Addr, returning nil if s is empty or not a
// valid IP address (matches the *netip.Addr column representation).
func parseIP(s string) *netip.Addr {
	if s == "" {
		return nil
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return nil
	}
	return &addr
}
