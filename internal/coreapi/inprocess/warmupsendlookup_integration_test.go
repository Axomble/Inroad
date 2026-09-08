//go:build integration

package inprocess

import (
	"testing"

	"github.com/inroad/inroad/internal/coreapi"
)

// The header-loss lookup against the real query: what the SQL matches, and — the
// half that carries the isolation invariant — what it refuses. makeWarmupSend
// drives a real A→B send to 'sent' and records its Message-ID BRACKETED
// ("<{sendID}@acme.test>"), exactly as MarkWarmupSent stores what the transport
// returned, so both the stored and the inbound side get exercised.

// lookupClient asserts the capability the poller feature-detects. A compile-time
// var would not do: the poller resolves it from a coreapi.Client at runtime, and
// this is the assertion that would catch it silently disappearing.
func lookupClient(t *testing.T, f warmupFixture) coreapi.WarmupSendLookupClient {
	t.Helper()
	c, ok := f.core.(coreapi.WarmupSendLookupClient)
	if !ok {
		t.Fatalf("in-process client (%T) does not implement WarmupSendLookupClient — "+
			"the inbox poller would silently degrade to header-only warmup detection", f.core)
	}
	return c
}

// A tokenless inbound message is matched to its send whether the provider echoed
// the angle brackets or not. Both forms are the same identifier (RFC 5322 makes <>
// part of the field), and a raw compare would quietly miss one of them.
func TestFindWarmupSendByMessageIDIgnoresAngleBracketsOnBothSides(t *testing.T) {
	ctx, f := setupWarmup(t)
	sendID, recipient := makeWarmupSend(t, ctx, f)
	lookup := lookupClient(t, f)

	inbound := map[string]string{
		"as stored, bracketed":                "<" + sendID + "@acme.test>",
		"stripped by the provider":            sendID + "@acme.test",
		"folded, with surrounding whitespace": "  <" + sendID + "@acme.test>  ",
	}
	for name, id := range inbound {
		t.Run(name, func(t *testing.T) {
			ref, ok, err := lookup.FindWarmupSendByMessageID(ctx, f.ws1.String(), recipient, id)
			if err != nil {
				t.Fatalf("FindWarmupSendByMessageID: %v", err)
			}
			if !ok {
				t.Fatalf("Message-ID %q did not match the send that carried it", id)
			}
			if ref.WarmupSendID != sendID {
				t.Errorf("WarmupSendID = %q, want %q", ref.WarmupSendID, sendID)
			}
		})
	}
}

// The tenant pin. A Message-ID is inbound content, so a workspace must not be able
// to resolve another's send by quoting it — the same rule every other statement in
// this package upholds, checked here because this one is reached from unverified
// mail rather than from an authenticated request.
func TestFindWarmupSendByMessageIDIsPinnedToTheWorkspace(t *testing.T) {
	ctx, f := setupWarmup(t)
	sendID, recipient := makeWarmupSend(t, ctx, f)
	lookup := lookupClient(t, f)

	ref, ok, err := lookup.FindWarmupSendByMessageID(ctx, f.ws2.String(), recipient, "<"+sendID+"@acme.test>")
	if err != nil {
		t.Fatalf("FindWarmupSendByMessageID: %v", err)
	}
	if ok {
		t.Fatalf("a foreign workspace resolved send %q from its Message-ID", ref.WarmupSendID)
	}
}

// The recipient pin, and the reason the lookup takes a mailbox at all: a match must
// mean "a send addressed to THE MAILBOX NOW POLLING". Asked about the SENDER's own
// mailbox — the mailbox most likely to also see this Message-ID, since it is the
// one that wrote it — the same row must not resolve. Without this, anyone able to
// email a connected mailbox could turn a quoted Message-ID into a placement
// observation against a mailbox that never received the message.
func TestFindWarmupSendByMessageIDRefusesAMailboxTheSendWasNotAddressedTo(t *testing.T) {
	ctx, f := setupWarmup(t)
	sendID, _ := makeWarmupSend(t, ctx, f)
	lookup := lookupClient(t, f)

	// f.a is the SENDER of the send makeWarmupSend just drove (A→B).
	ref, ok, err := lookup.FindWarmupSendByMessageID(ctx, f.ws1.String(), f.a.String(), "<"+sendID+"@acme.test>")
	if err != nil {
		t.Fatalf("FindWarmupSendByMessageID: %v", err)
	}
	if ok {
		t.Fatalf("the sending mailbox resolved its own send %q as a received one", ref.WarmupSendID)
	}
}

// An unknown Message-ID — every campaign reply, newsletter and human message the
// poller sees — is a clean miss, never an error: it must not fail a poll.
func TestFindWarmupSendByMessageIDUnknownIDIsNotAnError(t *testing.T) {
	ctx, f := setupWarmup(t)
	_, recipient := makeWarmupSend(t, ctx, f)
	lookup := lookupClient(t, f)

	for _, id := range []string{"<nothing-we-sent@corp.example>", "", "<>", "   "} {
		ref, ok, err := lookup.FindWarmupSendByMessageID(ctx, f.ws1.String(), recipient, id)
		if err != nil {
			t.Fatalf("Message-ID %q: %v", id, err)
		}
		if ok {
			t.Fatalf("Message-ID %q matched send %q; an empty or unknown id must match nothing "+
				"(warmup_sends.message_id DEFAULTs to '')", id, ref.WarmupSendID)
		}
	}
}
