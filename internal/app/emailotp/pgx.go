package emailotp

import (
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// pgxTimestamp wraps a time.Time as a non-null pgx timestamptz.
func pgxTimestamp(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// pgxTime extracts a time.Time from a pgx timestamptz, returning the zero time for
// NULL.
func pgxTime(ts pgtype.Timestamptz) time.Time {
	if !ts.Valid {
		return time.Time{}
	}
	return ts.Time
}
