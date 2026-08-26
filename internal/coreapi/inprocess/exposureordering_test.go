package inprocess

import (
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/esp"
	"github.com/inroad/inroad/internal/platform/rotation"
)

// Two narrowings now stand between eligibility and rotation, and they can disagree:
// the only mailbox on the recipient's provider may be the one sitting on the
// over-exposed fault domain. These fix the order in which they run.

// Ids ascending in the order each fixture lists its rows, so an assertion on the
// kept slice reads in the same order the pool query would return.
var (
	mailboxMatched   = uuid.MustParse("00000000-0000-0000-0000-0000000000d1")
	mailboxUnmatched = uuid.MustParse("00000000-0000-0000-0000-0000000000d2")
	mailboxOutside   = uuid.MustParse("00000000-0000-0000-0000-0000000000d3")
)

// onProvider tags a pool row with the transport ESP matching classifies on. Both
// fields are needed: provider selects the code path, and for provider='smtp' the
// submission host is the only evidence of who really handles the mail.
func onProvider(r gen.ListCampaignSenderCandidatesRow, host string) gen.ListCampaignSenderCandidatesRow {
	r.Provider, r.SmtpHost = "smtp", host
	return r
}

// keptIDs names the candidates a narrowing kept, for a readable diff.
func keptIDs(candidates []rotation.Candidate) []string {
	out := make([]string, len(candidates))
	for i, c := range candidates {
		out[i] = c.MailboxID
	}
	return out
}

// THE ORDERING DECISION. ESP matching runs first; the budget narrows inside its
// result. The fixture makes the two disagree as sharply as they can — the only
// Google mailbox is the over-exposed one, and the only under-exposed domain has no
// Google mailbox on it — so reversing the order hands a Google recipient a
// Microsoft mailbox.
func TestESPMatchingIsAppliedBeforeTheExposureBudget(t *testing.T) {
	google := onProvider(budgetRow(mailboxMatched, "bulk@heavy.test", 1, 100, 90), "smtp.gmail.com")
	microsoft := onProvider(budgetRow(mailboxUnmatched, "solo@other.test", 1, 100, 10),
		"acme-test.mail.protection.outlook.com")
	rows := []gen.ListCampaignSenderCandidatesRow{google, microsoft}

	eligible := eligibleCandidates(rows, noDomainLanes)
	if len(eligible) != 2 {
		t.Fatalf("eligible = %d rows, want both — the fixture would prove nothing", len(eligible))
	}
	// The fixture proves itself from the budget's side: on its own, the budget does
	// remove the Google mailbox. So the assertion below can only mean matching ran
	// first.
	if budgeted := withinExposureBudget(rows, eligible, noDomainLanes); len(budgeted) != 1 ||
		budgeted[0].MailboxID != mailboxUnmatched.String() {
		t.Fatalf("budget alone kept %v, want only %s — heavy.test holds 90%% of the campaign",
			keptIDs(budgeted), mailboxUnmatched)
	}

	got := narrowed(rows, eligible, esp.Google, noDomainLanes)
	if want := []string{mailboxMatched.String()}; !reflect.DeepEqual(keptIDs(got), want) {
		t.Errorf("narrowed = %v, want %v — a same-provider send is a deliverability gain on THIS "+
			"message, while concentration is a portfolio risk that may never materialise; the budget "+
			"must not buy the second by spending the first", keptIDs(got), want)
	}
}

// Running second does not defeat the budget: it still narrows WITHIN the matched
// cohort, and measures the shares over that cohort, because the cohort is the set
// selection is actually choosing from.
func TestExposureBudgetStillNarrowsInsideTheMatchedCohort(t *testing.T) {
	heavy := onProvider(budgetRow(mailboxMatched, "g1@heavy.test", 1, 100, 90), "smtp.gmail.com")
	light := onProvider(budgetRow(mailboxUnmatched, "g2@other.test", 1, 100, 10), "smtp.gmail.com")
	// Outside the cohort, and carrying enough history that counting it in the
	// denominator would put heavy.test back under the cap (90/1090 = 8%). It must not
	// be counted: it is not a candidate for this recipient.
	outside := onProvider(budgetRow(mailboxOutside, "m@other.test", 1, 100, 1000),
		"acme-test.mail.protection.outlook.com")
	rows := []gen.ListCampaignSenderCandidatesRow{heavy, light, outside}

	eligible := eligibleCandidates(rows, noDomainLanes)
	if len(eligible) != 3 {
		t.Fatalf("eligible = %d rows, want all three", len(eligible))
	}

	got := narrowed(rows, eligible, esp.Google, noDomainLanes)
	if want := []string{mailboxUnmatched.String()}; !reflect.DeepEqual(keptIDs(got), want) {
		t.Errorf("narrowed = %v, want %v — heavy.test carries 90%% of the MATCHED cohort's "+
			"assignments and must yield to its under-exposed peer", keptIDs(got), want)
	}
}

// An unmatchable recipient must still reach the budget. partitionByESP falls back to
// the whole eligible set when nothing matches, and the budget then runs over that
// set — otherwise the fallback would silently switch concentration limiting off for
// every recipient whose provider is not in the pool, which is most of them.
func TestTheBudgetStillRunsWhenNothingMatchesTheRecipient(t *testing.T) {
	google := onProvider(budgetRow(mailboxMatched, "bulk@heavy.test", 1, 100, 90), "smtp.gmail.com")
	alsoGoogle := onProvider(budgetRow(mailboxUnmatched, "solo@other.test", 1, 100, 10), "smtp.gmail.com")
	rows := []gen.ListCampaignSenderCandidatesRow{google, alsoGoogle}

	eligible := eligibleCandidates(rows, noDomainLanes)
	got := narrowed(rows, eligible, esp.Microsoft, noDomainLanes)
	if want := []string{mailboxUnmatched.String()}; !reflect.DeepEqual(keptIDs(got), want) {
		t.Errorf("narrowed = %v, want %v — no mailbox is Microsoft, so matching falls back to the "+
			"full pool and the budget must still narrow it", keptIDs(got), want)
	}
}

// Two mailboxes we cannot place are two unknowns, not one shared fault domain.
// Bucketing them together would invent a failure they share out of our own ignorance
// and then throttle them for it — the mistake the incident fold and the observer
// detector each had to be corrected for.
func TestUnclassifiableMailboxesAreNotOneFaultDomain(t *testing.T) {
	rows := []gen.ListCampaignSenderCandidatesRow{
		budgetRow(mailboxMatched, "no-domain-a", 1, 100, 45),
		budgetRow(mailboxUnmatched, "no-domain-b", 1, 100, 45),
		budgetRow(mailboxOutside, "c@known.test", 1, 100, 10),
	}
	eligible := eligibleCandidates(rows, noDomainLanes)
	if len(eligible) != 3 {
		t.Fatalf("eligible = %d rows, want all three", len(eligible))
	}

	got := withinExposureBudget(rows, eligible, noDomainLanes)
	if !reflect.DeepEqual(got, eligible) {
		t.Errorf("budgeted = %v, want all three %v — the two unplaceable mailboxes hold 90%% between "+
			"them, and grouping them under one empty key would throttle them for a fate we never "+
			"established they share", keptIDs(got), keptIDs(eligible))
	}
}
