package warmup

import (
	netmail "net/mail"
	"strings"
	"testing"
)

// header parses a raw RFC5322 header block the way the poller's transports do,
// so these tests exercise the same folding and canonicalization real mail goes
// through rather than a hand-built map that could not reproduce either.
func header(t *testing.T, raw string) netmail.Header {
	t.Helper()
	msg, err := netmail.ReadMessage(strings.NewReader(raw + "\r\n\r\nbody\r\n"))
	if err != nil {
		t.Fatalf("parse test header: %v", err)
	}
	return msg.Header
}

const (
	gmailRecipient = "alice@gmail.com"
	selfRecipient  = "alice@acme.test"
)

func TestExtractIdentity(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		receiver Receiver
		want     Identity
	}{
		{
			name: "gmail stamps every verdict and the message is signed",
			raw: strings.Join([]string{
				"Authentication-Results: mx.google.com;",
				"       dkim=pass header.i=@send.acme.test header.s=s1;",
				"       spf=pass (google.com: domain of bounce@send.acme.test designates 10.0.0.1 as permitted sender) smtp.mailfrom=bounce@send.acme.test;",
				"       dmarc=pass (p=NONE sp=NONE dis=NONE) header.from=acme.test",
				"DKIM-Signature: v=1; a=rsa-sha256; d=send.acme.test; s=s1; b=abc==",
				"Return-Path: <bounce@send.acme.test>",
			}, "\r\n"),
			receiver: Receiver{Address: gmailRecipient, Provider: "gmail"},
			want: Identity{
				DKIMDomain: "send.acme.test", ReturnPathDomain: "send.acme.test",
				SPFResult: AuthPass, DKIMResult: AuthPass, DMARCResult: AuthPass,
			},
		},
		{
			name: "m365 stamps under its own authserv-id",
			raw: strings.Join([]string{
				"Authentication-Results: spf.protection.outlook.com; dkim=fail; spf=softfail; dmarc=none",
				"Return-Path: <bounce@send.acme.test>",
			}, "\r\n"),
			receiver: Receiver{Address: "alice@contoso.com", Provider: "m365"},
			want: Identity{
				ReturnPathDomain: "send.acme.test",
				// softfail is not in the vocabulary and must not be rounded to fail.
				SPFResult: AuthUnknown, DKIMResult: AuthFail, DMARCResult: AuthNone,
			},
		},
		{
			name: "an unsigned message through a relay that stamps nothing establishes nothing",
			raw: strings.Join([]string{
				"Subject: hello",
				"Return-Path: <bounce@send.acme.test>",
			}, "\r\n"),
			receiver: Receiver{Address: selfRecipient, Provider: "smtp"},
			want:     Identity{ReturnPathDomain: "send.acme.test", SPFResult: AuthUnknown, DKIMResult: AuthUnknown, DMARCResult: AuthUnknown},
		},
		{
			// THE security property of this slice. Gmail prepends its own header, so
			// a forged one necessarily sits below it — and the one below must lose,
			// even though it claims the more favourable result.
			name: "a forged Authentication-Results below the receiver's cannot overturn it",
			// Both headers claim Gmail's authserv-id, so the trust check alone cannot
			// separate them and ORDER is the only thing that does. An attacker who
			// merely names an unrelated authserv-id is already rejected by
			// receiverStamped; the one worth testing forges a plausible one.
			raw: strings.Join([]string{
				"Authentication-Results: mx.google.com; dkim=fail; spf=fail; dmarc=fail",
				"Authentication-Results: mx.google.com; dkim=pass; spf=pass; dmarc=pass",
				"DKIM-Signature: v=1; a=rsa-sha256; d=send.acme.test; s=s1; b=abc==",
			}, "\r\n"),
			receiver: Receiver{Address: gmailRecipient, Provider: "gmail"},
			want: Identity{
				DKIMDomain: "send.acme.test",
				SPFResult:  AuthFail, DKIMResult: AuthFail, DMARCResult: AuthFail,
			},
		},
		{
			// The hole the provider allowlist opens if it is not gated on the
			// recipient's own provider: IMAP stamps nothing, so an attacker's forged
			// header is the ONLY candidate and would be believed on the strength of
			// naming Google.
			name: "an IMAP mailbox does not believe a header claiming to be Gmail's",
			raw: strings.Join([]string{
				"Authentication-Results: mx.google.com; dkim=pass; spf=pass; dmarc=pass",
				"DKIM-Signature: v=1; a=rsa-sha256; d=send.acme.test; s=s1; b=abc==",
			}, "\r\n"),
			receiver: Receiver{Address: selfRecipient, Provider: "smtp"},
			want: Identity{
				DKIMDomain: "send.acme.test",
				SPFResult:  AuthUnknown, DKIMResult: AuthUnknown, DMARCResult: AuthUnknown,
			},
		},
		{
			// The converse: a self-hosted receiver whose MX lives in its own
			// organizational domain IS the receiving system, and must be trusted, or
			// self-hosters — the product's core audience — observe nothing ever.
			name: "a self-hosted receiver in the recipient's own domain is trusted",
			raw: strings.Join([]string{
				"Authentication-Results: mx.acme.test; dkim=pass; spf=neutral; dmarc=none",
			}, "\r\n"),
			receiver: Receiver{Address: selfRecipient, Provider: "smtp"},
			want:     Identity{SPFResult: AuthNeutral, DKIMResult: AuthPass, DMARCResult: AuthNone},
		},
		{
			// The INVERSE of what this test asserted before, and the change is the
			// security fix. Skipping an untrusted header and continuing down the
			// message was exploitable: wherever the receiver's own stamp fails the
			// trust check, the scan walks past it and believes a forged header
			// below. Only the topmost is considered now, so an untrusted one at the
			// top yields unknown. Nothing legitimate is lost — the sender cannot
			// prepend above the receiving MTA — and unknown gates nothing.
			name: "an untrusted topmost header yields unknown rather than a search",
			raw: strings.Join([]string{
				"Authentication-Results: evil.test; dkim=pass; spf=pass; dmarc=pass",
				"Authentication-Results: mx.google.com; dkim=fail; spf=none; dmarc=fail",
			}, "\r\n"),
			receiver: Receiver{Address: gmailRecipient, Provider: "gmail"},
			want:     Identity{SPFResult: AuthUnknown, DKIMResult: AuthUnknown, DMARCResult: AuthUnknown},
		},
		{
			// HIGH 1 from the security audit, as a permanent test. A shared consumer
			// domain is not a trust unit: nobody with an @gmail.com mailbox owns
			// gmail.com's MTAs. It was worse than a blind spot — Gmail's genuine
			// stamp folds to google.com, which does NOT equal gmail.com, so the real
			// verdict failed the check and the forgery was the only candidate left.
			name: "a consumer-domain mailbox does not treat its own domain as a trust unit",
			raw: strings.Join([]string{
				"Authentication-Results: mx.google.com; dkim=fail; spf=fail; dmarc=fail",
				"Authentication-Results: gmail.com; dkim=pass; spf=pass; dmarc=pass",
			}, "\r\n"),
			receiver: Receiver{Address: gmailRecipient, Provider: "smtp"},
			want:     Identity{SPFResult: AuthUnknown, DKIMResult: AuthUnknown, DMARCResult: AuthUnknown},
		},
		{
			// The case that ISOLATES the consumer-domain rule. With a genuine header
			// on top, topmost-only already saves us; here the relay stamps NOTHING,
			// so the forgery is the only candidate and the trust rule is the sole
			// defence. This is the configuration the audit exploited.
			name: "a relay that stamps nothing does not let a consumer-domain forgery stand",
			raw: strings.Join([]string{
				"Authentication-Results: gmail.com; dkim=pass; spf=pass; dmarc=pass",
			}, "\r\n"),
			receiver: Receiver{Address: gmailRecipient, Provider: "smtp"},
			want:     Identity{SPFResult: AuthUnknown, DKIMResult: AuthUnknown, DMARCResult: AuthUnknown},
		},
		{
			// The other half of MEDIUM 3. An unterminated quote used to leave the
			// remainder parsed as ordinary content, so "; dmarc=pass" inside it
			// became a methodspec — and the doc comment claimed the opposite was
			// happening, which is the "two things that must agree" drift again.
			name: "an unterminated quote makes the header untrusted",
			raw: strings.Join([]string{
				`Authentication-Results: mx.google.com; dkim=fail; spf=pass smtp.mailfrom="oops (c) ; dmarc=pass ; more`,
			}, "\r\n"),
			receiver: Receiver{Address: gmailRecipient, Provider: "gmail"},
			want:     Identity{SPFResult: AuthUnknown, DKIMResult: AuthUnknown, DMARCResult: AuthUnknown},
		},
		{
			// HIGH 2. Exchange Online omits the authserv-id and opens with a
			// methodspec, so the old parser read "spf=fail" AS the id, could not
			// fold it, skipped the genuine header, and fell through to a forgery
			// naming outlook.com. Every m365 mailbox was silently unknown, and the
			// allowlist matched only forged headers.
			name: "an Exchange Online header with no authserv-id is the receiver's own",
			raw: strings.Join([]string{
				"Authentication-Results: spf=fail (sender IP is 10.0.0.1) smtp.mailfrom=evil.test; dkim=fail (no sig) header.d=evil.test;dmarc=fail action=oreject header.from=evil.test;compauth=fail reason=001",
				"Authentication-Results: outlook.com; dkim=pass; spf=pass; dmarc=pass",
			}, "\r\n"),
			receiver: Receiver{Address: "alice@contoso.com", Provider: "m365"},
			want:     Identity{SPFResult: AuthFail, DKIMResult: AuthFail, DMARCResult: AuthFail},
		},
		{
			// The id-less form is accepted ONLY for a mailbox we know is on m365.
			// For anyone else it is unattributable, and accepting it would let a
			// relay that stamps nothing be impersonated by a forgery that simply
			// omits the id.
			name: "an id-less header is not believed for a non-m365 mailbox",
			raw: strings.Join([]string{
				"Authentication-Results: spf=pass smtp.mailfrom=evil.test; dkim=pass; dmarc=pass",
			}, "\r\n"),
			receiver: Receiver{Address: selfRecipient, Provider: "smtp"},
			want:     Identity{SPFResult: AuthUnknown, DKIMResult: AuthUnknown, DMARCResult: AuthUnknown},
		},
		{
			// MEDIUM 3. Closing the receiver's own comment EARLY with a ')' spills
			// attacker text into the methodspec stream ahead of the genuine verdict,
			// where first-occurrence-wins locks it in. Truncating at the damage does
			// not help — the injection precedes it — but closing early leaves the
			// receiver's own ')' unbalanced, so the whole header is refused.
			name: "a comment closed early by injected text makes the header untrusted",
			raw: strings.Join([]string{
				"Authentication-Results: mx.google.com; spf=pass (google.com: domain of a)x; dmarc=pass; z@evil.test designates 10.0.0.1) smtp.mailfrom=a@evil.test; dmarc=fail",
			}, "\r\n"),
			receiver: Receiver{Address: gmailRecipient, Provider: "gmail"},
			want:     Identity{SPFResult: AuthUnknown, DKIMResult: AuthUnknown, DMARCResult: AuthUnknown},
		},
		{
			// LOW 5. Anything <= 253 bytes used to be persisted verbatim as a
			// "domain". Rejected outright rather than sanitised: a repaired string
			// is a DIFFERENT domain and would file this observation under a fault
			// domain it has nothing to do with.
			name: "a d= value that is not a hostname is not stored as one",
			raw: strings.Join([]string{
				`DKIM-Signature: v=1; a=rsa-sha256; d=<script>alert(1)</script>; s=s1; b=abc==`,
				"Return-Path: <bounce@ok.test>",
			}, "\r\n"),
			receiver: Receiver{Address: selfRecipient, Provider: "smtp"},
			want:     Identity{ReturnPathDomain: "ok.test", SPFResult: AuthUnknown, DKIMResult: AuthUnknown, DMARCResult: AuthUnknown},
		},
		{
			// A comment carrying both delimiters this parser splits on. Parsed
			// naively, the "dkim=pass" inside the parentheses becomes a methodspec.
			name: "a comment containing a semicolon and an equals sign is not a methodspec",
			raw: strings.Join([]string{
				`Authentication-Results: mx.google.com; spf=fail (google.com: bad; dkim=pass ; end) smtp.mailfrom=x@y.test; dmarc=fail`,
			}, "\r\n"),
			receiver: Receiver{Address: gmailRecipient, Provider: "gmail"},
			want:     Identity{SPFResult: AuthFail, DKIMResult: AuthUnknown, DMARCResult: AuthFail},
		},
		{
			name: "an authserv-id version number and a method version are both tolerated",
			raw: strings.Join([]string{
				"Authentication-Results: mx.google.com 1; dkim/1=pass; spf=pass",
			}, "\r\n"),
			receiver: Receiver{Address: gmailRecipient, Provider: "gmail"},
			want:     Identity{SPFResult: AuthPass, DKIMResult: AuthPass, DMARCResult: AuthUnknown},
		},
		{
			// First occurrence wins even when it normalizes to unknown: a later pass
			// must not upgrade an earlier result this vocabulary could not express.
			name: "the first result for a method wins over a later, kinder one",
			raw: strings.Join([]string{
				"Authentication-Results: mx.google.com; dkim=permerror; dkim=pass",
			}, "\r\n"),
			receiver: Receiver{Address: gmailRecipient, Provider: "gmail"},
			want:     Identity{SPFResult: AuthUnknown, DKIMResult: AuthUnknown, DMARCResult: AuthUnknown},
		},
		{
			name: "a signature with no d= yields to the next one that has it",
			raw: strings.Join([]string{
				"DKIM-Signature: v=1; a=rsa-sha256; s=s1; b=abc==",
				"DKIM-Signature: v=1; a=rsa-sha256; d=Send.Acme.Test.; s=s2; b=def==",
			}, "\r\n"),
			receiver: Receiver{Address: selfRecipient, Provider: "smtp"},
			want:     Identity{DKIMDomain: "send.acme.test", SPFResult: AuthUnknown, DKIMResult: AuthUnknown, DMARCResult: AuthUnknown},
		},
		{
			// Folding may split a tag value across lines; the unfolded value carries
			// the continuation whitespace into the middle of the domain.
			name: "folding whitespace inside a d= value is removed",
			raw: strings.Join([]string{
				"DKIM-Signature: v=1; a=rsa-sha256; d=send.",
				"\tacme.test; s=s1; b=abc==",
			}, "\r\n"),
			receiver: Receiver{Address: selfRecipient, Provider: "smtp"},
			want:     Identity{DKIMDomain: "send.acme.test", SPFResult: AuthUnknown, DKIMResult: AuthUnknown, DMARCResult: AuthUnknown},
		},
		{
			name:     "a null return path has no domain",
			raw:      "Return-Path: <>",
			receiver: Receiver{Address: selfRecipient, Provider: "smtp"},
			want:     Identity{SPFResult: AuthUnknown, DKIMResult: AuthUnknown, DMARCResult: AuthUnknown},
		},
		{
			// The exact host, not the eTLD+1: an authentication identity is published
			// per host, and folding it would erase the sibling-vs-sibling distinction
			// the fault-domain slices exist to make.
			name:     "a subdomain return path keeps its exact host",
			raw:      "Return-Path: <bounce@Mail.Send.Acme.Test>",
			receiver: Receiver{Address: selfRecipient, Provider: "smtp"},
			want:     Identity{ReturnPathDomain: "mail.send.acme.test", SPFResult: AuthUnknown, DKIMResult: AuthUnknown, DMARCResult: AuthUnknown},
		},
		{
			name: "an authserv-id with no methodspecs states nothing",
			raw: strings.Join([]string{
				"Authentication-Results: mx.google.com",
			}, "\r\n"),
			receiver: Receiver{Address: gmailRecipient, Provider: "gmail"},
			want:     Identity{SPFResult: AuthUnknown, DKIMResult: AuthUnknown, DMARCResult: AuthUnknown},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractIdentity(header(t, tc.raw), tc.receiver)
			if got != tc.want {
				t.Errorf("ExtractIdentity()\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

// A verdict outside the vocabulary reaches the database as a CHECK violation,
// which aborts the receipt transaction and — because the poller returns before
// SetInboxCursor — wedges the mailbox's poll cursor permanently. So the invariant
// is not "usually one of five", it is "always one of five", including for input
// designed to break the parser.
func TestExtractIdentityAlwaysYieldsAVocabularyVerdict(t *testing.T) {
	valid := map[string]bool{AuthPass: true, AuthFail: true, AuthNeutral: true, AuthNone: true, AuthUnknown: true}

	hostile := []string{
		"Authentication-Results: mx.google.com; spf=",
		"Authentication-Results: mx.google.com; =pass",
		"Authentication-Results: mx.google.com; spf=pass (unterminated comment",
		`Authentication-Results: mx.google.com; spf=pass "unterminated quote`,
		`Authentication-Results: mx.google.com; spf=pass (nested (deeper (still))) ; dkim=pass`,
		`Authentication-Results: mx.google.com; spf=pass (escaped \) still inside); dkim=fail`,
		"Authentication-Results: ; spf=pass",
		"Authentication-Results: mx.google.com;;;;",
		"DKIM-Signature: ;;;=;=",
		"DKIM-Signature: d=",
		"Return-Path: @@@",
		"Return-Path: <",
	}

	for _, raw := range hostile {
		t.Run(raw, func(t *testing.T) {
			for _, r := range []Receiver{
				{Address: gmailRecipient, Provider: "gmail"},
				{Address: selfRecipient, Provider: "smtp"},
				{}, // a caller that knows nothing about the recipient
			} {
				got := ExtractIdentity(header(t, raw), r)
				for field, v := range map[string]string{"spf": got.SPFResult, "dkim": got.DKIMResult, "dmarc": got.DMARCResult} {
					if !valid[v] {
						t.Errorf("receiver %+v: %s = %q, which the database CHECK rejects", r, field, v)
					}
				}
			}
		})
	}
}

// An unidentifiable Receiver must trust nothing. It is what a caller that failed
// to resolve the mailbox would pass, and OrganizationalDomain("") returning ""
// must not be allowed to match an authserv-id that also folds to "" — that would
// make a lookup failure trust every header instead of none.
func TestUnidentifiableReceiverTrustsNothing(t *testing.T) {
	h := header(t, "Authentication-Results: mx.google.com; dkim=pass; spf=pass; dmarc=pass")

	for _, r := range []Receiver{
		{},
		{Address: "not-an-address", Provider: "smtp"},
		{Address: "", Provider: "smtp"},
		// The one that reaches the provider allowlist. Without the address guard
		// this trusted mx.google.com on the strength of a provider string alone.
		{Address: "", Provider: "gmail"},
		{Address: "   ", Provider: "gmail"},
	} {
		got := ExtractIdentity(h, r)
		if got.DKIMResult != AuthUnknown || got.SPFResult != AuthUnknown || got.DMARCResult != AuthUnknown {
			t.Errorf("receiver %+v believed an unverifiable header: %+v", r, got)
		}
	}
}

// The provider alone is sufficient, and this documents why that is safe rather
// than sloppy: Provider is an assertion that the mailbox IS hosted there, so a
// header stamped by that provider is the receiver's own by construction. The
// address only ever adds the self-hosted rule. Stated as a test because the
// tempting "harden" — also requiring the address to match the provider — would
// break every mailbox whose address is on a custom domain, which is most of them.
func TestProviderAloneEstablishesTheReceiver(t *testing.T) {
	h := header(t, "Authentication-Results: mx.google.com; dkim=pass; spf=pass; dmarc=pass")

	got := ExtractIdentity(h, Receiver{Address: "alice@some-custom-domain.test", Provider: "gmail"})
	if got.DKIMResult != AuthPass {
		t.Errorf("a Gmail-hosted mailbox on a custom domain ignored Gmail's own verdict: %+v", got)
	}
}

func TestUnknownIdentityIsAVocabularyValue(t *testing.T) {
	id := UnknownIdentity()
	if id.SPFResult != AuthUnknown || id.DKIMResult != AuthUnknown || id.DMARCResult != AuthUnknown {
		t.Errorf("UnknownIdentity() = %+v, want all verdicts %q", id, AuthUnknown)
	}
	if (Identity{}).SPFResult == AuthUnknown {
		t.Error("the zero Identity now has a valid verdict — UnknownIdentity() exists precisely because it does not, " +
			"and if that changed this helper and its callers should be revisited")
	}
}
