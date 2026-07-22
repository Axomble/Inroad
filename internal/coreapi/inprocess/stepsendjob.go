package inprocess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/app/enrollment"
	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/unsub"
)

// replySubject synthesizes the subject line for a step (spec A5). Step 1 uses
// its own subject verbatim. From step 2, an empty step subject means "reply in
// thread" → "Re: <step-1 subject>" (threadSubject); a non-empty step subject is
// a deliberate new subject and is used verbatim (still threaded via the
// In-Reply-To/References headers). threadSubject is the step-1 raw subject and
// is only consulted for the empty-subject case.
func replySubject(order int, stepSubject, threadSubject string) string {
	if order <= 1 || stepSubject != "" {
		return stepSubject
	}
	return "Re: " + threadSubject
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

	// Resolve the next step by order rather than current_step+1: DeleteStep does
	// not renumber, so orders can have gaps (e.g. {1,3}). GetNextStep skips gaps;
	// ErrNoRows means the cursor is at/after the last step → done.
	step, err := c.q.GetNextStep(ctx, gen.GetNextStepParams{
		CampaignID: b.CampaignID, WorkspaceID: ws, StepOrder: b.CurrentStep,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return coreapi.StepSendJob{Skip: true}, nil
		}
		return coreapi.StepSendJob{}, err
	}
	nextOrder := int(step.StepOrder)

	// Is there a step after this one? Its existence decides last-step; its delay
	// is the cadence gap to the following send. One query answers both.
	after, err := c.q.GetNextStep(ctx, gen.GetNextStepParams{
		CampaignID: b.CampaignID, WorkspaceID: ws, StepOrder: step.StepOrder,
	})
	lastStep := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !lastStep {
		return coreapi.StepSendJob{}, err
	}
	nextDelay := 0
	if !lastStep {
		nextDelay = int(after.DelaySeconds)
	}

	// Thread subject is only needed to build "Re: <step-1 subject>" for a
	// later step that left its own subject empty (spec A5). Step 1 (deleted)
	// missing → leave empty, replySubject then yields a bare "Re: ".
	threadSubject := ""
	if nextOrder > 1 {
		if s1, serr := c.q.GetStepByOrder(ctx, gen.GetStepByOrderParams{
			CampaignID: b.CampaignID, WorkspaceID: ws, StepOrder: 1,
		}); serr == nil {
			threadSubject = s1.Subject
		} else if !errors.Is(serr, pgx.ErrNoRows) {
			return coreapi.StepSendJob{}, serr
		}
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
		CampaignID: b.CampaignID.String(), ContactID: b.ContactID.String(), MailboxID: b.MailboxID.String(),
		CurrentStep: int(b.CurrentStep), StepOrder: nextOrder, NextDelaySeconds: nextDelay, LastStep: lastStep,
		Suppressed: suppressed, EffectiveDailyCap: cap, SentToday: int(sentToday),
		ToEmail: b.ToEmail,
		Vars: coreapi.ContactVars{
			FirstName: b.FirstName, LastName: b.LastName, Email: b.ToEmail,
			Company: b.Company, Custom: decodeCustom(b.CustomFields),
		},
		Subject: replySubject(nextOrder, step.Subject, threadSubject), ThreadSubject: threadSubject,
		BodyText: step.BodyText, BodyHTML: step.BodyHtml,
		UnsubURL: c.publicURL + "/u/" + token, InReplyTo: inReplyTo, References: references,
		FromEmail: b.FromEmail, FromName: b.FromName, SMTPHost: b.SmtpHost, SMTPPort: int(b.SmtpPort),
		SMTPUsername: b.SmtpUsername, SMTPPassword: password, UseTLS: b.UseTls,
	}, nil
}

// MarkStepSent records the step send (one sends row, with result) and advances
// the enrollment cursor via the enrollment state machine, in one transaction.
// It reuses the immutable values GetStepSendJob already resolved (carried on the
// job) rather than re-fetching the bundle, next step and references. Returns
// whether the enrollment completed and the next due time.
//
// Tenant safety: cross-tenant reads are rejected in GetStepSendJob (which built
// the job); here workspace_id is pinned on every write (the send row's
// workspace_id value and each enrollment UPDATE's WHERE), so a mismatch cannot
// touch another tenant's rows.
func (c client) MarkStepSent(ctx context.Context, job coreapi.StepSendJob, res coreapi.StepResult) (coreapi.Advance, error) {
	eid, err := uuid.Parse(job.EnrollmentID)
	if err != nil {
		return coreapi.Advance{}, err
	}
	ws, err := uuid.Parse(job.WorkspaceID)
	if err != nil {
		return coreapi.Advance{}, err
	}
	campaignID, err := uuid.Parse(job.CampaignID)
	if err != nil {
		return coreapi.Advance{}, err
	}
	contactID, err := uuid.Parse(job.ContactID)
	if err != nil {
		return coreapi.Advance{}, err
	}
	mailboxID, err := uuid.Parse(job.MailboxID)
	if err != nil {
		return coreapi.Advance{}, err
	}
	sentStep := job.StepOrder
	lastStep := job.LastStep

	// Step 1's Message-ID becomes the thread root for the References chain.
	threadRoot := ""
	if sentStep == 1 && res.Status == "sent" {
		threadRoot = res.MessageID
	}
	var nextDueAt time.Time
	if !lastStep {
		// Cadence reference point is the send time (now); the enrollment stamps
		// last_sent_at=now() in the same transition.
		nextDueAt = time.Now().Add(time.Duration(job.NextDelaySeconds) * time.Second)
	}

	// One transaction (spec §6): the send row and the cursor advance commit
	// together so a crash between them can't leave a recorded send with a stale
	// cursor, or an advanced cursor with no send row.
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return coreapi.Advance{}, err
	}
	defer tx.Rollback(ctx)
	qtx := c.q.WithTx(tx)

	// ON CONFLICT DO NOTHING → a duplicate advance (sweeper racing the lazy
	// chain) inserts no second row and returns ErrNoRows; treat that as
	// already-recorded and still advance (current_step is set absolutely, so a
	// repeat is idempotent).
	if _, err := qtx.RecordStepSend(ctx, gen.RecordStepSendParams{
		WorkspaceID: ws, CampaignID: campaignID, ContactID: contactID, MailboxID: mailboxID,
		ToEmail: job.ToEmail, StepOrder: int32(sentStep), ReferencesHeader: job.References,
		Status: res.Status, MessageID: res.MessageID, Error: res.Err,
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return coreapi.Advance{}, err
	}

	enrollTx := enrollment.NewService(enrollment.NewPgStore(qtx))
	if err := enrollTx.MarkStepSent(ctx, ws, eid, int32(sentStep), nextDueAt, lastStep, threadRoot); err != nil {
		return coreapi.Advance{}, err
	}
	if err := tx.Commit(ctx); err != nil {
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

// IncrementEnrollmentCapDeferrals bumps the enrollment's cap-deferral counter
// and returns the new value (workspace-pinned).
func (c client) IncrementEnrollmentCapDeferrals(ctx context.Context, enrollmentID, workspaceID string) (int, error) {
	eid, err := uuid.Parse(enrollmentID)
	if err != nil {
		return 0, err
	}
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return 0, err
	}
	n, err := c.q.IncrementEnrollmentCapDeferrals(ctx, gen.IncrementEnrollmentCapDeferralsParams{ID: eid, WorkspaceID: ws})
	return int(n), err
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
