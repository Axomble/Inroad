package inbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// Sentinel errors Service.Reply returns, mapped to HTTP status codes by the
// handler (ErrNotFound/ErrValidation are declared elsewhere in this package
// and reused unchanged here).
var (
	// ErrNoInboundMessage is returned when the thread has no inbound message
	// to reply to — a legacy row (or a future oddity) that never received one.
	ErrNoInboundMessage = errors.New("inbox: thread has no inbound message to reply to")
	// ErrRecipientSuppressed is returned when the address a reply would go to
	// is on the workspace's suppression list. A manual reply must never bypass
	// the same suppression check a real send honors (docs/security.md) — it is
	// never blocked by the mailbox's daily cap or minimum send interval, but
	// suppression is never bypassed.
	ErrRecipientSuppressed = errors.New("inbox: recipient is on the workspace's suppression list")
	// ErrReplyBodyInvalid is returned for a body_text outside [1,100000] bytes
	// — mapped to 422 (well-formed JSON, invalid business input), distinct
	// from ErrValidation's 400 (malformed request).
	ErrReplyBodyInvalid = errors.New("inbox: reply body must be between 1 and 100000 characters")
)

// minReplyBodyLen / maxReplyBodyLen mirror the OpenAPI contract's
// SendInboxReplyRequest.body_text minLength/maxLength — validated again here
// (never trust a boundary crossing once), so a caller that bypasses the
// generated client still gets a 422, not a constraint violation surfacing as
// a 500 (this domain has no body-length constraint in Postgres to catch it).
const (
	minReplyBodyLen = 1
	maxReplyBodyLen = 100000
)

// SuppressionChecker reports whether an address is on the workspace's
// suppression list. Suppression is owned by the suppression app domain;
// app/* packages never import each other (CLAUDE.md), so this narrow,
// consumer-defined interface is satisfied at wiring time in cmd/inroad by
// suppression.Store — the same shape as campaign.SuppressionChecker.
type SuppressionChecker interface {
	IsSuppressed(ctx context.Context, ws uuid.UUID, email string) (bool, error)
}

// ReplyEnqueuer schedules an inbox:reply_send task. Defined here (consumer
// side) with primitive args, like campaign.TestSendEnqueuer, so the domain
// doesn't depend on platform/queue. Satisfied by *queue.Client.
//
// ARCHITECTURE: decrypting a mailbox credential and dialing a provider must
// happen ONLY in the execution plane (docs/security.md invariant 1 —
// cmd/inroad never opens a Sealer or a provider connection). Reply therefore
// does every synchronous validation here (thread ownership, an inbound
// message exists, the recipient is not suppressed) and then ENQUEUES the
// actual send as an inbox:reply_send task; internal/worker/inbox resolves the
// transport (through the SAME coreapi credential path every real send uses)
// and sends.
type ReplyEnqueuer interface {
	EnqueueInboxReplySend(threadID, bodyText, workspaceID string) error
}

// ServiceOption configures an optional Service dependency, mirroring
// campaign.ServiceOption. Both are optional (nil-safe — see
// checkRecipientNotSuppressed and Reply's own nil check) so every existing
// caller of NewService(store) keeps compiling unchanged.
type ServiceOption func(*Service)

// WithSuppressionChecker wires Reply's suppression-list guard: a manual reply
// must never go to an address the workspace has explicitly unsubscribed or
// bounced. Without it, Reply skips the check — cmd/inroad always wires one in
// production; internal/worker/inbox re-checks independently before dialing as
// defense in depth against the race between this check and an incoming
// unsubscribe.
func WithSuppressionChecker(c SuppressionChecker) ServiceOption {
	return func(s *Service) { s.suppression = c }
}

// WithReplyEnqueuer wires Reply's inbox:reply_send task enqueue.
func WithReplyEnqueuer(e ReplyEnqueuer) ServiceOption {
	return func(s *Service) { s.replyEnq = e }
}

// Reply validates a manual reply on threadID (the thread exists in ws, has an
// inbound message to reply to, and the recipient — that message's sender —
// is not suppressed) and enqueues the actual render+send as an
// inbox:reply_send task. Nothing is decrypted or dialed here — see
// ReplyEnqueuer's doc. On success the thread is marked read: the caller who
// just replied has, by construction, seen it.
//
// A manual reply is NEVER blocked or delayed by the mailbox's daily cap or
// minimum send interval (product decision — an operator's own reply is not
// automation), but it IS counted toward the mailbox's daily sent volume (see
// queries/send.sql's CountSentToday) so campaign scheduling still sees true
// volume. Suppression, by contrast, is never bypassed.
func (s *Service) Reply(ctx context.Context, ws, threadID uuid.UUID, bodyText string) error {
	if len(bodyText) < minReplyBodyLen || len(bodyText) > maxReplyBodyLen {
		return ErrReplyBodyInvalid
	}
	detail, err := s.store.GetThread(ctx, ws, threadID)
	if err != nil {
		return err // already ErrNotFound-mapped by the store
	}
	latest, ok := latestInboundMessage(detail.Messages)
	if !ok {
		return ErrNoInboundMessage
	}
	if err := s.checkRecipientNotSuppressed(ctx, ws, latest.FromEmail); err != nil {
		return err
	}
	if s.replyEnq == nil {
		return errors.New("inbox: reply sending is not configured")
	}
	if err := s.replyEnq.EnqueueInboxReplySend(threadID.String(), bodyText, ws.String()); err != nil {
		return err
	}
	// The send is now QUEUED. From here, returning an error would surface as a
	// 500 to the caller, who would reasonably retry — and the enqueue's
	// unix-second dedup key does NOT catch a retry seconds later, so a
	// SetUnread failure propagated here would risk a second reply actually
	// going out. Marking read is cosmetic (an optimistic UI update the caller
	// already knows the value of — see the handler's own doc), so a failure
	// here is logged, not retried, mirroring how the worker treats a
	// post-send RecordInboxReply failure for the identical reason.
	if err := s.store.SetUnread(ctx, ws, threadID, false); err != nil {
		slog.ErrorContext(ctx, "inbox_reply_mark_read_failed", "workspace_id", ws, "thread_id", threadID, "err", err)
	}
	return nil
}

// checkRecipientNotSuppressed mirrors campaign.Service's identical helper: a
// checker error is propagated (fails closed), never swallowed into "not
// suppressed"; no checker wired (nil) is unwired — a deployment/test choice,
// like campaign's — but cmd/inroad always wires one in production.
func (s *Service) checkRecipientNotSuppressed(ctx context.Context, ws uuid.UUID, email string) error {
	if s.suppression == nil {
		return nil
	}
	suppressed, err := s.suppression.IsSuppressed(ctx, ws, email)
	if err != nil {
		return err
	}
	if suppressed {
		return ErrRecipientSuppressed
	}
	return nil
}

// latestInboundMessage returns the most recent inbound message in a thread's
// chronologically-ordered (ascending) message list — the reply target's
// From: address and Message-ID anchor In-Reply-To resolves against. ok=false
// when the thread has no inbound message at all.
func latestInboundMessage(messages []Message) (Message, bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Direction == "inbound" {
			return messages[i], true
		}
	}
	return Message{}, false
}

// RecordOutboundReply appends a delivered manual reply's outbound message to
// its thread and bumps last_message_at, in ONE transaction — the outbound-leg
// analogue of RecordReply's inbound atomicity, and the ONLY write path
// internal/worker/inbox's reply-send handler uses (via its own coreapi seam).
// Unlike RecordReply it does not flip unread (Service.Reply already marked
// the thread read when it enqueued the send) and does not touch
// last_reply_class (an operator's own reply is not itself a classified
// inbound reply) — see Store.RecordOutboundReply.
func (s *Service) RecordOutboundReply(ctx context.Context, ws, threadID uuid.UUID, msg InsertMessageInput) error {
	if msg.Direction != "outbound" {
		return fmt.Errorf("%w: message direction must be outbound", ErrValidation)
	}
	return s.store.RecordOutboundReply(ctx, threadID, ws, msg)
}
