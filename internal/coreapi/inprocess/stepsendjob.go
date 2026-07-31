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
	"github.com/inroad/inroad/internal/platform/cadence"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/unsub"
)

// loadSchedule reads the campaign's send windows and compiles them with its
// timezone, returning the validated schedule. An unconfigured campaign resolves
// to the default business-hours window (see cadence.ScheduleFrom); a remaining
// error means an unknown timezone — a binary without tzdata, or a zone removed
// from the IANA database — and is surfaced rather than papered over, since the
// alternative is sending outside the operator's window.
func (c *client) loadSchedule(ctx context.Context, ws, campaignID uuid.UUID, timezone string) (cadence.Schedule, error) {
	rows, err := c.q.ListSendWindows(ctx, gen.ListSendWindowsParams{CampaignID: campaignID, WorkspaceID: ws})
	if err != nil {
		return cadence.Schedule{}, err
	}
	windows := make([]cadence.SendWindow, len(rows))
	for i, r := range rows {
		windows[i] = cadence.SendWindow{
			Weekday: int(r.Weekday), StartMinute: int(r.StartMinute), EndMinute: int(r.EndMinute),
		}
	}
	// ScheduleFrom substitutes the default when a campaign has no window rows —
	// created by a path that doesn't seed them — instead of failing every send it
	// will ever make.
	sched := cadence.ScheduleFrom(timezone, windows)
	if _, err := sched.Compile(); err != nil {
		return cadence.Schedule{}, fmt.Errorf("campaign %s schedule: %w", campaignID, err)
	}
	return sched, nil
}

// nextStepDueAt places the following step's send inside the campaign's window.
// The step delay sets the EARLIEST the next send may go out (measured from this
// send, the cadence reference point); the window decides the actual instant, and
// humanization keeps it off the clock grid.
//
// Keyed on the enrollment id so a retried MarkStepSent recomputes the identical
// instant — the stamped next_due_at and the enqueued advance must not drift, or
// the sweeper fires the step twice. Returns the zero time for a last step (there
// is nothing to schedule).
func nextStepDueAt(job coreapi.StepSendJob, sentAt time.Time) (time.Time, error) {
	if job.LastStep {
		return time.Time{}, nil
	}
	win, err := job.Schedule.Compile()
	if err != nil {
		return time.Time{}, fmt.Errorf("enrollment %s next step: %w", job.EnrollmentID, err)
	}
	earliest := sentAt.Add(time.Duration(job.NextDelaySeconds) * time.Second)
	return win.Next(earliest, job.EnrollmentID)
}

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

// campaignLimitReached reports whether the campaign has already used up
// campaigns.daily_limit for the UTC day. A nil limit is "no campaign limit" —
// today's behaviour for every campaign that has not set one — and skips the count
// entirely, so an unlimited campaign pays nothing for this feature. A non-positive
// stored limit can only come from a direct write (the column CHECKs > 0 and the API
// rejects below 1) and is treated as unset rather than as a campaign that may never
// send.
//
// The limit is a campaign-wide total across the whole sender pool, which is what an
// operator means by "this campaign sends at most 100/day". It can only ever lower
// throughput: it is a second gate, never a raise of any mailbox's own cap.
func (c client) campaignLimitReached(ctx context.Context, ws, campaignID uuid.UUID, limit *int32) (bool, error) {
	if limit == nil || *limit <= 0 {
		return false, nil
	}
	sent, err := c.q.CountCampaignSentToday(ctx, gen.CountCampaignSentTodayParams{
		CampaignID: campaignID, WorkspaceID: ws,
	})
	if err != nil {
		return false, err
	}
	return sent >= int64(*limit), nil
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
	// The thread lost its mailbox (deleted mid-sequence, see threadLostItsMailbox).
	// Re-resolving would send the follow-up "Re:" from a DIFFERENT address,
	// carrying In-Reply-To/References for a Message-ID that address never sent
	// while thread_root_id still points at the dead thread — broken for the
	// recipient and a spam signal. Stop instead, returning BEFORE a sender is
	// resolved or any credential is opened, so nothing is sent and no mailbox is
	// pinned.
	if threadLostItsMailbox(b.CurrentStep, b.EnrollmentMailboxID) {
		return coreapi.StepSendJob{
			EnrollmentID: enrollmentID, WorkspaceID: ws.String(), MailboxRemoved: true,
		}, nil
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

	// The campaign-wide daily limit, stacked on top of the per-mailbox caps: the
	// mailbox gate protects the mailbox, this one protects the campaign, and neither
	// can raise the other. Checked here — after we know a step is actually due, but
	// BEFORE a mailbox is pinned or a credential is unsealed — so a limited campaign
	// neither decrypts secrets it will not use nor bumps a pool member's rotation
	// counters for a send that does not happen.
	limited, err := c.campaignLimitReached(ctx, ws, b.CampaignID, b.DailyLimit)
	if err != nil {
		return coreapi.StepSendJob{}, err
	}
	if limited {
		// The schedule travels even though no sender is resolved: a deferred retry
		// SENDS as soon as it runs, so the worker has to wake inside the campaign's
		// window. Without it, a limited campaign resumes at whatever instant the
		// backoff lands on — for a UTC-midnight retry, 20:00 in New York.
		sched, serr := c.loadSchedule(ctx, ws, b.CampaignID, b.Timezone)
		if serr != nil {
			return coreapi.StepSendJob{}, serr
		}
		return coreapi.StepSendJob{
			EnrollmentID: enrollmentID, WorkspaceID: ws.String(), CampaignLimited: true,
			Schedule: sched,
		}, nil
	}

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

	// Which mailbox this step sends from: the enrollment's pinned mailbox, a pool
	// selection claimed and pinned now, or campaigns.mailbox_id for a campaign with
	// no pool. Resolved BEFORE the transport is loaded, so the credentials opened
	// below belong to the RESOLVED mailbox rather than the campaign's.
	sender, err := c.resolveSender(ctx, ws, eid, b)
	if err != nil {
		return coreapi.StepSendJob{}, err
	}

	// Transport dispatch on the mailbox provider (see GetSendJob): API providers
	// (gmail, m365) return a refreshed short-lived access token and no password
	// (the provider's oauth2 config selects the refresh endpoint); smtp unseals
	// the stored password unchanged.
	var accessToken, password []byte
	if sender.provider == "gmail" || sender.provider == "m365" {
		at, err := c.oauthAccessToken(ctx, sender.provider, sender.mailboxID, ws, sender.secretCiphertext, c.oauthConfigFor(sender.provider))
		if err != nil {
			return coreapi.StepSendJob{}, err
		}
		accessToken = []byte(at)
	} else {
		sealer, serr := c.keyring.SealerFor(ctx, ws)
		if serr != nil {
			return coreapi.StepSendJob{}, serr
		}
		password, err = sealer.Open(sender.secretCiphertext)
		if err != nil {
			return coreapi.StepSendJob{}, err
		}
	}
	suppressed, err := c.q.IsSuppressed(ctx, gen.IsSuppressedParams{WorkspaceID: ws, Lower: b.ToEmail})
	if err != nil {
		return coreapi.StepSendJob{}, err
	}
	token := unsub.MakeToken(c.jwtSecret, ws.String(), b.ToEmail)

	// Load and validate the sending schedule BEFORE the send: MarkStepSent needs
	// it to place the next step, and compiling it here means a corrupted schedule
	// stops the send instead of surfacing after the message has already gone out.
	sched, err := c.loadSchedule(ctx, ws, b.CampaignID, b.Timezone)
	if err != nil {
		return coreapi.StepSendJob{}, err
	}

	return coreapi.StepSendJob{
		EnrollmentID: enrollmentID, WorkspaceID: ws.String(),
		CampaignID: b.CampaignID.String(), ContactID: b.ContactID.String(), MailboxID: sender.mailboxID.String(),
		SendID:      sendID.String(),
		CurrentStep: int(b.CurrentStep), StepOrder: nextOrder, NextDelaySeconds: nextDelay, LastStep: lastStep,
		Suppressed: suppressed, HealthPaused: sender.healthPaused,
		EffectiveDailyCap: sender.effectiveCap, SentToday: sender.sentToday,
		MinIntervalSeconds: int(sender.minIntervalSeconds),
		ToEmail:            b.ToEmail,
		Vars: coreapi.ContactVars{
			FirstName: b.FirstName, LastName: b.LastName, Email: b.ToEmail,
			Company: b.Company, Custom: decodeCustom(b.CustomFields),
		},
		Subject: replySubject(nextOrder, step.Subject, threadSubject), ThreadSubject: threadSubject,
		BodyText: step.BodyText, BodyHTML: step.BodyHtml, TrackingEnabled: b.TrackingEnabled,
		Schedule: sched,
		UnsubURL: c.publicURL + "/u/" + token, InReplyTo: inReplyTo, References: references,
		FromEmail: sender.fromEmail, FromName: sender.fromName,
		Provider: sender.provider, AccessToken: accessToken,
		SMTPHost: sender.smtpHost, SMTPPort: int(sender.smtpPort),
		SMTPUsername: sender.smtpUsername, SMTPPassword: password, AllowPlaintext: sender.allowPlaintext,
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
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return coreapi.ClaimSkip, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := c.q.WithTx(tx)

	if _, err := qtx.ClaimStepSend(ctx, gen.ClaimStepSendParams{
		ID:          sendID,
		WorkspaceID: ws, CampaignID: campaignID, ContactID: contactID, MailboxID: mailboxID,
		ToEmail: job.ToEmail, StepOrder: int32(job.StepOrder), ReferencesHeader: job.References,
		LeaseSeconds: claimLeaseSeconds,
	}); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return coreapi.ClaimSkip, err
		}
		_ = tx.Rollback(ctx)
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
	if _, err := qtx.ReserveMailboxSendSlot(ctx, gen.ReserveMailboxSendSlotParams{
		ID: mailboxID, WorkspaceID: ws,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return coreapi.ClaimDeferred, nil
		}
		return coreapi.ClaimSkip, err
	}
	if err := tx.Commit(ctx); err != nil {
		return coreapi.ClaimSkip, err
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
	// Cadence reference point is now; the enrollment stamps last_sent_at=now() in
	// the same transition. The step delay is the earliest — the campaign's send
	// window decides the instant.
	nextDueAt, err := nextStepDueAt(job, time.Now())
	if err != nil {
		return coreapi.Advance{}, err
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
	// Cadence reference point is the send time (now); the enrollment stamps
	// last_sent_at=now() in the same transition. The step delay is the earliest —
	// the campaign's send window decides the instant.
	nextDueAt, err := nextStepDueAt(job, time.Now())
	if err != nil {
		return coreapi.Advance{}, err
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
