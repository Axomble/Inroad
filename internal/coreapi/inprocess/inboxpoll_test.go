package inprocess

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// MarkReplied and RecordReplyClass must short-circuit on an empty enrollmentID
// (a matched send with no enrollment — the legacy direct-send path) BEFORE
// touching the DB, so a zero-value client (nil queries) is safe to drive. This
// pins the no-op guard without needing Postgres; the full stop+tag write is
// exercised in the integration suite (Docker required).
func TestMarkRepliedNoEnrollmentIsNoOp(t *testing.T) {
	c := client{}
	if err := c.MarkReplied(context.Background(), "", "ws", "positive", "lexicon", 0.9); err != nil {
		t.Fatalf("MarkReplied with empty enrollment should no-op, got %v", err)
	}
}

func TestRecordReplyClassNoEnrollmentIsNoOp(t *testing.T) {
	c := client{}
	if err := c.RecordReplyClass(context.Background(), "", "ws", "out_of_office", "headers", 1.0); err != nil {
		t.Fatalf("RecordReplyClass with empty enrollment should no-op, got %v", err)
	}
}

// Regression guard: an empty class/source MUST map to SQL NULL, never "". The
// 000014 reply_class CHECK rejects ”, so if recordReplyClass ever wrote &""
// again, the interim untagged MarkReplied(...,"","",0) on an enrolled contact
// would fail the CHECK, error out of processMessage, and wedge the mailbox poll
// in a permanent retry loop.
func TestNilIfEmpty(t *testing.T) {
	if got := nilIfEmpty(""); got != nil {
		t.Fatalf("nilIfEmpty(\"\") = %v, want nil (SQL NULL, not empty string)", *got)
	}
	got := nilIfEmpty("positive")
	if got == nil || *got != "positive" {
		t.Fatalf("nilIfEmpty(%q) = %v, want pointer to %q", "positive", got, "positive")
	}
}

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
