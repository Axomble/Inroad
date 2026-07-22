package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
)

// fetchBatchSize bounds how many messages one inbox:poll pass pulls from a
// mailbox, mirroring mail.InboxReader.Fetch's maxN contract (bounds the IMAP
// request itself, not just the returned slice).
const fetchBatchSize = 200

// PollHandler returns an asynq handler for inbox:poll tasks. It opens one
// mailbox's IMAP connection, establishes/validates the poll baseline via
// CurrentState, fetches anything new since the stored cursor, classifies
// each message as a bounce/reply/neither, and persists the advanced cursor.
func PollHandler(core coreapi.Client, reader mail.InboxReader) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var p queue.InboxPollPayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return err
		}

		job, err := core.GetInboxPollJob(ctx, p.MailboxID, p.WorkspaceID)
		if err != nil {
			return err
		}
		defer zeroize(job.Password)

		cfg := mail.IMAPConfig{Host: job.Host, Port: job.Port, Username: job.Username, Password: string(job.Password)}

		uidValidity, uidNext, err := reader.CurrentState(cfg)
		if err != nil {
			return err
		}

		// Re-baseline on a first poll (never-polled mailbox, UIDValidity==0)
		// or a UIDVALIDITY reset (the server renumbered the mailbox — old UIDs
		// are meaningless): jump the cursor to the current top and process
		// nothing this pass. This also keeps a mailbox's pre-existing inbox
		// from being treated as a flood of replies the first time it's polled.
		if job.UIDValidity == 0 || uidValidity != job.UIDValidity {
			// RFC 3501 guarantees UIDNEXT > 0, but a misbehaving server must
			// never be able to underflow this uint32 and wedge the mailbox's
			// cursor at math.MaxUint32.
			var base uint32
			if uidNext > 0 {
				base = uidNext - 1
			}
			return core.SetInboxCursor(ctx, p.MailboxID, p.WorkspaceID, base, uidValidity)
		}

		msgs, _, err := reader.Fetch(cfg, job.LastSeenUID, fetchBatchSize)
		if err != nil {
			return err
		}

		var replies, bounces, skipped int
		for _, msg := range msgs {
			matched, err := processMessage(ctx, core, p.WorkspaceID, msg, &replies, &bounces)
			if err != nil {
				return err
			}
			if !matched {
				skipped++
			}
		}

		slog.Info("inbox_poll_processed", "mailbox_id", p.MailboxID,
			"messages", len(msgs), "replies", replies, "bounces", bounces, "skipped", skipped)
		return core.SetInboxCursor(ctx, p.MailboxID, p.WorkspaceID, scannedWindowTop(job.LastSeenUID, uidNext), uidValidity)
	}
}

// scannedWindowTop is the highest UID a successful bounded Fetch(sinceUID,
// fetchBatchSize) has definitively examined — sinceUID+fetchBatchSize,
// capped at the mailbox's current head (uidNext-1, guarded against a
// misbehaving uidNext==0). The cursor always advances to this value after a
// successful fetch+process pass, regardless of how many messages actually
// existed in that range: a UID absent from the range (expunged or never
// assigned) is a gap, not unprocessed mail, so leaving the cursor at the old
// max-processed-UID (or unmoved, on an empty batch) would re-scan the same
// stalled window forever while newer mail sits above it, silently killing
// detection for that mailbox.
func scannedWindowTop(sinceUID, uidNext uint32) uint32 {
	var head uint32
	if uidNext > 0 {
		head = uidNext - 1
	}
	top := sinceUID + uint32(fetchBatchSize)
	if top > head {
		top = head
	}
	return top
}

// processMessage classifies one fetched message and takes the corresponding
// action. *bounces is bumped on a hard bounce that gets marked; *replies is
// bumped only when a matched reply actually calls MarkReplied (i.e. the
// matched send has an enrollment — a match against the legacy direct-send
// path has nothing to stop, so it isn't counted as an engaged reply). The
// returned bool reports whether the message matched anything (a bounce or a
// reply) — used only for the skipped-count in the poll summary log.
func processMessage(ctx context.Context, core coreapi.Client, workspaceID string, msg mail.InboundMessage, replies, bounces *int) (bool, error) {
	d := ParseDSN(msg.Header, msg.ContentType, msg.Body)
	if d.Kind != NotABounce {
		// A DSN is never also a reply — always handled here, never falls
		// through to the reply-matching path below.
		switch d.Kind {
		case HardBounce:
			s, err := core.FindSendByMessageID(ctx, workspaceID, d.OriginalMessageID)
			if err != nil {
				if errors.Is(err, coreapi.ErrNoMatch) {
					return false, nil
				}
				return false, err
			}
			if err := core.MarkBounced(ctx, s.EnrollmentID, workspaceID, s.ContactEmail, true); err != nil {
				return false, err
			}
			*bounces++
			return true, nil
		default: // SoftBounce: log only, no state change.
			slog.Info("inbox_poll_soft_bounce", "workspace_id", workspaceID, "status", d.StatusCode)
			return true, nil
		}
	}

	if IsAutoReply(msg.Header) {
		return false, nil
	}

	for _, id := range MessageIDs(msg.Header) {
		s, err := core.FindSendByMessageID(ctx, workspaceID, id)
		if err != nil {
			if errors.Is(err, coreapi.ErrNoMatch) {
				continue
			}
			return false, err
		}
		if s.EnrollmentID != "" {
			if err := core.MarkReplied(ctx, s.EnrollmentID, workspaceID); err != nil {
				return false, err
			}
			*replies++
		}
		return true, nil
	}
	return false, nil
}

// zeroize overwrites the decrypted IMAP password in place after use. Mirrors
// sequence.zeroize.
func zeroize(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
