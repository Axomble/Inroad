package oauthprovider

import (
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// pgUUIDPtr wraps an optional uuid.UUID as a nullable pgtype.UUID (nil -> SQL NULL),
// for the optional client creator column.
func pgUUIDPtr(id *uuid.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}

// pgTime wraps a time.Time as a non-null pgtype.Timestamptz.
func pgTime(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// timeOrZero extracts a time.Time from a nullable pgtype.Timestamptz (NULL -> zero).
func timeOrZero(ts pgtype.Timestamptz) time.Time {
	if !ts.Valid {
		return time.Time{}
	}
	return ts.Time
}
