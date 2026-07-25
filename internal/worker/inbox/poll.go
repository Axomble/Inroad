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
	"github.com/inroad/inroad/internal/platform/replyclassify"
)

// fetchBatchSize bounds how many messages one inbox:poll pass pulls from a
// mailbox, mirroring mail.InboxReader.Fetch's maxN contract (bounds the IMAP
// request itself, not just the returned slice).
const fetchBatchSize = 200

// GmailFetcher polls a Gmail mailbox for new inbound messages via the Gmail API,
// resuming from an opaque historyId cursor. *mail.GmailReader satisfies it; the
// worker depends on the interface so it can be unit-tested with a fake (the
// concrete reader's wire seam is unexported). It is the provider-parallel of
// mail.InboxReader for the API transport.
type GmailFetcher interface {
	Fetch(ctx context.Context, accessToken, sinceHistoryID string, maxN int) (msgs []mail.InboundMessage, newCursor string, err error)
}

// GraphFetcher polls an M365 mailbox for new inbound messages via the Microsoft
// Graph delta query, resuming from an opaque delta/next-link URL cursor.
// *mail.GraphReader satisfies it; the worker depends on the interface so it can
// be unit-tested with a fake (the concrete reader's wire seam is unexported). It
// is the provider-parallel of GmailFetcher for the Graph API transport.
type GraphFetcher interface {
	Fetch(ctx context.Context, accessToken, sinceCursor string, maxN int) (msgs []mail.InboundMessage, newCursor string, err error)
}

// PollHandler returns an asynq handler for inbox:poll tasks. It dispatches on
// the mailbox provider: gmail polls via the Gmail API (opaque historyId cursor),
// m365 polls via the Microsoft Graph delta query (opaque delta-link cursor),
// smtp opens the mailbox's IMAP connection, establishes/validates the poll
// baseline via CurrentState, and fetches anything new since the stored UID
// cursor. All paths run the SAME reply/bounce classification (processMessage,
// which routes a matched reply through the injected classifier) and persist
// their respective cursor.
func PollHandler(core coreapi.Client, reader mail.InboxReader, gmail GmailFetcher, graph GraphFetcher, classifier *replyclassify.Classifier) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var p queue.InboxPollPayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return err
		}

		job, err := core.GetInboxPollJob(ctx, p.MailboxID, p.WorkspaceID)
		if err != nil {
			return err
		}

		if job.Provider == "gmail" {
			return pollGmail(ctx, core, gmail, classifier, p, job)
		}
		if job.Provider == "m365" {
			return pollGraph(ctx, core, graph, classifier, p, job)
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
			matched, err := processMessage(ctx, core, classifier, p.WorkspaceID, msg, &replies, &bounces)
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

// pollGmail runs one inbox poll pass for a gmail mailbox: fetch new messages
// since the opaque historyId cursor, classify each with the SAME processMessage
// path as IMAP (ParseDSN + reply matcher), and persist the advanced cursor via
// SetInboxCursorString (the IMAP UID cursor columns are untouched). The
// short-lived access token is zeroized after the pass, like the IMAP password.
func pollGmail(ctx context.Context, core coreapi.Client, reader GmailFetcher, classifier *replyclassify.Classifier, p queue.InboxPollPayload, job coreapi.InboxPollJob) error {
	defer zeroize(job.AccessToken)

	msgs, newCursor, err := reader.Fetch(ctx, string(job.AccessToken), job.Cursor, fetchBatchSize)
	if err != nil {
		return err
	}

	var replies, bounces, skipped int
	for _, msg := range msgs {
		matched, err := processMessage(ctx, core, classifier, p.WorkspaceID, msg, &replies, &bounces)
		if err != nil {
			return err
		}
		if !matched {
			skipped++
		}
	}

	slog.Info("inbox_poll_processed", "mailbox_id", p.MailboxID, "provider", "gmail",
		"messages", len(msgs), "replies", replies, "bounces", bounces, "skipped", skipped)
	return core.SetInboxCursorString(ctx, p.MailboxID, p.WorkspaceID, newCursor)
}

// pollGraph runs one inbox poll pass for an m365 mailbox: fetch new messages
// since the opaque Graph delta-link cursor, classify each with the SAME
// processMessage path as IMAP/Gmail (ParseDSN + reply matcher), and persist the
// advanced cursor via SetInboxCursorString (the IMAP UID cursor columns are
// untouched). The short-lived access token is zeroized after the pass, like the
// IMAP password and the Gmail token. Byte-for-byte parallel to pollGmail; only
// the transport and the "provider" log value differ.
func pollGraph(ctx context.Context, core coreapi.Client, reader GraphFetcher, classifier *replyclassify.Classifier, p queue.InboxPollPayload, job coreapi.InboxPollJob) error {
	defer zeroize(job.AccessToken)

	msgs, newCursor, err := reader.Fetch(ctx, string(job.AccessToken), job.Cursor, fetchBatchSize)
	if err != nil {
		return err
	}

	var replies, bounces, skipped int
	for _, msg := range msgs {
		matched, err := processMessage(ctx, core, classifier, p.WorkspaceID, msg, &replies, &bounces)
		if err != nil {
			return err
		}
		if !matched {
			skipped++
		}
	}

	slog.Info("inbox_poll_processed", "mailbox_id", p.MailboxID, "provider", "m365",
		"messages", len(msgs), "replies", replies, "bounces", bounces, "skipped", skipped)
	return core.SetInboxCursorString(ctx, p.MailboxID, p.WorkspaceID, newCursor)
}

// processMessage classifies one fetched message and takes the corresponding
// action. A DSN is handled first (hard bounce → MarkBounced) and never falls
// through to the reply path. A non-DSN message that matches a send is routed
// through the reply classifier:
//
//   - automated (auto_reply / out_of_office) → RecordReplyClass, enrollment
//     kept ACTIVE (the OOO-trap fix) and counted as skipped, NOT a reply —
//     UNLESS the automated message also carries an explicit opt-out, in which
//     case compliance wins and it is routed to MarkUnsubscribed;
//   - unsubscribe → MarkUnsubscribed (address suppressed + stop), counted as
//     handled;
//   - anything else (positive/negative/neutral/unknown) → MarkReplied tagged,
//     counted as an engaged reply.
//
// *bounces is bumped on a marked hard bounce; *replies is bumped on a matched
// reply routed to MarkReplied that actually has an enrollment to stop (so the
// metric keeps its "engaged enrollment reply" meaning). enrollmentID may be ""
// (legacy direct-send): classification and routing still run — MarkUnsubscribed
// still suppresses the address, and the tagged RecordReplyClass/MarkReplied
// writes no-op the enrollment update coreapi-side. The returned bool reports
// whether the message matched (a bounce or a stopping/suppressing reply) — used
// only for the skipped-count in the poll summary log; an automated tag reports
// false so it counts as skipped rather than a reply.
func processMessage(ctx context.Context, core coreapi.Client, classifier *replyclassify.Classifier, workspaceID string, msg mail.InboundMessage, replies, bounces *int) (bool, error) {
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

	// The standalone IsAutoReply early-skip is intentionally gone: the
	// classifier's Layer 1 is a strict superset of it, so an automated reply is
	// now routed through classification (tagged via RecordReplyClass) rather
	// than silently dropped before it ever matches a send.
	for _, id := range MessageIDs(msg.Header) {
		s, err := core.FindSendByMessageID(ctx, workspaceID, id)
		if err != nil {
			if errors.Is(err, coreapi.ErrNoMatch) {
				continue
			}
			return false, err
		}

		in := replyclassify.Input{
			Headers:  map[string][]string(msg.Header), // net/mail.Header is already map[string][]string
			Subject:  msg.Header.Get("Subject"),
			BodyText: string(msg.Body),
		}
		r := classifier.Classify(ctx, in)
		switch {
		case replyclassify.IsAutomated(r.Class):
			if classifier.LooksLikeUnsubscribe(in) {
				// Compliance wins over automation: an explicit opt-out is
				// honored even inside an automated message. The accepted
				// trade-off is occasionally suppressing an OOO whose footer
				// says "unsubscribe" — suppression is the fail-safe/compliant
				// direction, so we err that way on purpose.
				if err := core.MarkUnsubscribed(ctx, s.EnrollmentID, workspaceID, s.ContactEmail); err != nil {
					return false, err
				}
				return true, nil
			}
			// auto_reply / out_of_office: tag but keep the enrollment ACTIVE
			// (the OOO-trap fix). Not an engaged reply → reported as skipped.
			if err := core.RecordReplyClass(ctx, s.EnrollmentID, workspaceID, r.Class, r.Source, r.Confidence); err != nil {
				return false, err
			}
			return false, nil
		case r.Class == replyclassify.ClassUnsubscribe:
			// Reply-based opt-out: suppress the address (compliance) + stop.
			if err := core.MarkUnsubscribed(ctx, s.EnrollmentID, workspaceID, s.ContactEmail); err != nil {
				return false, err
			}
			return true, nil
		default:
			// positive / negative / neutral / unknown: stop, tagged.
			if err := core.MarkReplied(ctx, s.EnrollmentID, workspaceID, r.Class, r.Source, r.Confidence); err != nil {
				return false, err
			}
			// Only an enrollment reply is an "engaged reply": a legacy
			// direct-send match (EnrollmentID == "") has nothing to stop, so
			// MarkReplied no-ops coreapi-side and it isn't counted.
			if s.EnrollmentID != "" {
				*replies++
			}
			return true, nil
		}
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
