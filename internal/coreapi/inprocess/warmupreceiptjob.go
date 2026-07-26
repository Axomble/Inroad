package inprocess

import (
	"context"
	"errors"
	"fmt"
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

// warmupEngagePlan builds the deterministic recipient action set from the receipt.
// It is pure (seeded hash, no rand, no I/O) so it is unit-testable without a DB and
// reproducible across a re-poll: rescue only when the message landed in spam,
// always mark-read, reply per the recipient's reply_rate via the seeded decision,
// and a heavy-tailed humanized dwell keyed on the receipt id.
func warmupEngagePlan(receiptID, recipientMailbox, dayKey string, replyRate float64, placement string) coreapi.WarmupEngagePlan {
	seed := warmupReceiptSeed(receiptID, recipientMailbox, dayKey)
	return coreapi.WarmupEngagePlan{
		ReceiptID:   receiptID,
		DoRescue:    placement == placementSpam,
		DoMarkRead:  true,
		DoReply:     warmup.ReplyDecision(seed, replyRate),
		EngageAfter: warmup.EngageDwell(receiptID),
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
		WorkspaceID: ws, WarmupSendID: sendUUID, RecipientMailbox: recipient, Placement: in.Placement,
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
				return coreapi.WarmupEngagePlan{}, coreapi.ErrCrossTenant
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
		// Rebuild against the STORED placement + received_at so DoRescue/DoReply/EngageAfter
		// match what the fresh insert returned and what GetWarmupEngageJob will recompute.
		dayKey := dup.ReceivedAt.Time.UTC().Format("2006-01-02")
		return warmupEngagePlan(dup.ID.String(), recipient.String(), dayKey, float64(p.ReplyRate), dup.Placement), nil
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
	if err := tx.Commit(ctx); err != nil {
		return coreapi.WarmupEngagePlan{}, err
	}

	// Build the deterministic plan (pure). The recipient's reply_rate (read in-tx
	// above) drives the reply decision; the seed is anchored on the just-committed
	// received_at so a later GetWarmupEngageJob reproduces the same decision.
	dayKey := row.ReceivedAt.Time.UTC().Format("2006-01-02")
	return warmupEngagePlan(row.ID.String(), recipient.String(), dayKey, float64(p.ReplyRate), in.Placement), nil
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
// reply. The token embeds a deterministic reply send id derived from (recipient, UTC
// day, recipient's send index) via deriveWarmupReplySendID — a DISTINCT id namespace
// from the normal-send derivation, so a reply can never collide with a normal
// due-send at the same (mailbox, day, index) tuple (which would let one silently
// no-op the other at claim time). It stays deterministic, so the reply's later claim
// reclaims the SAME row. The returned job carries everything the claim/send/finalize
// path needs EXCEPT transport, which the caller fills from the recipient's already-
// decrypted credentials.
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

	sentToday, err := c.q.GetWarmupSentToday(ctx, gen.GetWarmupSentTodayParams{MailboxID: recipient, WorkspaceID: ws})
	if err != nil {
		return coreapi.WarmupSendJob{}, false, err
	}
	now := time.Now().UTC()
	replySendID := deriveWarmupReplySendID(recipient, now.Format("2006-01-02"), int(sentToday))
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
	rows, err := c.q.ListWarmupHealthSignals(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	var errs []error
	for _, r := range rows {
		// spamRate is "of MY sent warmup mail, the fraction that landed in spam" —
		// spam / (inbox + spam), both SENDER-attributed (spec §4/§8). inbox+spam == 0
		// (no recent sends observed) yields rate 0 → healthy.
		sent := r.Inbox + r.Spam
		var spamRate float64
		if sent > 0 {
			spamRate = float64(r.Spam) / float64(sent)
		}
		// Bounce rate and invalid-token count have no persistence in the v1 schema,
		// so they are 0 here (documented gap); spam-placement rate is the only live
		// signal. HealthState escalates immediately and recovers one level per clean
		// window.
		state, reason := warmup.HealthState(spamRate, 0, 0, r.HealthState)
		// Timed-block floor (spec §8): an escalation to a worse state applies
		// immediately, but a recovery (step down) is held back while paused_until is
		// still in the future — so recovery can't bypass the 72h/24h dwell by walking
		// paused→throttled→watch→healthy on consecutive 5-minute sweeps.
		if !warmup.ShouldApplyTransition(r.HealthState, state, r.PausedUntil.Time, now) {
			continue
		}
		if err := c.q.UpdateWarmupHealth(ctx, gen.UpdateWarmupHealthParams{
			MailboxID:    r.MailboxID,
			WorkspaceID:  r.WorkspaceID,
			HealthState:  state,
			HealthReason: reason,
			PausedUntil:  warmupPausedUntil(state, now),
		}); err != nil {
			errs = append(errs, fmt.Errorf("warmup: update health for mailbox %s: %w", r.MailboxID.String(), err))
			continue
		}
	}
	return errors.Join(errs...)
}
