package inbox

import (
	"net/mail"
	"strings"
)

// MessageIDs extracts candidate message-ids from In-Reply-To followed by
// References (in that order), for matching against sends.message_id. Not
// deduped: callers check each candidate in turn and stop at the first hit,
// so a duplicate token is harmless — cheaper to allow than to guard against.
func MessageIDs(hdr mail.Header) []string {
	var ids []string
	ids = append(ids, messageIDTokens(hdr.Get("In-Reply-To"))...)
	ids = append(ids, messageIDTokens(hdr.Get("References"))...)
	return ids
}

// messageIDTokens splits a header value on whitespace and keeps only tokens
// shaped like <...>, per RFC 5322's msg-id syntax.
func messageIDTokens(v string) []string {
	var out []string
	for _, tok := range strings.Fields(v) {
		if strings.HasPrefix(tok, "<") && strings.HasSuffix(tok, ">") {
			out = append(out, tok)
		}
	}
	return out
}

// IsAutoReply reports whether the message is an auto-responder per RFC 3834
// (Auto-Submitted present and not "no") and so should NOT be treated as an
// engaged reply. Scoped to Auto-Submitted only, matching design spec A4 —
// Precedence: bulk/list is a distinct (mailing-list) signal, not an
// auto-reply one, and is intentionally not matched here.
func IsAutoReply(hdr mail.Header) bool {
	v := strings.TrimSpace(hdr.Get("Auto-Submitted"))
	if v == "" {
		return false
	}
	return !strings.EqualFold(v, "no")
}
