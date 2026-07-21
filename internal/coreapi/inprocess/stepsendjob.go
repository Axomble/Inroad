package inprocess

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/enrollment"
	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/unsub"
)

// replySubject synthesizes the subject line for a step. Step 1 uses its subject
// verbatim; from step 2 the subject is prefixed with "Re: " (idempotently) so
// the message reads as a follow-up in the thread. Clients thread on the
// In-Reply-To/References headers, so this is cosmetic but expected.
func replySubject(order int, stepSubject string) string {
	if order <= 1 || strings.HasPrefix(stepSubject, "Re: ") {
		return stepSubject
	}
	return "Re: " + stepSubject
}

// decodeCustom turns the contact's custom_fields JSONB into the string map the
// personalizer consumes. Non-string values are stringified; a decode failure
// yields nil (unknown {{custom.*}} placeholders then resolve to empty).
func decodeCustom(b []byte) map[string]string {
	if len(b) == 0 {
		return nil
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		switch t := v.(type) {
		case string:
			out[k] = t
		case nil:
			out[k] = ""
		default:
			out[k] = fmt.Sprintf("%v", t)
		}
	}
	return out
}

// threading computes the In-Reply-To / References headers for the step about to
// send. Empty for step 1. For later steps it prefers the immediately-preceding
// sent message (proper chain), falling back to the stored thread root.
func (c client) threading(ctx context.Context, order int, campaignID, contactID uuid.UUID, threadRootID string) (inReplyTo, references string) {
	if order <= 1 {
		return "", ""
	}
	prior, err := c.q.LatestSentForContact(ctx, gen.LatestSentForContactParams{CampaignID: campaignID, ContactID: contactID})
	if err == nil && prior.MessageID != "" {
		return prior.MessageID, strings.TrimSpace(prior.ReferencesHeader + " " + prior.MessageID)
	}
	if threadRootID != "" {
		return threadRootID, threadRootID
	}
	return "", ""
}

// GetStepSendJob resolves the enrollment's next due step and builds the send
// job. Read-only: creates no rows. workspaceID is pinned in the SQL WHERE
// (defense in depth on the unguessable enrollment UUID).
func (c client) GetStepSendJob(ctx context.Context, enrollmentID, workspaceID string) (coreapi.StepSendJob, error) {
	eid, err := uuid.Parse(enrollmentID)
	if err != nil {
		return coreapi.StepSendJob{}, err
	}
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return coreapi.StepSendJob{}, err
	}
	b, err := c.q.GetStepEnrollmentBundle(ctx, gen.GetStepEnrollmentBundleParams{ID: eid, WorkspaceID: ws})
	if err != nil {
		return coreapi.StepSendJob{}, err
	}
	if b.WorkspaceID != ws {
		return coreapi.StepSendJob{}, coreapi.ErrCrossTenant
	}
	// Not active (already stopped/completed) → nothing to do.
	if b.Status != string(enrollment.StatusActive) {
		return coreapi.StepSendJob{Skip: true}, nil
	}

	nextOrder := int(b.CurrentStep) + 1
	step, err := c.q.GetStepByOrder(ctx, gen.GetStepByOrderParams{
		CampaignID: b.CampaignID, WorkspaceID: ws, StepOrder: int32(nextOrder),
	})
	if err != nil {
		// No such step (cursor already at/after the last step). Treat as done;
		// the worker no-ops and MarkStepSent isn't called.
		return coreapi.StepSendJob{Skip: true}, nil
	}
	maxOrder, err := c.q.MaxStepOrder(ctx, gen.MaxStepOrderParams{CampaignID: b.CampaignID, WorkspaceID: ws})
	if err != nil {
		return coreapi.StepSendJob{}, err
	}

	inReplyTo, references := c.threading(ctx, nextOrder, b.CampaignID, b.ContactID, b.ThreadRootID)

	password, err := c.sealer.Open(b.SecretCiphertext)
	if err != nil {
		return coreapi.StepSendJob{}, err
	}
	suppressed, err := c.q.IsSuppressed(ctx, gen.IsSuppressedParams{WorkspaceID: ws, Lower: b.ToEmail})
	if err != nil {
		return coreapi.StepSendJob{}, err
	}
	sentToday, err := c.q.CountSentToday(ctx, b.MailboxID)
	if err != nil {
		return coreapi.StepSendJob{}, err
	}
	ageDays := int(time.Since(b.MailboxCreatedAt.Time).Hours() / 24)
	cap := effectiveCap(int(b.DailyCap), int(b.RampStartCap), int(b.RampDays), b.RampEnabled, ageDays)
	token := unsub.MakeToken(c.jwtSecret, ws.String(), b.ToEmail)

	return coreapi.StepSendJob{
		EnrollmentID: enrollmentID, WorkspaceID: ws.String(),
		StepOrder: nextOrder, LastStep: nextOrder >= int(maxOrder),
		Suppressed: suppressed, EffectiveDailyCap: cap, SentToday: int(sentToday),
		ToEmail: b.ToEmail,
		Vars: coreapi.ContactVars{
			FirstName: b.FirstName, LastName: b.LastName, Email: b.ToEmail,
			Company: b.Company, Custom: decodeCustom(b.CustomFields),
		},
		Subject: replySubject(nextOrder, step.Subject), BodyText: step.BodyText, BodyHTML: step.BodyHtml,
		UnsubURL: c.publicURL + "/u/" + token, InReplyTo: inReplyTo, References: references,
		FromEmail: b.FromEmail, FromName: b.FromName, SMTPHost: b.SmtpHost, SMTPPort: int(b.SmtpPort),
		SMTPUsername: b.SmtpUsername, SMTPPassword: password, UseTLS: b.UseTls,
	}, nil
}

// MarkStepSent records the step send (one sends row, with result) and advances
// the enrollment cursor via the enrollment state machine. Returns whether the
// enrollment completed and the next due time.
func (c client) MarkStepSent(ctx context.Context, enrollmentID, workspaceID string, res coreapi.StepResult) (coreapi.Advance, error) {
	eid, err := uuid.Parse(enrollmentID)
	if err != nil {
		return coreapi.Advance{}, err
	}
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return coreapi.Advance{}, err
	}
	b, err := c.q.GetStepEnrollmentBundle(ctx, gen.GetStepEnrollmentBundleParams{ID: eid, WorkspaceID: ws})
	if err != nil {
		return coreapi.Advance{}, err
	}
	if b.WorkspaceID != ws {
		return coreapi.Advance{}, coreapi.ErrCrossTenant
	}
	sentStep := int(b.CurrentStep) + 1
	maxOrder, err := c.q.MaxStepOrder(ctx, gen.MaxStepOrderParams{CampaignID: b.CampaignID, WorkspaceID: ws})
	if err != nil {
		return coreapi.Advance{}, err
	}
	lastStep := sentStep >= int(maxOrder)

	_, references := c.threading(ctx, sentStep, b.CampaignID, b.ContactID, b.ThreadRootID)

	if _, err := c.q.RecordStepSend(ctx, gen.RecordStepSendParams{
		WorkspaceID: ws, CampaignID: b.CampaignID, ContactID: b.ContactID, MailboxID: b.MailboxID,
		ToEmail: b.ToEmail, StepOrder: int32(sentStep), ReferencesHeader: references,
		Status: res.Status, MessageID: res.MessageID, Error: res.Err,
	}); err != nil {
		return coreapi.Advance{}, err
	}

	// Step 1's Message-ID becomes the thread root for the References chain.
	threadRoot := ""
	if sentStep == 1 && res.Status == "sent" {
		threadRoot = res.MessageID
	}
	var nextDueAt time.Time
	if !lastStep {
		next, nerr := c.q.GetStepByOrder(ctx, gen.GetStepByOrderParams{
			CampaignID: b.CampaignID, WorkspaceID: ws, StepOrder: int32(sentStep + 1),
		})
		delay := int32(0)
		if nerr == nil {
			delay = next.DelaySeconds
		}
		// Cadence reference point is the send time (now); the enrollment stamps
		// last_sent_at=now() in the same transition.
		nextDueAt = time.Now().Add(time.Duration(delay) * time.Second)
	}

	if err := c.enroll.MarkStepSent(ctx, ws, eid, int32(sentStep), nextDueAt, lastStep, threadRoot); err != nil {
		return coreapi.Advance{}, err
	}
	return coreapi.Advance{Completed: lastStep, NextDueAt: nextDueAt}, nil
}

// MarkStepStopped halts an enrollment via the single stop entry point.
func (c client) MarkStepStopped(ctx context.Context, enrollmentID, workspaceID, reason string) error {
	eid, err := uuid.Parse(enrollmentID)
	if err != nil {
		return err
	}
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return err
	}
	return c.enroll.MarkStepStopped(ctx, ws, eid, enrollment.StopReason(reason))
}

// ListDueEnrollments returns active enrollments past their due window for the
// periodic sweeper.
func (c client) ListDueEnrollments(ctx context.Context) ([]coreapi.DueEnrollment, error) {
	rows, err := c.q.ListDueEnrollments(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]coreapi.DueEnrollment, len(rows))
	for i, r := range rows {
		out[i] = coreapi.DueEnrollment{EnrollmentID: r.ID.String(), WorkspaceID: r.WorkspaceID.String()}
	}
	return out, nil
}
