package warmup

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

// EngageHandler returns an asynq handler for warmup:engage tasks. It runs the
// recipient-side engagement of one received warmup message — rescue-from-spam,
// mark-read, and (probabilistically) a threaded reply — then flips the receipt's
// engaged guard so a retry is a no-op. Every step is idempotent: the IMAP/Gmail
// mark-read and rescue are safe to repeat (a message already read / already in the
// inbox is a clean no-op), the reply is claim-before-send (its deterministic
// SendID reclaims the same warmup_sends row), and MarkWarmupEngaged guards on NOT
// engaged so the reply counter never double-bumps. Engagement acts ONLY on the
// recipient's own mailbox, reached through coreapi + the shared SSRF-guarded
// transport — no new DB or dial path.
func EngageHandler(core coreapi.Client, engager mail.Engager, sender Sender) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var p queue.WarmupEngagePayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return err
		}
		job, err := core.GetWarmupEngageJob(ctx, p.ReceiptID, p.WorkspaceID)
		if err != nil {
			return err
		}
		// Decrypted secrets: wipe both after use, like every other worker secret. The
		// reply send (job.ReplySend) reuses these SAME slices, so one wipe covers both.
		defer zeroize(job.SMTPPassword)
		defer zeroize(job.AccessToken)

		// The engage transport is the recipient's OWN mailbox: IMAP fields + the shared
		// password for smtp mailboxes, AccessToken for gmail. SourceFolder + MessageID
		// (attacker-influenceable receipt content) are passed as literal protocol
		// arguments by the engager, never concatenated into a command.
		target := mail.EngageTarget{
			Provider:       job.Provider,
			AccessToken:    job.AccessToken,
			IMAPHost:       job.IMAPHost,
			IMAPPort:       job.IMAPPort,
			IMAPUsername:   job.IMAPUsername,
			IMAPPassword:   job.SMTPPassword,
			AllowPlaintext: job.AllowPlaintext,
			SourceFolder:   job.SourceFolder,
			MessageID:      job.MessageID,
		}

		// Rescue first (only when it landed in spam) so mark-read then finds it in the
		// inbox where a real recipient reads it.
		if job.DoRescue {
			if err := engageStep(ctx, "rescue", job.Provider, func() error { return engager.Rescue(ctx, target) }); err != nil {
				return err
			}
		}
		if job.DoMarkRead {
			if err := engageStep(ctx, "mark-read", job.Provider, func() error { return engager.MarkRead(ctx, target) }); err != nil {
				return err
			}
		}

		// A threaded reply is a NEW warmup send FROM the recipient. Mirror the tick
		// send's claim→send→finalize flow (send.go); replied reflects whether it
		// actually delivered, so MarkWarmupEngaged bumps the reply counter only then.
		replied := false
		if job.DoReply {
			r, err := sendWarmupReply(ctx, core, sender, job.ReplySend)
			if err != nil {
				return err
			}
			replied = r
		}

		// Flip the engaged guard last (idempotent): a retried engage re-does the
		// idempotent steps above, then this no-ops (guarded on NOT engaged), so the
		// reply counter is never double-counted.
		return core.MarkWarmupEngaged(ctx, p.ReceiptID, p.WorkspaceID, replied)
	}
}

// engageStep runs one engagement action, treating mail.ErrEngageUnsupported (the
// Graph/M365 provider in v1) as a documented clean skip — logged, not failed — and
// surfacing any other error so asynq retries. The reply send is unaffected: it uses
// the ordinary send transport, which every provider supports.
func engageStep(_ context.Context, action, provider string, fn func() error) error {
	switch err := fn(); {
	case err == nil:
		return nil
	case errors.Is(err, mail.ErrEngageUnsupported):
		slog.Info("warmup engage: step unsupported for provider, skipping", "action", action, "provider", provider)
		return nil
	default:
		return fmt.Errorf("warmup engage %s: %w", action, err)
	}
}

// sendWarmupReply claims, sends, and finalizes the recipient's threaded reply,
// mirroring send.go's claim→send→mark/release/fail flow. It returns replied=true
// only when the reply actually delivered (a fresh send, or a recover-forward over a
// prior run's already-'sent' row), so the engage handler bumps the reply counter
// exactly once. A retryable send failure returns the error (nothing delivered,
// claim released) so asynq retries; a permanent failure is fail-forward (finalized
// 'failed', replied=false) so one bad reply never wedges the engage.
func sendWarmupReply(ctx context.Context, core coreapi.Client, sender Sender, reply coreapi.WarmupSendJob) (bool, error) {
	outcome, err := core.ClaimWarmupSend(ctx, reply)
	if err != nil {
		return false, err
	}
	switch outcome {
	case coreapi.ClaimSkip:
		// Another worker owns a fresh 'sending' lease, or the row is terminal — the
		// reply is being / was handled elsewhere. Nothing to do.
		return false, nil
	case coreapi.ClaimAlreadySent:
		// A prior run delivered THIS exact reply but its engage didn't finalize.
		// Recover-forward: count it replied without re-sending.
		return true, nil
	}

	msgID, sendErr := sender.Send(ctx,
		mail.OutboundJob{
			Provider: reply.Provider, Host: reply.SMTPHost, Port: reply.SMTPPort,
			Username: reply.SMTPUsername, Password: string(reply.SMTPPassword),
			AllowPlaintext: reply.AllowPlaintext, AccessToken: string(reply.AccessToken),
		},
		mail.Message{
			FromEmail: reply.FromEmail, FromName: reply.FromName, To: reply.ToEmail,
			Subject: reply.Subject, BodyText: reply.BodyText, BodyHTML: reply.BodyHTML,
			InReplyTo: reply.InReplyTo, References: reply.References,
			ExtraHeaders: map[string]string{warmupHeader: reply.Token},
		},
	)
	switch {
	case sendErr == nil:
		if err := core.MarkWarmupSent(ctx, reply, msgID); err != nil {
			return false, fmt.Errorf("mark warmup reply sent: %w", err)
		}
		return true, nil
	case mail.Retryable(sendErr):
		if rerr := core.ReleaseWarmupSend(ctx, reply); rerr != nil {
			return false, fmt.Errorf("release warmup reply after transient failure: %w", rerr)
		}
		return false, sendErr
	default:
		if ferr := core.FailWarmupSend(ctx, reply, sendErr.Error()); ferr != nil {
			return false, ferr
		}
		return false, nil
	}
}
