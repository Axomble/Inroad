package inbox

import "testing"

// hardBounceDSN mirrors a Postfix-style DSN: multipart/report with a
// message/delivery-status part and a message/rfc822-headers part.
const hardBounceDSN = `From: Mail Delivery System <MAILER-DAEMON@mail.example.com>
To: sender@example.com
Subject: Undelivered Mail Returned to Sender
Content-Type: multipart/report; report-type=delivery-status;
	boundary="BOUNDARY1"
MIME-Version: 1.0

--BOUNDARY1
Content-Description: Notification
Content-Type: text/plain; charset=us-ascii

This is the mail system at host mail.example.com.

I'm sorry to have to inform you that your message could not
be delivered to one or more recipients.

--BOUNDARY1
Content-Description: Delivery report
Content-Type: message/delivery-status

Reporting-MTA: dns; mail.example.com
X-Postfix-Queue-ID: 4XXXXX
X-Postfix-Sender: rfc822; sender@example.com
Arrival-Date: Mon, 1 Jan 2026 10:00:00 -0500

Final-Recipient: rfc822; nobody@recipient.example.com
Original-Recipient: rfc822; nobody@recipient.example.com
Action: failed
Status: 5.1.1
Remote-MTA: dns; mx.recipient.example.com
Diagnostic-Code: smtp; 550 5.1.1 <nobody@recipient.example.com>: Recipient address rejected: User unknown in virtual mailbox table

--BOUNDARY1
Content-Description: Undelivered Message Headers
Content-Type: message/rfc822-headers

Return-Path: <sender@example.com>
Received: from mail.example.com (localhost [127.0.0.1]) by mail.example.com (Postfix) with ESMTP id 4XXXXX for <nobody@recipient.example.com>; Mon, 1 Jan 2026 10:00:00 -0500 (EST)
Message-ID: <orig@x>
To: nobody@recipient.example.com
From: sender@example.com
Subject: Hello there

--BOUNDARY1--
`

// softBounceDSN mirrors an Exchange/Microsoft-style DSN: different MTA
// naming (Reporting-MTA host, no X-Postfix-* fields) and the original
// message returned as a full message/rfc822 part instead of
// message/rfc822-headers — exercising the other variant of that part.
const softBounceDSN = `From: Postmaster <postmaster@contoso.example>
To: sender@example.com
Subject: Undeliverable: Hello there
Content-Type: multipart/report; report-type=delivery-status;
	boundary="BOUNDARY2"
MIME-Version: 1.0

--BOUNDARY2
Content-Type: text/plain; charset="us-ascii"

Your message wasn't delivered to someone@contoso.example because the remote server is refusing connections.

--BOUNDARY2
Content-Type: message/delivery-status

Reporting-MTA: dns;CO1PR00MB1234.namprd00.prod.contoso.example
Received-From-MTA: dns;mail.example.com
Arrival-Date: Mon, 1 Jan 2026 10:05:00 +0000

Final-Recipient: rfc822;someone@contoso.example
Action: failed
Status: 4.2.2
Diagnostic-Code: smtp;452 4.2.2 Mailbox full

--BOUNDARY2
Content-Type: message/rfc822

From: sender@example.com
To: someone@contoso.example
Subject: Hello there
Message-ID: <orig2@y>
Date: Mon, 1 Jan 2026 10:04:00 +0000

Body text here.
--BOUNDARY2--
`

const normalEmail = `From: alice@example.com
To: bob@example.com
Subject: Quick question
Content-Type: text/plain; charset=us-ascii
Message-ID: <regular@example.com>

Hey, are we still on for tomorrow?
`

// successDSN is a relay-success delivery report (Status: 2.x.x). A DSN isn't
// necessarily a failure — success/relayed notifications must never be
// classified as a bounce.
const successDSN = `From: Mail Delivery System <MAILER-DAEMON@mail.example.com>
To: sender@example.com
Subject: Successful Mail Delivery Report
Content-Type: multipart/report; report-type=delivery-status;
	boundary="BOUNDARY3"
MIME-Version: 1.0

--BOUNDARY3
Content-Description: Notification
Content-Type: text/plain; charset=us-ascii

This is the mail system at host mail.example.com.

Your message was successfully delivered.

--BOUNDARY3
Content-Description: Delivery report
Content-Type: message/delivery-status

Reporting-MTA: dns; mail.example.com
Arrival-Date: Mon, 1 Jan 2026 10:00:00 -0500

Final-Recipient: rfc822; someone@recipient.example.com
Action: delivered
Status: 2.0.0
Remote-MTA: dns; mx.recipient.example.com

--BOUNDARY3
Content-Description: Undelivered Message Headers
Content-Type: message/rfc822-headers

Message-ID: <orig3@z>
To: someone@recipient.example.com
From: sender@example.com
Subject: Hello there

--BOUNDARY3--
`

// wrongReportTypeDSN declares report-type=disposition-notification (an MDN,
// RFC 3798 — a "read receipt", not a bounce) but its body happens to carry a
// correctly-shaped message/delivery-status part (per-message field group,
// blank line, then a hard-bounce-shaped recipient group) all the same. This
// is synthetic (a real MDN wouldn't contain such a part) but it pins down
// the report-type gate: without checking report-type, this part would parse
// exactly like a real hard bounce, so this proves ParseDSN only walks a
// report for Status/Final-Recipient/Message-ID when report-type is actually
// delivery-status.
const wrongReportTypeDSN = `From: Mail Delivery System <MAILER-DAEMON@mail.example.com>
To: sender@example.com
Subject: Read: Hello there
Content-Type: multipart/report; report-type=disposition-notification;
	boundary="BOUNDARY4"
MIME-Version: 1.0

--BOUNDARY4
Content-Type: text/plain; charset=us-ascii

Your message was displayed.

--BOUNDARY4
Content-Type: message/delivery-status

Reporting-MTA: dns; mail.example.com

Final-Recipient: rfc822; nobody@recipient.example.com
Action: failed
Status: 5.1.1

--BOUNDARY4--
`

// missingReportTypeParamDSN is a real-world quirk: a mailer-daemon DSN whose
// Content-Type declares multipart/report but omits the report-type
// parameter entirely (some older/misconfigured MTAs do this). The From
// mailer-daemon fallback signal lets this still be walked as a
// delivery-status report — only an explicit, different report-type (the
// wrongReportTypeDSN case above) is excluded.
const missingReportTypeParamDSN = `From: Mail Delivery System <MAILER-DAEMON@mail.example.com>
To: sender@example.com
Subject: Undelivered Mail Returned to Sender
Content-Type: multipart/report; boundary="BOUNDARY5"
MIME-Version: 1.0

--BOUNDARY5
Content-Type: text/plain; charset=us-ascii

Your message could not be delivered.

--BOUNDARY5
Content-Type: message/delivery-status

Reporting-MTA: dns; mail.example.com

Final-Recipient: rfc822; nobody@recipient.example.com
Action: failed
Status: 5.1.1

--BOUNDARY5
Content-Type: message/rfc822-headers

Message-ID: <orig5@x>
To: nobody@recipient.example.com
From: sender@example.com
Subject: Hello there

--BOUNDARY5--
`

// mailerDaemonUnstructured is a From: mailer-daemon message with no
// multipart/report structure — just a free-form bounce transcript, as some
// older/misconfigured MTAs still send.
const mailerDaemonUnstructured = `From: MAILER-DAEMON@mail.example.com
To: sender@example.com
Subject: Returned mail: see transcript for details
Content-Type: text/plain; charset=us-ascii

   ----- The following addresses had permanent fatal errors -----
<nobody@example.com>

   ----- Transcript of session follows -----
... while talking to mx.example.com.:
>>> RCPT To:<nobody@example.com>
<<< 550 5.1.1 User unknown
554 5.0.0 Service unavailable
`

func TestParseDSNHardBounce(t *testing.T) {
	hdr, ct, body := parseFixture(t, hardBounceDSN)
	r := ParseDSN(hdr, ct, body)
	if r.Kind != HardBounce {
		t.Errorf("Kind = %v, want HardBounce", r.Kind)
	}
	if r.StatusCode != "5.1.1" {
		t.Errorf("StatusCode = %q, want 5.1.1", r.StatusCode)
	}
	if r.OriginalMessageID != "<orig@x>" {
		t.Errorf("OriginalMessageID = %q, want <orig@x>", r.OriginalMessageID)
	}
	if r.FailedRecipient != "nobody@recipient.example.com" {
		t.Errorf("FailedRecipient = %q, want nobody@recipient.example.com", r.FailedRecipient)
	}
}

func TestParseDSNSoftBounce(t *testing.T) {
	hdr, ct, body := parseFixture(t, softBounceDSN)
	r := ParseDSN(hdr, ct, body)
	if r.Kind != SoftBounce {
		t.Errorf("Kind = %v, want SoftBounce", r.Kind)
	}
	if r.StatusCode != "4.2.2" {
		t.Errorf("StatusCode = %q, want 4.2.2", r.StatusCode)
	}
	if r.OriginalMessageID != "<orig2@y>" {
		t.Errorf("OriginalMessageID = %q, want <orig2@y>", r.OriginalMessageID)
	}
	if r.FailedRecipient != "someone@contoso.example" {
		t.Errorf("FailedRecipient = %q, want someone@contoso.example", r.FailedRecipient)
	}
}

func TestParseDSNSuccessReportIsNotABounce(t *testing.T) {
	hdr, ct, body := parseFixture(t, successDSN)
	r := ParseDSN(hdr, ct, body)
	if r.Kind != NotABounce {
		t.Errorf("Kind = %v, want NotABounce for a 2.x.x success report", r.Kind)
	}
}

// Only report-type=delivery-status is walked for Status/Final-Recipient/
// Message-ID — an MDN (disposition-notification) or ARF (feedback-report)
// must not be misread as a bounce even if a part inside happens to look
// like a delivery-status block.
func TestParseDSNWrongReportTypeIsNotWalked(t *testing.T) {
	hdr, ct, body := parseFixture(t, wrongReportTypeDSN)
	r := ParseDSN(hdr, ct, body)
	if r.Kind != NotABounce {
		t.Errorf("Kind = %v, want NotABounce (report-type != delivery-status)", r.Kind)
	}
}

// When report-type is missing (not merely wrong), the mailer-daemon From
// fallback still lets a structurally-valid delivery-status report be
// walked and classified.
func TestParseDSNMissingReportTypeParamFallsBackToMailerDaemonFrom(t *testing.T) {
	hdr, ct, body := parseFixture(t, missingReportTypeParamDSN)
	r := ParseDSN(hdr, ct, body)
	if r.Kind != HardBounce {
		t.Errorf("Kind = %v, want HardBounce (mailer-daemon fallback)", r.Kind)
	}
	if r.StatusCode != "5.1.1" {
		t.Errorf("StatusCode = %q, want 5.1.1", r.StatusCode)
	}
	if r.OriginalMessageID != "<orig5@x>" {
		t.Errorf("OriginalMessageID = %q, want <orig5@x>", r.OriginalMessageID)
	}
}

func TestParseDSNNormalEmailIsNotABounce(t *testing.T) {
	hdr, ct, body := parseFixture(t, normalEmail)
	r := ParseDSN(hdr, ct, body)
	if r.Kind != NotABounce {
		t.Errorf("Kind = %v, want NotABounce", r.Kind)
	}
}

// A mailer-daemon message without a structured message/delivery-status part
// can't have Status/Final-Recipient/Message-ID reliably scraped from
// free-form transcript text, so v1 treats it as NotABounce (log + skip; see
// design spec §2, "Unparseable DSN ⇒ log + skip"). A heuristic pass over the
// transcript text is out of scope here.
func TestParseDSNMailerDaemonWithoutStructuredReportIsNotABounce(t *testing.T) {
	hdr, ct, body := parseFixture(t, mailerDaemonUnstructured)
	r := ParseDSN(hdr, ct, body)
	if r.Kind != NotABounce {
		t.Errorf("Kind = %v, want NotABounce (unparseable, best-effort skip)", r.Kind)
	}
}

// Malformed input must never panic — a fetched inbox message is untrusted.
func TestParseDSNMalformedMultipartDoesNotPanic(t *testing.T) {
	hdr, _, _ := parseFixture(t, normalEmail)
	r := ParseDSN(hdr, `multipart/report; report-type=delivery-status; boundary="X"`, []byte("not actually multipart data"))
	if r.Kind != NotABounce {
		t.Errorf("Kind = %v, want NotABounce for malformed body", r.Kind)
	}
}
