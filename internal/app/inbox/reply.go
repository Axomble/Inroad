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

// ARCHITECTURE (both reply paths).
//
// Decrypting a mailbox credential and dialing a provider must happen ONLY in
// the execution plane (docs/security.md invariant 1 — cmd/inroad never opens a
// Sealer or a provider connection). So the control plane does every synchronous
// validation (thread ownership, an inbound message exists, the recipient is not
// suppressed, the workspace is under its outstanding-send cap) and then hands
// the send to the queue; internal/worker/inbox resolves the transport through
// the SAME coreapi credential path every real send uses, and sends.
//
// WHAT IT HANDS THE QUEUE IS A POINTER. The reply's text is written to an
// inbox_pending_replies row and the task carries that row's id. It used to carry
// the text itself, in an inbox:reply_send payload, and that was a disclosure:
// on terminal failure queue.DeadLetterErrorHandler stores a payload byte-for-
// byte in task_dead_letters, and GET /dead-letters serves it verbatim under
// campaigns:read — an OAuth-grantable scope, while inbox:read deliberately is
// NOT one, precisely because reply bodies are correspondence
// (internal/app/auth/scopes.go). A delegated third-party client could therefore
// read reply text through a scope built to exclude it.
//
// Reply and ScheduleReply now differ ONLY in the send_after they resolve — an
// immediate reply is a row whose send_after has already passed — and share
// queueReply for everything else (see pending.go).

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

// Reply queues a manual reply for immediate delivery: it runs queueReply with a
// send_after that has already passed, so the row is claimable the moment its
// task fires (see immediateSendClaimSlack for why "already passed" and not
// "now"). Nothing is decrypted or dialed here — see the ARCHITECTURE note above.
//
// The row it creates is returned to nobody. That is the one deliberate
// difference from ScheduleReply: this endpoint's contract is a bodyless 202, and
// handing back an id would imply an undo window that, by definition, this path
// does not have. The row still exists, still appears in the outbox until it
// sends, and is still what carries the body to the worker.
//
// On success the thread is marked read: the caller who just replied has, by
// construction, seen it.
//
// A manual reply is NEVER blocked or delayed by the mailbox's daily cap or
// minimum send interval (product decision — an operator's own reply is not
// automation), but it IS counted toward the mailbox's daily sent volume (see
// queries/send.sql's CountSentToday) so campaign scheduling still sees true
// volume. Suppression, by contrast, is never bypassed — and neither, now, is
// the workspace's outstanding-send cap, which this path inherits by writing the
// same rows the deferred path does.
func (s *Service) Reply(ctx context.Context, ws, threadID uuid.UUID, bodyText string, createdBy *uuid.UUID) error {
	// Collapse a double-submit rather than delivering it twice.
	//
	// This route used to be deduped by its asynq TaskID — "inboxreply:<thread>:
	// <unix-second>" — whose conflict asynq swallows as success, so a second click
	// inside the same second sent once. Routing the path through a pending row
	// replaced that key with the row's own uuid, and two clicks became two rows and
	// two real emails to a contact. The guard has to live here now, because the row
	// is created before anything is enqueued.
	//
	// Returning nil on a hit is the same observable behaviour the TaskID conflict
	// had, and this route hands back no id, so a caller cannot tell the difference —
	// which is the point. It is checked BEFORE queueReply so a duplicate does not
	// consume a slot against the outstanding-send cap.
	// Nil-checked here rather than relying on queueReply's identical guard below:
	// this runs BEFORE it, so an unwired store panicked instead of returning the
	// clean "deferred replies are not configured" error. Caught by
	// TestReplyWithoutAPendingStoreFails, which existed and was right.
	if s.pending == nil {
		return fmt.Errorf("%w: deferred replies are not configured", ErrValidation)
	}
	dupe, err := s.pending.FindDuplicatePendingReply(ctx, ws, threadID, bodyText, immediateReplyDedupWindow)
	if err != nil {
		return err
	}
	if dupe != uuid.Nil {
		slog.InfoContext(ctx, "inbox_reply_duplicate_collapsed",
			"workspace_id", ws, "thread_id", threadID, "existing_pending_id", dupe)
		return nil
	}

	if _, err := s.queueReply(ctx, ws, threadID, bodyText, immediateSendAfter(s.now()), createdBy); err != nil {
		return err
	}
	// The send is now QUEUED. From here, returning an error would surface as a
	// 500 to the caller, who would reasonably retry — and a retry would queue a
	// SECOND row, which really would send twice. Marking read is cosmetic (an
	// optimistic UI update the caller already knows the value of — see the
	// handler's own doc), so a failure here is logged, not retried, mirroring
	// how the worker treats a post-send RecordInboxReply failure for the
	// identical reason.
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

// validateReplyBody bounds a manual reply's body. Shared by the immediate
// (Reply) and deferred (ScheduleReply) paths so the two can never disagree
// about what a valid reply is — a body accepted for scheduling must be one the
// send path would also have accepted.
func validateReplyBody(bodyText string) error {
	if len(bodyText) < minReplyBodyLen || len(bodyText) > maxReplyBodyLen {
		return ErrReplyBodyInvalid
	}
	return nil
}
