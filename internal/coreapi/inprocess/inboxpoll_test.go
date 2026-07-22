package inprocess

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestEnrollmentRefIDInvalidYieldsEmpty(t *testing.T) {
	// A legacy direct send has no sequence_enrollments row, so the LEFT JOIN
	// in GetSendByMessageID comes back NULL — pgtype.UUID{Valid: false}.
	if got := enrollmentRefID(pgtype.UUID{Valid: false}); got != "" {
		t.Fatalf("no enrollment should map to \"\", got %q", got)
	}
}

func TestEnrollmentRefIDValidYieldsUUIDString(t *testing.T) {
	id := uuid.New()
	got := enrollmentRefID(pgtype.UUID{Bytes: id, Valid: true})
	if got != id.String() {
		t.Fatalf("enrollment id mapping = %q, want %q", got, id.String())
	}
}
