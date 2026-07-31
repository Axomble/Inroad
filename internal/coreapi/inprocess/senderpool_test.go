package inprocess

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/rotation"
)

func pinned() pgtype.UUID { return pgtype.UUID{Bytes: uuid.New(), Valid: true} }

// mailboxBirthday is a fixed connect date for the candidate rows below. Ramp is
// disabled on them, so the age only feeds the weighted score's log2 term — these
// tests assert eligibility, not ranking.
var mailboxBirthday = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// The detection rule for a thread whose sending mailbox was deleted. The
// false-positive that matters is current_step = 0: a first send legitimately has
// no pin, and mistaking it for a deleted mailbox would stop every new enrollment.
func TestThreadLostItsMailbox(t *testing.T) {
	for _, tc := range []struct {
		name        string
		currentStep int32
		mailbox     pgtype.UUID
		want        bool
	}{
		{"sent a step, pin cleared by the mailbox delete", 1, pgtype.UUID{}, true},
		{"deep in the sequence, pin cleared", 7, pgtype.UUID{}, true},
		{"not started yet, no pin expected", 0, pgtype.UUID{}, false},
		{"sent a step and still pinned", 1, pinned(), false},
		{"not started but somehow pinned", 0, pinned(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := threadLostItsMailbox(tc.currentStep, tc.mailbox); got != tc.want {
				t.Errorf("threadLostItsMailbox(%d, valid=%v) = %v, want %v",
					tc.currentStep, tc.mailbox.Valid, got, tc.want)
			}
		})
	}
}

// candidateRow builds one pool row with the fields eligibility reads.
func candidateRow(id uuid.UUID, weight int32, enabled bool, status string, dailyCap int32, sentToday int64) gen.ListCampaignSenderCandidatesRow {
	return gen.ListCampaignSenderCandidatesRow{
		MailboxID: id, Weight: weight, Enabled: enabled, MailboxStatus: status,
		DailyCap: dailyCap, RampEnabled: false, SentToday: sentToday,
		MailboxCreatedAt: pgtype.Timestamptz{Time: mailboxBirthday, Valid: true},
	}
}

// Eligibility is the gate rotation.Select trusts, so each exclusion is asserted
// separately rather than only in aggregate.
func TestEligibleCandidatesExcludesUnusableRows(t *testing.T) {
	ok, disabled, inactive, capped := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	got := eligibleCandidates([]gen.ListCampaignSenderCandidatesRow{
		candidateRow(ok, 5, true, mailboxStatusActive, 100, 10),
		candidateRow(disabled, 5, false, mailboxStatusActive, 100, 0),
		candidateRow(inactive, 5, true, "disconnected", 100, 0),
		candidateRow(capped, 5, true, mailboxStatusActive, 100, 100),
	})
	if len(got) != 1 {
		t.Fatalf("eligible = %d rows (%+v), want only the usable one", len(got), got)
	}
	if got[0].MailboxID != ok.String() {
		t.Errorf("eligible mailbox = %s, want %s", got[0].MailboxID, ok)
	}
	if got[0].RemainingToday != 90 {
		t.Errorf("remaining = %d, want 100 - 10", got[0].RemainingToday)
	}
	if got[0].Weight != 5 {
		t.Errorf("weight = %d, want 5", got[0].Weight)
	}
}

// A pool where nothing is eligible must reach rotation as an empty set, which is
// the deferral signal rather than a selection.
func TestEligibleCandidatesEmptyPoolIsNoEligibleSender(t *testing.T) {
	got := eligibleCandidates([]gen.ListCampaignSenderCandidatesRow{
		candidateRow(uuid.New(), 1, false, mailboxStatusActive, 100, 0),
	})
	if len(got) != 0 {
		t.Fatalf("eligible = %+v, want none", got)
	}
	if _, err := rotation.Select(rotation.ModeWeighted, got); err == nil {
		t.Error("an empty eligible set must not yield a selection")
	}
}
