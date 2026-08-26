package warmup

import (
	netmail "net/mail"
	"strings"
	"testing"
)

// parseHeaderOrSkip is header() without the t.Fatalf: the mutation walk below
// generates byte sequences net/mail legitimately refuses, and those inputs never
// reach the extractor in production either.
func parseHeaderOrSkip(raw string) (netmail.Header, error) {
	msg, err := netmail.ReadMessage(strings.NewReader(raw + "\r\n\r\nbody\r\n"))
	if err != nil {
		return nil, err
	}
	return msg.Header, nil
}

// The three receivers these cases are parsed against. They mirror
// identity_test.go's fixtures: a hosted consumer mailbox, a hosted business
// mailbox, and a mailbox on a domain the workspace controls.
var (
	gmailReceiver = Receiver{Address: gmailRecipient, Provider: "gmail"}
	m365Receiver  = Receiver{Address: "alice@contoso.com", Provider: "m365"}
	smtpReceiver  = Receiver{Address: selfRecipient, Provider: "smtp"}
)

// gmailInternalHop is the claim-less handoff Gmail prepends above its own MX hop.
// It names no peer at all, which is why skipping it discards nothing — and why
// the scan may skip it without becoming the "walk past the genuine value" shape
// trustedAuthResults was corrected to avoid.
const gmailInternalHop = "Received: by 2002:a05:6512:3f0b:b0:53e:3feb:6f7d with SMTP id csp1; " +
	"Tue, 12 Aug 2026 03:14:24 -0700 (PDT)"

func TestObservedRelayIP(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		receiver Receiver
		want     string
	}{
		{
			name: "gmail records the peer its own MX observed, below the claim-less handoff",
			//nolint:gocritic // stringConcatSimplify: these fixtures are real multi-line Received headers whose parts already span continuation lines. Concatenating them, as the check suggests, buries the hop boundaries the test is about.
			raw: strings.Join([]string{
				gmailInternalHop,
				"Received: from relay.acme.test (relay.acme.test. [203.0.113.25])" +
					" by mx.google.com with ESMTPS id abc123" +
					" for <alice@gmail.com>" +
					" (version=TLS1_3 cipher=TLS_AES_256_GCM_SHA384);" +
					" Tue, 12 Aug 2026 03:14:23 -0700 (PDT)",
			}, "\r\n"),
			receiver: gmailReceiver,
			want:     "203.0.113.25",
		},
		{
			name: "a self-hosted receiver records the relay its own MTA observed",
			raw: "Received: from relay.other.test (relay.other.test [203.0.113.9])" +
				" by mx.acme.test (Postfix) with ESMTPS id 4ABC123" +
				" for <alice@acme.test>; Tue, 12 Aug 2026 10:14:25 +0000 (UTC)",
			receiver: smtpReceiver,
			want:     "203.0.113.9",
		},
		{
			name: "the bare parenthesised address Exchange writes instead of a bracketed one",
			raw: "Received: from relay.acme.test (203.0.113.25)" +
				" by contoso-com.mail.protection.outlook.com with Microsoft SMTP Server" +
				" (version=TLS1_2 cipher=TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384) id 15.20.5678.20" +
				" via Frontend Transport; Tue, 12 Aug 2026 10:14:23 +0000",
			receiver: m365Receiver,
			want:     "203.0.113.25",
		},
		{
			name: "an IPv6 relay is recorded in one canonical spelling",
			raw: "Received: from relay.acme.test (relay.acme.test. [IPv6:2001:0DB8:0000:0000:0000:0000:0000:0001])" +
				" by mx.google.com with ESMTPS id abc123; Tue, 12 Aug 2026 03:14:23 -0700 (PDT)",
			receiver: gmailReceiver,
			want:     "2001:db8::1",
		},
		{
			name: "an IPv4-mapped IPv6 literal is the same relay as its IPv4 form",
			raw: "Received: from relay.acme.test (relay.acme.test. [::ffff:203.0.113.25])" +
				" by mx.google.com with ESMTPS id abc123; Tue, 12 Aug 2026 03:14:23 -0700 (PDT)",
			receiver: gmailReceiver,
			want:     "203.0.113.25",
		},
		{
			name: "a zone identifier is not part of a relay identity",
			raw: "Received: from relay.acme.test (relay.acme.test. [2001:db8::1%eth0])" +
				" by mx.google.com with ESMTPS id abc123; Tue, 12 Aug 2026 03:14:23 -0700 (PDT)",
			receiver: gmailReceiver,
			want:     "2001:db8::1",
		},

		// The security property. Everything below the receiving boundary is
		// sender-influenceable, so a prepended hop claiming another IP must lose to
		// the topmost hop the receiver itself wrote.
		{
			name: "an attacker-prepended hop below the receiver's own loses",
			raw: strings.Join([]string{
				gmailInternalHop,
				"Received: from relay.acme.test (relay.acme.test. [203.0.113.25])" +
					" by mx.google.com with ESMTPS id abc123; Tue, 12 Aug 2026 03:14:23 -0700 (PDT)",
				"Received: from spoof.example (spoof.example. [198.51.100.7])" +
					" by mx.google.com with ESMTPS id forged; Tue, 12 Aug 2026 03:00:00 -0700 (PDT)",
			}, "\r\n"),
			receiver: gmailReceiver,
			want:     "203.0.113.25",
		},
		{
			name: "several prepended hops cannot outvote the one hop the receiver wrote",
			raw: strings.Join([]string{
				"Received: from relay.acme.test (relay.acme.test. [203.0.113.25])" +
					" by mx.google.com with ESMTPS id abc123; Tue, 12 Aug 2026 03:14:23 -0700 (PDT)",
				"Received: from spoof.example (spoof.example. [198.51.100.7])" +
					" by mx.google.com with ESMTPS id f1; Tue, 12 Aug 2026 03:00:02 -0700 (PDT)",
				"Received: from spoof.example (spoof.example. [198.51.100.7])" +
					" by mx.google.com with ESMTPS id f2; Tue, 12 Aug 2026 03:00:01 -0700 (PDT)",
				"Received: from spoof.example (spoof.example. [198.51.100.7])" +
					" by mx.google.com with ESMTPS id f3; Tue, 12 Aug 2026 03:00:00 -0700 (PDT)",
			}, "\r\n"),
			receiver: gmailReceiver,
			want:     "203.0.113.25",
		},
		{
			name: "the sender-chosen HELO name is never read as an address",
			raw: "Received: from [198.51.100.7] (relay.acme.test. [203.0.113.25])" +
				" by mx.google.com with ESMTPS id abc123; Tue, 12 Aug 2026 03:14:23 -0700 (PDT)",
			receiver: gmailReceiver,
			want:     "203.0.113.25",
		},
		{
			name: "an address in the by clause's own comment is not the observed peer",
			raw: "Received: from relay.acme.test" +
				" by mx.google.com (mx.google.com [198.51.100.9]) with ESMTPS id abc123;" +
				" Tue, 12 Aug 2026 03:14:23 -0700 (PDT)",
			receiver: gmailReceiver,
			want:     "",
		},
		{
			name: "the first address in the one comment wins even when it is unusable",
			raw: "Received: from relay.acme.test (relay.acme.test. [10.0.0.1] via [203.0.113.25])" +
				" by mx.google.com with ESMTPS id abc123; Tue, 12 Aug 2026 03:14:23 -0700 (PDT)",
			receiver: gmailReceiver,
			want:     "",
		},
		{
			name: "a HELO that injects a comment of its own cannot displace the receiver's",
			raw: "Received: from x ([198.51.100.7]) (relay.acme.test. [203.0.113.25])" +
				" by mx.google.com with ESMTPS id abc123; Tue, 12 Aug 2026 03:14:23 -0700 (PDT)",
			receiver: gmailReceiver,
			want:     "",
		},

		// Attribution. A hop we cannot show the receiver wrote states nothing, and
		// the scan stops there rather than looking below it — which is where a
		// forgery necessarily sits.
		{
			name: "a topmost hop stamped by someone other than the receiver is not believed",
			raw: "Received: from relay.acme.test (relay.acme.test. [203.0.113.25])" +
				" by mx.spoof.example with ESMTPS id abc123; Tue, 12 Aug 2026 03:14:23 -0700 (PDT)",
			receiver: gmailReceiver,
			want:     "",
		},
		{
			name: "an unattributable topmost hop does not hand the answer to a forgery below it",
			//nolint:gocritic // stringConcatSimplify: these fixtures are real multi-line Received headers whose parts already span continuation lines. Concatenating them, as the check suggests, buries the hop boundaries the test is about.
			raw: strings.Join([]string{
				"Received: from relay.acme.test (relay.acme.test. [203.0.113.25])" +
					" by mx.internal.googlemail.example with ESMTPS id abc123;" +
					" Tue, 12 Aug 2026 03:14:23 -0700 (PDT)",
				"Received: from spoof.example (spoof.example. [198.51.100.7])" +
					" by mx.google.com with ESMTPS id forged; Tue, 12 Aug 2026 03:00:00 -0700 (PDT)",
			}, "\r\n"),
			receiver: gmailReceiver,
			want:     "",
		},
		{
			name: "a hop with no by clause at all names no receiver",
			raw: "Received: from relay.acme.test (relay.acme.test. [203.0.113.25]);" +
				" Tue, 12 Aug 2026 03:14:23 -0700 (PDT)",
			receiver: gmailReceiver,
			want:     "",
		},
		{
			name: "a consumer mailbox polled over raw IMAP can vouch for no hop",
			raw: "Received: from relay.acme.test (relay.acme.test. [203.0.113.25])" +
				" by mx.google.com with ESMTPS id abc123; Tue, 12 Aug 2026 03:14:23 -0700 (PDT)",
			receiver: Receiver{Address: gmailRecipient, Provider: "smtp"},
			want:     "",
		},
		{
			name: "a receiver we cannot name vouches for nothing",
			raw: "Received: from relay.acme.test (relay.acme.test. [203.0.113.25])" +
				" by mx.google.com with ESMTPS id abc123; Tue, 12 Aug 2026 03:14:23 -0700 (PDT)",
			receiver: Receiver{Provider: "gmail"},
			want:     "",
		},

		// The receiver's own infrastructure is not a relay identity.
		{
			name: "m365's internal hops sit above its ingress hop, so nothing is recorded",
			raw: strings.Join([]string{
				"Received: from DM6PR11MB4620.namprd11.prod.outlook.com (2603:10b6:5:2a5::13)" +
					" by SN7PR11MB6996.namprd11.prod.outlook.com with HTTPS;" +
					" Tue, 12 Aug 2026 10:14:25 +0000",
				"Received: from BN8PR03CA0025.namprd03.prod.outlook.com (2603:10b6:408:94::38)" +
					" by DM6PR11MB4620.namprd11.prod.outlook.com (2603:10b6:5:2a5::13)" +
					" with Microsoft SMTP Server id 15.20.5678.20; Tue, 12 Aug 2026 10:14:24 +0000",
				"Received: from relay.acme.test (203.0.113.25)" +
					" by BN8PR03CA0025.outlook.office365.com (2603:10b6:408:94:cafe::5c)" +
					" with Microsoft SMTP Server id 15.20.5678.20 via Frontend Transport;" +
					" Tue, 12 Aug 2026 10:14:23 +0000",
			}, "\r\n"),
			receiver: m365Receiver,
			want:     "",
		},
		{
			name: "a peer inside the receiver's own domain identifies the receiver, not a relay",
			raw: "Received: from mail.acme.test (mail.acme.test [203.0.113.9])" +
				" by mx.acme.test (Postfix) with ESMTPS id 4ABC123;" +
				" Tue, 12 Aug 2026 10:14:25 +0000 (UTC)",
			receiver: smtpReceiver,
			want:     "",
		},
		{
			name: "a receiver-internal topmost hop does not hand the answer to a forgery below it",
			//nolint:gocritic // stringConcatSimplify: these fixtures are real multi-line Received headers whose parts already span continuation lines. Concatenating them, as the check suggests, buries the hop boundaries the test is about.
			raw: strings.Join([]string{
				"Received: from mail.acme.test (mail.acme.test [203.0.113.9])" +
					" by mx.acme.test (Postfix) with ESMTPS id 4ABC123;" +
					" Tue, 12 Aug 2026 10:14:25 +0000 (UTC)",
				"Received: from spoof.example (spoof.example [198.51.100.7])" +
					" by mx.acme.test (Postfix) with ESMTPS id 4FORGED;" +
					" Tue, 12 Aug 2026 10:00:00 +0000 (UTC)",
			}, "\r\n"),
			receiver: smtpReceiver,
			want:     "",
		},

		// A hop that states no peer at all is skipped, because there is nothing in
		// it to read. Only these are skipped.
		{
			name: "a qmail hop whose comment contains the word by states no peer",
			//nolint:gocritic // stringConcatSimplify: these fixtures are real multi-line Received headers whose parts already span continuation lines. Concatenating them, as the check suggests, buries the hop boundaries the test is about.
			raw: strings.Join([]string{
				"Received: (qmail 12345 invoked by uid 89); 12 Aug 2026 10:14:25 -0000",
				"Received: from relay.other.test (relay.other.test [203.0.113.9])" +
					" by mx.acme.test (Postfix) with ESMTPS id 4ABC123;" +
					" Tue, 12 Aug 2026 10:14:25 +0000 (UTC)",
			}, "\r\n"),
			receiver: smtpReceiver,
			want:     "203.0.113.9",
		},
		{
			name: "an lmtp handoff above the ingress hop still stops the scan",
			//nolint:gocritic // stringConcatSimplify: these fixtures are real multi-line Received headers whose parts already span continuation lines. Concatenating them, as the check suggests, buries the hop boundaries the test is about.
			raw: strings.Join([]string{
				"Received: from mx.acme.test ([127.0.0.1])" +
					" by mx.acme.test (Dovecot) with LMTP id abc;" +
					" Tue, 12 Aug 2026 10:14:26 +0000",
				"Received: from relay.other.test (relay.other.test [203.0.113.9])" +
					" by mx.acme.test (Postfix) with ESMTPS id 4ABC123;" +
					" Tue, 12 Aug 2026 10:14:25 +0000 (UTC)",
			}, "\r\n"),
			receiver: smtpReceiver,
			want:     "",
		},

		// Parsing shapes that must not derail the scan.
		{
			name: "a quoted string inside a comment may hold an unbalanced parenthesis",
			raw: "Received: from relay.acme.test (\"weird (name\" [203.0.113.25])" +
				" by mx.google.com with ESMTPS id abc123; Tue, 12 Aug 2026 03:14:23 -0700 (PDT)",
			receiver: gmailReceiver,
			want:     "203.0.113.25",
		},
		{
			name: "a nested comment does not end the from clause early",
			raw: "Received: from relay.acme.test (rdns (verified) [203.0.113.25])" +
				" by mx.google.com with ESMTPS id abc123; Tue, 12 Aug 2026 03:14:23 -0700 (PDT)",
			receiver: gmailReceiver,
			want:     "203.0.113.25",
		},
		{
			name: "an escaped closing parenthesis does not end the comment",
			raw: `Received: from relay.acme.test (rdns\) [203.0.113.25])` +
				" by mx.google.com with ESMTPS id abc123; Tue, 12 Aug 2026 03:14:23 -0700 (PDT)",
			receiver: gmailReceiver,
			want:     "203.0.113.25",
		},
		{
			name: "a lower-case ipv6 tag is the same address literal form",
			raw: "Received: from relay.acme.test (relay.acme.test. [ipv6:2001:db8::2])" +
				" by mx.google.com with ESMTPS id abc123; Tue, 12 Aug 2026 03:14:23 -0700 (PDT)",
			receiver: gmailReceiver,
			want:     "2001:db8::2",
		},
		{
			name: "a receiver host written with a trailing dot is still the receiver",
			raw: "Received: from relay.acme.test (relay.acme.test. [203.0.113.25])" +
				" by mx.google.com. with ESMTPS id abc123; Tue, 12 Aug 2026 03:14:23 -0700 (PDT)",
			receiver: gmailReceiver,
			want:     "203.0.113.25",
		},
		{
			name: "a receiver host written with a trailing semicolon is still the receiver",
			raw: "Received: from relay.acme.test (relay.acme.test. [203.0.113.25])" +
				" by mx.google.com; Tue, 12 Aug 2026 03:14:23 -0700 (PDT)",
			receiver: gmailReceiver,
			want:     "203.0.113.25",
		},
		{
			name: "an upper-case FROM and BY are the same keywords",
			raw: "Received: FROM relay.acme.test (relay.acme.test. [203.0.113.25])" +
				" BY mx.google.com with ESMTPS id abc123; Tue, 12 Aug 2026 03:14:23 -0700 (PDT)",
			receiver: gmailReceiver,
			want:     "203.0.113.25",
		},
		{
			name: "a from clause with no comment offers no receiver-written address",
			raw: "Received: from relay.acme.test" +
				" by mx.google.com with ESMTPS id abc123; Tue, 12 Aug 2026 03:14:23 -0700 (PDT)",
			receiver: gmailReceiver,
			want:     "",
		},
		{
			name: "a from clause whose only comment is a product name offers no address",
			raw: "Received: from relay.acme.test (EHLO relay.acme.test)" +
				" by mx.google.com with ESMTPS id abc123; Tue, 12 Aug 2026 03:14:23 -0700 (PDT)",
			receiver: gmailReceiver,
			want:     "",
		},
		{
			name: "a helo parameter that looks like an address is not an observed address",
			raw: "Received: from relay.acme.test (port=25 helo=203.0.113.25)" +
				" by mx.google.com with ESMTPS id abc123; Tue, 12 Aug 2026 03:14:23 -0700 (PDT)",
			receiver: gmailReceiver,
			want:     "",
		},
		{
			name: "an unbracketed address among other words in a comment is not an address literal",
			raw: "Received: from relay.acme.test (relay.acme.test 203.0.113.25)" +
				" by mx.google.com with ESMTPS id abc123; Tue, 12 Aug 2026 03:14:23 -0700 (PDT)",
			receiver: gmailReceiver,
			want:     "",
		},

		// Malformed input. Every one of these must yield "" rather than panic.
		{
			name:     "no Received header at all",
			raw:      "Subject: hello",
			receiver: gmailReceiver,
			want:     "",
		},
		{
			name:     "an empty Received header",
			raw:      "Received:",
			receiver: gmailReceiver,
			want:     "",
		},
		{
			name:     "a Received header that is not a Received header",
			raw:      "Received: garbage",
			receiver: gmailReceiver,
			want:     "",
		},
		{
			name:     "a from keyword with nothing after it",
			raw:      "Received: from",
			receiver: gmailReceiver,
			want:     "",
		},
		{
			name:     "an unterminated comment",
			raw:      "Received: from relay.acme.test (relay.acme.test. [203.0.113.25] by mx.google.com",
			receiver: gmailReceiver,
			want:     "",
		},
		{
			name:     "an unbalanced closing parenthesis",
			raw:      "Received: from relay.acme.test) (relay.acme.test. [203.0.113.25]) by mx.google.com",
			receiver: gmailReceiver,
			want:     "",
		},
		{
			name:     "an unterminated quoted string",
			raw:      `Received: from relay.acme.test ("rdns [203.0.113.25]) by mx.google.com`,
			receiver: gmailReceiver,
			want:     "",
		},
		{
			name:     "an unterminated bracket",
			raw:      "Received: from relay.acme.test (relay.acme.test. [203.0.113.25) by mx.google.com",
			receiver: gmailReceiver,
			want:     "",
		},
		{
			name:     "an empty bracket",
			raw:      "Received: from relay.acme.test (relay.acme.test. []) by mx.google.com",
			receiver: gmailReceiver,
			want:     "",
		},
		{
			name:     "a bracket holding something that is not an address",
			raw:      "Received: from relay.acme.test (relay.acme.test. [not-an-ip]) by mx.google.com",
			receiver: gmailReceiver,
			want:     "",
		},
		{
			name:     "a bracket holding a hostname that resolves nowhere here",
			raw:      "Received: from relay.acme.test (relay.acme.test. [relay.acme.test]) by mx.google.com",
			receiver: gmailReceiver,
			want:     "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ObservedRelayIP(header(t, tc.raw), tc.receiver); got != tc.want {
				t.Errorf("ObservedRelayIP() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Private, loopback, link-local, CGNAT, multicast and unspecified addresses are
// not relay identities: an attacker can name them freely and they identify no
// infrastructure. Each is written into an otherwise VALID gmail ingress hop, so
// the only thing that can make the case pass is the range check itself.
func TestObservedRelayIPRejectsAddressesThatIdentifyNoRelay(t *testing.T) {
	unroutable := []string{
		"10.0.0.1",
		"172.16.0.1",
		"192.168.1.1",
		"127.0.0.1",
		"100.64.0.1",         // CGNAT, RFC 6598
		"100.127.255.1",      // the far end of the CGNAT block
		"::ffff:100.64.0.1",  // the same CGNAT address written as an IPv4-mapped literal
		"::ffff:192.168.1.1", // and the same for a private one
		"169.254.1.1",
		"0.0.0.0",
		"224.0.0.1",
		"::1",
		"::",
		"fd00::1",
		"fe80::1",
		"ff02::1",
	}
	for _, addr := range unroutable {
		t.Run(addr, func(t *testing.T) {
			raw := "Received: from relay.acme.test (relay.acme.test. [" + addr + "])" +
				" by mx.google.com with ESMTPS id abc123; Tue, 12 Aug 2026 03:14:23 -0700 (PDT)"
			if got := ObservedRelayIP(header(t, raw), gmailReceiver); got != "" {
				t.Errorf("ObservedRelayIP() = %q for %s, want \"\"", got, addr)
			}
		})
	}
}

// 100.63.255.255 and 100.128.0.1 bracket the CGNAT block. They are ordinary
// public addresses, and a range check written as a naive first-octet test would
// wrongly reject them — which would silently drop real relays.
func TestObservedRelayIPKeepsAddressesAdjacentToTheCGNATBlock(t *testing.T) {
	for _, addr := range []string{"100.63.255.255", "100.128.0.1"} {
		t.Run(addr, func(t *testing.T) {
			raw := "Received: from relay.acme.test (relay.acme.test. [" + addr + "])" +
				" by mx.google.com with ESMTPS id abc123; Tue, 12 Aug 2026 03:14:23 -0700 (PDT)"
			if got := ObservedRelayIP(header(t, raw), gmailReceiver); got != addr {
				t.Errorf("ObservedRelayIP() = %q, want %q", got, addr)
			}
		})
	}
}

// The number of Received headers is chosen by whoever sends the message, so the
// scan is bounded. Past the bound nothing is recorded — a hop that deep is not
// one the receiver wrote.
func TestObservedRelayIPBoundsTheHopsItScans(t *testing.T) {
	ingress := "Received: from relay.acme.test (relay.acme.test. [203.0.113.25])" +
		" by mx.google.com with ESMTPS id abc123; Tue, 12 Aug 2026 03:14:23 -0700 (PDT)"

	withinBound := append(repeat(gmailInternalHop, maxReceivedHopsScanned-1), ingress)
	if got := ObservedRelayIP(header(t, strings.Join(withinBound, "\r\n")), gmailReceiver); got != "203.0.113.25" {
		t.Errorf("ingress hop at the bound: got %q, want %q", got, "203.0.113.25")
	}

	pastBound := append(repeat(gmailInternalHop, maxReceivedHopsScanned), ingress)
	if got := ObservedRelayIP(header(t, strings.Join(pastBound, "\r\n")), gmailReceiver); got != "" {
		t.Errorf("ingress hop past the bound: got %q, want \"\"", got)
	}
}

func repeat(value string, n int) []string {
	out := make([]string, 0, n+1)
	for i := 0; i < n; i++ {
		out = append(out, value)
	}
	return out
}

// The receipt path cannot tolerate a panic: recordWarmup runs before
// SetInboxCursor, so an abort here wedges the mailbox's poll cursor and stops ALL
// inbound processing for it — campaign replies and bounce detection included.
// This walks byte-level mutations of a real hop rather than asserting a value,
// because the property under test is "never panics, always returns a string".
func TestObservedRelayIPSurvivesMutatedHeaders(t *testing.T) {
	base := "Received: from relay.acme.test (relay.acme.test. [203.0.113.25])" +
		" by mx.google.com with ESMTPS id abc123; Tue, 12 Aug 2026 03:14:23 -0700 (PDT)"
	injections := []string{"", "(", ")", "[", "]", `"`, `\`, ";", " ", "%", ":", "ipv6:", "from", "by"}

	for _, inject := range injections {
		for cut := 0; cut <= len(base); cut++ {
			raw := base[:cut] + inject
			// Truncating mid-header can produce bytes net/mail refuses; those inputs
			// never reach the extractor, so they are not what is being tested.
			msg, err := parseHeaderOrSkip(raw)
			if err != nil {
				continue
			}
			_ = ObservedRelayIP(msg, gmailReceiver)
		}
	}
}

// A header the receiver could not have written must never produce a value, no
// matter how the attacker spells the by host. Each candidate is a plausible
// near-miss for mx.google.com.
func TestObservedRelayIPRefusesNearMissReceiverHosts(t *testing.T) {
	for _, byHost := range []string{
		"mx.google.com.evil.example",
		"google.com.evil.example",
		"mxgoogle.com",
		"mx.google.co",
		"gmail.com", // Gmail's genuine stamp is mx.google.com; gmail.com is shared with strangers
		"[203.0.113.25]",
		"unknown",
	} {
		t.Run(byHost, func(t *testing.T) {
			raw := "Received: from relay.acme.test (relay.acme.test. [203.0.113.25])" +
				" by " + byHost + " with ESMTPS id abc123; Tue, 12 Aug 2026 03:14:23 -0700 (PDT)"
			if got := ObservedRelayIP(header(t, raw), gmailReceiver); got != "" {
				t.Errorf("ObservedRelayIP() = %q for by %s, want \"\"", got, byHost)
			}
		})
	}
}
