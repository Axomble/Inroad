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

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/esp"
	"github.com/inroad/inroad/internal/platform/rotation"
	"github.com/inroad/inroad/internal/platform/sendcap"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// mailboxStatusActive is the only mailbox status a pool member may send from.
// A plain string rather than an import: app/* packages own their own status
// vocabularies and coreapi must not depend on one.
const mailboxStatusActive = "active"

// resolvedSender is the mailbox one step will send from: the transport fields the
// send job carries plus today's capacity numbers the worker's cap check reads.
// It is resolved from the enrollment's pinned mailbox, from the campaign's sender
// pool, or — for a campaign that has no pool rows — from campaigns.mailbox_id.
//
// effectiveCap is the mailbox's cold-sending cap for today: ramped AND scaled by
// warmup health, so the worker's existing cap gate enforces health gating without
// knowing about it. healthPaused is carried separately because 'paused' is not a
// cap of zero: a zero cap STOPS an enrollment, while a paused mailbox may recover
// and its thread must wait for it (see the pools spec's threading constraint —
// re-routing the thread is not an option).
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
	healthPaused       bool
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
// A pool whose every member is disabled, inactive, health-paused or capped defers
// instead (exhaustedPoolSender), and pins nothing.
//
// ESP matching and the exposure budget narrow step 2's candidate set only. Neither
// CAN apply at step 1, which keeps the thread's mailbox: a follow-up carries
// In-Reply-To/References from the previous message, so a live thread is never
// re-routed no matter what the recipient's ESP turns out to be, nor how much of the
// campaign's volume its fault domain has come to carry. Both are therefore
// properties of the initial assignment and of nothing else.
func (c client) resolveSender(ctx context.Context, ws, enrollmentID uuid.UUID, b gen.GetStepEnrollmentBundleRow) (resolvedSender, error) {
	if b.EnrollmentMailboxID.Valid {
		return c.withTodaysCapacity(ctx, ws, bundleSender(b))
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
		// The DOMAIN half of the gate, read only on this path: it decides which
		// pool members may take a NEW lead, and an enrollment that already has a
		// mailbox (above) is not taking one.
		domainLanes, derr := c.domainLanes(ctx, ws)
		if derr != nil {
			return resolvedSender{}, derr
		}
		eligible := eligibleCandidates(rows, domainLanes)
		if len(eligible) == 0 {
			return c.exhaustedPoolSender(b, rows, domainLanes)
		}
		winner, serr := rotation.Select(b.RotationMode,
			c.narrowCandidates(ctx, ws, b.ToEmail, rows, eligible, domainLanes))
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
		return c.withTodaysCapacity(ctx, ws, bundleSender(b))
	}
	m, err := c.q.GetMailbox(ctx, gen.GetMailboxParams{ID: pinned, WorkspaceID: ws})
	if err != nil {
		return resolvedSender{}, err
	}
	return c.withTodaysCapacity(ctx, ws, mailboxSender(m))
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

// domainLanes reads the workspace's enabled participants and folds them into the
// worst lane per ORGANIZATIONAL domain, which is the domain half of the campaign
// gate. Grouping happens in Go because it needs public-suffix data Postgres does
// not have (see internal/platform/warmup/domain.go).
//
// One small workspace-pinned read — one row per participating mailbox — on the
// initial-assignment path only. A follow-up step keeps its thread's mailbox and
// never reaches it.
func (c client) domainLanes(ctx context.Context, ws uuid.UUID) (warmup.DomainLanes, error) {
	rows, err := c.q.ListWorkspaceWarmupLanes(ctx, ws)
	if err != nil {
		return nil, fmt.Errorf("sender pool: read domain lanes for workspace %s: %w", ws, err)
	}
	participants := make([]warmup.MailboxLane, len(rows))
	for i, r := range rows {
		participants[i] = warmup.MailboxLane{Email: r.Email, Lane: r.Lane}
	}
	return warmup.WorstLanesByDomain(participants), nil
}

// eligibleCandidates keeps the pool rows that can send cold mail right now and
// projects them onto the pure selector's Candidate. RemainingToday carries the
// health-scaled capacity, which is the ONLY place health enters selection — the
// selector itself must not scale by it again (see rotation.Candidate).
//
// A never-assigned member's LastAssignedAt is the zero time, which is what puts it
// first under LRU.
func eligibleCandidates(rows []gen.ListCampaignSenderCandidatesRow, domainLanes warmup.DomainLanes) []rotation.Candidate {
	out := make([]rotation.Candidate, 0, len(rows))
	for _, r := range rows {
		remaining := availableToday(r, domainLanes.For(r.Email))
		if remaining <= 0 {
			continue
		}
		out = append(out, rotation.Candidate{
			MailboxID: r.MailboxID.String(), Weight: int(r.Weight), RemainingToday: remaining,
			WarmupAgeDays: mailboxAgeDays(r.MailboxCreatedAt),
			AssignedCount: r.AssignedCount, LastAssignedAt: r.LastAssignedAt.Time,
		})
	}
	return out
}

// narrowCandidates applies both candidate narrowings to the eligible set before
// rotation ranks it. It reads the recipient's ESP from the cache; the ordering and
// the arithmetic live in the pure `narrowed` below.
//
// The recipient's ESP is read from the cache ONLY. resolveSender already runs
// three or four queries inside the send job, and a DNS lookup here would put a
// network round trip on the hot path of every first send; a miss (or a read
// error) therefore reads as unknown, which skips matching entirely and falls
// back to the full pool. The cache is filled off the hot path by
// worker/recipientesp.
//
// Skipped outright when the pool has one eligible member — there is nothing to
// choose between, so the lookup would be a query spent to reach the same answer,
// and neither narrowing can change a one-element set either.
func (c client) narrowCandidates(ctx context.Context, ws uuid.UUID, toEmail string,
	rows []gen.ListCampaignSenderCandidatesRow, eligible []rotation.Candidate,
	domainLanes warmup.DomainLanes,
) []rotation.Candidate {
	if len(eligible) < 2 {
		return eligible
	}
	return narrowed(rows, eligible, c.recipientESP(ctx, ws, toEmail), domainLanes)
}

// narrowed is the pure half of candidate narrowing: ESP matching FIRST, then the
// exposure budget within whatever it left.
//
// THE ORDER IS A DECISION, not an implementation detail, because the two can
// disagree — the only mailbox matching the recipient's provider may be the one
// sitting on the over-exposed fault domain. ESP matching wins for three reasons:
//
//  1. What each is worth. Matching improves the deliverability of THIS message and
//     does so now; the budget hedges a portfolio risk that may never materialise.
//     Trading a certain gain for an uncertain one is the wrong side of that trade,
//     and the budget's own doc comment says as much — being wrong about it should
//     only ever pick a different healthy mailbox.
//  2. What each needs to see. partitionByESP has to answer "does the pool contain
//     ANY mailbox on the recipient's provider", and it falls back to the full set
//     when the answer is no. Narrowing first would make it ask that of a set already
//     pruned for an unrelated reason, so a pool that does match would silently read
//     as unmatched. The budget has no such dependency: it is guaranteed non-empty
//     and composes safely onto any subset.
//  3. What it costs the budget. Nothing structural — it still narrows within the
//     matched cohort, measured over that cohort, which is where a pool's
//     concentration usually sits (a workspace's Google mailboxes spread over three
//     domains). It simply cannot force a cross-provider send to relieve one.
//
// Neither step can empty the set, so the composition cannot reduce sending.
func narrowed(rows []gen.ListCampaignSenderCandidatesRow, eligible []rotation.Candidate,
	recipient esp.ESP, domainLanes warmup.DomainLanes,
) []rotation.Candidate {
	return withinExposureBudget(rows, partitionByESP(rows, eligible, recipient), domainLanes)
}

// withinExposureBudget keeps the candidates whose FAULT DOMAIN is not already
// carrying more of the campaign than it should — the concentration limit, wired to
// this pool's rows.
//
// It reads no evidence about how mail performed. The shares come from
// campaign_senders.assigned_count (the campaign's own history) and the grouping from
// mailboxes.email; the only lane it consults is the evaluator's own conclusion, which
// is not route-derived and not carried by any message. Nothing here is influenceable
// the way security.md invariants 57–59 describe.
//
// A quarantined or blocked domain never reaches this function with anything to
// narrow: availableToday already removed its mailboxes. Containment stays
// LaneMaySend's decision and this stays a ceiling, so the two cannot become two
// implementations of "may this send".
func withinExposureBudget(rows []gen.ListCampaignSenderCandidatesRow, eligible []rotation.Candidate,
	domainLanes warmup.DomainLanes,
) []rotation.Candidate {
	return rotation.WithinExposureBudgetFor(eligible, faultDomains(rows), exposureCeilings(domainLanes))
}

// faultDomains resolves each pool member to the thing that can fail for all of it at
// once: the shared-reputation domain of its address.
//
// Built from the pool rows the caller already has, keyed by mailbox id, because
// rotation.Candidate carries no address and platform/rotation must not learn what a
// mailbox domain is. A candidate absent from the map resolves to "" (unknown), which
// the budget never groups — the right answer for a row that is not in this pool.
//
// warmup.SharedReputationDomain, not OrganizationalDomain, so the key space here is
// the SAME one warmup.DomainLanes is built and read under. That is what lets
// exposureCeilings look a group's lane up by the key it grouped on; deriving the two
// separately would be the "two things that must agree" defect that file exists to
// prevent. It also carries the consumer-provider carve-out for free: two strangers on
// gmail.com key on "" and are never throttled as though they shared a fate.
func faultDomains(rows []gen.ListCampaignSenderCandidatesRow) rotation.FaultDomainOf {
	byMailbox := make(map[string]string, len(rows))
	for _, r := range rows {
		byMailbox[r.MailboxID.String()] = warmup.SharedReputationDomain(r.Email)
	}
	return func(c rotation.Candidate) string { return byMailbox[c.MailboxID] }
}

// exposureCeilings is each fault domain's share ceiling: tighter for a domain the
// evaluator has put on watch or in recovery, and zero — "no opinion", which
// rotation reads as the flat cap — for every other lane.
func exposureCeilings(domainLanes warmup.DomainLanes) func(domain string) float64 {
	return func(domain string) float64 {
		return warmup.ExposureCeiling(domainLanes.ForDomain(domain))
	}
}

// recipientESP reads one domain's cached classification. Every failure — a
// malformed address, a cache miss, a database error — is esp.Unknown, because
// none of them is a reason to fail a send that would otherwise go out. A read
// error is logged rather than swallowed, so a persistently broken cache is
// visible instead of silently degrading every campaign to unmatched sending.
func (c client) recipientESP(ctx context.Context, ws uuid.UUID, toEmail string) esp.ESP {
	domain := esp.Domain(toEmail)
	if domain == "" {
		return esp.Unknown
	}
	stored, err := c.q.GetRecipientDomainESP(ctx, gen.GetRecipientDomainESPParams{
		WorkspaceID: ws, Domain: domain,
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.WarnContext(ctx, "recipient_esp_lookup_failed", "domain", domain, "err", err)
		}
		return esp.Unknown
	}
	if !esp.Valid(stored) {
		return esp.Unknown
	}
	return esp.ESP(stored)
}

// partitionByESP keeps the eligible candidates whose mailbox sends through want,
// so a Google contact is assigned a Google mailbox and a Microsoft contact a
// Microsoft one — or returns the whole set when that would select nothing. Pure,
// and order preserving: the input is ordered by mailbox_id, which is rotation's
// tie-break, so the subset stays deterministic.
//
// A NARROWING, not a re-ranking. rotation.Select is called on the subset unchanged,
// so every rotation mode behaves identically to before within it. Adding an ESP
// factor to rotation.Candidate instead would have been silently inert under
// round_robin and least_recently_used, whose comparisons never consult the score —
// the same reason the exposure budget narrows rather than scores.
//
// Falling back on an empty subset is the rule that makes this safe to enable
// unconditionally: a matched pool that is exhausted for today must not defer a
// send that an unmatched mailbox could deliver. Matching is an optimisation and
// never a gate.
//
// Unknown and Other never match (esp.Matchable). Unknown has no information;
// Other is a catch-all bucket rather than shared infrastructure, so pairing an
// "other" recipient with an "other" sender would be a coincidence presented as a
// decision.
func partitionByESP(rows []gen.ListCampaignSenderCandidatesRow, eligible []rotation.Candidate, want esp.ESP) []rotation.Candidate {
	if !want.Matchable() {
		return eligible
	}
	matching := make(map[string]bool, len(rows))
	for _, r := range rows {
		matching[r.MailboxID.String()] = esp.FromMailbox(r.Provider, r.SmtpHost) == want
	}
	matched := make([]rotation.Candidate, 0, len(eligible))
	for _, cand := range eligible {
		if matching[cand.MailboxID] {
			matched = append(matched, cand)
		}
	}
	if len(matched) == 0 {
		return eligible
	}
	return matched
}

// availableToday is how much cold volume this pool row may still send today: its
// health-scaled cap minus what it has already sent, never negative. Zero for a row
// that cannot send at all — a disabled member, an inactive mailbox, or one the
// warmup engine has PAUSED, which is excluded from the eligible set exactly like a
// disabled row rather than merely deprioritised. The engine that knows a mailbox
// is burning must be able to stop the thing burning it.
//
// The single place eligibility and remaining capacity are decided, so the selector
// and the exhausted-pool report cannot disagree about which members can send.
//
// domainLane is the worst lane on this mailbox's organizational domain, resolved
// by the caller from one workspace read (see domainLanes) rather than per row.
func availableToday(r gen.ListCampaignSenderCandidatesRow, domainLane string) int {
	if !r.Enabled || r.MailboxStatus != mailboxStatusActive {
		return 0
	}
	// The pool LANE is a separate axis from health and gates differently: health
	// decides how MUCH may be sent, the lane decides WHETHER new work may be taken
	// at all. A withheld mailbox — its own lane quarantined or blocked, or its
	// organizational domain's worst one — contributes no capacity, exactly like a
	// paused one.
	//
	// This must live HERE, not only in the reporting paths. Preflight and the
	// senders panel already refused a withheld mailbox, but the rotation reads this
	// function — so without it the UI reported sending=false and cap_today=0 while
	// the worker kept assigning new leads at the mailbox's full ramped cap. The
	// predicate is shared with those paths for exactly that reason.
	if warmup.NewLeadsWithheld(r.Lane, domainLane) {
		return 0
	}
	return max(sendcap.Cold(candidateCap(r), r.HealthState)-int(r.SentToday), 0)
}

// exhaustedPoolSender builds the job for a pool that HAS members but none that can
// send right now. It reports the pool's aggregate capacity and consumption, so the
// worker takes the existing cap-deferral branch (sent_today >= effective cap)
// unchanged — no new outcome and no new failure mode, exactly as a capped
// single-mailbox send behaves today. Every member's real (ramped) cap counts
// towards the pool's capacity and whatever of it is not available today counts as
// consumed: for a capped member that is what it sent, and for a disabled, inactive
// or health-gated one it is the whole cap, since none of it can be used.
//
// A health-PAUSED member additionally sets healthPaused, which routes to the same
// deferral explicitly. Belt and braces: the aggregate numbers above already defer,
// but only while some member has a non-zero cap. A paused mailbox whose cap is
// zero would otherwise report zero capacity and reach the degenerate-cap branch,
// which STOPS the enrollment — and a paused mailbox may recover, so its thread has
// to wait rather than die.
//
// Nothing is pinned: no mailbox was chosen. The transport stays the campaign's
// fallback, which the deferral guarantees is never dialed.
func (c client) exhaustedPoolSender(b gen.GetStepEnrollmentBundleRow, rows []gen.ListCampaignSenderCandidatesRow,
	domainLanes warmup.DomainLanes,
) (resolvedSender, error) {
	s := bundleSender(b)
	var poolCap, consumed int
	for _, r := range rows {
		limit := candidateCap(r)
		poolCap += limit
		consumed += limit - min(availableToday(r, domainLanes.For(r.Email)), limit)
		if r.Enabled && r.MailboxStatus == mailboxStatusActive &&
			(r.HealthState == sendcap.HealthPaused || warmup.NewLeadsWithheld(r.Lane, domainLanes.For(r.Email))) {
			// Same reasoning as a paused mailbox: a withheld lane can clear (a
			// cooldown elapses, DNS starts passing), so the enrollment must wait
			// rather than die on the degenerate zero-capacity branch.
			s.healthPaused = true
		}
	}
	s.effectiveCap, s.sentToday = poolCap, consumed
	return s, nil
}

// candidateCap is a pool row's effective cap today, ramp included and health NOT
// applied — the mailbox's real ceiling. Health scaling is availableToday's job.
func candidateCap(r gen.ListCampaignSenderCandidatesRow) int {
	return sendcap.Effective(int(r.DailyCap), int(r.RampStartCap), int(r.RampDays), r.RampEnabled,
		mailboxAgeDays(r.MailboxCreatedAt))
}

// withTodaysCapacity fills in the resolved mailbox's consumption and its
// cold-sending cap for today: what it has already sent, and its ramped cap scaled
// by warmup health. Applied to EVERY resolved sender — the enrollment's pinned
// mailbox included — so a thread pinned to a mailbox that later degrades is
// throttled on its next step. Gating only new assignments would leave every
// already-enrolled contact sending at full volume from a mailbox in trouble, which
// is most of the volume.
//
// A paused mailbox keeps its real cap in effectiveCap and reports healthPaused
// instead: the numbers a job carries reach the logs and must describe something
// true, and a cap of zero would stop the enrollment rather than defer it.
func (c client) withTodaysCapacity(ctx context.Context, ws uuid.UUID, s resolvedSender) (resolvedSender, error) {
	n, err := c.q.CountSentToday(ctx, s.mailboxID)
	if err != nil {
		return resolvedSender{}, err
	}
	s.sentToday = int(n)
	health, err := c.q.GetMailboxColdHealth(ctx, gen.GetMailboxColdHealthParams{
		MailboxID: s.mailboxID, WorkspaceID: ws,
	})
	if err != nil {
		return resolvedSender{}, err
	}
	if health == sendcap.HealthPaused {
		s.healthPaused = true
		return s, nil
	}
	s.effectiveCap = sendcap.Cold(s.effectiveCap, health)
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
		effectiveCap: sendcap.Effective(int(b.DailyCap), int(b.RampStartCap), int(b.RampDays), b.RampEnabled,
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
		effectiveCap: sendcap.Effective(int(m.DailyCap), int(m.RampStartCap), int(m.RampDays), m.RampEnabled,
			mailboxAgeDays(m.CreatedAt)),
	}
}

// mailboxAgeDays is how many whole days ago the mailbox was connected — the age
// the ramp schedule is measured against.
func mailboxAgeDays(createdAt pgtype.Timestamptz) int {
	return int(time.Since(createdAt.Time).Hours() / 24)
}
