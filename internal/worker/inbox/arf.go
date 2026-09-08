package inbox

import (
	"bufio"
	"io"
	"mime"
	"net/mail"
	"net/textproto"
	"strings"
)

// FeedbackKind classifies an RFC 5965 abuse feedback report (ARF).
type FeedbackKind int

const (
	// NotAFeedbackReport means the message isn't an ARF, or is one we couldn't
	// read with enough confidence to act on — treated the same way downstream
	// (log + skip, no complaint), exactly like NotABounce.
	NotAFeedbackReport FeedbackKind = iota
	// AbuseComplaint is a recipient reporting our mail as spam or fraud: the
	// strongest opt-out signal there is.
	AbuseComplaint
)

// ARFResult is what ParseARF extracts from a feedback report. It mirrors
// DSNResult: a classified Kind, the raw discriminator it was classified from, and
// the identifiers pulled out of the report's parts.
type ARFResult struct {
	Kind FeedbackKind
	// FeedbackType is the raw RFC 5965 Feedback-Type field ("abuse", "fraud",
	// "not-spam", …). Kept alongside Kind — like DSNResult.StatusCode — so a
	// declined report can be logged with the reason it was declined.
	FeedbackType string
	// ComplainedRecipient is the address the report says complained, from
	// Original-Rcpt-To. Empty if absent.
	//
	// It is NOT the address anything gets suppressed on. An ARF arrives as
	// ordinary mail from an unauthenticated sender, so this field is
	// attacker-supplied: acting on it would let anyone able to email a connected
	// mailbox suppress an address they do not own and inflate the complaint rate
	// that pauses campaigns — the exact harm the DSN path refuses Final-Recipient
	// for. The caller resolves the complaint against OUR OWN send instead and uses
	// this only to cross-check it.
	//
	// Original-Mail-From is deliberately NOT a fallback for it. Per RFC 5965 that
	// field is the offending message's envelope SENDER — our own mailbox — so
	// falling back to it would name the wrong party entirely.
	ComplainedRecipient string
	// OriginalMessageID is the Message-ID of our own sent mail the report is
	// about, read from the returned message/rfc822(-headers) part. Empty if the
	// report redacted the message. It is the ONLY field that can tie the report to
	// mail this workspace actually sent.
	OriginalMessageID string
}

// ParseARF inspects a parsed inbox message and returns NotAFeedbackReport if it
// isn't an RFC 5965 abuse report. contentType is the message's own (outer)
// Content-Type and body is everything after the header.
//
// The leading mail.Header is accepted and UNUSED, so that this and ParseDSN are
// one seam a caller can hold both halves of identically (see the report-type note
// below for why an ARF needs no From-based fallback). Naming it _ is the point: a
// reader should not go looking for where the header is consulted.
//
// It is the sibling of ParseDSN and handles exactly the case ParseDSN declines:
// multipart/report with report-type=feedback-report, which ParseDSN refuses so
// that a complaint is never misread as a bounce. The two are mutually exclusive by
// construction — each gates on its own report-type.
//
// Detection requires an EXPLICIT report-type=feedback-report. There is no
// From-address fallback like ParseDSN's mailer-daemon heuristic: an ARF always
// declares its report-type, and a heuristic here would let any message that
// happens to look official claim to be a complaint — and a complaint suppresses an
// address and can pause a campaign, where a misread bounce trims one send.
//
// Never panics: malformed multipart bodies, missing parts, or garbage fields all
// fall back to a best-effort (possibly empty) ARFResult. A broken report from one
// provider must never fail a mailbox's whole poll.
func ParseARF(_ mail.Header, contentType string, body []byte) ARFResult {
	mediaType, params, _ := mime.ParseMediaType(contentType)
	if !strings.EqualFold(mediaType, "multipart/report") {
		return ARFResult{Kind: NotAFeedbackReport}
	}
	if !strings.EqualFold(params["report-type"], "feedback-report") {
		return ARFResult{Kind: NotAFeedbackReport}
	}
	boundary := params["boundary"]
	if boundary == "" {
		return ARFResult{Kind: NotAFeedbackReport}
	}
	return parseFeedbackReport(boundary, body)
}

// parseFeedbackReport walks a multipart/report body, pulling Feedback-Type and
// Original-Rcpt-To out of the message/feedback-report part and Message-ID out of
// the returned message/rfc822(-headers) part — the ARF parallel of parseReport.
func parseFeedbackReport(boundary string, body []byte) ARFResult {
	var result ARFResult
	walkReportParts(boundary, body, func(partMediaType string, part io.Reader) {
		switch {
		case strings.EqualFold(partMediaType, "message/feedback-report"):
			readFeedbackFields(part, &result)
		case strings.EqualFold(partMediaType, "message/rfc822"), strings.EqualFold(partMediaType, "message/rfc822-headers"):
			result.OriginalMessageID = readMessageID(part)
		}
	})
	result.Kind = classifyFeedbackType(result.FeedbackType)
	return result
}

// readFeedbackFields reads the message/feedback-report part's field group (RFC
// 5965 §3.1: ordinary header-style fields) into result. A part that is not a field
// group at all leaves both values empty — a false negative, not a crash,
// per this package's best-effort contract.
func readFeedbackFields(r io.Reader, result *ARFResult) {
	tp := textproto.NewReader(bufio.NewReader(r))
	fields, _ := tp.ReadMIMEHeader()
	result.FeedbackType = strings.TrimSpace(fields.Get("Feedback-Type"))
	result.ComplainedRecipient = reportAddress(fields.Get("Original-Rcpt-To"))
}

// classifyFeedbackType decides which registered Feedback-Type values are a
// complaint about our mail.
//
// abuse and fraud are: both mean the recipient reported the message as unwanted,
// which is an opt-out however it was filed.
//
// Everything else is not, and the exclusions matter more than the inclusions:
//   - not-spam (RFC 6650) is the INVERSE signal — a recipient rescuing our mail
//     out of their spam folder. Suppressing them would be exactly backwards.
//   - virus is a statement about an attachment, not about a recipient's wishes.
//   - other is deliberately unspecified by the RFC, so it means nothing actionable.
//
// An unregistered or absent value is not a complaint either: acting on a report we
// could not classify would make the default direction "suppress", and suppression
// is the half an operator cannot undo.
func classifyFeedbackType(feedbackType string) FeedbackKind {
	switch strings.ToLower(strings.TrimSpace(feedbackType)) {
	case "abuse", "fraud":
		return AbuseComplaint
	default:
		return NotAFeedbackReport
	}
}

// reportAddress reduces an ARF address field to a bare addr-spec. RFC 5965's own
// examples wrap the value in angle brackets, and some generators additionally
// carry RFC 3464's "rfc822;" address-type prefix.
func reportAddress(v string) string {
	v = stripAddrType(v)
	if len(v) >= 2 && strings.HasPrefix(v, "<") && strings.HasSuffix(v, ">") {
		v = v[1 : len(v)-1]
	}
	return strings.TrimSpace(v)
}
