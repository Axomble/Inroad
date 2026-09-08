package inbox

import "testing"

// abuseARF is a realistically-shaped RFC 5965 feedback report: the three parts a
// real FBL sends (human-readable text, the machine-readable
// message/feedback-report, the offending message), with the address fields
// angle-bracketed the way RFC 5965's own examples write them.
const abuseARF = `From: FBL Sender <fbl@fbl.provider.example>
To: abuse-reports@acme.test
Subject: FW: Spam complaint
Date: Sun, 8 Mar 2026 14:00:00 +0000
Message-ID: <arf-1@fbl.provider.example>
MIME-Version: 1.0
Content-Type: multipart/report; report-type=feedback-report;
	boundary="ARF1"

--ARF1
Content-Type: text/plain; charset="US-ASCII"

This is an email abuse report for an email message received from IP
192.0.2.10 on Sun, 8 Mar 2026 13:59:00 +0000.

--ARF1
Content-Type: message/feedback-report

Feedback-Type: abuse
User-Agent: SomeFBL/1.0
Version: 1
Original-Mail-From: <bounces@acme.test>
Original-Rcpt-To: <recipient@corp.example>
Arrival-Date: Sun, 8 Mar 2026 13:59:00 +0000
Reporting-MTA: dns; mx.fbl.provider.example
Source-IP: 192.0.2.10
Reported-Domain: acme.test

--ARF1
Content-Type: message/rfc822

From: sales@acme.test
To: recipient@corp.example
Subject: Quick question
Message-ID: <orig-arf@acme.test>
Date: Sun, 8 Mar 2026 13:58:00 +0000

Hey, are we still on for tomorrow?
--ARF1--
`

// notSpamARF is the INVERSE signal, registered by RFC 6650: a recipient rescuing
// our mail out of their spam folder. It is a feedback report and it is not a
// complaint.
const notSpamARF = `From: FBL Sender <fbl@fbl.provider.example>
To: abuse-reports@acme.test
Subject: FW: not spam
MIME-Version: 1.0
Content-Type: multipart/report; report-type=feedback-report; boundary="ARF2"

--ARF2
Content-Type: message/feedback-report

Feedback-Type: not-spam
User-Agent: SomeFBL/1.0
Version: 1
Original-Rcpt-To: <recipient@corp.example>

--ARF2
Content-Type: message/rfc822-headers

Message-ID: <orig-notspam@acme.test>
From: sales@acme.test

--ARF2--
`

// malformedFeedbackARF declares report-type=feedback-report but its
// message/feedback-report part is garbage: no Feedback-Type, and a body that is
// not a field group at all. A broken report must yield nothing rather than
// erroring — one provider's malformed report cannot be allowed to fail a
// mailbox's whole poll.
const malformedFeedbackARF = `From: FBL Sender <fbl@fbl.provider.example>
To: abuse-reports@acme.test
Subject: FW: complaint
MIME-Version: 1.0
Content-Type: multipart/report; report-type=feedback-report; boundary="ARF3"

--ARF3
Content-Type: message/feedback-report

<<< not a field group at all >>>

--ARF3--
`

// noOriginalMessageARF is a complaint we cannot attribute: the report is
// well-formed and names a recipient, but redacts the offending message, so
// nothing in it can be matched against mail we actually sent.
const noOriginalMessageARF = `From: FBL Sender <fbl@fbl.provider.example>
To: abuse-reports@acme.test
Subject: FW: complaint
MIME-Version: 1.0
Content-Type: multipart/report; report-type=feedback-report; boundary="ARF4"

--ARF4
Content-Type: message/feedback-report

Feedback-Type: abuse
User-Agent: SomeFBL/1.0
Version: 1
Original-Rcpt-To: <recipient@corp.example>

--ARF4--
`

func TestParseARFAbuseReport(t *testing.T) {
	hdr, ct, body := parseFixture(t, abuseARF)
	r := ParseARF(hdr, ct, body)
	if r.Kind != AbuseComplaint {
		t.Fatalf("Kind = %v, want AbuseComplaint", r.Kind)
	}
	if r.FeedbackType != "abuse" {
		t.Errorf("FeedbackType = %q, want abuse", r.FeedbackType)
	}
	// Angle brackets are part of the FIELD, not of the address.
	if r.ComplainedRecipient != "recipient@corp.example" {
		t.Errorf("ComplainedRecipient = %q, want recipient@corp.example", r.ComplainedRecipient)
	}
	// The only identifier that ties the report to mail we actually sent.
	if r.OriginalMessageID != "<orig-arf@acme.test>" {
		t.Errorf("OriginalMessageID = %q, want <orig-arf@acme.test>", r.OriginalMessageID)
	}
}

// The no-regression half of the seam, in both directions: the DSN parser must not
// read an ARF as a bounce (it already declines report-type=feedback-report), and
// the ARF parser must not read a delivery-status report as a complaint. A bounce
// trims one address; a complaint suppresses it and can pause a campaign.
func TestParseARFAndParseDSNDoNotOverlap(t *testing.T) {
	arfHdr, arfCT, arfBody := parseFixture(t, abuseARF)
	if got := ParseDSN(arfHdr, arfCT, arfBody); got.Kind != NotABounce {
		t.Errorf("ParseDSN(ARF).Kind = %v, want NotABounce", got.Kind)
	}
	for name, fixture := range map[string]string{
		"hard bounce": hardBounceDSN,
		"soft bounce": softBounceDSN,
		"success DSN": successDSN,
	} {
		t.Run(name, func(t *testing.T) {
			hdr, ct, body := parseFixture(t, fixture)
			if got := ParseARF(hdr, ct, body); got.Kind != NotAFeedbackReport {
				t.Errorf("ParseARF(%s).Kind = %v, want NotAFeedbackReport", name, got.Kind)
			}
		})
	}
}

// An MDN (RFC 3798 read receipt) is the third multipart/report report-type. It is
// neither a bounce nor a complaint, and the gate is the report-type parameter —
// there is deliberately no From-based fallback here, unlike ParseDSN's
// mailer-daemon heuristic: an ARF always declares its report-type, and guessing
// would let any message claim to be a complaint.
func TestParseARFMDNIsNotAComplaint(t *testing.T) {
	hdr, ct, body := parseFixture(t, wrongReportTypeDSN)
	if got := ParseARF(hdr, ct, body); got.Kind != NotAFeedbackReport {
		t.Errorf("Kind = %v, want NotAFeedbackReport for report-type=disposition-notification", got.Kind)
	}
}

func TestParseARFNotSpamIsNotAComplaint(t *testing.T) {
	hdr, ct, body := parseFixture(t, notSpamARF)
	r := ParseARF(hdr, ct, body)
	if r.Kind != NotAFeedbackReport {
		t.Errorf("Kind = %v, want NotAFeedbackReport: not-spam is the inverse signal, "+
			"and suppressing on it would be exactly backwards", r.Kind)
	}
	if r.FeedbackType != "not-spam" {
		t.Errorf("FeedbackType = %q, want the raw value preserved for the log line", r.FeedbackType)
	}
}

func TestParseARFMalformedFeedbackPartYieldsNothing(t *testing.T) {
	hdr, ct, body := parseFixture(t, malformedFeedbackARF)
	r := ParseARF(hdr, ct, body)
	if r.Kind != NotAFeedbackReport {
		t.Errorf("Kind = %v, want NotAFeedbackReport for an unreadable feedback part", r.Kind)
	}
	if r.ComplainedRecipient != "" {
		t.Errorf("ComplainedRecipient = %q, want empty", r.ComplainedRecipient)
	}
}

// A well-formed complaint that redacts the offending message still parses; it
// simply carries no OriginalMessageID, and the caller is what declines to act on
// an unattributable report.
func TestParseARFWithoutTheOriginalMessageCarriesNoMessageID(t *testing.T) {
	hdr, ct, body := parseFixture(t, noOriginalMessageARF)
	r := ParseARF(hdr, ct, body)
	if r.Kind != AbuseComplaint {
		t.Fatalf("Kind = %v, want AbuseComplaint", r.Kind)
	}
	if r.OriginalMessageID != "" {
		t.Errorf("OriginalMessageID = %q, want empty", r.OriginalMessageID)
	}
}

// Untrusted input must never panic: a fetched inbox message is attacker-supplied.
func TestParseARFMalformedMultipartDoesNotPanic(t *testing.T) {
	hdr, _, _ := parseFixture(t, normalEmail)
	r := ParseARF(hdr, `multipart/report; report-type=feedback-report; boundary="X"`, []byte("not actually multipart data"))
	if r.Kind != NotAFeedbackReport {
		t.Errorf("Kind = %v, want NotAFeedbackReport for a malformed body", r.Kind)
	}
}

// A multipart/report with no boundary parameter cannot be walked at all.
func TestParseARFWithoutABoundaryYieldsNothing(t *testing.T) {
	hdr, _, _ := parseFixture(t, normalEmail)
	r := ParseARF(hdr, "multipart/report; report-type=feedback-report", []byte("whatever"))
	if r.Kind != NotAFeedbackReport {
		t.Errorf("Kind = %v, want NotAFeedbackReport with no boundary", r.Kind)
	}
}

// An ordinary email is not a report of any kind.
func TestParseARFNormalEmailIsNotAComplaint(t *testing.T) {
	hdr, ct, body := parseFixture(t, normalEmail)
	if got := ParseARF(hdr, ct, body); got.Kind != NotAFeedbackReport {
		t.Errorf("Kind = %v, want NotAFeedbackReport", got.Kind)
	}
}

// The address-field normalisation, directly: ARF generators disagree about angle
// brackets and some carry RFC 3464's "rfc822;" type prefix.
func TestReportAddressStripsTypePrefixAndAngleBrackets(t *testing.T) {
	cases := map[string]string{
		"<user@example.com>":         "user@example.com",
		"user@example.com":           "user@example.com",
		"rfc822; <user@example.com>": "user@example.com",
		"rfc822;user@example.com":    "user@example.com",
		"  <user@example.com>  ":     "user@example.com",
		"":                           "",
	}
	for in, want := range cases {
		if got := reportAddress(in); got != want {
			t.Errorf("reportAddress(%q) = %q, want %q", in, got, want)
		}
	}
}
