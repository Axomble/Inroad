package inbox

import (
	"context"
	"log/slog"
	netmail "net/mail"
	"strings"
	"time"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/mime"
	"github.com/inroad/inroad/internal/platform/replyclassify"
)

// replyDispatch is one matched, classified inbound reply plus the seams the
// automation acts through. It exists so the two dispatch strategies below take
// one receiver instead of eight arguments each, and so the pieces they share
// (CRM capture, the deferral) are written once.
type replyDispatch struct {
	core       coreapi.Client
	classifier *replyclassify.Classifier
	// workspaceID is the pinned tenant for every coreapi call below; it comes
	// from the polled mailbox, never from message content.
	workspaceID string
	msg         mail.InboundMessage
	// in is the classifier input, reused for the unsubscribe re-check.
	in     replyclassify.Input
	send   coreapi.SendRef
	result replyclassify.Result
	// replies counts ENGAGED enrollment replies for the poll summary; only a
	// stopping reply on a real enrollment bumps it.
	replies *int
}

// byLabel dispatches on the resolved label's role flags. It reproduces byClass
// exactly for the seven builtin labels seeded by migration 000047 — the flags
// were chosen to — while letting a workspace attach the same automation to a
// label of its own.
//
// Order is deliberate and matches byClass: compliance (suppression) first,
// because an opt-out must be honoured whatever else the message is; then the
// stopping path; then the non-stopping (automated) path, which is the only one
// that may defer.
//
// The bool reports whether the message was HANDLED (used for the poll summary's
// skipped count), not whether it succeeded.
func (d replyDispatch) byLabel(ctx context.Context, label coreapi.ReplyLabel) (bool, error) {
	// Compliance wins over automation: an explicit opt-out inside an automated
	// message is honoured, exactly as in byClass. The accepted trade-off is
	// occasionally suppressing an OOO whose footer says "unsubscribe" —
	// suppression is the fail-safe direction, so we err that way on purpose.
	if label.SuppressesContact || (label.IsAutomated && d.classifier.LooksLikeUnsubscribe(d.in)) {
		if err := d.core.MarkUnsubscribed(ctx, d.send.EnrollmentID, d.workspaceID, d.send.ContactEmail); err != nil {
			return false, err
		}
		return true, nil
	}

	if label.StopsEnrollment {
		if err := d.core.MarkReplied(ctx, d.send.EnrollmentID, d.workspaceID,
			d.result.Class, d.result.Source, d.result.Confidence); err != nil {
			return false, err
		}
		if err := d.captureDeal(ctx, label); err != nil {
			return false, err
		}
		// Only an enrollment reply is an "engaged reply": a legacy direct-send
		// match (EnrollmentID == "") has nothing to stop, so MarkReplied no-ops
		// coreapi-side and it isn't counted.
		if d.send.EnrollmentID != "" {
			*d.replies++
		}
		return true, nil
	}

	// Non-stopping label (the automated family): the enrollment stays ACTIVE.
	// A deferral is the only extra thing that may happen here, and only when the
	// sender actually stated a return date we could parse.
	deferred, err := d.deferUntilReturn(ctx, label)
	if err != nil {
		return false, err
	}
	if err := d.captureDeal(ctx, label); err != nil {
		return false, err
	}
	if err := d.core.RecordReplyClass(ctx, d.send.EnrollmentID, d.workspaceID,
		d.result.Class, d.result.Source, d.result.Confidence); err != nil {
		return false, err
	}
	// An un-deferred automated reply is not an engaged reply — reported as
	// skipped, as before. A deferral IS an action taken on the enrollment, so it
	// reports handled.
	return deferred, nil
}

// byClass is the pre-taxonomy dispatch, kept verbatim as the fallback for a
// classified key no label claims: a custom label deleted after the fact, a
// workspace whose seed has not run, or a core with no ReplyLabelClient (a worker
// fake). An unresolvable key must degrade to what this repo did before the
// taxonomy existed, never to "no automation".
func (d replyDispatch) byClass(ctx context.Context) (bool, error) {
	switch {
	case replyclassify.IsAutomated(d.result.Class):
		if d.classifier.LooksLikeUnsubscribe(d.in) {
			if err := d.core.MarkUnsubscribed(ctx, d.send.EnrollmentID, d.workspaceID, d.send.ContactEmail); err != nil {
				return false, err
			}
			return true, nil
		}
		// auto_reply / out_of_office: tag but keep the enrollment ACTIVE (the
		// OOO-trap fix). Not an engaged reply → reported as skipped.
		if err := d.core.RecordReplyClass(ctx, d.send.EnrollmentID, d.workspaceID,
			d.result.Class, d.result.Source, d.result.Confidence); err != nil {
			return false, err
		}
		return false, nil
	case d.result.Class == replyclassify.ClassUnsubscribe:
		// Reply-based opt-out: suppress the address (compliance) + stop.
		if err := d.core.MarkUnsubscribed(ctx, d.send.EnrollmentID, d.workspaceID, d.send.ContactEmail); err != nil {
			return false, err
		}
		return true, nil
	default:
		// positive / negative / neutral / unknown: stop, tagged.
		if err := d.core.MarkReplied(ctx, d.send.EnrollmentID, d.workspaceID,
			d.result.Class, d.result.Source, d.result.Confidence); err != nil {
			return false, err
		}
		if d.result.Class == replyclassify.ClassPositive {
			if err := d.captureDeal(ctx, coreapi.ReplyLabel{CapturesDeal: true}); err != nil {
				return false, err
			}
		}
		if d.send.EnrollmentID != "" {
			*d.replies++
		}
		return true, nil
	}
}

// captureDeal opens (or idempotently re-touches) the CRM deal behind this reply
// when the label says the reply captures one. A no-op for a label that doesn't,
// for a legacy direct-send match with no enrollment, and for a core that does
// not implement the optional CRM capability.
//
// The capture itself is idempotent CRM-side (ON CONFLICT on
// (workspace_id, source_thread_ref, primary_contact_id)), which is what lets it
// be called from the redelivered poll without a second capture path.
func (d replyDispatch) captureDeal(ctx context.Context, label coreapi.ReplyLabel) error {
	if !label.CapturesDeal || d.send.EnrollmentID == "" {
		return nil
	}
	capture, ok := d.core.(coreapi.CRMCaptureClient)
	if !ok {
		return nil
	}
	senderEmail, senderName := d.send.ContactEmail, ""
	if address, err := netmail.ParseAddress(d.msg.Header.Get("From")); err == nil {
		senderEmail, senderName = address.Address, address.Name
	}
	threadRef := strings.TrimSpace(d.msg.Header.Get("In-Reply-To"))
	if threadRef == "" {
		if refs := strings.Fields(d.msg.Header.Get("References")); len(refs) > 0 {
			threadRef = refs[0]
		}
	}
	return capture.CaptureCRMReply(ctx, coreapi.CRMReplyInput{
		WorkspaceID: d.workspaceID, EnrollmentID: d.send.EnrollmentID, SendID: d.send.SendID,
		ThreadRef: threadRef, MessageID: strings.TrimSpace(d.msg.Header.Get("Message-ID")),
		Subject: d.msg.Header.Get("Subject"), SenderEmail: strings.ToLower(senderEmail),
		RecipientEmail: strings.ToLower(d.msg.Header.Get("To")), SenderDisplayName: senderName,
		ReplyClass: d.result.Class, OccurredAt: d.occurredAt(),
	})
}

// deferUntilReturn pushes the enrollment's next step past a return date stated
// in the body, and reports whether it did.
//
// Parse failure is NOT an error and NOT a deferral: the caller falls through to
// tag-only, which is what this repo did before the taxonomy. Guessing a date
// would either send into the absence anyway or park a live sequence, both worse
// than doing nothing.
func (d replyDispatch) deferUntilReturn(ctx context.Context, label coreapi.ReplyLabel) (bool, error) {
	if !label.DefersEnrollment || d.send.EnrollmentID == "" {
		return false, nil
	}
	body, _, _ := mime.Extract(d.msg.ContentType, d.msg.Body) // Extract never errors per its own contract
	if strings.TrimSpace(body) == "" {
		body = string(d.msg.Body)
	}
	until, ok := parseReturnDate(body, time.Now().UTC())
	if !ok {
		return false, nil
	}
	if err := d.core.DeferEnrollment(ctx, d.send.EnrollmentID, d.workspaceID, until); err != nil {
		return false, err
	}
	slog.InfoContext(ctx, "inbox_poll_enrollment_deferred",
		"workspace_id", d.workspaceID, "enrollment_id", d.send.EnrollmentID,
		"reply_class", d.result.Class, "until", until)
	return true, nil
}

// occurredAt is the reply's own Date header, falling back to now when the
// sender omitted or malformed it.
func (d replyDispatch) occurredAt() time.Time {
	if headerDate, err := d.msg.Header.Date(); err == nil {
		return headerDate.UTC()
	}
	return time.Now().UTC()
}

// resolveReplyLabel looks up the workspace's label for a classified key.
//
// It returns ok=false — meaning "use byClass" — for all three ways the taxonomy
// can be unavailable: a core without the optional capability, a key no label
// claims, and a failed lookup. A lookup failure must not drop the reply on the
// floor, so it is logged rather than returned: the pre-taxonomy path still stops
// the enrollment and still honours an unsubscribe.
func resolveReplyLabel(ctx context.Context, core coreapi.Client, workspaceID, key string) (coreapi.ReplyLabel, bool) {
	resolver, ok := core.(coreapi.ReplyLabelClient)
	if !ok {
		return coreapi.ReplyLabel{}, false
	}
	label, found, err := resolver.ResolveReplyLabel(ctx, workspaceID, key)
	if err != nil {
		slog.WarnContext(ctx, "reply_label_resolve_failed",
			"workspace_id", workspaceID, "reply_class", key, "err", err)
		return coreapi.ReplyLabel{}, false
	}
	return label, found
}
