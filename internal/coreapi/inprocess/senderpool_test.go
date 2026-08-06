package inprocess

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/esp"
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

// withTransport tags a pool row with the mailbox transport fields ESP matching
// classifies on. Both are needed: provider selects the code path, and for
// provider='smtp' the host is the only evidence of who really handles the mail.
func withTransport(r gen.ListCampaignSenderCandidatesRow, provider, host string) gen.ListCampaignSenderCandidatesRow {
	r.Provider, r.SmtpHost = provider, host
	return r
}

// espPool builds a two-member pool in which the SECOND member wins under every
// rotation mode: more weight and capacity (weighted), never assigned
// (round_robin), and never used (LRU). Each test then makes the FIRST member the
// ESP match, so a passing assertion can only mean the partition ran — no
// scoring tweak could have produced it.
func espPool() (google, other gen.ListCampaignSenderCandidatesRow) {
	g := withTransport(candidateRow(uuid.MustParse("00000000-0000-0000-0000-0000000000a1"),
		1, true, mailboxStatusActive, 10, 0), "smtp", "smtp.gmail.com")
	g.AssignedCount = 99
	g.LastAssignedAt = pgtype.Timestamptz{Time: mailboxBirthday.Add(24 * time.Hour), Valid: true}

	o := withTransport(candidateRow(uuid.MustParse("00000000-0000-0000-0000-0000000000b2"),
		100, true, mailboxStatusActive, 1000, 0), "smtp", "mail.acme.test")
	return g, o
}

// The load-bearing property of partitioning over scoring: narrowing the set
// before rotation.Select behaves identically under ALL THREE modes. A score
// multiplier on rotation.Candidate would have been silently inert under
// round_robin and least_recently_used, whose comparisons never read the score.
func TestPartitionByESPWinsUnderEveryRotationMode(t *testing.T) {
	g, o := espPool()
	rows := []gen.ListCampaignSenderCandidatesRow{g, o}
	eligible := eligibleCandidates(rows)
	if len(eligible) != 2 {
		t.Fatalf("eligible = %d rows, want both", len(eligible))
	}
	for _, mode := range []string{rotation.ModeWeighted, rotation.ModeRoundRobin, rotation.ModeLRU} {
		t.Run(mode, func(t *testing.T) {
			// Unmatched: the fixture's second member wins in every mode.
			unmatched, err := rotation.Select(mode, eligible)
			if err != nil {
				t.Fatalf("Select: %v", err)
			}
			if unmatched.MailboxID != o.MailboxID.String() {
				t.Fatalf("baseline winner = %s, want the un-ESP-matched %s — fixture no longer proves anything",
					unmatched.MailboxID, o.MailboxID)
			}
			matched, err := rotation.Select(mode, partitionByESP(rows, eligible, esp.Google))
			if err != nil {
				t.Fatalf("Select on the matched subset: %v", err)
			}
			if matched.MailboxID != g.MailboxID.String() {
				t.Errorf("winner = %s, want the Google-matched %s", matched.MailboxID, g.MailboxID)
			}
		})
	}
}

// Matching is an optimisation, never a gate: when nothing in the pool matches,
// the full eligible set must reach rotation unchanged rather than deferring a
// send an unmatched mailbox could deliver.
func TestPartitionByESPFallsBackToTheFullPool(t *testing.T) {
	g, o := espPool()
	rows := []gen.ListCampaignSenderCandidatesRow{g, o}
	eligible := eligibleCandidates(rows)

	for _, tc := range []struct {
		name string
		want esp.ESP
	}{
		{"no mailbox is microsoft", esp.Microsoft},
		{"an uncached recipient tells us nothing", esp.Unknown},
		{"other is a bucket, not shared infrastructure", esp.Other},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := partitionByESP(rows, eligible, tc.want)
			if len(got) != len(eligible) {
				t.Fatalf("subset = %d candidates, want the full pool of %d", len(got), len(eligible))
			}
			for i := range got {
				if got[i].MailboxID != eligible[i].MailboxID {
					t.Errorf("candidate %d = %s, want %s (order must be preserved)",
						i, got[i].MailboxID, eligible[i].MailboxID)
				}
			}
		})
	}
}

// A cache miss must cost nothing. With one eligible member there is nothing to
// choose between, so espMatched must return before it reaches the database —
// which a zero-value client proves by panicking on a nil *gen.Queries if it does
// not.
func TestESPMatchSkipsTheLookupWhenThereIsNothingToChoose(t *testing.T) {
	g, _ := espPool()
	rows := []gen.ListCampaignSenderCandidatesRow{g}
	eligible := eligibleCandidates(rows)

	got := client{}.espMatched(t.Context(), uuid.New(), "someone@acme.test", rows, eligible)
	if len(got) != 1 || got[0].MailboxID != g.MailboxID.String() {
		t.Errorf("subset = %+v, want the single eligible member unchanged", got)
	}
}
