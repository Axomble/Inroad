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
// shaped like <...> that are usable as a lookup key, per RFC 5322's msg-id
// syntax. Shape alone is not enough — see usableMessageID.
func messageIDTokens(v string) []string {
	var out []string
	for _, tok := range strings.Fields(v) {
		if strings.HasPrefix(tok, "<") && strings.HasSuffix(tok, ">") && usableMessageID(tok) {
			out = append(out, tok)
		}
	}
	return out
}

// maxMessageIDLen bounds a Message-ID we are willing to use as a lookup key.
// RFC 5322 caps an unfolded header line at 998 octets, so nothing longer is an
// identifier anyone could have issued — it is padding.
const maxMessageIDLen = 998

// usableMessageID reports whether v can safely be used as a Message-ID lookup
// key. It is the ONE guard for every such key in this package: the warmup
// header-loss fallback, the reply matcher's In-Reply-To/References tokens, the
// DSN's returned Original-Message-ID and the ARF's quoted one.
//
// All four come off UNAUTHENTICATED inbound mail and all four end up as a
// Postgres text parameter. textproto.ReadMIMEHeader validates header KEYS only,
// so a raw 0xFF or NUL in the VALUE survives verbatim through the IMAP, Gmail
// and Graph readers — and Postgres refuses such a parameter with SQLSTATE 22021
// ("invalid byte sequence for encoding \"UTF8\""), which is neither
// pgx.ErrNoRows nor transient. That error propagates out of the poll BEFORE
// SetInboxCursor / SetInboxCursorString, so the message is refetched on the next
// pass and fails identically: one email freezes that mailbox's cursor and stops
// every campaign reply, bounce, complaint and warmup receipt behind it,
// permanently. It is the same wedge coreapi.ErrInvalidComplaint exists to avoid,
// reached through a key rather than through an ingest verdict.
//
// Refusing costs nothing legitimate. RFC 5322's msg-id is dot-atom "@" dot-atom
// — US-ASCII by definition — so a byte outside printable ASCII is already not an
// identifier we could have issued or stored, and answering "no match" for it is
// the truth rather than a degradation.
func usableMessageID(v string) bool {
	if v == "" || len(v) > maxMessageIDLen {
		return false
	}
	// Bytes, not runes: the question is whether every octet is printable
	// US-ASCII, and any multi-byte rune fails that by definition.
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 || v[i] > 0x7e {
			return false
		}
	}
	return true
}
