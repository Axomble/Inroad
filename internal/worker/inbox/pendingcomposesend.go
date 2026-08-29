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

// ComposeCore is the narrow execution-plane seam for deferred composed emails.
type ComposeCore interface {
	ClaimPendingInboxCompose(ctx context.Context, workspaceID, pendingID string) (coreapi.PendingInboxCompose, error)
	MarkPendingInboxComposeSent(ctx context.Context, workspaceID, pendingID, messageID string) error
	ReleasePendingInboxCompose(ctx context.Context, workspaceID, pendingID, reason string) error
	FailPendingInboxCompose(ctx context.Context, workspaceID, pendingID, reason string) error
	IsSuppressed(ctx context.Context, workspaceID, email string) (bool, error)
	ResolveSenderTransport(ctx context.Context, workspaceID, mailboxID string) (coreapi.SenderTransport, error)
}

// PendingComposeSendHandler delivers a deferred composed email.
//
// Structurally identical to PendingReplySendHandler — the same claim-guard,
// release-on-transient, never-return-past-the-dial discipline — over the
// compose table. The differences are all in the payload: recipients and subject
// are the message's own rather than derived from a thread, and there is no
// thread to record the sent message onto.
func PendingComposeSendHandler(core ComposeCore, sender Mailer) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var p queue.InboxPendingComposeSendPayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			slog.ErrorContext(ctx, "inbox_pending_compose_bad_payload", "err", err)
			return nil
		}

		compose, err := core.ClaimPendingInboxCompose(ctx, p.WorkspaceID, p.PendingID)
		if err != nil {
			if errors.Is(err, coreapi.ErrInboxPendingNotClaimable) {
				// The undo path, and not a failure.
				slog.InfoContext(ctx, "inbox_pending_compose_not_claimable",
					"pending_id", p.PendingID, "workspace_id", p.WorkspaceID)
				return nil
			}
			return fmt.Errorf("claim pending compose: %w", err)
		}

		// Re-checked after claiming, across EVERY recipient including Bcc. One
		// suppressed address fails the whole message rather than being quietly
		// dropped: the operator addressed those people deliberately, and
		// silently not delivering to one of them is the worse outcome.
		for _, recipient := range allRecipients(compose) {
			suppressed, err := core.IsSuppressed(ctx, p.WorkspaceID, recipient)
			if err != nil {
				return releaseCompose(ctx, core, p, reasonSuppressionCheckFailed,
					fmt.Errorf("check suppression: %w", err))
			}
			if suppressed {
				slog.InfoContext(ctx, "inbox_pending_compose_recipient_suppressed",
					"pending_id", p.PendingID)
				failCompose(ctx, core, p, reasonRecipientSuppressed)
				return nil
			}
		}

		transport, err := core.ResolveSenderTransport(ctx, p.WorkspaceID, compose.MailboxID)
		if err != nil {
			return releaseCompose(ctx, core, p, reasonTransportUnavailable,
				fmt.Errorf("resolve sender transport: %w", err))
		}
		defer zeroize(transport.SMTPPassword)
		defer zeroize(transport.AccessToken)

		// ONE dial per recipient, across To, Cc and Bcc alike.
		//
		// mail.Message carries a single To and no Cc/Bcc fields, so there is no
		// way to express a multi-recipient header with the platform sender as it
		// stands. Rather than joining addresses into the To header — which would
		// disclose every recipient to all of them, and silently turn a Bcc into
		// a visible one — each address gets its own message. The tradeoff is
		// that recipients cannot see each other and cannot reply-all; for a
		// one-to-one outreach tool that is the safer default, and it is stated
		// in the API contract rather than left for someone to discover.
		//
		// The FIRST message's id is the one recorded. A partial failure part-way
		// through releases for retry, which re-sends to the earlier addresses:
		// the accepted posture is "occasionally deliver twice to some recipients
		// rather than never to the rest", the mirror of the reply path's
		// "never double, occasionally drop". Bounded by MaxComposeRecipients.
		var messageID string
		for i, recipient := range allRecipients(compose) {
			id, err := sender.Send(ctx,
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
					FromEmail: transport.FromEmail,
					FromName:  transport.FromName,
					To:        recipient,
					Subject:   compose.Subject,
					BodyText:  compose.BodyText,
				})
			if err != nil {
				return releaseCompose(ctx, core, p, reasonProviderRejected,
					fmt.Errorf("send composed email to recipient %d: %w", i+1, err))
			}
			if i == 0 {
				messageID = id
			}
		}

		// Past the dial: logged, never returned.
		if err := core.MarkPendingInboxComposeSent(ctx, p.WorkspaceID, p.PendingID, messageID); err != nil {
			slog.ErrorContext(ctx, "inbox_pending_compose_mark_sent_failed",
				"pending_id", p.PendingID, "err", err)
		}
		return nil
	}
}

// allRecipients flattens To, Cc and Bcc — a suppressed address is suppressed
// however it was addressed.
func allRecipients(c coreapi.PendingInboxCompose) []string {
	out := make([]string, 0, len(c.ToEmails)+len(c.CcEmails)+len(c.BccEmails))
	out = append(out, c.ToEmails...)
	out = append(out, c.CcEmails...)
	out = append(out, c.BccEmails...)
	return out
}

// releaseCompose mirrors releaseAndReturn, including the reason-token rule: a
// raw provider error must never reach last_error, which is served to clients —
// nor the error returned to asynq, which becomes the DEAD LETTER's last_error
// and is served to clients too. See releaseAndReturn for the full reasoning; the
// hole was identical on both handlers and is closed the same way.
func releaseCompose(
	ctx context.Context,
	core ComposeCore,
	p queue.InboxPendingComposeSendPayload,
	reason string,
	cause error,
) error {
	slog.WarnContext(ctx, "inbox_pending_compose_attempt_failed",
		"pending_id", p.PendingID, "reason", reason, "err", cause)
	if err := core.ReleasePendingInboxCompose(ctx, p.WorkspaceID, p.PendingID, reason); err != nil {
		slog.ErrorContext(ctx, "inbox_pending_compose_release_failed", "pending_id", p.PendingID, "err", err)
	}
	return attemptFailure(reason)
}

func failCompose(ctx context.Context, core ComposeCore, p queue.InboxPendingComposeSendPayload, reason string) {
	if err := core.FailPendingInboxCompose(ctx, p.WorkspaceID, p.PendingID, reason); err != nil {
		slog.ErrorContext(ctx, "inbox_pending_compose_fail_write_failed", "pending_id", p.PendingID, "err", err)
	}
}
