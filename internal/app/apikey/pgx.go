package apikey

import (
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// pgUUID wraps a uuid.UUID as a non-null pgtype.UUID (the generated column type
// for the nullable created_by_user_id).
func pgUUID(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}

// pgTimePtr wraps an optional time as a pgtype.Timestamptz: a nil pointer is a
// SQL NULL (no expiry), a set pointer a real timestamp.
func pgTimePtr(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

// uuidPtr extracts a *uuid.UUID from a nullable pgtype.UUID (NULL -> nil), for the
// created-by field of a projected key.
func uuidPtr(v pgtype.UUID) *uuid.UUID {
	if !v.Valid {
		return nil
	}
	id := uuid.UUID(v.Bytes)
	return &id
}

// timePtr extracts a *time.Time from a nullable pgtype.Timestamptz (NULL -> nil).
func timePtr(ts pgtype.Timestamptz) *time.Time {
	if !ts.Valid {
		return nil
	}
	t := ts.Time
	return &t
}
