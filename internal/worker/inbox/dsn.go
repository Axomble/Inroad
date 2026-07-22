// Package inbox contains the pure per-message logic for reply and bounce
// detection: parsing delivery-status notifications (DSNs) and matching
// threading headers back to our own sent mail. No I/O — the IMAP fetch lives
// in platform/mail, the DB access lives behind coreapi.
package inbox

import (
	"bufio"
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"net/textproto"
	"strings"
)

// BounceKind classifies a delivery-status notification.
type BounceKind int

const (
	// NotABounce means the message isn't a DSN, or is a DSN we couldn't parse
	// with enough confidence to classify — treated the same way downstream
	// (log + skip, no stop/suppress).
	NotABounce BounceKind = iota
	SoftBounce
	HardBounce
)

func (k BounceKind) String() string {
	switch k {
	case SoftBounce:
		return "soft"
	case HardBounce:
		return "hard"
	default:
		return "not-a-bounce"
	}
}

// DSNResult is what ParseDSN extracts from a delivery-status notification.
type DSNResult struct {
	Kind BounceKind
	// OriginalMessageID is the Message-ID of our own sent mail that bounced,
	// read from the returned message/rfc822(-headers) part. Empty if absent.
	OriginalMessageID string
	// FailedRecipient is the address that bounced, read from Final-Recipient.
	// Empty if absent.
	FailedRecipient string
	// StatusCode is the raw enhanced status code, e.g. "5.1.1". Empty if the
	// message isn't a parseable DSN.
	StatusCode string
}

// ParseDSN inspects a parsed inbox message and returns NotABounce if it
// isn't a delivery-status notification. hdr and contentType are the
// message's own (outer) header/Content-Type; body is everything after the
// header, i.e. the multipart/report body.
//
// Detection: the message is only walked for Status/Final-Recipient/
// Message-ID when its Content-Type is multipart/report AND its report-type
// parameter is delivery-status (RFC 3462/3464) — this excludes other
// multipart/report uses such as an MDN (disposition-notification, RFC 3798)
// or an ARF feedback-report from being misread as a bounce. A From address
// that looks like a mailer-daemon/postmaster is a secondary fallback signal
// used only when report-type is missing outright (some older/misconfigured
// MTAs omit it) — never to override an explicit, different report-type. A
// mailer-daemon message that isn't multipart/report at all has nothing
// reliable to extract from a free-form body, so it resolves to NotABounce
// (log + skip upstream) rather than guessing from prose.
//
// Never panics: malformed multipart bodies, missing parts, or garbage
// headers all fall back to a best-effort (possibly empty) DSNResult.
func ParseDSN(hdr mail.Header, contentType string, body []byte) DSNResult {
	mediaType, params, _ := mime.ParseMediaType(contentType)
	if !strings.EqualFold(mediaType, "multipart/report") {
		return DSNResult{Kind: NotABounce}
	}

	reportType := params["report-type"]
	from := strings.ToLower(hdr.Get("From"))
	looksLikeMailerDaemon := strings.Contains(from, "mailer-daemon") || strings.Contains(from, "postmaster")

	isDeliveryStatusReport := strings.EqualFold(reportType, "delivery-status") ||
		(reportType == "" && looksLikeMailerDaemon)
	if !isDeliveryStatusReport {
		return DSNResult{Kind: NotABounce}
	}

	boundary := params["boundary"]
	if boundary == "" {
		return DSNResult{Kind: NotABounce}
	}

	return parseReport(boundary, body)
}

// parseReport walks a multipart/report body, pulling Status/Final-Recipient
// out of the message/delivery-status part and Message-ID out of the
// returned message/rfc822 or message/rfc822-headers part.
func parseReport(boundary string, body []byte) DSNResult {
	var result DSNResult

	mr := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			break // malformed multipart body: stop, return best-effort so far
		}

		partMediaType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		switch {
		case strings.EqualFold(partMediaType, "message/delivery-status"):
			readDeliveryStatus(part, &result)
		case strings.EqualFold(partMediaType, "message/rfc822"), strings.EqualFold(partMediaType, "message/rfc822-headers"):
			result.OriginalMessageID = readMessageID(part)
		}
		part.Close()
	}

	result.Kind = classifyStatus(result.StatusCode)
	return result
}

// readDeliveryStatus reads the per-message field group followed by the
// first per-recipient field group of a message/delivery-status part (RFC
// 3464), and fills in FailedRecipient/StatusCode. Only the first recipient
// is considered — matches the design spec's scope (one recipient per send).
//
// This assumes the RFC 3464 field-group order (per-message fields, a blank
// line, then per-recipient fields): the first ReadMIMEHeader call consumes
// whatever comes first. A non-conformant MTA that omits the per-message
// group entirely would have its recipient fields read as the "per-message"
// group instead, leaving FailedRecipient/StatusCode empty — a false
// negative (NotABounce), not a crash; best-effort per this package's
// contract.
func readDeliveryStatus(r io.Reader, result *DSNResult) {
	tp := textproto.NewReader(bufio.NewReader(r))
	_, _ = tp.ReadMIMEHeader() // per-message fields (Reporting-MTA, Arrival-Date, ...) — not needed
	recipient, _ := tp.ReadMIMEHeader()

	result.FailedRecipient = stripAddrType(recipient.Get("Final-Recipient"))
	if fields := strings.Fields(recipient.Get("Status")); len(fields) > 0 {
		result.StatusCode = fields[0]
	}
}

// readMessageID parses a message/rfc822(-headers) part as headers and reads
// Message-ID.
func readMessageID(r io.Reader) string {
	tp := textproto.NewReader(bufio.NewReader(r))
	h, _ := tp.ReadMIMEHeader()
	return h.Get("Message-Id")
}

// stripAddrType strips the "type;" prefix RFC 3464 address fields carry,
// e.g. "rfc822; nobody@x" -> "nobody@x".
func stripAddrType(v string) string {
	if i := strings.Index(v, ";"); i >= 0 {
		return strings.TrimSpace(v[i+1:])
	}
	return strings.TrimSpace(v)
}

// classifyStatus maps an enhanced status code's first digit to a
// BounceKind: 5.x.x permanent failures are hard, 4.x.x transient ones are
// soft. Anything else (missing/unrecognized) is NotABounce.
func classifyStatus(status string) BounceKind {
	if status == "" {
		return NotABounce
	}
	switch status[0] {
	case '5':
		return HardBounce
	case '4':
		return SoftBounce
	default:
		return NotABounce
	}
}
