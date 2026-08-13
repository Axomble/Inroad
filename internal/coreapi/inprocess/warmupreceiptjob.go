package inprocess

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// Warmup placement values (spec §4). These exact strings match the
// warmup_receipts.placement CHECK constraint (migration 000018).
const (
	placementInbox = "inbox"
	placementSpam  = "spam"
	placementOther = "other"
)

// validPlacement reports whether p is one of the three allowed placements. The DB
// CHECK enforces this too, but validating at the seam fails loud with a clear
// error instead of a constraint violation deep in a transaction.
func validPlacement(p string) bool {
	return p == placementInbox || p == placementSpam || p == placementOther
}

// warmupReceiptSeed is the stable per-receipt seed the deterministic engage plan
// is built from. Both RecordWarmupReceipt (at insert time) and GetWarmupEngageJob
// (later, from the reloaded row) build it from the SAME (receipt id, recipient,
// received-at UTC day), so the reply decision they derive always agrees — no plan
// state is persisted.
func warmupReceiptSeed(receiptID, recipientMailbox, dayKey string) string {
	return receiptID + ":" + recipientMailbox + ":" + dayKey
}

// engagePlanInputs is the pure snapshot warmupEngagePlan reasons over — the
// receipt's identity, the recipient's configured reply rate, the observed
// placement, and the two instants the delay is computed between. Passed as a
// struct rather than seven positional arguments so a call site can't silently
// transpose two strings.
type engagePlanInputs struct {
	ReceiptID        string
	RecipientMailbox string
	DayKey           string
	ReplyRate        float64
	Placement        string
	ReceivedAt       time.Time // when the message was observed (the delay's anchor)
	Now              time.Time // the delay is returned relative to this
}

// warmupEngagePlan builds the deterministic recipient action set from the receipt.
// It is pure (seeded hash, no rand, no I/O) so it is unit-testable without a DB and
// reproducible across a re-poll: rescue only when the message landed in spam,
// always mark-read, and reply per the recipient's reply_rate via the seeded decision.
//
// EngageAfter is drawn from whichever distribution matches what this engagement will
// actually DO. A passive-only engagement (no reply) uses the short EngageDwell —
// opening and un-spamming a message genuinely is quick. An engagement that WILL reply
// uses the far longer, waking-hours-bounded ReplyEngageAfter, because the whole
// engagement is one asynq task and therefore one sitting: a human opens the message
// and answers it in the same pass, tens of minutes to hours after it arrived. Reusing
// the 90s dwell for replies is what made warmup traffic feel instant.
//
// Warmup has no per-mailbox timezone in the v1 schema, so the waking window is UTC
// (nil loc) — the same convention warmup.NextDue's scheduling already uses.
func warmupEngagePlan(in engagePlanInputs) coreapi.WarmupEngagePlan {
	seed := warmupReceiptSeed(in.ReceiptID, in.RecipientMailbox, in.DayKey)
	doReply := warmup.ReplyDecision(seed, in.ReplyRate)

	engageAfter := warmup.EngageDwell(in.ReceiptID)
	if doReply {
		engageAfter = warmup.ReplyEngageAfter(in.ReceiptID, in.ReceivedAt, in.Now, nil)
	}

	return coreapi.WarmupEngagePlan{
		ReceiptID:   in.ReceiptID,
		DoRescue:    in.Placement == placementSpam,
		DoMarkRead:  true,
		DoReply:     doReply,
		EngageAfter: engageAfter,
	}
}

// warmupPausedUntil is the pause window for a health state after a transition:
// paused halts sending for 72h, throttled for 24h (spec §8); watch/healthy clear
// the window (NULL) so a recovered mailbox can resume. Pure — unit-testable.
func warmupPausedUntil(state string, now time.Time) pgtype.Timestamptz {
	switch state {
	case warmup.StatePaused:
		return pgtype.Timestamptz{Time: now.Add(72 * time.Hour), Valid: true}
	case warmup.StateThrottled:
		return pgtype.Timestamptz{Time: now.Add(24 * time.Hour), Valid: true}
	default:
		return pgtype.Timestamptz{}
	}
}

// RecordWarmupReceipt idempotently records a received warmup message and returns
// the recipient's engagement plan. See the coreapi.Client interface doc. The
// upsert + participant read + both stat writes run in ONE transaction so a NEW
// receipt, its sender-attributed placement, and the plan it derives land atomically:
// a transient participant-read error rolls the whole receipt back and the poller's
// retry re-inserts + re-plans (no silently-dropped engagement). A recipient that is
// NOT a warmup participant is a clean no-op skip — the tx rolls back so NO receipt or
// stat persists, and an empty plan is returned. The plan is built (pure) after commit.
//
// SELF-HEALING: the caller (inbox poller) commits the receipt here, then enqueues
// warmup:engage separately, so an enqueue failure AFTER this commit would strand the
// engagement forever (the retry re-enters here, hits the duplicate, and previously got
// an empty plan). To close that gap, a duplicate that is still UNENGAGED re-returns the
// SAME deterministic plan (rebuilt from the stored row) so the poller re-enqueues; a
// duplicate that is already ENGAGED (C5b ran) returns the empty plan. Re-enqueue is
// safe: the asynq TaskID warmupengage:<receiptID> dedups a still-pending task, and once
// engaged=true this returns empty again, so it can never double-engage.
func (c client) RecordWarmupReceipt(ctx context.Context, in coreapi.WarmupReceiptInput) (coreapi.WarmupEngagePlan, error) {
	ws, err := uuid.Parse(in.WorkspaceID)
	if err != nil {
		return coreapi.WarmupEngagePlan{}, err
	}
	sendID, err := uuid.Parse(in.WarmupSendID)
	if err != nil {
		return coreapi.WarmupEngagePlan{}, err
	}
	recipient, err := uuid.Parse(in.RecipientMailbox)
	if err != nil {
		return coreapi.WarmupEngagePlan{}, err
	}
	if !validPlacement(in.Placement) {
		return coreapi.WarmupEngagePlan{}, fmt.Errorf("coreapi: invalid warmup placement %q", in.Placement)
	}
	sendUUID := pgtype.UUID{Bytes: sendID, Valid: true}

	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return coreapi.WarmupEngagePlan{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed
	qtx := c.q.WithTx(tx)

	row, err := qtx.UpsertWarmupReceipt(ctx, gen.UpsertWarmupReceiptParams{
		WorkspaceID: ws, WarmupSendID: sendID, RecipientMailbox: recipient, Placement: in.Placement,
		SourceFolder: in.SourceFolder, MessageID: in.MessageID,
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return coreapi.WarmupEngagePlan{}, err
		}
		// Zero rows inserted: either a DUPLICATE (conflict) or a cross-tenant
		// recipient (the self-enforcing SELECT was empty). Roll back the empty tx and
		// disambiguate with a workspace-pinned pair lookup: a same-workspace duplicate
		// is found (idempotent no-op → empty plan); a miss means the recipient does
		// not belong to the workspace (fail closed).
		_ = tx.Rollback(ctx)
		dup, gerr := c.q.GetWarmupReceiptByPair(ctx, gen.GetWarmupReceiptByPairParams{
			WarmupSendID: sendUUID, RecipientMailbox: recipient, WorkspaceID: ws,
		})
		if gerr != nil {
			if errors.Is(gerr, pgx.ErrNoRows) {
				_, perr := c.q.GetWarmupParticipant(ctx, gen.GetWarmupParticipantParams{
					MailboxID: recipient, WorkspaceID: ws,
				})
				if perr == nil {
					// A valid-HMAC token presented against a send it was NOT addressed to
					// is the highest-signal indicator of token compromise or replay in the
					// system. Losing it silently — as this did — means an active forgery
					// leaves no trace at all when the write fails.
					if oerr := c.q.RecordWarmupTokenFailureObservation(ctx, gen.RecordWarmupTokenFailureObservationParams{
						WorkspaceID: ws, RecipientMailbox: recipient,
						ReasonCode: "receipt_binding_mismatch",
					}); oerr != nil {
						slog.Warn("warmup_binding_mismatch_evidence_lost",
							"workspace_id", ws.String(), "mailbox_id", recipient.String(),
							"warmup_send_id", sendID.String(), "err", oerr)
					}
					return coreapi.WarmupEngagePlan{}, nil
				}
				if errors.Is(perr, pgx.ErrNoRows) {
					return coreapi.WarmupEngagePlan{}, coreapi.ErrCrossTenant
				}
				return coreapi.WarmupEngagePlan{}, perr
			}
			return coreapi.WarmupEngagePlan{}, gerr
		}
		// Duplicate receipt. Already engaged (C5b ran) → nothing to do, empty plan.
		if dup.Engaged {
			return coreapi.WarmupEngagePlan{}, nil
		}
		// Still unengaged: the original engage task may have been lost to a post-commit
		// enqueue failure. Rebuild the SAME deterministic plan from the stored row so the
		// poller re-enqueues it. The recipient's reply_rate is read with a second
		// workspace-pinned lookup; a recipient that is no longer a participant is the same
		// clean no-op skip the fresh-insert path takes (empty plan → no re-enqueue).
		p, perr := c.q.GetWarmupParticipant(ctx, gen.GetWarmupParticipantParams{MailboxID: recipient, WorkspaceID: ws})
		if perr != nil {
			if errors.Is(perr, pgx.ErrNoRows) {
				return coreapi.WarmupEngagePlan{}, nil
			}
			return coreapi.WarmupEngagePlan{}, perr
		}
		// Rebuild against the STORED placement + received_at so DoRescue/DoReply match
		// what the fresh insert returned and what GetWarmupEngageJob will recompute.
		// EngageAfter is re-derived relative to NOW, so a reply whose original target
		// has already passed fires at the next waking instant instead of waiting the
		// full delay a second time.
		return warmupEngagePlan(engagePlanInputs{
			ReceiptID:        dup.ID.String(),
			RecipientMailbox: recipient.String(),
			DayKey:           dup.ReceivedAt.Time.UTC().Format("2006-01-02"),
			ReplyRate:        float64(p.ReplyRate),
			Placement:        dup.Placement,
			ReceivedAt:       dup.ReceivedAt.Time,
			Now:              c.now().UTC(),
		}), nil
	}

	// New receipt. Read the recipient's participant config INSIDE the tx so plan
	// derivation is atomic with the receipt. A recipient that is NOT a warmup
	// participant is a clean no-op skip: the deferred rollback discards the just-
	// inserted receipt (NO stat is written) and we return an empty plan. A transient
	// read error rolls back too, so the poller's retry re-inserts + re-plans.
	p, err := qtx.GetWarmupParticipant(ctx, gen.GetWarmupParticipantParams{MailboxID: recipient, WorkspaceID: ws})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return coreapi.WarmupEngagePlan{}, nil
		}
		return coreapi.WarmupEngagePlan{}, err
	}

	// Record placement with the CORRECT attribution (spec §4): the recipient's row
	// gets a received-volume bump; the SENDER's row (resolved via
	// warmup_sends.from_mailbox for this send) gets the inbox|spam deliverability
	// signal. Placement belongs to whoever SENT the mail, not who observed it.
	if err := qtx.RecordWarmupReceivedStat(ctx, gen.RecordWarmupReceivedStatParams{
		MailboxID: recipient, WorkspaceID: ws,
	}); err != nil {
		return coreapi.WarmupEngagePlan{}, err
	}
	if err := qtx.RecordWarmupSenderPlacementStat(ctx, gen.RecordWarmupSenderPlacementStatParams{
		WorkspaceID: ws, WarmupSendID: sendID, Placement: in.Placement,
	}); err != nil {
		return coreapi.WarmupEngagePlan{}, err
	}
	if err := qtx.RecordWarmupPlacementObservation(ctx, gen.RecordWarmupPlacementObservationParams{
		WorkspaceID: ws, WarmupSendID: sendID, RecipientMailbox: recipient,
		ReceiptID: row.ID, Placement: in.Placement, ObservedAt: row.ReceivedAt,
	}); err != nil {
		return coreapi.WarmupEngagePlan{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return coreapi.WarmupEngagePlan{}, err
	}

	// Build the deterministic plan (pure). The recipient's reply_rate (read in-tx
	// above) drives the reply decision; the seed is anchored on the just-committed
	// received_at so a later GetWarmupEngageJob reproduces the same decision.
	return warmupEngagePlan(engagePlanInputs{
		ReceiptID:        row.ID.String(),
		RecipientMailbox: recipient.String(),
		DayKey:           row.ReceivedAt.Time.UTC().Format("2006-01-02"),
		ReplyRate:        float64(p.ReplyRate),
		Placement:        in.Placement,
		ReceivedAt:       row.ReceivedAt.Time,
		Now:              c.now().UTC(),
	}), nil
}

// GetWarmupEngageJob loads the transport + reply content for one receipt. See the
// coreapi.Client interface doc. The Do* flags are recomputed deterministically from
// the receipt so they agree with the plan RecordWarmupReceipt returned.
func (c client) GetWarmupEngageJob(ctx context.Context, receiptID, workspaceID string) (coreapi.WarmupEngageJob, error) {
	rid, err := uuid.Parse(receiptID)
	if err != nil {
		return coreapi.WarmupEngageJob{}, err
	}
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return coreapi.WarmupEngageJob{}, err
	}

	b, err := c.q.GetWarmupEngageBundle(ctx, gen.GetWarmupEngageBundleParams{ID: rid, WorkspaceID: ws})
	if err != nil {
		// pgx.ErrNoRows (foreign / vanished receipt) propagates as not-found.
		return coreapi.WarmupEngageJob{}, err
	}

	// Decrypt the recipient's send transport via the SAME keyring/sealer path
	// GetInboxPollJob/GetWarmupSendJob use: API providers refresh a short-lived
	// access token; smtp unseals the stored password. Both are []byte, zeroized by
	// the worker after the reply send.
	var accessToken, password []byte
	if b.Provider == "gmail" || b.Provider == "m365" {
		at, aerr := c.oauthAccessToken(ctx, b.Provider, b.RecipientMailbox, ws, b.SecretCiphertext, c.oauthConfigFor(b.Provider))
		if aerr != nil {
			return coreapi.WarmupEngageJob{}, aerr
		}
		accessToken = []byte(at)
	} else {
		sealer, serr := c.keyring.SealerFor(ctx, ws)
		if serr != nil {
			return coreapi.WarmupEngageJob{}, serr
		}
		password, err = sealer.Open(b.SecretCiphertext)
		if err != nil {
			return coreapi.WarmupEngageJob{}, err
		}
	}

	dayKey := b.ReceivedAt.Time.UTC().Format("2006-01-02")
	// Seed on the CANONICAL uuid (rid.String()), not the raw receiptID argument, so a
	// non-canonical-but-valid UUID string can't flip the reply decision away from the
	// plan RecordWarmupReceipt recorded (which seeded on row.ID.String()).
	seed := warmupReceiptSeed(rid.String(), b.RecipientMailbox.String(), dayKey)
	doRescue := b.Placement == placementSpam
	// Reply only when the seeded decision says so AND the thread still has a turn to
	// send; an exhausted / deleted thread means DoReply resolves to false.
	doReply := warmup.ReplyDecision(seed, float64(b.ReplyRate))

	var reply coreapi.WarmupSendJob
	if doReply {
		built, ok, berr := c.buildWarmupReply(ctx, rid, b.RecipientMailbox, ws)
		if berr != nil {
			return coreapi.WarmupEngageJob{}, berr
		}
		if ok {
			// The reply is a NEW warmup send FROM the recipient, over the RECIPIENT's
			// own transport — the same credentials already decrypted above. Reusing the
			// exact accessToken/password slices means the worker's single deferred
			// zeroize wipes them here and on the engage job together.
			built.Provider = b.Provider
			built.AccessToken = accessToken
			built.SMTPHost = b.SmtpHost
			built.SMTPPort = int(b.SmtpPort)
			built.SMTPUsername = b.SmtpUsername
			built.SMTPPassword = password
			built.AllowPlaintext = b.AllowPlaintext
			reply = built
		} else {
			doReply = false
		}
	}

	return coreapi.WarmupEngageJob{
		Provider:       b.Provider,
		AccessToken:    accessToken,
		IMAPHost:       b.ImapHost,
		IMAPPort:       int(b.ImapPort),
		IMAPUsername:   b.ImapUsername,
		SMTPHost:       b.SmtpHost,
		SMTPPort:       int(b.SmtpPort),
		SMTPUsername:   b.SmtpUsername,
		SMTPPassword:   password,
		AllowPlaintext: b.AllowPlaintext,
		SourceFolder:   b.SourceFolder,
		MessageID:      b.MessageID,
		DoRescue:       doRescue,
		DoMarkRead:     true,
		DoReply:        doReply,
		ReplySend:      reply,
	}, nil
}

// buildWarmupReply resolves the next library turn for a receipt's thread and builds
// the reply as a NEW warmup send FROM the recipient BACK TO the original sender,
// minting a fresh signed receipt token for it. ok is false when the thread has no
// remaining turn (exhausted) or is gone (send SET NULL) — the caller then builds no
// reply. The token embeds a deterministic reply send id derived from the IMMUTABLE
// receipt id via deriveWarmupReplySendID — a DISTINCT id namespace from the normal-send
// derivation, so a reply can never collide with a normal due-send (which would let one
// silently no-op the other at claim time). Anchoring on the receipt (not the recipient's
// mutable sent-today index) makes a post-send engage retry re-derive the SAME id and
// reclaim the existing 'sent' row (recover-forward) rather than re-send. The returned job
// carries everything the claim/send/finalize path needs EXCEPT transport, which the caller
// fills from the recipient's already-decrypted credentials.
func (c client) buildWarmupReply(ctx context.Context, receiptID, recipient, ws uuid.UUID) (coreapi.WarmupSendJob, bool, error) {
	th, err := c.q.GetWarmupReplyThread(ctx, gen.GetWarmupReplyThreadParams{ID: receiptID, WorkspaceID: ws})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return coreapi.WarmupSendJob{}, false, nil // send/thread gone → no reply
		}
		return coreapi.WarmupSendJob{}, false, err
	}
	content, err := c.warmupContent.Thread(ctx, th.ContentKey)
	if err != nil {
		return coreapi.WarmupSendJob{}, false, err
	}
	body, ok := warmup.Reply(content, int(th.Turn))
	if !ok {
		return coreapi.WarmupSendJob{}, false, nil // thread exhausted → no reply
	}

	// Anchor the reply's warmup_sends id to the IMMUTABLE receipt id (one receipt maps to
	// one reply), NOT the recipient's mutable sent-today index. A post-send engage retry
	// then re-derives the SAME id and reclaims the existing 'sent' row (ClaimAlreadySent →
	// recover-forward), instead of a drifting id that would INSERT a fresh row and re-send.
	replySendID := deriveWarmupReplySendID(receiptID)
	token := warmup.Sign(warmup.Payload{
		WorkspaceID: ws.String(), WarmupSendID: replySendID.String(), FromMailbox: recipient.String(),
	}, c.warmupSecret)

	return coreapi.WarmupSendJob{
		WorkspaceID: ws.String(),
		FromMailbox: recipient.String(),
		ToMailbox:   th.SenderMailbox.String(),
		ThreadID:    th.ThreadID.String(),
		IsReply:     true,
		SendID:      replySendID.String(),
		ToEmail:     th.SenderEmail,
		FromEmail:   th.RecipientEmail,
		FromName:    th.RecipientName,
		Subject:     "Re: " + content.Subject,
		BodyText:    body,
		InReplyTo:   th.RootMessageID,
		References:  th.RootMessageID,
		Token:       token,
	}, true, nil
}

// MarkWarmupEngaged flips the receipt's engaged guard and, when the engagement
// replied, bumps the recipient's daily replies counter — atomically. See the
// coreapi.Client interface doc. Idempotent: a re-run over an already-engaged row
// flips nothing (pgx.ErrNoRows) and skips the reply bump.
func (c client) MarkWarmupEngaged(ctx context.Context, receiptID, workspaceID string, replied bool) error {
	rid, err := uuid.Parse(receiptID)
	if err != nil {
		return err
	}
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return err
	}

	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed
	qtx := c.q.WithTx(tx)

	recipient, err := qtx.SetWarmupReceiptEngaged(ctx, gen.SetWarmupReceiptEngagedParams{ID: rid, WorkspaceID: ws})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Already engaged (a retried engage) or not visible to this workspace:
			// nothing to do. The reply counter was bumped by the first run.
			return nil
		}
		return err
	}
	if replied {
		if err := qtx.IncrementWarmupReplyStat(ctx, gen.IncrementWarmupReplyStatParams{MailboxID: recipient, WorkspaceID: ws}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ListDueWarmupMailboxes returns the coarse sweep fan-out of enabled, non-paused
// participants. See the coreapi.Client interface doc.
func (c client) ListDueWarmupMailboxes(ctx context.Context) ([]coreapi.MailboxRef, error) {
	rows, err := c.q.ListDueWarmupMailboxes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]coreapi.MailboxRef, len(rows))
	for i, r := range rows {
		out[i] = coreapi.MailboxRef{ID: r.MailboxID.String(), WorkspaceID: r.WorkspaceID.String()}
	}
	return out, nil
}

// EvaluateWarmupHealth recomputes and persists health transitions across all
// enabled participants. See the coreapi.Client interface doc. Only actual state
// changes are written, so a steady-state sweep touches nothing. A per-participant
// update error is accumulated (errors.Join) and evaluation CONTINUES for the rest,
// so one bad row never stalls health eval for every mailbox.
func (c client) EvaluateWarmupHealth(ctx context.Context) error {
	workspaces, err := c.q.ListWorkspacesWithWarmupParticipants(ctx)
	if err != nil {
		return err
	}
	now := c.now().UTC()
	var errs []error
	for _, ws := range workspaces {
		// Signals are aggregated ONCE per workspace, not recomputed per participant.
		// The Phase 0 sweep ran eight correlated subqueries for every enabled mailbox
		// on every tick, including an arm no index could serve.
		if _, err := c.q.UpsertWarmupSignalSnapshotsForWorkspace(ctx, ws); err != nil {
			// Skip this workspace rather than evaluating it on stale evidence. The
			// previous snapshot survives and the staleness rule below will refuse to
			// promote on it, so a persistent refresh failure degrades to "no
			// promotions" rather than to wrong ones.
			errs = append(errs, fmt.Errorf("warmup: refresh signals for workspace %s: %w", ws, err))
			continue
		}
		if err := c.evaluateWorkspaceParticipants(ctx, ws, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// warmupEvidenceTTL is how old a snapshot may be before it stops counting as
// evidence. Twice the five-minute sweep interval tolerates one missed tick; beyond
// that the participant reads as unknown and cannot be promoted. Staleness must
// never read as safety (design §8, acceptance criterion 3).
const warmupEvidenceTTL = 10 * time.Minute

func (c client) evaluateWorkspaceParticipants(ctx context.Context, ws uuid.UUID, now time.Time) error {
	rows, err := c.q.ListWarmupEvaluationRows(ctx, gen.ListWarmupEvaluationRowsParams{
		WorkspaceID: ws, EvidenceTtlSeconds: int32(warmupEvidenceTTL / time.Second),
	})
	if err != nil {
		return fmt.Errorf("warmup: list participants for workspace %s: %w", ws, err)
	}
	var errs []error
	for _, r := range rows {
		decision := warmup.EvaluateParticipant(warmup.Signals{
			CurrentHealth: r.HealthState,
			CurrentLane:   r.Lane,
			AuthPassing:   r.AuthPassing,
			// Computed by the DB clock (see the query): a participant enabled between
			// the refresh and this read has no snapshot row at all, so the result is
			// NULL, which must read as no evidence rather than as zeros that look clean.
			EvidenceFresh:         r.EvidenceFresh != nil && *r.EvidenceFresh,
			Inbox:                 int(r.PlacementInbox),
			Spam:                  int(r.PlacementSpam),
			CampaignDelivered:     int(r.CampaignDelivered),
			CampaignHardBounces:   int(r.CampaignHardBounces),
			CampaignComplaints:    int(r.CampaignComplaints),
			WarmupDelivered:       int(r.WarmupDelivered),
			WarmupHardBounces:     int(r.WarmupHardBounces),
			ObserverTokenFailures: int(r.ObserverTokenFailures),
			QuarantinedSince:      r.QuarantinedSince.Time,
		}, now)

		// The timed-block floor applies to the HEALTH axis only: a recovery is held
		// while paused_until is in the future so a mailbox cannot walk
		// paused→throttled→watch→healthy on consecutive five-minute sweeps. A lane
		// change still lands immediately — auth regressing or evidence going stale is
		// containment, not a recovery being rushed, and delaying it for a dwell timer
		// would be backwards.
		decision = warmup.HoldRecoveryDuringBlock(decision, r.HealthState, r.PausedUntil.Time, now)

		if !warmup.ShouldApplyTransition(r.HealthState, decision.Health, r.Lane, decision.Lane) {
			continue
		}
		if _, err := c.q.ApplyWarmupParticipantTransition(ctx, gen.ApplyWarmupParticipantTransitionParams{
			MailboxID: r.MailboxID, WorkspaceID: r.WorkspaceID,
			FromState: r.HealthState, ToState: decision.Health,
			FromLane: r.Lane, ToLane: decision.Lane,
			ReasonCode: decision.HealthReasonCode, Reason: decision.HealthReason,
			LaneReasonCode: decision.LaneReasonCode, LaneReason: decision.LaneReason,
			PausedUntil:      warmupPausedUntil(decision.Health, now),
			PlacementSamples: int32(decision.PlacementSamples), SpamRate: float32(decision.SpamRate),
			// The transition row keeps one bounce column pair for continuity with
			// Phase 0 history; the CAMPAIGN population is recorded because it is the
			// one that reflects real recipients. The warmup population is evaluated
			// separately by the policy and can still drive the state.
			BounceSamples: int32(decision.CampaignBounceSamples), BounceRate: float32(decision.CampaignBounceRate),
			ComplaintSamples: int32(decision.ComplaintSamples), ComplaintRate: float32(decision.ComplaintRate),
			InvalidTokens: int32(decision.ObserverTokenFailures), PolicyVersion: warmup.PolicyVersion,
		}); err != nil {
			errs = append(errs, fmt.Errorf("warmup: apply transition for mailbox %s: %w", r.MailboxID.String(), err))
			continue
		}
	}
	return errors.Join(errs...)
}
