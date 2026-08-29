package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
)

// ReplyCore is the narrow coreapi capability ReplySendHandler needs: load one
// manual reply's job (mailbox, recipient, threading headers), re-check
// suppression, resolve the sending mailbox's decrypted transport, and record
// the delivered reply. It is deleted with that handler — see its doc for when.
// Defined here (consumer side) — the same "avoid
// widening coreapi.Client's ~40-method surface for one call site" trade as
// testsend.Core — and satisfied by the in-process client via type assertion
// at the composition root (internal/worker/handlers.go). IsSuppressed and
// ResolveSenderTransport are the SAME methods testsend.Core already declares
// (client implements them once; Go's structural typing needs no duplicate
// implementation).
type ReplyCore interface {
	// GetInboxReplyJob loads one thread's reply job, workspace-pinned.
	GetInboxReplyJob(ctx context.Context, threadID, workspaceID string) (coreapi.InboxReplyJob, error)
	// IsSuppressed reports whether `to` is on the workspace's suppression
	// list. This is the defense-in-depth half of the API-side suppression
	// check: that check can race an incoming unsubscribe between enqueue and
	// this task running — and on a drain path the gap can be long — so the
	// worker re-checks the SAME table right before dialing, workspace-pinned.
	IsSuppressed(ctx context.Context, workspaceID, to string) (bool, error)
	// ResolveSenderTransport decrypts the resolved mailbox's send credential
	// (refreshing an OAuth token for gmail/m365), workspace-pinned.
	ResolveSenderTransport(ctx context.Context, workspaceID, mailboxID string) (coreapi.SenderTransport, error)
	// RecordInboxReply persists one delivered reply's outbound message row
	// and last_message_at bump, workspace-pinned.
	RecordInboxReply(ctx context.Context, in coreapi.RecordInboxReplyInput) error
	// ClaimInboxReply attempts to claim taskID for delivery — claim-before-
	// send, called immediately before ResolveSenderTransport (see
	// ReplySendHandler). claimed=false means a prior attempt at this EXACT
	// task already reached the dial (most likely a worker crash between the
	// provider ACK and this handler returning, which asynq's lease then
	// redelivers to another worker as a full re-run): the caller must skip,
	// not send again.
	ClaimInboxReply(ctx context.Context, workspaceID, taskID string) (claimed bool, err error)
	// ReleaseInboxReply releases a claim taken by ClaimInboxReply. Called
	// ONLY after a TRANSIENT send failure, before returning err for asynq to
	// retry — without releasing, the retry's own claim attempt would see its
	// own abandoned claim and skip forever, permanently dropping a reply
	// that never actually sent.
	ReleaseInboxReply(ctx context.Context, workspaceID, taskID string) error
}

// Mailer sends one email through whichever transport the resolved
// SenderTransport selects. Satisfied by *mail.MultiSender in production;
// defined here so tests inject a fake instead of dialing a real server.
// Mirrors testsend.Mailer.
type Mailer interface {
	Send(ctx context.Context, tj mail.OutboundJob, msg mail.Message) (messageID string, err error)
}

// rePrefix is the idempotent "Re: " reply subject prefix. Comparison is
// case-insensitive so "RE: " / "re: " from a prior client is also recognized
// and never doubled.
const rePrefix = "Re: "

// reSubject prefixes subject with a single "Re: " — idempotent, so a reply on
// a thread whose subject already carries one (e.g. a synthesized follow-up
// step subject, see internal/app/inbox's own replySubject) is never doubled
// to "Re: Re: ".
func reSubject(subject string) string {
	if strings.HasPrefix(strings.ToLower(subject), strings.ToLower(rePrefix)) {
		return subject
	}
	return rePrefix + subject
}

// ReplySendHandler returns an asynq handler for inbox:reply_send tasks. It is a
// DRAIN PATH: nothing produces this task any more.
//
// POST /inbox/threads/{id}/reply now writes the reply body to an
// inbox_pending_replies row and enqueues inbox:pending_reply_send, which carries
// only that row's id (see internal/app/inbox.queueReply for why: a task payload
// is stored verbatim in task_dead_letters and served by GET /dead-letters under
// campaigns:read, an OAuth-grantable scope that must never see correspondence).
// queue.EnqueueInboxReplySend is deleted, which is what makes this drain finite.
//
// This handler stays only so tasks that were ALREADY in Redis when that shipped
// still get delivered. Its behaviour is deliberately unchanged — it must keep
// sending from the payload's own BodyText, because a legacy task has no row to
// read a body from, and "load it from the row instead" would silently drop every
// reply in flight at deploy time.
//
// WHEN TO DELETE IT: the release after this one. Its entry log
// ("inbox_reply_send_legacy_drain") is the signal — once it has stopped
// appearing for longer than the queue's retention, nothing is left to drain, and
// this file goes together with queue.TaskInboxReplySend,
// queue.InboxReplySendPayload, the legacyContentBearingTaskTypes entry that
// suppresses its capture, and this registration in register.go.
//
// Decrypting the mailbox credential and dialing the provider still happen ONLY
// here (docs/security.md invariant 1).
func ReplySendHandler(core ReplyCore, sender Mailer) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		//nolint:staticcheck // SA1019: the deprecated payload is the point — this
		// handler exists solely to finish tasks that already carry it.
		var p queue.InboxReplySendPayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return err
		}

		// The drain's own signal, and the operator's evidence that deleting this
		// path is (or is not yet) safe. IDS ONLY — never the payload: BodyText is
		// the operator's correspondence, and logging it would recreate in the log
		// sink exactly the disclosure that moving the body out of the payload
		// closed. WARN rather than INFO because every line here is a task from
		// before the cutover, which is worth noticing rather than filtering.
		slog.WarnContext(ctx, "inbox_reply_send_legacy_drain",
			"workspace_id", p.WorkspaceID, "thread_id", p.ThreadID, "task_id", p.TaskID)

		job, err := core.GetInboxReplyJob(ctx, p.ThreadID, p.WorkspaceID)
		if err != nil {
			if errors.Is(err, coreapi.ErrInboxNoInbound) {
				// The API-side check rejected this before the task was ever
				// enqueued; seeing it here means the thread changed shape
				// between then and now (these tasks predate the cutover, so
				// "then" may be a while ago). Permanent — retrying cannot fix
				// a thread with no inbound message. Log and drop.
				slog.WarnContext(ctx, "inbox_reply_no_inbound_message",
					"workspace_id", p.WorkspaceID, "thread_id", p.ThreadID)
				return nil
			}
			return fmt.Errorf("load inbox reply job: %w", err)
		}

		// Defense in depth (docs/security.md): re-check suppression right
		// here, before any credential is decrypted. The API-side check can
		// race an incoming unsubscribe between enqueue and this task running,
		// and these tasks are old; a suppressed recipient is skipped (not an error
		// — the task should not retry) and logged at WARN so a persistent
		// hit is observable. The raw recipient address is deliberately not
		// logged.
		suppressed, err := core.IsSuppressed(ctx, p.WorkspaceID, job.ToEmail)
		if err != nil {
			return fmt.Errorf("check suppression: %w", err)
		}
		if suppressed {
			slog.WarnContext(ctx, "inbox_reply_recipient_suppressed",
				"workspace_id", p.WorkspaceID, "thread_id", p.ThreadID)
			return nil
		}

		// Claim-before-send (docs/security.md invariant 4a's "never double,
		// occasionally drop a rare ambiguous send" posture, applied here the
		// same way ClaimStepSend/ClaimWarmupSend apply it to a sequence/warmup
		// send): a worker that crashes AFTER the provider ACK but before this
		// handler returns leaves asynq's lease to expire and redeliver the
		// SAME task (same p.TaskID) to another worker as a full re-run, which
		// would otherwise re-dial and double-send. Placed immediately before
		// the credential is decrypted — no point claiming a send this
		// handler hasn't yet decided to attempt (the job-load/suppression
		// checks above may still skip it entirely).
		claimed, err := core.ClaimInboxReply(ctx, p.WorkspaceID, p.TaskID)
		if err != nil {
			return fmt.Errorf("claim inbox reply: %w", err)
		}
		if !claimed {
			slog.WarnContext(ctx, "inbox_reply_already_claimed",
				"workspace_id", p.WorkspaceID, "thread_id", p.ThreadID)
			return nil
		}

		transport, err := core.ResolveSenderTransport(ctx, p.WorkspaceID, job.MailboxID)
		if err != nil {
			// The claim was already taken; a transport-resolve failure is
			// treated as transient (like every other coreapi call in this
			// handler) but, unlike the send itself below, nothing was ever
			// dialed, so releasing first is still correct: a retry must be
			// able to re-claim.
			if relErr := core.ReleaseInboxReply(ctx, p.WorkspaceID, p.TaskID); relErr != nil {
				slog.ErrorContext(ctx, "inbox_reply_release_claim_failed",
					"workspace_id", p.WorkspaceID, "thread_id", p.ThreadID, "err", relErr)
			}
			return fmt.Errorf("resolve sender transport: %w", err)
		}
		// Zeroize the decrypted secret(s) before returning, mirroring
		// testsend/sequence: the primary long-lived buffer this handler
		// allocated should not linger past it in memory.
		defer zeroize(transport.SMTPPassword)
		defer zeroize(transport.AccessToken)

		subject := reSubject(job.Subject)
		messageID, err := sender.Send(ctx,
			mail.OutboundJob{
				Provider: transport.Provider, Host: transport.SMTPHost, Port: transport.SMTPPort,
				Username: transport.SMTPUsername, Password: string(transport.SMTPPassword),
				AllowPlaintext: transport.AllowPlaintext, AccessToken: string(transport.AccessToken),
			},
			mail.Message{
				FromEmail: transport.FromEmail, FromName: transport.FromName, To: job.ToEmail,
				Subject: subject, BodyText: p.BodyText,
				InReplyTo: job.InReplyTo, References: job.References,
				// ListUnsubscribe / BodyHTML deliberately left empty: a
				// conversational reply is not subject to the campaign
				// unsubscribe/tracking machinery.
			},
		)
		if err != nil {
			// Transient send failure: release the claim BEFORE returning err
			// for asynq to retry — the retry's own ClaimInboxReply call must
			// be able to re-claim, or a failed send before it ever reached
			// the provider would be permanently (and silently) dropped
			// rather than retried. If the release itself fails, log it and
			// still return the send error: the retry will then see the
			// claim as already taken and skip rather than double-send — the
			// fail-safe direction (drop, never double).
			if relErr := core.ReleaseInboxReply(ctx, p.WorkspaceID, p.TaskID); relErr != nil {
				slog.ErrorContext(ctx, "inbox_reply_release_claim_failed",
					"workspace_id", p.WorkspaceID, "thread_id", p.ThreadID, "err", relErr)
			}
			return fmt.Errorf("send reply: %w", err)
		}

		// The reply is now DELIVERED. From here, returning an error would make
		// asynq retry the WHOLE handler — including sender.Send — and risk a
		// double-send to the recipient. A bookkeeping failure after a
		// successful send is logged, not retried: the mail already went out,
		// so re-running this handler is strictly worse than a thread whose
		// history is briefly missing one row.
		if err := core.RecordInboxReply(ctx, coreapi.RecordInboxReplyInput{
			WorkspaceID: p.WorkspaceID, ThreadID: p.ThreadID, MessageID: messageID,
			FromEmail: transport.FromEmail, FromName: transport.FromName, ToEmail: job.ToEmail,
			Subject: subject, BodyText: p.BodyText,
		}); err != nil {
			slog.ErrorContext(ctx, "inbox_reply_record_failed",
				"workspace_id", p.WorkspaceID, "thread_id", p.ThreadID, "err", err)
		}
		return nil
	}
}
