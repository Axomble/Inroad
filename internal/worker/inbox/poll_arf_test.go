package inbox

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
)

// These tests cover the inbound half of the complaint path: an RFC 5965 feedback
// report that arrives AS MAIL, rather than through POST /deliverability/events.
// The endpoint's ingest is reused verbatim — the score, at-risk list and breaker
// already consume complaints — so what is tested here is what the poller decides
// to feed it, and (mostly) what it refuses to.

// complaintCore adds the OPTIONAL complaint-ingest capability to stubCore. Its own
// type, like fakeInboxCapture, so a test can drive a core WITHOUT the capability
// and prove the poll still completes.
type complaintCore struct {
	*stubCore
	complaints []coreapi.ComplaintInput
	err        error
}

func (c *complaintCore) IngestComplaint(_ context.Context, in coreapi.ComplaintInput) error {
	c.complaints = append(c.complaints, in)
	return c.err
}

var _ coreapi.DeliverabilityComplaintClient = (*complaintCore)(nil)

// arfSendMessageID is the Message-ID the abuseARF fixture's returned message
// carries — the send the complaint is about.
const arfSendMessageID = "<orig-arf@acme.test>"

// newComplaintCore seeds the send abuseARF quotes, addressed to the contact the
// report names, so the resolved send and the report agree.
func newComplaintCore() *complaintCore {
	return &complaintCore{stubCore: &stubCore{
		job: coreapi.InboxPollJob{UIDValidity: 5, LastSeenUID: 10},
		sendRefs: map[string]coreapi.SendRef{arfSendMessageID: {
			SendID: "snd-1", EnrollmentID: "e1", ContactEmail: "recipient@corp.example",
			MailboxID: pollMailbox, CampaignID: "camp-1", ContactID: "ct-1",
		}},
	}}
}

func runARFPoll(t *testing.T, core coreapi.Client, fixture string) error {
	t.Helper()
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{inboundMsg(t, 11, fixture)},
	}
	return runWarmupPoll(t, core, reader, &spyEngageEnqueuer{})
}

// The headline: a feedback report delivered to a connected mailbox becomes a
// complaint on the SAME idempotent ingest POST /deliverability/events feeds, so it
// suppresses the contact, scores, and reaches the breaker with no new path.
//
// The address suppressed is the one on OUR OWN send, resolved from the Message-ID
// the report quotes — never the address the report names. An ARF arrives as
// unauthenticated mail, so Original-Rcpt-To is attacker-supplied: acting on it
// would let anyone able to email a connected mailbox suppress an address they do
// not own and inflate the rate that pauses campaigns.
func TestPollARFComplaintIngestsAgainstTheResolvedSend(t *testing.T) {
	core := newComplaintCore()

	if err := runARFPoll(t, core, abuseARF); err != nil {
		t.Fatal(err)
	}
	if len(core.complaints) != 1 {
		t.Fatalf("expected 1 ingested complaint, got %d", len(core.complaints))
	}
	got := core.complaints[0]
	if got.WorkspaceID != pollWS {
		t.Errorf("WorkspaceID = %q, want %q (from the poll job, never the report)", got.WorkspaceID, pollWS)
	}
	if got.Email != "recipient@corp.example" {
		t.Errorf("Email = %q, want the resolved send's contact", got.Email)
	}
	if got.SendID != "snd-1" {
		t.Errorf("SendID = %q, want snd-1 — without it the complaint reaches no campaign breaker", got.SendID)
	}
	// One complaint per send, forever: a re-poll or a redelivered report writes
	// nothing and therefore causes nothing (no second suppression, no second
	// evaluation). The prefix keeps it out of a provider feed's id space.
	if got.ProviderEventID != "arf:snd-1" {
		t.Errorf("ProviderEventID = %q, want arf:snd-1", got.ProviderEventID)
	}
}

// A report about mail this workspace never sent — a forwarded report, a purged
// send, or a forgery quoting an id we do not have — resolves to nothing and
// suppresses nothing. This is the guard that keeps the whole path bounded: an
// attacker must already know a real Message-ID of a real send.
func TestPollARFWithNoMatchingSendIngestsNothing(t *testing.T) {
	core := &complaintCore{stubCore: &stubCore{job: coreapi.InboxPollJob{UIDValidity: 5, LastSeenUID: 10}}}

	if err := runARFPoll(t, core, abuseARF); err != nil {
		t.Fatalf("an unattributable complaint must not fail the poll, got %v", err)
	}
	if len(core.complaints) != 0 {
		t.Fatalf("a complaint about mail we never sent must not be ingested, got %+v", core.complaints)
	}
	if !core.cursorSet {
		t.Fatal("the poll must still complete and advance the cursor")
	}
}

// A report that redacts the offending message names an address and nothing else.
// Suppressing on that alone is exactly the forged-report attack the DSN path
// refuses Final-Recipient for, so it is logged and skipped instead.
func TestPollARFWithNoOriginalMessageIDIngestsNothing(t *testing.T) {
	core := newComplaintCore()

	if err := runARFPoll(t, core, noOriginalMessageARF); err != nil {
		t.Fatal(err)
	}
	if len(core.complaints) != 0 {
		t.Fatalf("a report naming only an address must not be ingested, got %+v", core.complaints)
	}
}

// Cross-check: when the report DOES name a recipient and it is not the contact our
// send went to, the two disagree about what happened and the report is declined.
// The send row is always the authority; this only ever refuses, never redirects.
func TestPollARFWhoseReportedRecipientDisagreesWithTheSendIngestsNothing(t *testing.T) {
	core := newComplaintCore()
	core.sendRefs[arfSendMessageID] = coreapi.SendRef{
		SendID: "snd-1", EnrollmentID: "e1", ContactEmail: "someone-else@corp.example",
	}

	if err := runARFPoll(t, core, abuseARF); err != nil {
		t.Fatal(err)
	}
	if len(core.complaints) != 0 {
		t.Fatalf("a report whose recipient does not match the send must be declined, got %+v", core.complaints)
	}
}

// The branch a forger would use, and the one nothing else covered: RFC 5965
// makes Original-Rcpt-To OPTIONAL, and the cross-check above is SKIPPED entirely
// when it is absent (`a.ComplainedRecipient != "" && ...`). So this shape has no
// corroboration at all beyond the quoted Message-ID.
//
// The behaviour pinned here is that it still ingests. That is deliberate, not an
// oversight: real FBLs do redact the field, and refusing those reports would
// silently drop genuine complaints — a compliance failure — while buying nothing,
// because the cross-check only ever REFUSES a report and never adds proof. The
// whole bar for this path is therefore "already knows a real Message-ID of a real
// send" with nothing behind it, which is what docs/security.md invariant 65 has
// to state rather than imply. If that bar is ever raised, this test is the one
// that must change, and its failure will say so.
func TestPollARFWithoutAReportedRecipientStillIngestsOnTheResolvedSend(t *testing.T) {
	// The fixture must actually BE the uncorroborated shape. Without this, a
	// fixture that drifted back to carrying Original-Rcpt-To would make the test
	// re-cover the already-covered branch and pass for the wrong reason.
	if r := ParseARF(parseFixture(t, abuseARFWithoutRecipient)); r.ComplainedRecipient != "" {
		t.Fatalf("fixture setup: ComplainedRecipient = %q, want empty", r.ComplainedRecipient)
	}
	core := newComplaintCore()

	if err := runARFPoll(t, core, abuseARFWithoutRecipient); err != nil {
		t.Fatal(err)
	}
	if len(core.complaints) != 1 {
		t.Fatalf("a report with no Original-Rcpt-To must still ingest against the resolved send, got %+v", core.complaints)
	}
	got := core.complaints[0]
	if got.Email != "recipient@corp.example" {
		t.Errorf("Email = %q, want the resolved send's contact — never anything the report named", got.Email)
	}
	if got.SendID != "snd-1" || got.ProviderEventID != "arf:snd-1" {
		t.Errorf("SendID/ProviderEventID = %q/%q, want snd-1/arf:snd-1", got.SendID, got.ProviderEventID)
	}
}

// The same address in a different case is the same mailbox, so a case difference
// must not throw away a real complaint.
func TestPollARFRecipientCrossCheckIsCaseInsensitive(t *testing.T) {
	core := newComplaintCore()
	core.sendRefs[arfSendMessageID] = coreapi.SendRef{
		SendID: "snd-1", EnrollmentID: "e1", ContactEmail: "Recipient@Corp.Example",
	}

	if err := runARFPoll(t, core, abuseARF); err != nil {
		t.Fatal(err)
	}
	if len(core.complaints) != 1 {
		t.Fatalf("expected the complaint to survive a case difference, got %+v", core.complaints)
	}
	if core.complaints[0].Email != "Recipient@Corp.Example" {
		t.Errorf("Email = %q, want the send's stored contact address verbatim", core.complaints[0].Email)
	}
}

// not-spam is a recipient rescuing our mail from their spam folder. Ingesting it
// as a complaint would suppress the one contact who told us they wanted the mail.
func TestPollNotSpamFeedbackReportIngestsNothing(t *testing.T) {
	core := newComplaintCore()
	core.sendRefs["<orig-notspam@acme.test>"] = coreapi.SendRef{SendID: "snd-2", ContactEmail: "recipient@corp.example"}

	if err := runARFPoll(t, core, notSpamARF); err != nil {
		t.Fatal(err)
	}
	if len(core.complaints) != 0 {
		t.Fatalf("not-spam must never be ingested as a complaint, got %+v", core.complaints)
	}
}

// A broken report must not fail the mailbox's whole poll — every other inbound
// signal (campaign replies, bounces) rides on the same cursor.
func TestPollMalformedFeedbackReportDoesNotFailThePoll(t *testing.T) {
	core := newComplaintCore()

	if err := runARFPoll(t, core, malformedFeedbackARF); err != nil {
		t.Fatalf("a malformed report must not fail the poll, got %v", err)
	}
	if len(core.complaints) != 0 {
		t.Fatalf("a malformed report must not be ingested, got %+v", core.complaints)
	}
	if !core.cursorSet {
		t.Fatal("the cursor must still advance past a malformed report")
	}
}

// A core without the capability records no complaint and, above all, does not fail
// the poll — the degrade every optional coreapi capability here promises.
func TestPollARFWithoutTheComplaintCapabilityDoesNotFailThePoll(t *testing.T) {
	core := newComplaintCore().stubCore // no IngestComplaint

	if err := runARFPoll(t, core, abuseARF); err != nil {
		t.Fatalf("a core without the ingest capability must not fail the poll, got %v", err)
	}
	if !core.cursorSet {
		t.Fatal("the poll must still complete and advance the cursor")
	}
	// It must also not fall through into reply classification and stop the
	// enrollment: an ARF is not a reply.
	if len(core.replied) != 0 || len(core.unsubscribed) != 0 {
		t.Fatalf("an ARF must never be classified as a reply; replied=%v unsub=%v", core.replied, core.unsubscribed)
	}
}

// A TRANSIENT ingest failure is not a reason to drop a complaint. The write is
// idempotent on provider_event_id, so failing the poll lets asynq retry it with no
// risk of double-counting — and a lost complaint is a compliance failure, not a
// lost metric.
func TestPollARFIngestErrorFailsThePollSoItRetries(t *testing.T) {
	core := newComplaintCore()
	core.err = errors.New("db down")

	err := runARFPoll(t, core, abuseARF)
	if !errors.Is(err, core.err) {
		t.Fatalf("expected the ingest error to fail the poll, got %v", err)
	}
	if core.cursorSet {
		t.Fatal("a failed complaint ingest must not advance the cursor")
	}
}

// A PERMANENT rejection is the opposite case and must not be retried at all.
// Retrying one is not merely useless: the poll returns before SetInboxCursor, so
// the mailbox's cursor never advances and EVERY inbound signal for it — campaign
// replies, bounces, warmup receipts — stops, forever, on the strength of one
// unauthenticated inbound message. The complaint is logged and skipped instead.
func TestPollARFRejectedAsPermanentlyInvalidIsSkippedNotRetriedForever(t *testing.T) {
	core := newComplaintCore()
	core.err = fmt.Errorf("email is required: %w", coreapi.ErrInvalidComplaint)

	if err := runARFPoll(t, core, abuseARF); err != nil {
		t.Fatalf("a permanently invalid complaint must not fail the poll, got %v", err)
	}
	if !core.cursorSet {
		t.Fatal("the cursor must advance past a complaint the control plane will never accept")
	}
}

// The no-regression guard at the poll level, both ways: a delivery-status report
// still bounces and never ingests a complaint, and a feedback report never marks a
// bounce. They are separate report-types with very different consequences.
func TestPollDSNStillBouncesAndNeverIngestsAComplaint(t *testing.T) {
	core := newComplaintCore()
	core.sendRefs["<orig@x>"] = coreapi.SendRef{SendID: "snd-b", EnrollmentID: "e-b", ContactEmail: "nobody@recipient.example.com"}

	if err := runARFPoll(t, core, hardBounceDSN); err != nil {
		t.Fatal(err)
	}
	if len(core.complaints) != 0 {
		t.Fatalf("a DSN must never be ingested as a complaint, got %+v", core.complaints)
	}
	if len(core.bounced) != 1 || !core.bounced[0].hard {
		t.Fatalf("the DSN must still be marked a hard bounce, got %+v", core.bounced)
	}
}

func TestPollARFIsNotMarkedAsABounce(t *testing.T) {
	core := newComplaintCore()

	if err := runARFPoll(t, core, abuseARF); err != nil {
		t.Fatal(err)
	}
	if len(core.bounced) != 0 {
		t.Fatalf("a complaint must never be recorded as a bounce, got %+v", core.bounced)
	}
}
