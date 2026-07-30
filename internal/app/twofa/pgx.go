package twofa

import (
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// pgxTimestamp wraps a time.Time as a non-null pgx timestamptz.
func pgxTimestamp(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// pgxTime extracts a time.Time from a pgx timestamptz, returning the zero time
// for NULL.
func pgxTime(ts pgtype.Timestamptz) time.Time {
	if !ts.Valid {
		return time.Time{}
	}
	return ts.Time
}

// parseIP parses s into a *netip.Addr, or nil if empty/invalid (matching the
// nullable INET column representation).
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
