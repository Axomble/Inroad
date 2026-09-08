package inprocess

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// FindWarmupSendByMessageID resolves an inbound message back to the warmup send it
// is a receipt for, keyed on the Message-ID we recorded when we sent it.
//
// It satisfies coreapi.WarmupSendLookupClient, which the inbox poller consumes by
// type assertion rather than through Client — see that interface for why, and for
// the isolation rule the caller must uphold (only a genuinely ABSENT token may
// reach this; a forged one never does).
//
// ok=false is the ORDINARY answer: the poller asks about every tokenless inbound
// message, nearly all of which are campaign replies, newsletters and human mail.
// Absence is therefore not an error, and a not-found row must never fail a poll.
func (c client) FindWarmupSendByMessageID(ctx context.Context, workspaceID, toMailboxID, messageID string) (coreapi.WarmupSendRef, bool, error) {
	id := normalizeMessageID(messageID)
	if id == "" {
		// Nothing to match on. Refused here as well as in SQL because
		// warmup_sends.message_id DEFAULTs to '' — belt and braces, since the
		// consequence of matching would be a receipt attributed to an unsent row.
		return coreapi.WarmupSendRef{}, false, nil
	}
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return coreapi.WarmupSendRef{}, false, err
	}
	to, err := uuid.Parse(toMailboxID)
	if err != nil {
		return coreapi.WarmupSendRef{}, false, err
	}
	sendID, err := c.q.FindWarmupSendByMessageIDForRecipient(ctx, gen.FindWarmupSendByMessageIDForRecipientParams{
		WorkspaceID: ws, ToMailbox: to, MessageID: id,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return coreapi.WarmupSendRef{}, false, nil
		}
		return coreapi.WarmupSendRef{}, false, err
	}
	return coreapi.WarmupSendRef{WarmupSendID: sendID.String()}, true, nil
}

// normalizeMessageID reduces a raw RFC 5322 Message-ID field value to the
// identifier itself: the angle brackets are part of the FIELD, not of the id, and
// providers are inconsistent about echoing them — so a raw string compare against
// a stored value silently misses instead of failing loudly.
//
// Normalising happens HERE, once, on the side of the seam that owns the
// comparison, so no caller has to know that <> are delimiters and no call site
// grows its own strings.Trim. The query matches both stored forms; this handles
// both inbound forms.
//
// Only the OUTERMOST pair is removed. strings.Trim would eat a run of brackets,
// which would make "<<a@b>>" and "<a@b>" compare equal — different identifiers,
// and one of them is what a mangling relay produces.
func normalizeMessageID(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && strings.HasPrefix(v, "<") && strings.HasSuffix(v, ">") {
		v = v[1 : len(v)-1]
	}
	return strings.TrimSpace(v)
}
