package inbox

import (
	"strings"
	"testing"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
)

// Every ingest path in this package resolves an inbound message through a
// Message-ID read off UNAUTHENTICATED mail, and every one of them hands that
// value to a Postgres text parameter. textproto.ReadMIMEHeader validates header
// KEYS only, so a raw 0xFF or NUL in the VALUE survives verbatim through the
// IMAP, Gmail and Graph readers (the fakes here model exactly that, and the
// stores' refusal of it — see errInvalidEncoding).
//
// The consequence of NOT guarding is not a lost message: each of these lookups
// runs before SetInboxCursor, so a permanent failure means the message is
// refetched next pass and fails identically, forever. One email freezes the
// mailbox and stops every campaign reply, bounce, complaint and warmup receipt
// behind it. These tests pin all four doors shut at once.

// nonASCIIMessageID is the payload: a Message-ID with a raw 0xFF byte in it,
// which is not valid UTF-8 and therefore not a value Postgres will accept.
const nonASCIIMessageID = "<\xffwedge@x.test>"

// replyWithNonASCIIThreadHeaders is an ordinary campaign reply whose threading
// headers carry the unusable id — the reply matcher's door.
func replyWithNonASCIIThreadHeaders() string {
	return "From: alice@example.com\nTo: bob@example.com\nSubject: Re: Hello\n" +
		"In-Reply-To: " + nonASCIIMessageID + "\nReferences: " + nonASCIIMessageID +
		"\n\nSounds good.\n"
}

func TestPollReplyWithANonASCIIThreadHeaderNeitherFailsNorReachesTheLookup(t *testing.T) {
	core := &stubCore{job: coreapi.InboxPollJob{UIDValidity: 5, LastSeenUID: 10}}
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{inboundMsg(t, 11, replyWithNonASCIIThreadHeaders())},
	}

	if err := runPoll(t, core, reader); err != nil {
		t.Fatalf("an unusable threading header must not fail the poll, got %v", err)
	}
	if !core.cursorSet {
		t.Fatal("the cursor must advance; leaving it wedges every inbound signal for this mailbox")
	}
	if len(core.lookupIDs) != 0 {
		t.Fatalf("no lookup may be attempted on an unusable id, got %q", core.lookupIDs)
	}
}

// The DSN door. Original-Message-ID is parsed out of the returned message part,
// which is attacker-controlled body content (invariant 52 says so in as many
// words), and it feeds BOTH the warmup hard-bounce evidence write and the
// campaign send lookup — so the guard sits in front of both.
func TestPollHardBounceWithANonASCIIOriginalMessageIDIsALoggedNoOp(t *testing.T) {
	fixture := strings.Replace(hardBounceDSN, "<orig@x>", nonASCIIMessageID, 1)
	if fixture == hardBounceDSN {
		t.Fatal("fixture setup: the DSN's original Message-ID was not substituted")
	}
	core := &stubCore{job: coreapi.InboxPollJob{UIDValidity: 5, LastSeenUID: 10}}
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{inboundMsg(t, 11, fixture)},
	}

	if err := runPoll(t, core, reader); err != nil {
		t.Fatalf("an unusable DSN Message-ID must not fail the poll, got %v", err)
	}
	if !core.cursorSet {
		t.Fatal("the cursor must advance past an unattributable bounce")
	}
	if len(core.lookupIDs) != 0 {
		t.Fatalf("no send lookup may be attempted on an unusable id, got %q", core.lookupIDs)
	}
	if len(core.bounced) != 0 {
		t.Fatalf("nothing may be suppressed, got %v", core.bounced)
	}
}

// The ARF door, which is the widest of the four: an ingested complaint
// suppresses an address workspace-wide and can pause a campaign, so this path is
// the one whose permanent failure would be most valuable to an attacker.
func TestPollARFWithANonASCIIOriginalMessageIDIsDeclinedNotRetriedForever(t *testing.T) {
	fixture := strings.Replace(abuseARF, arfSendMessageID, nonASCIIMessageID, 1)
	if fixture == abuseARF {
		t.Fatal("fixture setup: the ARF's quoted Message-ID was not substituted")
	}
	core := newComplaintCore()

	if err := runARFPoll(t, core, fixture); err != nil {
		t.Fatalf("an unusable quoted Message-ID must not fail the poll, got %v", err)
	}
	if !core.cursorSet {
		t.Fatal("the cursor must advance past a report we cannot attribute")
	}
	if len(core.lookupIDs) != 0 {
		t.Fatalf("no send lookup may be attempted on an unusable id, got %q", core.lookupIDs)
	}
	if len(core.complaints) != 0 {
		t.Fatalf("nothing may be ingested, got %+v", core.complaints)
	}
}
