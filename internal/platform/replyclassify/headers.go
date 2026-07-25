package replyclassify

import (
	"regexp"
	"strings"
)

// oooAwayFromRe boundary-anchors the "away from" out-of-office subject cue so
// unrelated subjects like "Takeaway from our call" (no word boundary before
// "away") do not false-positive as out_of_office.
var oooAwayFromRe = regexp.MustCompile(`\baway from\b`)

// classifyHeaders is Layer 1: a deterministic, offline scan of the subject +
// message headers for well-known machine-reply markers. It returns (result,
// true) when it definitively recognizes an automated message; (zero, false)
// otherwise so the pipeline falls through to the lexicon/model layers.
//
// The alloc-light subject cues run FIRST so an OOO-subject reply short-circuits
// before any header is touched (this is the hot path — the inbox poll classifies
// up to 200 replies per pass). Header access is a zero-alloc case-insensitive
// linear scan over in.Headers (~8 headers are read from a ~40-header map, so a
// scan beats building a lower-cased copy of the whole set).
//
// Compliance (unsubscribe) is intentionally NOT handled here — it is a Layer 2
// lexicon concern so a human "please unsubscribe" reply is treated as an opt-out
// request, not an automated message.
func classifyHeaders(in Input) (Result, bool) {
	// --- Out-of-office subjects (run before any header access) ---
	subject := strings.ToLower(strings.TrimSpace(in.Subject))
	if subject != "" && isOOOSubject(subject) {
		return Result{Class: ClassOutOfOffice, Confidence: 0.98, Source: SourceHeader}, true
	}

	// RFC 3834 Auto-Submitted. "auto-replied" is canonically a vacation/auto
	// responder; any other non-"no" value is a generic machine-generated message.
	if as := strings.ToLower(headerFirst(in.Headers, "Auto-Submitted")); as != "" && as != "no" {
		if strings.Contains(as, "auto-replied") {
			return Result{Class: ClassOutOfOffice, Confidence: 0.95, Source: SourceHeader}, true
		}
		return Result{Class: ClassAutoReply, Confidence: 0.95, Source: SourceHeader}, true
	}

	// Vendor auto-responder headers (Exchange, Zimbra, helpdesks).
	if headerHas(in.Headers, "X-Autoreply") || headerHas(in.Headers, "X-Autorespond") {
		return Result{Class: ClassOutOfOffice, Confidence: 0.93, Source: SourceHeader}, true
	}
	if headerHas(in.Headers, "X-Auto-Response-Suppress") {
		// Present on Exchange auto-responses; the message itself is automated.
		return Result{Class: ClassAutoReply, Confidence: 0.9, Source: SourceHeader}, true
	}

	// Precedence: bulk/junk/list/auto_reply marks list/automated traffic.
	switch strings.ToLower(headerFirst(in.Headers, "Precedence")) {
	case "auto_reply":
		return Result{Class: ClassAutoReply, Confidence: 0.9, Source: SourceHeader}, true
	case "bulk", "junk", "list":
		return Result{Class: ClassAutoReply, Confidence: 0.75, Source: SourceHeader}, true
	}

	// Delivery-status / disposition-notification reports are machine replies.
	ct := strings.ToLower(headerFirst(in.Headers, "Content-Type"))
	if strings.Contains(ct, "multipart/report") &&
		(strings.Contains(ct, "delivery-status") || strings.Contains(ct, "disposition-notification")) {
		return Result{Class: ClassAutoReply, Confidence: 0.95, Source: SourceHeader}, true
	}

	// Null Return-Path <> is the canonical bounce / non-reply-expecting envelope.
	if strings.TrimSpace(headerFirst(in.Headers, "Return-Path")) == "<>" {
		return Result{Class: ClassAutoReply, Confidence: 0.85, Source: SourceHeader}, true
	}

	// mailer-daemon / postmaster / no-reply style senders are machine sources.
	if from := strings.ToLower(headerFirst(in.Headers, "From")); from != "" {
		if strings.Contains(from, "mailer-daemon") ||
			strings.Contains(from, "postmaster@") ||
			strings.Contains(from, "no-reply@") ||
			strings.Contains(from, "noreply@") ||
			strings.Contains(from, "donotreply@") {
			return Result{Class: ClassAutoReply, Confidence: 0.8, Source: SourceHeader}, true
		}
	}

	return Result{}, false
}

// isOOOSubject reports whether a lower-cased subject matches a well-known
// vacation/auto-responder subject convention.
func isOOOSubject(subject string) bool {
	return strings.HasPrefix(subject, "out of office") ||
		strings.HasPrefix(subject, "out of the office") ||
		strings.HasPrefix(subject, "auto:") ||
		strings.HasPrefix(subject, "autoreply") ||
		strings.Contains(subject, "automatic reply") ||
		strings.Contains(subject, "auto-reply") ||
		strings.Contains(subject, "on vacation") ||
		strings.Contains(subject, "on holiday") ||
		oooAwayFromRe.MatchString(subject)
}

// headerFirst returns the trimmed first value of the named header via a
// case-insensitive linear scan over the original map — zero allocations, no
// lower-cased copy of the whole header set.
func headerFirst(h map[string][]string, name string) string {
	for k, v := range h {
		if len(v) > 0 && strings.EqualFold(k, name) {
			return strings.TrimSpace(v[0])
		}
	}
	return ""
}

// headerHas reports whether the named header is present (case-insensitive scan).
func headerHas(h map[string][]string, name string) bool {
	for k := range h {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}
