package inbox

import (
	"context"
	"testing"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/replyclassify"
)

// These tests cover the PLUMBING half of identity extraction (design §6). The
// parse itself is pure and tested in internal/platform/warmup/identity_test.go;
// what cannot be tested there is whether the poller hands it a Receiver that
// describes the mailbox actually doing the reading.
//
// That distinction is the whole security property. warmup.ExtractIdentity trusts
// an Authentication-Results header only when its authserv-id identifies the
// RECEIVING system, which it can only decide from the receiver's own address and
// provider. Supply neither and every verdict silently degrades to unknown; supply
// the wrong provider and a forged header becomes believable. Both failures are
// invisible in the pure tests, which construct the Receiver by hand.

// warmupMsgWithHeaders builds a verified warmup message carrying caller-chosen
// extra headers, so a test can drive the identity extractor from the poll path.
// extra is inserted ABOVE the warmup token, which is where a receiving MTA
// prepends its own Authentication-Results.
func warmupMsgWithHeaders(t *testing.T, uid uint32, token, messageID, extra string) mail.InboundMessage {
	t.Helper()
	raw := extra + "From: partner@warm.test\nTo: me@recv.test\nSubject: Re: catching up\n" +
		"Message-ID: " + messageID + "\nX-Inroad-Warmup: " + token + "\n\nGreat, talk soon.\n"
	return inboundMsg(t, uid, raw)
}

// runGmailWarmupPoll is runGmailPoll with the warmup hook armed. The existing
// gmail runner passes a nil secret, so no message it fetches is ever detected as
// warmup — which is right for the classification tests and useless here.
func runGmailWarmupPoll(t *testing.T, core coreapi.Client, gmail GmailFetcher, enq WarmupEngageEnqueuer) error {
	t.Helper()
	return PollHandler(core, nil, gmail, nil, replyclassify.New(nil), warmupSecret, enq)(context.Background(), pollTask(t))
}

// wantIdentity asserts all five identity fields of the single recorded receipt.
func wantIdentity(t *testing.T, got coreapi.WarmupReceiptInput, dkimDomain, returnPath, spf, dkim, dmarc string) {
	t.Helper()
	if got.DKIMDomain != dkimDomain {
		t.Errorf("DKIMDomain = %q, want %q", got.DKIMDomain, dkimDomain)
	}
	if got.ReturnPathDomain != returnPath {
		t.Errorf("ReturnPathDomain = %q, want %q", got.ReturnPathDomain, returnPath)
	}
	if got.SPFResult != spf || got.DKIMResult != dkim || got.DMARCResult != dmarc {
		t.Errorf("verdicts spf/dkim/dmarc = %q/%q/%q, want %q/%q/%q",
			got.SPFResult, got.DKIMResult, got.DMARCResult, spf, dkim, dmarc)
	}
}

// onlyReceipt returns the single receipt the poll recorded.
func onlyReceipt(t *testing.T, core *warmupStubCore) coreapi.WarmupReceiptInput {
	t.Helper()
	if len(core.receipts) != 1 {
		t.Fatalf("expected exactly 1 warmup receipt recorded, got %d", len(core.receipts))
	}
	return core.receipts[0]
}

// The headline: a self-hosted (IMAP) mailbox whose own MTA stamped the results.
// The authserv-id `recv.test` shares an organizational domain with the polled
// mailbox's address, which is the ONLY reason the verdicts are believed — so this
// fails outright if InboxPollJob.Email is not threaded into the Receiver, because
// an empty address matches no authserv-id and every verdict degrades to unknown.
func TestWarmupReceiptCarriesTheVerdictsTheReceiversOwnMTAStamped(t *testing.T) {
	core := newWarmupCore(t)
	core.job.Email = "me@recv.test"
	token := warmupToken(t, warmupSecret, pollWS, "send-1")
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{warmupMsgWithHeaders(t, 11, token, "<wm-1@warm>",
			"Authentication-Results: recv.test; spf=pass smtp.mailfrom=bounce@mail.acme.test; "+
				"dkim=pass header.d=acme.test; dmarc=pass header.from=acme.test\n"+
				"Return-Path: <bounce@mail.acme.test>\n"+
				"DKIM-Signature: v=1; a=rsa-sha256; d=acme.test; s=sel; b=abc\n")},
	}

	if err := runWarmupPoll(t, core, reader, &spyEngageEnqueuer{}); err != nil {
		t.Fatal(err)
	}
	wantIdentity(t, onlyReceipt(t, core), "acme.test", "mail.acme.test", "pass", "pass", "pass")
}

// The security property, asserted where the Receiver is actually built rather
// than handed in by a test. An IMAP mailbox stamps no Authentication-Results at
// all, so a forged `mx.google.com` header is the only candidate in the message —
// and it must still not be believed, because the recipient is not on Gmail.
//
// This is what breaks if the poller ever reports a provider it isn't (or a
// hardcoded one): the provider allowlist would fire for a mailbox Google never
// touched, and an attacker who won the first-write race would be choosing the
// verdicts we record.
func TestForgedProviderAuthResultsAreNotBelievedForAnIMAPMailbox(t *testing.T) {
	core := newWarmupCore(t)
	core.job.Email = "me@recv.test"
	token := warmupToken(t, warmupSecret, pollWS, "send-1")
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{warmupMsgWithHeaders(t, 11, token, "<wm-1@warm>",
			"Authentication-Results: mx.google.com; spf=pass; dkim=pass; dmarc=pass\n"+
				"DKIM-Signature: v=1; a=rsa-sha256; d=attacker.test; s=sel; b=abc\n")},
	}

	if err := runWarmupPoll(t, core, reader, &spyEngageEnqueuer{}); err != nil {
		t.Fatal(err)
	}
	got := onlyReceipt(t, core)
	if got.SPFResult != "unknown" || got.DKIMResult != "unknown" || got.DMARCResult != "unknown" {
		t.Fatalf("verdicts spf/dkim/dmarc = %q/%q/%q, want all unknown: an IMAP mailbox stamps no "+
			"Authentication-Results, so a header claiming to be Google's is the sender's",
			got.SPFResult, got.DKIMResult, got.DMARCResult)
	}
}

// The API branch of GetInboxPollJob had no identity at all before this slice —
// it returned Provider/AccessToken/Cursor and nothing else — so the gmail path is
// where the receiver is most likely to arrive empty. Google's authserv-id is
// believed here ONLY because the receiver reports provider "gmail"; the address
// alone would not do it, since recv.test is not google.com.
func TestGmailPollThreadsItsProviderSoGoogleStampedResultsAreBelieved(t *testing.T) {
	core := newWarmupCore(t)
	core.job = coreapi.InboxPollJob{
		Provider: "gmail", AccessToken: []byte("tok"), Cursor: "1000", Email: "me@recv.test",
	}
	token := warmupToken(t, warmupSecret, pollWS, "send-1")
	gmail := &fakeGmailReader{
		newCursor: "2000",
		msgs: []mail.InboundMessage{warmupMsgWithHeaders(t, 0, token, "<wm-g@warm>",
			"Authentication-Results: mx.google.com; spf=pass smtp.mailfrom=bounce@mail.acme.test; "+
				"dkim=pass header.d=acme.test; dmarc=fail header.from=acme.test\n"+
				"Return-Path: <bounce@mail.acme.test>\n"+
				"DKIM-Signature: v=1; a=rsa-sha256; d=acme.test; s=sel; b=abc\n")},
	}

	if err := runGmailWarmupPoll(t, core, gmail, &spyEngageEnqueuer{}); err != nil {
		t.Fatal(err)
	}
	wantIdentity(t, onlyReceipt(t, core), "acme.test", "mail.acme.test", "pass", "pass", "fail")
}

// Design §8's last bullet, at the level where it can actually regress. Headers
// that parse as a message but state nothing usable — a signature with no d=, a
// Return-Path that is not an address, an Authentication-Results with no
// methodspecs — must leave a receipt behind with empty domains and unknown
// verdicts, AND must let the cursor advance.
//
// The cursor is the assertion that matters. The tabbed-capability bug did exactly
// this: a refusal deep in the receipt transaction returned before SetInboxCursor,
// so the same message was re-fetched and re-failed forever and the mailbox stopped
// processing all inbound mail — campaign replies and bounces included — over one
// field. Identity gates nothing, so it must never be able to cost that.
func TestUnparseableIdentityHeadersRecordUnknownAndStillAdvanceTheCursor(t *testing.T) {
	core := newWarmupCore(t)
	core.job.Email = "me@recv.test"
	token := warmupToken(t, warmupSecret, pollWS, "send-1")
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12,
		msgs: []mail.InboundMessage{warmupMsgWithHeaders(t, 11, token, "<wm-1@warm>",
			"Authentication-Results: not-a-header-at-all\n"+
				"Return-Path: definitely not an address\n"+
				"DKIM-Signature: v=1; a=rsa-sha256; s=sel; b=abc\n")},
	}

	if err := runWarmupPoll(t, core, reader, &spyEngageEnqueuer{}); err != nil {
		t.Fatalf("a malformed header failed the poll: %v", err)
	}
	got := onlyReceipt(t, core)
	if got.Placement != placementInbox {
		t.Errorf("placement = %q, want %q: the placement is the reputation signal and the identity "+
			"is metadata on it, so a failed parse must not cost the observation", got.Placement, placementInbox)
	}
	wantIdentity(t, got, "", "", "unknown", "unknown", "unknown")
	if !core.cursorSet {
		t.Fatal("the cursor did not advance: a message whose identity cannot be parsed would be " +
			"re-fetched forever and ALL inbound processing for this mailbox would stop")
	}
}

// The junk scan is a second call site of recordWarmup, and it is the one a change
// to the reading path is most likely to miss — it was already the path where the
// tab capability had to be threaded separately. A spam-placed warmup message is
// still a message the receiver authenticated, so its identity is recorded too.
func TestJunkScannedWarmupAlsoCarriesItsIdentity(t *testing.T) {
	core := newWarmupCore(t)
	core.job.Email = "me@recv.test"
	token := warmupToken(t, warmupSecret, pollWS, "send-2")
	reader := &fakeReader{
		uidValidity: 5, uidNext: 12, // empty INBOX batch
		junkFolder: "Junk",
		junkMsgs: []mail.InboundMessage{warmupMsgWithHeaders(t, 3, token, "<wm-j@warm>",
			"Authentication-Results: recv.test; spf=fail smtp.mailfrom=bounce@mail.acme.test; "+
				"dkim=fail header.d=acme.test; dmarc=fail header.from=acme.test\n"+
				"Return-Path: <bounce@mail.acme.test>\n"+
				"DKIM-Signature: v=1; a=rsa-sha256; d=acme.test; s=sel; b=abc\n")},
	}

	if err := runWarmupPoll(t, core, reader, &spyEngageEnqueuer{}); err != nil {
		t.Fatal(err)
	}
	got := onlyReceipt(t, core)
	if got.Placement != placementSpam {
		t.Fatalf("placement = %q, want spam", got.Placement)
	}
	wantIdentity(t, got, "acme.test", "mail.acme.test", "fail", "fail", "fail")
}
