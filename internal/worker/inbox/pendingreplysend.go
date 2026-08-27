package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
)

// Stable, client-safe reasons for a failed delivery attempt. These are what
// last_error stores and what the API returns; the underlying error text stays in
// the logs. Phrased for an operator reading their outbox, not for a developer
// reading a stack trace.
const (
	reasonTransportUnavailable   = "the sending mailbox could not be reached — check its connection"
	reasonProviderRejected       = "the mail provider rejected the message"
	reasonSuppressionCheckFailed = "the recipient's suppression status could not be checked"
	reasonRecipientSuppressed    = "the recipient has unsubscribed or bounced"
	reasonNoInboundMessage       = "the thread no longer has a message to reply to"
)

// PendingReplyCore is the narrow execution-plane seam for deferred manual
// replies. Consumer-defined here (not a widening of coreapi.Client, which
// already carries ~40 methods and ~13 test fakes) and satisfied by the
// in-process client via type assertion at registration — the same trade
// ReplyCore makes.
type PendingReplyCore interface {
	// ClaimPendingInboxReply transitions the row to 'sending' and resolves what
	// to send. coreapi.ErrInboxPendingNotClaimable means stop: cancelled,
	// already sent, not yet due, or another worker holds it.
	ClaimPendingInboxReply(ctx context.Context, workspaceID, pendingID string) (coreapi.PendingInboxReply, error)
	MarkPendingInboxReplySent(ctx context.Context, workspaceID, pendingID, messageID string) error
	ReleasePendingInboxReply(ctx context.Context, workspaceID, pendingID, reason string) error
	FailPendingInboxReply(ctx context.Context, workspaceID, pendingID, reason string) error
	IsSuppressed(ctx context.Context, workspaceID, email string) (bool, error)
	ResolveSenderTransport(ctx context.Context, workspaceID, mailboxID string) (coreapi.SenderTransport, error)
	RecordInboxReply(ctx context.Context, in coreapi.RecordInboxReplyInput) error
}

// PendingReplySendHandler delivers a deferred manual reply.
//
// The shape mirrors ReplySendHandler, with one structural difference that is
// the entire point of the feature: the task carries only a row id, and the ROW
// decides whether to send at all. The operator's undo is an UPDATE on that row;
// this task still fires and finds it unclaimable, so cancellation never depends
// on reaching into the queue (the codebase's only asynq Inspector is the
// read-only one behind the queue-depth metric; it mutates nothing).
//
// Return-value discipline, since it decides between double-sending and losing
// mail:
//   - Not claimable -> nil. The task is done; retrying would never claim.
//   - Load/transport failure -> release, then return err so asynq retries.
//   - Send failure -> release, then return err. Releasing FIRST matters: a
//     retry that found its own abandoned 'sending' row would have to wait out
//     the full lease before trying again.
//   - Send succeeded -> mark sent, and NEVER return an error afterwards. A
//     non-nil return past the dial would retry a reply that has already left.
func PendingReplySendHandler(core PendingReplyCore, sender Mailer) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var p queue.InboxPendingReplySendPayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			// A malformed payload can never become well-formed on retry.
			slog.ErrorContext(ctx, "inbox_pending_reply_bad_payload", "err", err)
			return nil
		}

		pending, err := core.ClaimPendingInboxReply(ctx, p.WorkspaceID, p.PendingID)
		if err != nil {
			if errors.Is(err, coreapi.ErrInboxPendingNotClaimable) {
				// The ordinary undo path lands here, and it is not a failure.
				slog.InfoContext(ctx, "inbox_pending_reply_not_claimable",
					"pending_id", p.PendingID, "workspace_id", p.WorkspaceID)
				return nil
			}
			if errors.Is(err, coreapi.ErrInboxNoInbound) {
				// The thread lost its inbound message between scheduling and
				// now (a deletion). Nothing to reply to, and no retry can
				// change that — fail the row so the outbox shows why.
				slog.WarnContext(ctx, "inbox_pending_reply_no_inbound", "pending_id", p.PendingID)
				failPending(ctx, core, p, reasonNoInboundMessage)
				return nil
			}
			return fmt.Errorf("claim pending reply: %w", err)
		}

		// Re-checked AFTER claiming, and this is materially more valuable than
		// on the immediate path: the delay window makes the race seconds-to-
		// minutes wide, so a contact who unsubscribed since scheduling is a real
		// case. Suppression is permanent, so the row is failed, not released.
		suppressed, err := core.IsSuppressed(ctx, p.WorkspaceID, pending.Job.ToEmail)
		if err != nil {
			return releaseAndReturn(ctx, core, p, reasonSuppressionCheckFailed,
				fmt.Errorf("check suppression: %w", err))
		}
		if suppressed {
			slog.InfoContext(ctx, "inbox_pending_reply_recipient_suppressed", "pending_id", p.PendingID)
			failPending(ctx, core, p, reasonRecipientSuppressed)
			return nil
		}

		transport, err := core.ResolveSenderTransport(ctx, p.WorkspaceID, pending.Job.MailboxID)
		if err != nil {
			return releaseAndReturn(ctx, core, p, reasonTransportUnavailable,
				fmt.Errorf("resolve sender transport: %w", err))
		}
		defer zeroize(transport.SMTPPassword)
		defer zeroize(transport.AccessToken)

		subject := reSubject(pending.Job.Subject)
		messageID, err := sender.Send(ctx,
			mail.OutboundJob{
				Provider:       transport.Provider,
				Host:           transport.SMTPHost,
				Port:           transport.SMTPPort,
				Username:       transport.SMTPUsername,
				Password:       string(transport.SMTPPassword),
				AllowPlaintext: transport.AllowPlaintext,
				AccessToken:    string(transport.AccessToken),
			},
			mail.Message{
				FromEmail:  transport.FromEmail,
				FromName:   transport.FromName,
				To:         pending.Job.ToEmail,
				Subject:    subject,
				BodyText:   pending.BodyText,
				InReplyTo:  pending.Job.InReplyTo,
				References: pending.Job.References,
			})
		if err != nil {
			return releaseAndReturn(ctx, core, p, reasonProviderRejected,
				fmt.Errorf("send pending reply: %w", err))
		}

		// Past the dial. Everything below is logged, never returned: the mail
		// has left, and an error here must not cause a second delivery.
		if err := core.MarkPendingInboxReplySent(ctx, p.WorkspaceID, p.PendingID, messageID); err != nil {
			slog.ErrorContext(ctx, "inbox_pending_reply_mark_sent_failed",
				"pending_id", p.PendingID, "err", err)
		}
		if err := core.RecordInboxReply(ctx, coreapi.RecordInboxReplyInput{
			WorkspaceID: p.WorkspaceID,
			ThreadID:    pending.ThreadID,
			MessageID:   messageID,
			FromEmail:   transport.FromEmail,
			FromName:    transport.FromName,
			ToEmail:     pending.Job.ToEmail,
			Subject:     subject,
			BodyText:    pending.BodyText,
		}); err != nil {
			slog.ErrorContext(ctx, "inbox_pending_reply_record_failed",
				"pending_id", p.PendingID, "err", err)
		}
		return nil
	}
}

// releaseAndReturn returns the claim before propagating a transient failure, so
// the retry can claim immediately rather than waiting out the lease. A failed
// release is logged but does not replace the original error — the caller needs
// to know what actually went wrong.
//
// `reason` is a STABLE TOKEN, never cause.Error(). last_error is persisted and
// served to any inbox:read caller, and a wrapped provider error echoes the SMTP
// host and port, the provider's raw rejection text, and (on the transport arm)
// internal keyring error shapes. docs/security.md closes exactly this hole one
// endpoint over — the draft route returns only its own sentinel precisely
// because "an upstream string can echo request detail back". The full error goes
// to the log line, where operators can see it and clients cannot.
func releaseAndReturn(
	ctx context.Context,
	core PendingReplyCore,
	p queue.InboxPendingReplySendPayload,
	reason string,
	cause error,
) error {
	slog.WarnContext(ctx, "inbox_pending_reply_attempt_failed",
		"pending_id", p.PendingID, "reason", reason, "err", cause)
	if err := core.ReleasePendingInboxReply(ctx, p.WorkspaceID, p.PendingID, reason); err != nil {
		slog.ErrorContext(ctx, "inbox_pending_reply_release_failed", "pending_id", p.PendingID, "err", err)
	}
	return cause
}

// failPending marks the row permanently failed. It returns nothing because
// there is nothing a caller could decide: the failure is one no retry can fix,
// so the task is always done. Even a failed WRITE returns no error — retrying
// the task would not fix a broken write, and the row's lease eventually makes it
// claimable again for one more (equally doomed) attempt.
//
// Callers therefore write `failPending(...); return nil`, which reads as
// terminal at the call site.
func failPending(ctx context.Context, core PendingReplyCore, p queue.InboxPendingReplySendPayload, reason string) {
	if err := core.FailPendingInboxReply(ctx, p.WorkspaceID, p.PendingID, reason); err != nil {
		slog.ErrorContext(ctx, "inbox_pending_reply_fail_write_failed", "pending_id", p.PendingID, "err", err)
	}
}
