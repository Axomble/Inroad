package inprocess

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// warmupSendIDNamespace is a fixed namespace for deriving each warmup send's id
// deterministically (uuid.NewSHA1) from (from_mailbox, UTC day, today's send
// index). Like stepSendIDNamespace the value is arbitrary — it only needs to be
// stable across process restarts so every recomputation of the same tuple yields
// the same id.
var warmupSendIDNamespace = uuid.MustParse("b2c4d6e8-1a3c-5e7f-9b0d-2c4e6a8f0b1d")

// deriveWarmupSendID computes the deterministic warmup_sends row id for one
// (mailbox, UTC day, send index) — the stable tuple analogous to stepsend's
// (campaign, contact, step_order).
//
// NOTE on the spec's dueUnix: §6 phrases the id as uuidv5(ns, mailbox+":"+dueUnix),
// but the fixed GetWarmupSendJob(ctx, mailboxID, workspaceID) signature (§4) carries
// no tick time, and the id must be derived read-side so the receipt token can embed
// it before the send. The (day, index) tuple is available read-side and delivers the
// SAME idempotency guarantee for the crash-before-send / send-failed retry the spec
// cares about: the retry re-reads the same SentToday index and reclaims the same
// row. (A post-finalize handler error advances the index and would enqueue at most
// one extra warmup email — acceptable for low-stakes self-to-self warmup traffic,
// and strictly safer than a campaign double-send.)
func deriveWarmupSendID(mailboxID uuid.UUID, day string, index int) uuid.UUID {
	key := mailboxID.String() + "|" + day + "|" + strconv.Itoa(index)
	return uuid.NewSHA1(warmupSendIDNamespace, []byte(key))
}

// warmupReplySendIDNamespace is a fixed namespace DISTINCT from warmupSendIDNamespace,
// used to derive an engagement reply's warmup_sends id from the IMMUTABLE receipt id.
// The separate namespace guarantees a reply's id can NEVER collide with a normal
// due-send's id (deriveWarmupSendID) — even for the same underlying uuid bytes — so one
// can never silently no-op the other at claim time.
var warmupReplySendIDNamespace = uuid.MustParse("c3d5e7f9-2b4d-6f8a-0c1e-3d5f7b9a1c2e")

// deriveWarmupReplySendID derives the deterministic warmup_sends id for an engagement
// REPLY from the receipt id — an IMMUTABLE key (one receipt maps to exactly one reply).
// Anchoring on the receipt, NOT the recipient's mutable sent-today index, is the
// idempotency fix: a post-send engage retry (e.g. MarkWarmupEngaged failed → asynq
// retries GetWarmupEngageJob) re-derives the SAME id — even though the reply's own
// MarkWarmupSent already bumped the sent counter — so ClaimWarmupSend sees the existing
// 'sent' row (ClaimAlreadySent → recover-forward) instead of INSERTing a fresh row,
// winning the claim, and SENDING THE REPLY TWICE. It uses a namespace distinct from
// deriveWarmupSendID so a reply can never collide with a normal due-send.
func deriveWarmupReplySendID(receiptID uuid.UUID) uuid.UUID {
	return uuid.NewSHA1(warmupReplySendIDNamespace, receiptID[:])
}

// GetWarmupSendJob resolves the next warmup action for a warming mailbox. It is
// read-only w.r.t. warmup_sends (the claim inserts that row) but MAY open a
// warmup_threads row when starting a new thread, so the returned job carries a
// valid thread id. workspaceID is pinned in every SQL WHERE.
func (c client) GetWarmupSendJob(ctx context.Context, mailboxID, workspaceID string) (coreapi.WarmupSendJob, error) {
	mbID, err := uuid.Parse(mailboxID)
	if err != nil {
		return coreapi.WarmupSendJob{}, err
	}
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return coreapi.WarmupSendJob{}, err
	}

	b, err := c.q.GetWarmupSenderBundle(ctx, gen.GetWarmupSenderBundleParams{MailboxID: mbID, WorkspaceID: ws})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Not an opted-in participant in this workspace → nothing to do.
			return coreapi.WarmupSendJob{Skip: true}, nil
		}
		return coreapi.WarmupSendJob{}, err
	}
	if b.WorkspaceID != ws {
		return coreapi.WarmupSendJob{}, coreapi.ErrCrossTenant
	}

	now := time.Now().UTC()
	// Paused / disabled sender → skip.
	if !b.Enabled || b.HealthState == warmup.StatePaused || (b.PausedUntil.Valid && b.PausedUntil.Time.After(now)) {
		return coreapi.WarmupSendJob{Skip: true}, nil
	}

	// Over today's target → skip.
	sentToday, err := c.q.GetWarmupSentToday(ctx, gen.GetWarmupSentTodayParams{MailboxID: mbID, WorkspaceID: ws})
	if err != nil {
		return coreapi.WarmupSendJob{}, err
	}
	days := int(now.Sub(b.StartedAt.Time).Hours() / 24)
	target := warmup.RampTarget(int(b.StartVolume), int(b.MaxVolume), int(b.RampIncrement), days)
	effective := int(math.Round(float64(target) * warmup.DailyVolumeFactor(mailboxID, now)))
	if int(sentToday) >= effective {
		return coreapi.WarmupSendJob{Skip: true}, nil
	}

	// Recency-spread partner (different, enabled, non-paused, same workspace): the
	// seed anchor AND the new-thread recipient. Selected FIRST so the reply
	// decision's seed is unchanged from before this tuning (partner spread for new
	// threads is preserved); when a reply is wanted we may instead target a
	// repliable partner below, but the new-thread fallback stays on this one.
	spreadPartner, err := c.q.SelectWarmupPartner(ctx, gen.SelectWarmupPartnerParams{WorkspaceID: ws, MailboxID: mbID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// No eligible partner (workspace has <2 usable participants) → skip.
			return coreapi.WarmupSendJob{Skip: true}, nil
		}
		return coreapi.WarmupSendJob{}, err
	}

	dayKey := now.Format("2006-01-02")
	sendIndex := int(sentToday)
	// Deterministic (seeded, not math/rand) reply-vs-new decision, stable across a
	// retried tick because it seeds on (mailbox, spread partner, day, index). Rolled
	// BEFORE partner-for-reply selection; the seed is unchanged from before the fix.
	seed := mailboxID + ":" + spreadPartner.MailboxID.String() + ":" + dayKey + ":" + strconv.Itoa(sendIndex)
	wantReply := warmup.ReplyDecision(seed, float64(b.ReplyRate))

	var (
		threadID   uuid.UUID
		isReply    bool
		subject    string
		body       string
		inReplyTo  string
		references string
	)

	// New-thread recipient defaults to the recency-spread partner; the reply branch
	// below overrides it with a repliable partner ONLY on a confirmed reply.
	toMailbox := spreadPartner.MailboxID
	toEmail := spreadPartner.Email

	if wantReply {
		// Prefer a partner that ACTUALLY has an open, non-exhausted thread to reply
		// into, rather than replying only into the recency-spread partner (which, as
		// the least-recently-active pick, is the least likely to have one — the cause
		// of the under-realized reply_rate). No repliable partner → fall through to
		// the new-thread path on the spread partner, unchanged.
		rp, rerr := c.q.SelectWarmupReplyPartner(ctx, gen.SelectWarmupReplyPartnerParams{
			WorkspaceID: ws, SenderMailbox: mbID, MaxTurn: int32(warmup.MaxContentTurns()),
		})
		switch {
		case rerr == nil:
			content, cerr := c.warmupContent.Thread(ctx, rp.ContentKey)
			if cerr != nil {
				return coreapi.WarmupSendJob{}, cerr
			}
			// @max_turn is a coarse bound; confirm this specific thread still has a
			// turn here (a shorter library thread can be exhausted below it). An
			// exhausted thread falls through to open a fresh one with spreadPartner.
			if turnBody, ok := warmup.Reply(content, int(rp.Turn)); ok {
				toMailbox = rp.MailboxID
				toEmail = rp.Email
				threadID = rp.ThreadID
				isReply = true
				subject = "Re: " + content.Subject
				body = turnBody
				inReplyTo = rp.RootMessageID
				// TODO(warmup): enrich References with the full ancestor chain once
				// per-message ids are persisted (a schema change, out of scope here).
				references = rp.RootMessageID
			}
		case errors.Is(rerr, pgx.ErrNoRows):
			// no repliable partner for this sender — fall through to a new thread
		default:
			return coreapi.WarmupSendJob{}, rerr
		}
	}

	// newThreadContentKey is set (== seed) only on the new-thread path, deferring
	// the actual InsertWarmupThread until after the fallible credential decrypt
	// below (see the ordering note there).
	var newThreadContentKey string
	if !isReply {
		// New thread: content_key seeds the library selection and is persisted so a
		// later reply regenerates the identical conversation. Resolving the content
		// here is fallible but creates NO row — the row-creating insert is deferred.
		newThreadContentKey = seed
		content, cerr := c.warmupContent.Thread(ctx, newThreadContentKey)
		if cerr != nil {
			return coreapi.WarmupSendJob{}, cerr
		}
		opener, ok := warmup.Reply(content, 0)
		if !ok {
			return coreapi.WarmupSendJob{}, fmt.Errorf("warmup: content thread %q has no opening turn", newThreadContentKey)
		}
		subject = content.Subject
		body = opener
	}

	sendID := deriveWarmupSendID(mbID, dayKey, sendIndex)
	token := warmup.Sign(warmup.Payload{
		WorkspaceID: ws.String(), WarmupSendID: sendID.String(), FromMailbox: mbID.String(),
	}, c.warmupSecret)

	// Decrypt the FROM mailbox transport via the SAME keyring/sealer path
	// GetStepSendJob uses: API providers refresh a short-lived access token; smtp
	// unseals the stored password. Both are []byte, zeroized by the worker. This
	// block creates NO rows — it must run BEFORE InsertWarmupThread so a transient
	// decrypt failure leaves zero rows behind (no orphan thread that a retried tick
	// would re-insert on top of), matching GetStepSendJob's zero-rows-on-failure.
	var accessToken, password []byte
	if b.Provider == "gmail" || b.Provider == "m365" {
		at, aerr := c.oauthAccessToken(ctx, b.Provider, mbID, ws, b.SecretCiphertext, c.oauthConfigFor(b.Provider))
		if aerr != nil {
			return coreapi.WarmupSendJob{}, aerr
		}
		accessToken = []byte(at)
	} else {
		sealer, serr := c.keyring.SealerFor(ctx, ws)
		if serr != nil {
			return coreapi.WarmupSendJob{}, serr
		}
		password, err = sealer.Open(b.SecretCiphertext)
		if err != nil {
			return coreapi.WarmupSendJob{}, err
		}
	}

	// Thread insert LAST: the final fallible, row-creating step before the job is
	// returned. All credential decryption above has already succeeded, so opening
	// this thread is the only side effect left — a failure earlier created nothing.
	if !isReply {
		th, ierr := c.q.InsertWarmupThread(ctx, gen.InsertWarmupThreadParams{
			WorkspaceID: ws, SenderMailbox: mbID, PartnerMailbox: toMailbox,
			Subject: subject, ContentKey: newThreadContentKey,
		})
		if ierr != nil {
			if errors.Is(ierr, pgx.ErrNoRows) {
				// Self-enforcing insert wrote nothing → sender not in workspace.
				return coreapi.WarmupSendJob{}, coreapi.ErrCrossTenant
			}
			return coreapi.WarmupSendJob{}, ierr
		}
		threadID = th.ID
	}

	return coreapi.WarmupSendJob{
		WorkspaceID: ws.String(),
		FromMailbox: mbID.String(),
		ToMailbox:   toMailbox.String(),
		ThreadID:    threadID.String(),
		IsReply:     isReply,
		SendID:      sendID.String(),
		ToEmail:     toEmail,
		FromEmail:   b.FromEmail,
		FromName:    b.FromName,
		Subject:     subject,
		// Warmup mail is intentionally plaintext for v1 (the library content is
		// text-only), so BodyHTML is deliberately left empty here — not a TODO.
		BodyText:       body,
		InReplyTo:      inReplyTo,
		References:     references,
		Token:          token,
		Provider:       b.Provider,
		AccessToken:    accessToken,
		SMTPHost:       b.SmtpHost,
		SMTPPort:       int(b.SmtpPort),
		SMTPUsername:   b.SmtpUsername,
		SMTPPassword:   password,
		AllowPlaintext: b.AllowPlaintext,
	}, nil
}

// ClaimWarmupSend claims one warmup send for delivery (claim-before-send),
// mirroring ClaimStepSend. The deterministic SendID is the warmup_sends row id: a
// fresh INSERT wins the claim, and a released ('queued') or STALE 'sending' lease
// is reclaimed on conflict. workspace_id is pinned on the insert value and the
// reclaim WHERE, so a cross-tenant id claims zero rows. On a lost claim it reads
// the row's status so the caller can recover forward: 'sent' → ClaimAlreadySent
// (do not re-send); anything else → ClaimSkip.
func (c client) ClaimWarmupSend(ctx context.Context, job coreapi.WarmupSendJob) (coreapi.ClaimOutcome, error) {
	ws, err := uuid.Parse(job.WorkspaceID)
	if err != nil {
		return coreapi.ClaimSkip, err
	}
	sendID, err := uuid.Parse(job.SendID)
	if err != nil {
		return coreapi.ClaimSkip, err
	}
	threadID, err := uuid.Parse(job.ThreadID)
	if err != nil {
		return coreapi.ClaimSkip, err
	}
	from, err := uuid.Parse(job.FromMailbox)
	if err != nil {
		return coreapi.ClaimSkip, err
	}
	to, err := uuid.Parse(job.ToMailbox)
	if err != nil {
		return coreapi.ClaimSkip, err
	}

	if _, err := c.q.ClaimWarmupSend(ctx, gen.ClaimWarmupSendParams{
		ID: sendID, WorkspaceID: ws, ThreadID: threadID, FromMailbox: from, ToMailbox: to,
		IsReply: job.IsReply, Token: job.Token, LeaseSeconds: claimLeaseSeconds,
	}); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return coreapi.ClaimSkip, err
		}
		// Lost the claim: learn the row's state to decide skip vs recover-forward.
		st, serr := c.q.GetWarmupSendState(ctx, gen.GetWarmupSendStateParams{ID: sendID, WorkspaceID: ws})
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

// MarkWarmupSent finalizes the claimed row to 'sent', advances the thread turn
// (setting root_message_id on turn 0), and increments the sender's daily sent
// counter — all in ONE transaction. The finalize is guarded on status='sending'
// and returns rows affected, so a re-run over an already-'sent' row does the side
// effects zero times (idempotent, never double-counts). workspace_id is pinned.
func (c client) MarkWarmupSent(ctx context.Context, job coreapi.WarmupSendJob, messageID string) error {
	ws, err := uuid.Parse(job.WorkspaceID)
	if err != nil {
		return err
	}
	sendID, err := uuid.Parse(job.SendID)
	if err != nil {
		return err
	}
	threadID, err := uuid.Parse(job.ThreadID)
	if err != nil {
		return err
	}
	from, err := uuid.Parse(job.FromMailbox)
	if err != nil {
		return err
	}

	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed
	qtx := c.q.WithTx(tx)

	n, err := qtx.SetWarmupSendSent(ctx, gen.SetWarmupSendSentParams{ID: sendID, WorkspaceID: ws, MessageID: messageID})
	if err != nil {
		return err
	}
	if n == 0 {
		// Row was already finalized by a prior run — the deferred rollback aborts
		// the empty tx, and we skip the (already-applied) side effects.
		return nil
	}
	if err := qtx.AdvanceWarmupThread(ctx, gen.AdvanceWarmupThreadParams{
		ID: threadID, WorkspaceID: ws, RootMessageID: messageID,
	}); err != nil {
		return err
	}
	if err := qtx.IncrementWarmupSentStat(ctx, gen.IncrementWarmupSentStatParams{MailboxID: from, WorkspaceID: ws}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ReleaseWarmupSend releases a claimed-but-unsent row after a RETRYABLE failure
// (back to 'queued', lease cleared) so the asynq retry reclaims it promptly.
// Guarded on status='sending' in SQL, workspace-pinned.
func (c client) ReleaseWarmupSend(ctx context.Context, job coreapi.WarmupSendJob) error {
	ws, err := uuid.Parse(job.WorkspaceID)
	if err != nil {
		return err
	}
	sendID, err := uuid.Parse(job.SendID)
	if err != nil {
		return err
	}
	return c.q.ReleaseWarmupSend(ctx, gen.ReleaseWarmupSendParams{ID: sendID, WorkspaceID: ws})
}

// FailWarmupSend finalizes the claimed row to 'failed' after a PERMANENT failure
// (no thread advance, no stat bump). Guarded on status='sending', workspace-pinned.
func (c client) FailWarmupSend(ctx context.Context, job coreapi.WarmupSendJob, errMsg string) error {
	ws, err := uuid.Parse(job.WorkspaceID)
	if err != nil {
		return err
	}
	sendID, err := uuid.Parse(job.SendID)
	if err != nil {
		return err
	}
	return c.q.FailWarmupSend(ctx, gen.FailWarmupSendParams{ID: sendID, WorkspaceID: ws, LastError: errMsg})
}

// NextWarmupDue computes the mailbox's next warmup send time and whether one is
// due now. It loads the participant + today's sent count and delegates the (pure,
// table-tested) policy to warmup.NextDue. A missing/disabled participant is not an
// error — there is simply nothing due. workspace_id is pinned.
func (c client) NextWarmupDue(ctx context.Context, mailboxID, workspaceID string) (time.Time, bool, error) {
	mbID, err := uuid.Parse(mailboxID)
	if err != nil {
		return time.Time{}, false, err
	}
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return time.Time{}, false, err
	}
	p, err := c.q.GetWarmupParticipant(ctx, gen.GetWarmupParticipantParams{MailboxID: mbID, WorkspaceID: ws})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, err
	}
	if !p.Enabled {
		return time.Time{}, false, nil
	}
	sentToday, err := c.q.GetWarmupSentToday(ctx, gen.GetWarmupSentTodayParams{MailboxID: mbID, WorkspaceID: ws})
	if err != nil {
		return time.Time{}, false, err
	}
	in := warmup.DueInputs{
		MailboxID:   mailboxID,
		StartVolume: int(p.StartVolume),
		MaxVolume:   int(p.MaxVolume),
		Increment:   int(p.RampIncrement),
		StartedAt:   p.StartedAt.Time,
		SentToday:   int(sentToday),
		HealthState: p.HealthState,
		Now:         time.Now().UTC(),
	}
	if p.PausedUntil.Valid {
		in.PausedUntil = p.PausedUntil.Time
	}
	plan := warmup.NextDue(in)
	return plan.NextDue, plan.SendNow, nil
}
