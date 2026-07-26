package inprocess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/app/enrollment"
	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/unsub"
)

// stepSendIDNamespace is a fixed namespace for deriving each step-send's id
// deterministically (uuid.NewSHA1) from (campaign, contact, step_order) —
// the same tuple ClaimStepSend's idempotency index is keyed on. A retried or
// raced advance (sweeper vs. the lazy chain) then recomputes the identical
// id, so every copy of a step's tracking tokens (embedded in the email body
// before the send row exists) resolve to the one canonical sends row, and the
// claim's ON CONFLICT lets exactly one of them win delivery. The value itself
// is arbitrary — it only needs to be fixed across process restarts.
var stepSendIDNamespace = uuid.MustParse("6f1b1a1e-6b7a-4c9e-9c1e-9b8e2a7d5f3a")

// claimLeaseSeconds is the delivery-claim lease: how long a 'sending' row is
// considered owned before a crashed worker's claim may be reclaimed. 5 minutes
// — longer than any single send timeout (30s) so a live send is never
// double-claimed, shorter than the 5-min enrollment sweeper so a genuinely
// crashed send is re-driven promptly. Both the step and direct claim paths use
// it. A package const (not config) keeps the correctness-critical value pinned;
// the asynq claim is defense in depth over it.
const claimLeaseSeconds int32 = 300

// deriveStepSendID computes the deterministic send id for one (campaign,
// contact, step_order) — see stepSendIDNamespace.
func deriveStepSendID(campaignID, contactID uuid.UUID, stepOrder int) uuid.UUID {
	key := campaignID.String() + "|" + contactID.String() + "|" + strconv.Itoa(stepOrder)
	return uuid.NewSHA1(stepSendIDNamespace, []byte(key))
}

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

	// Derived now, before the step is sent, so the worker can embed it in
	// tracking tokens at MIME-build time; ClaimStepSend inserts it as the send
	// row's id (an explicit id rather than the column default) so events recorded
	// against it line up with that row. Deterministic (not uuid.New()) so a
	// retried/raced advance for the same step recomputes the same id — see
	// stepSendIDNamespace.
	sendID := deriveStepSendID(b.CampaignID, b.ContactID, nextOrder)

	// Transport dispatch on the mailbox provider (see GetSendJob): API providers
	// (gmail, m365) return a refreshed short-lived access token and no password
	// (the provider's oauth2 config selects the refresh endpoint); smtp unseals
	// the stored password unchanged.
	var accessToken, password []byte
	if b.Provider == "gmail" || b.Provider == "m365" {
		at, err := c.oauthAccessToken(ctx, b.Provider, b.MailboxID, ws, b.SecretCiphertext, c.oauthConfigFor(b.Provider))
		if err != nil {
			return coreapi.StepSendJob{}, err
		}
		accessToken = []byte(at)
	} else {
		sealer, serr := c.keyring.SealerFor(ctx, ws)
		if serr != nil {
			return coreapi.StepSendJob{}, serr
		}
		password, err = sealer.Open(b.SecretCiphertext)
		if err != nil {
			return coreapi.StepSendJob{}, err
		}
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
	dailyCap := effectiveCap(int(b.DailyCap), int(b.RampStartCap), int(b.RampDays), b.RampEnabled, ageDays)
	token := unsub.MakeToken(c.jwtSecret, ws.String(), b.ToEmail)

	return coreapi.StepSendJob{
		EnrollmentID: enrollmentID, WorkspaceID: ws.String(),
		CampaignID: b.CampaignID.String(), ContactID: b.ContactID.String(), MailboxID: b.MailboxID.String(),
		SendID:      sendID.String(),
		CurrentStep: int(b.CurrentStep), StepOrder: nextOrder, NextDelaySeconds: nextDelay, LastStep: lastStep,
		Suppressed: suppressed, EffectiveDailyCap: dailyCap, SentToday: int(sentToday),
		ToEmail: b.ToEmail,
		Vars: coreapi.ContactVars{
			FirstName: b.FirstName, LastName: b.LastName, Email: b.ToEmail,
			Company: b.Company, Custom: decodeCustom(b.CustomFields),
		},
		Subject: replySubject(nextOrder, step.Subject, threadSubject), ThreadSubject: threadSubject,
		BodyText: step.BodyText, BodyHTML: step.BodyHtml, TrackingEnabled: b.TrackingEnabled,
		UnsubURL: c.publicURL + "/u/" + token, InReplyTo: inReplyTo, References: references,
		FromEmail: b.FromEmail, FromName: b.FromName,
		Provider: b.Provider, AccessToken: accessToken,
		SMTPHost: b.SmtpHost, SMTPPort: int(b.SmtpPort),
		SMTPUsername: b.SmtpUsername, SMTPPassword: password, AllowPlaintext: b.AllowPlaintext,
	}, nil
}

// ClaimStepSend claims one step-send for delivery (claim-before-send). The
// deterministic SendID (derived in GetStepSendJob, embedded in the tracking
// tokens) is the sends row id: a fresh INSERT wins the claim, and only a STALE
// 'sending' lease is reclaimed on conflict. workspace_id is pinned on the insert
// value and the reclaim WHERE, so a cross-tenant id claims zero rows.
//
// On a LOST claim (ON CONFLICT matched a non-stale row → the underlying query
// returns pgx.ErrNoRows) it does a workspace-pinned status lookup so the caller
// can RECOVER FORWARD: a 'sent' row means this exact step already delivered (a
// prior run's MarkStepDelivered committed but its cursor advance did not), so we
// must advance the cursor WITHOUT re-sending — returned as ClaimAlreadySent. Any
// other lost-claim state (a fresh 'sending' owned by another worker, or a
// terminal 'failed'/'skipped') returns ClaimSkip. The lookup happening a moment
// after the claim is benign: worst case we return ClaimSkip on a row that just
// became 'sent' (the sweeper/retry re-drives it) or ClaimAlreadySent on one that
// just became 'sent' by another worker (the cursor advance is idempotent).
func (c client) ClaimStepSend(ctx context.Context, job coreapi.StepSendJob) (coreapi.ClaimOutcome, error) {
	ws, err := uuid.Parse(job.WorkspaceID)
	if err != nil {
		return coreapi.ClaimSkip, err
	}
	campaignID, err := uuid.Parse(job.CampaignID)
	if err != nil {
		return coreapi.ClaimSkip, err
	}
	contactID, err := uuid.Parse(job.ContactID)
	if err != nil {
		return coreapi.ClaimSkip, err
	}
	mailboxID, err := uuid.Parse(job.MailboxID)
	if err != nil {
		return coreapi.ClaimSkip, err
	}
	sendID, err := uuid.Parse(job.SendID)
	if err != nil {
		return coreapi.ClaimSkip, err
	}
	if _, err := c.q.ClaimStepSend(ctx, gen.ClaimStepSendParams{
		ID:          sendID,
		WorkspaceID: ws, CampaignID: campaignID, ContactID: contactID, MailboxID: mailboxID,
		ToEmail: job.ToEmail, StepOrder: int32(job.StepOrder), ReferencesHeader: job.References,
		LeaseSeconds: claimLeaseSeconds,
	}); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return coreapi.ClaimSkip, err
		}
		// Lost the claim: learn the row's state to decide skip vs recover-forward.
		st, serr := c.q.GetSendState(ctx, gen.GetSendStateParams{ID: sendID, WorkspaceID: ws})
		if serr != nil {
			if errors.Is(serr, pgx.ErrNoRows) {
				// Not visible to this workspace (cross-tenant / vanished): skip.
				return coreapi.ClaimSkip, nil
			}
			return coreapi.ClaimSkip, serr
		}
		if st.Status == "sent" {
			return coreapi.ClaimAlreadySent, nil
		}
		return coreapi.ClaimSkip, nil
	}
	return coreapi.ClaimWon, nil
}

// MarkStepDelivered commits the step-send's successful delivery in its OWN
// statement — UPDATE sends SET status='sent', message_id, sent_at=now() — so the
// delivery becomes durable independently of the cursor advance. It reuses
// SetSendResult (the identical UPDATE), workspace-pinned. Called right after Send
// succeeds and BEFORE AdvanceStepCursor.
//
// Residual double-send window (documented; irreducible for non-transactional
// SMTP): if this single UPDATE does not commit after Send returned success —
// whether from a hard crash OR a returned error from the deliver-UPDATE itself
// (a lost connection, statement timeout, etc.) — the row is left 'sending'; after
// the lease expires the sweeper reclaims it and re-sends. This is the ONLY
// remaining window — we shrank it from the pre-fix "whole finalize tx + an
// ordinary asynq retry" down to "one UPDATE failing to commit". Any failure AFTER
// this commit recovers forward via ClaimAlreadySent instead of re-delivering.
func (c client) MarkStepDelivered(ctx context.Context, job coreapi.StepSendJob, messageID string) error {
	ws, err := uuid.Parse(job.WorkspaceID)
	if err != nil {
		return err
	}
	sendID, err := uuid.Parse(job.SendID)
	if err != nil {
		return err
	}
	return c.q.SetSendResult(ctx, gen.SetSendResultParams{
		ID: sendID, Status: "sent", MessageID: messageID, Error: "", WorkspaceID: ws,
	})
}

// AdvanceStepCursor advances the enrollment cursor for the just-delivered step in
// its own committed transaction (SetThreadRoot on step 1 + AdvanceStep/Complete).
// Idempotent: current_step is set absolutely, so a retried recover-forward
// re-advances to the same value. On step 1 it reads the message_id from the
// already-'sent' row (persisted by MarkStepDelivered) to record the thread root,
// so it behaves identically on the normal success path and on recover-forward
// (where there is no fresh send result). workspace_id is pinned on every write.
func (c client) AdvanceStepCursor(ctx context.Context, job coreapi.StepSendJob) (coreapi.Advance, error) {
	eid, err := uuid.Parse(job.EnrollmentID)
	if err != nil {
		return coreapi.Advance{}, err
	}
	ws, err := uuid.Parse(job.WorkspaceID)
	if err != nil {
		return coreapi.Advance{}, err
	}
	sendID, err := uuid.Parse(job.SendID)
	if err != nil {
		return coreapi.Advance{}, err
	}
	sentStep := job.StepOrder
	lastStep := job.LastStep

	threadRoot := ""
	if sentStep == 1 {
		st, err := c.q.GetSendState(ctx, gen.GetSendStateParams{ID: sendID, WorkspaceID: ws})
		if err != nil {
			return coreapi.Advance{}, err
		}
		if st.Status == "sent" {
			threadRoot = st.MessageID
		}
	}
	var nextDueAt time.Time
	if !lastStep {
		// Cadence reference point is now; the enrollment stamps last_sent_at=now()
		// in the same transition.
		nextDueAt = time.Now().Add(time.Duration(job.NextDelaySeconds) * time.Second)
	}

	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return coreapi.Advance{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed
	qtx := c.q.WithTx(tx)
	enrollTx := enrollment.NewService(enrollment.NewPgStore(qtx))
	if err := enrollTx.MarkStepSent(ctx, ws, eid, int32(sentStep), nextDueAt, lastStep, threadRoot); err != nil {
		return coreapi.Advance{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return coreapi.Advance{}, err
	}
	return coreapi.Advance{Completed: lastStep, NextDueAt: nextDueAt}, nil
}

// ReleaseStepSend expires the claim's lease after a RETRYABLE send failure so
// the asynq retry reclaims it promptly, without advancing the cursor. Guarded on
// status='sending' (in SQL), workspace-pinned.
func (c client) ReleaseStepSend(ctx context.Context, job coreapi.StepSendJob) error {
	ws, err := uuid.Parse(job.WorkspaceID)
	if err != nil {
		return err
	}
	sendID, err := uuid.Parse(job.SendID)
	if err != nil {
		return err
	}
	return c.q.ReleaseSend(ctx, gen.ReleaseSendParams{ID: sendID, WorkspaceID: ws})
}

// FinalizeStepSend finalizes the already-claimed step-send row to a NON-'sent'
// terminal state (permanent 'failed') and advances the enrollment cursor via the
// enrollment state machine, in ONE transaction (fail-forward). The SUCCESS path
// deliberately does NOT use this — it splits into MarkStepDelivered (durable
// delivery) + AdvanceStepCursor so a delivered row can never be left unrecorded
// by a failed combined tx. Because a 'failed' finalize is atomic with its cursor
// advance, a 'failed' row always has an advanced cursor (nothing to
// recover-forward). It reuses the immutable values GetStepSendJob resolved
// (carried on the job) rather than re-fetching the bundle, next step and
// references. Returns whether the enrollment completed and the next due time.
//
// Tenant safety: cross-tenant reads are rejected in GetStepSendJob (which built
// the job); here workspace_id is pinned on every write (the send UPDATE's WHERE
// and each enrollment UPDATE's WHERE), so a mismatch cannot touch another
// tenant's rows.
func (c client) FinalizeStepSend(ctx context.Context, job coreapi.StepSendJob, res coreapi.StepResult) (coreapi.Advance, error) {
	eid, err := uuid.Parse(job.EnrollmentID)
	if err != nil {
		return coreapi.Advance{}, err
	}
	ws, err := uuid.Parse(job.WorkspaceID)
	if err != nil {
		return coreapi.Advance{}, err
	}
	sendID, err := uuid.Parse(job.SendID)
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

	// One transaction (spec §6): the send-row finalize and the cursor advance
	// commit together so a crash between them can't leave a finalized send with a
	// stale cursor, or an advanced cursor with an unfinalized send row.
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return coreapi.Advance{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed
	qtx := c.q.WithTx(tx)

	// The row already exists (this worker claimed it 'sending'); finalize it to
	// its terminal state. workspace_id pinned in the UPDATE WHERE.
	if err := qtx.SetSendResult(ctx, gen.SetSendResultParams{
		ID:          sendID,
		Status:      res.Status,
		MessageID:   res.MessageID,
		Error:       res.Err,
		WorkspaceID: ws,
	}); err != nil {
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
