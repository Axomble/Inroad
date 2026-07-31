package inprocess

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/rotation"
)

// mailboxStatusActive is the only mailbox status a pool member may send from.
// A plain string rather than an import: app/* packages own their own status
// vocabularies and coreapi must not depend on one.
const mailboxStatusActive = "active"

// resolvedSender is the mailbox one step will send from: the transport fields the
// send job carries plus today's capacity numbers the worker's cap check reads.
// It is resolved from the enrollment's pinned mailbox, from the campaign's sender
// pool, or — for a campaign that has no pool rows — from campaigns.mailbox_id.
type resolvedSender struct {
	mailboxID          uuid.UUID
	provider           string
	fromEmail          string
	fromName           string
	smtpHost           string
	smtpPort           int32
	smtpUsername       string
	secretCiphertext   string
	allowPlaintext     bool
	minIntervalSeconds int32
	effectiveCap       int
	sentToday          int
}

// threadLostItsMailbox reports whether this enrollment's thread has lost the
// mailbox it was sending from.
//
// An enrollment past step 1 ALWAYS has a pinned mailbox: every send pins one, and
// migration 000032 backfilled every already-sent enrollment. So a cleared pin at
// current_step > 0 can only be sequence_enrollments.mailbox_id's ON DELETE SET
// NULL firing when the mailbox was deleted. current_step = 0 with no pin is the
// normal pre-first-send state and is emphatically NOT this case.
func threadLostItsMailbox(currentStep int32, pinned pgtype.UUID) bool {
	return currentStep > 0 && !pinned.Valid
}

// resolveSender decides which mailbox this enrollment's next step sends from.
//
//  1. An enrollment that already has a mailbox keeps it. A follow-up step is a
//     reply carrying In-Reply-To/References from the previous message, so moving
//     the thread to another mailbox would reference a Message-ID that mailbox
//     never sent — broken for the recipient and a spam signal.
//  2. Otherwise the campaign's pool is consulted: eligible members are ranked by
//     the pure selector and the winner is claimed write-once (concurrent workers
//     converge on one mailbox), then pinned for the rest of the thread.
//  3. A campaign with no pool rows was never configured, not broken: it sends
//     from campaigns.mailbox_id — which the bundle has already joined — and that
//     mailbox is pinned too, so configuring a pool later cannot re-route a thread
//     already in flight.
//
// A pool whose every member is disabled, inactive or capped defers instead
// (exhaustedPoolSender), and pins nothing.
func (c client) resolveSender(ctx context.Context, ws, enrollmentID uuid.UUID, b gen.GetStepEnrollmentBundleRow) (resolvedSender, error) {
	if b.EnrollmentMailboxID.Valid {
		return c.withSentToday(ctx, bundleSender(b))
	}
	rows, err := c.q.ListCampaignSenderCandidates(ctx, gen.ListCampaignSenderCandidatesParams{
		CampaignID: b.CampaignID, WorkspaceID: ws,
	})
	if err != nil {
		return resolvedSender{}, err
	}
	// No pool rows: the bundle's mailbox IS campaigns.mailbox_id (the join
	// COALESCEs to it), so the fallback needs no extra lookup.
	chosen := b.MailboxID
	if len(rows) > 0 {
		eligible := eligibleCandidates(rows)
		if len(eligible) == 0 {
			return c.exhaustedPoolSender(b, rows)
		}
		winner, serr := rotation.Select(b.RotationMode, eligible)
		if serr != nil {
			return resolvedSender{}, serr
		}
		chosen, err = uuid.Parse(winner.MailboxID)
		if err != nil {
			return resolvedSender{}, err
		}
	}
	pinned, err := c.claimEnrollmentSender(ctx, ws, enrollmentID, b.CampaignID, chosen)
	if err != nil {
		return resolvedSender{}, err
	}
	if pinned == b.MailboxID {
		return c.withSentToday(ctx, bundleSender(b))
	}
	m, err := c.q.GetMailbox(ctx, gen.GetMailboxParams{ID: pinned, WorkspaceID: ws})
	if err != nil {
		return resolvedSender{}, err
	}
	return c.withSentToday(ctx, mailboxSender(m))
}

// claimEnrollmentSender pins the enrollment's sending mailbox write-once and bumps
// the chosen member's rotation counters in the SAME transaction, so rotation state
// can never drift from the assignments that actually happened. Returns the
// mailbox that ended up stored, which on a lost race is the WINNER's, not ours: a
// retry or a second worker re-reads rather than recomputing, so the two can never
// disagree about a thread's mailbox.
func (c client) claimEnrollmentSender(ctx context.Context, ws, enrollmentID, campaignID, mailboxID uuid.UUID) (uuid.UUID, error) {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed
	qtx := c.q.WithTx(tx)

	if _, err := qtx.ClaimEnrollmentMailbox(ctx, gen.ClaimEnrollmentMailboxParams{
		ID: enrollmentID, WorkspaceID: ws, MailboxID: pgtype.UUID{Bytes: mailboxID, Valid: true},
	}); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, err
		}
		_ = tx.Rollback(ctx)
		stored, gerr := c.q.GetEnrollmentMailbox(ctx, gen.GetEnrollmentMailboxParams{
			ID: enrollmentID, WorkspaceID: ws,
		})
		if gerr != nil {
			return uuid.Nil, gerr
		}
		if !stored.Valid {
			// The claim matched no row and none is stored: the enrollment is gone
			// or belongs to another tenant. Surfaced rather than silently sending
			// from the fallback mailbox.
			return uuid.Nil, fmt.Errorf("enrollment %s: sender claim lost with no mailbox stored", enrollmentID)
		}
		return stored.Bytes, nil
	}
	// Zero rows for a fallback mailbox that is not a pool member — correct, there
	// is no counter to keep for it.
	if err := qtx.BumpCampaignSenderAssignment(ctx, gen.BumpCampaignSenderAssignmentParams{
		CampaignID: campaignID, WorkspaceID: ws, MailboxID: mailboxID,
	}); err != nil {
		return uuid.Nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, err
	}
	return mailboxID, nil
}

// eligibleCandidates keeps the pool rows that can send right now — enabled, an
// active mailbox, and still under today's cap — and projects them onto the pure
// selector's Candidate. Remaining capacity is computed here with effectiveCap so
// ramp math keeps exactly one implementation. A never-assigned member's
// LastAssignedAt is the zero time, which is what puts it first under LRU.
func eligibleCandidates(rows []gen.ListCampaignSenderCandidatesRow) []rotation.Candidate {
	out := make([]rotation.Candidate, 0, len(rows))
	for _, r := range rows {
		if !r.Enabled || r.MailboxStatus != mailboxStatusActive {
			continue
		}
		remaining := candidateCap(r) - int(r.SentToday)
		if remaining <= 0 {
			continue
		}
		out = append(out, rotation.Candidate{
			MailboxID: r.MailboxID.String(), Weight: int(r.Weight), RemainingToday: remaining,
			WarmupAgeDays: mailboxAgeDays(r.MailboxCreatedAt), HealthState: r.HealthState,
			AssignedCount: r.AssignedCount, LastAssignedAt: r.LastAssignedAt.Time,
		})
	}
	return out
}

// exhaustedPoolSender builds the job for a pool that HAS members but none that can
// send right now. It reports the pool's aggregate capacity and consumption, so the
// worker takes the existing cap-deferral branch (sent_today >= effective cap)
// unchanged — no new outcome and no new failure mode, exactly as a capped
// single-mailbox send behaves today. A disabled or inactive member counts its
// whole cap as consumed, because none of it is available today; a pool whose
// members all have a zero cap therefore reports zero capacity and stops the
// enrollment, the same degenerate-cap verdict a single mailbox would get.
//
// Nothing is pinned: no mailbox was chosen. The transport stays the campaign's
// fallback, which the deferral guarantees is never dialed.
func (c client) exhaustedPoolSender(b gen.GetStepEnrollmentBundleRow, rows []gen.ListCampaignSenderCandidatesRow) (resolvedSender, error) {
	s := bundleSender(b)
	var poolCap, consumed int
	for _, r := range rows {
		limit := candidateCap(r)
		poolCap += limit
		if r.Enabled && r.MailboxStatus == mailboxStatusActive {
			consumed += min(int(r.SentToday), limit)
			continue
		}
		consumed += limit
	}
	s.effectiveCap, s.sentToday = poolCap, consumed
	return s, nil
}

// candidateCap is a pool row's effective cap today, ramp included.
func candidateCap(r gen.ListCampaignSenderCandidatesRow) int {
	return effectiveCap(int(r.DailyCap), int(r.RampStartCap), int(r.RampDays), r.RampEnabled,
		mailboxAgeDays(r.MailboxCreatedAt))
}

// withSentToday fills in how much the resolved mailbox has already sent today.
func (c client) withSentToday(ctx context.Context, s resolvedSender) (resolvedSender, error) {
	n, err := c.q.CountSentToday(ctx, s.mailboxID)
	if err != nil {
		return resolvedSender{}, err
	}
	s.sentToday = int(n)
	return s, nil
}

// bundleSender reads the mailbox the step bundle already joined — the
// enrollment's pinned mailbox, or campaigns.mailbox_id when it has none.
func bundleSender(b gen.GetStepEnrollmentBundleRow) resolvedSender {
	return resolvedSender{
		mailboxID: b.MailboxID, provider: b.Provider,
		fromEmail: b.FromEmail, fromName: b.FromName,
		smtpHost: b.SmtpHost, smtpPort: b.SmtpPort, smtpUsername: b.SmtpUsername,
		secretCiphertext: b.SecretCiphertext, allowPlaintext: b.AllowPlaintext,
		minIntervalSeconds: b.MinIntervalSeconds,
		effectiveCap: effectiveCap(int(b.DailyCap), int(b.RampStartCap), int(b.RampDays), b.RampEnabled,
			mailboxAgeDays(b.MailboxCreatedAt)),
	}
}

// mailboxSender reads a mailbox row the pool selected, which the bundle did not
// join.
func mailboxSender(m gen.Mailbox) resolvedSender {
	return resolvedSender{
		mailboxID: m.ID, provider: m.Provider,
		fromEmail: m.Email, fromName: m.DisplayName,
		smtpHost: m.SmtpHost, smtpPort: m.SmtpPort, smtpUsername: m.SmtpUsername,
		secretCiphertext: m.SecretCiphertext, allowPlaintext: m.AllowPlaintext,
		minIntervalSeconds: m.MinIntervalSeconds,
		effectiveCap: effectiveCap(int(m.DailyCap), int(m.RampStartCap), int(m.RampDays), m.RampEnabled,
			mailboxAgeDays(m.CreatedAt)),
	}
}

// mailboxAgeDays is how many whole days ago the mailbox was connected — the age
// the ramp schedule is measured against.
func mailboxAgeDays(createdAt pgtype.Timestamptz) int {
	return int(time.Since(createdAt.Time).Hours() / 24)
}
