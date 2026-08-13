//go:build integration

package deliverability

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/deliverability"
)

// fixture is one workspace with a mailbox, a list, a contact-producing helper and
// a RUNNING campaign — the minimum needed for the breaker to have something to
// stop.
type fixture struct {
	pool     *pgxpool.Pool
	q        *gen.Queries
	store    *PgStore
	ws       uuid.UUID
	mailbox  uuid.UUID
	list     uuid.UUID
	campaign uuid.UUID
}

func newFixture(t *testing.T, ctx context.Context) *fixture {
	t.Helper()
	if err := db.Migrate(dbtest.DSN(t)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dbtest.DSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	f := &fixture{pool: pool, q: gen.New(pool), store: NewPgStore(pool)}
	f.ws, f.mailbox, f.list, f.campaign = seedTenant(t, ctx, f.q, f.pool)
	return f
}

// seedTenant creates an isolated workspace with one mailbox, one list and one
// RUNNING campaign, and returns their ids. Used for the primary tenant and again
// for the foreign tenant in the cross-tenant tests.
func seedTenant(t *testing.T, ctx context.Context, q *gen.Queries, pool *pgxpool.Pool) (ws, mailbox, list, campaign uuid.UUID) {
	t.Helper()
	w, err := q.CreateWorkspace(ctx, "Deliverability IT "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	email := uuid.NewString()[:8] + "@" + senderDomain
	mb, err := q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: w.ID, Provider: "smtp", Email: email, DisplayName: "IT",
		SmtpHost: "smtp.example.test", SmtpPort: 587, SmtpUsername: email,
		ImapHost: "imap.example.test", ImapPort: 993, ImapUsername: email,
		SecretCiphertext: "ct", DailyCap: 500, MinIntervalSeconds: 0,
		RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("mailbox: %v", err)
	}
	l, err := q.CreateList(ctx, gen.CreateListParams{WorkspaceID: w.ID, Name: "L"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	c, err := q.CreateCampaign(ctx, gen.CreateCampaignParams{
		WorkspaceID: w.ID, Name: "C", MailboxID: mb.ID, ListID: l.ID,
		Subject: "Hi", BodyText: "b", BodyHtml: "",
	})
	if err != nil {
		t.Fatalf("campaign: %v", err)
	}
	// Running, and supervised since well before anything these tests seed.
	//
	// guardrails_enabled_at defaults to now() (campaign creation), and the floor
	// excludes evidence from before it — so a fixture that back-dates its sends, as
	// most of these do, would be judged on a sample of one. Backdating the floor
	// models the ordinary case: a campaign created a while ago, supervised the whole
	// time, sending ever since. The migration-day tests override it explicitly, which
	// is the only place the floor is the subject rather than the setting.
	if _, err := pool.Exec(ctx,
		`UPDATE campaigns SET status = 'running', guardrails_enabled_at = now() - interval '90 days'
		 WHERE id = $1 AND workspace_id = $2`,
		c.ID, w.ID); err != nil {
		t.Fatalf("running: %v", err)
	}
	return w.ID, mb.ID, l.ID, c.ID
}

// seedMailbox adds a SECOND mailbox to the workspace, one that is NOT in the
// campaign's sender pool. It is how the tests tell the campaign-scoped signals
// query apart from the workspace-scoped one: a signal on this mailbox must reach
// the workspace rollup and must not reach the campaign's score.
func (f *fixture) seedMailbox(t *testing.T, ctx context.Context, domain string) uuid.UUID {
	t.Helper()
	email := uuid.NewString()[:8] + "@" + domain
	mb, err := f.q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: f.ws, Provider: "smtp", Email: email, DisplayName: "IT2",
		SmtpHost: "smtp.example.test", SmtpPort: 587, SmtpUsername: email,
		ImapHost: "imap.example.test", ImapPort: 993, ImapUsername: email,
		SecretCiphertext: "ct", DailyCap: 500, MinIntervalSeconds: 0,
		RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("second mailbox: %v", err)
	}
	return mb.ID
}

// seedWarmupParticipant opts a mailbox into warmup at a given health state — the
// verdict the warmup engine has already reached, which the score inherits rather
// than re-deriving.
func (f *fixture) seedWarmupParticipant(t *testing.T, ctx context.Context, mailbox uuid.UUID, state, reason string) {
	t.Helper()
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO warmup_participants (mailbox_id, workspace_id, health_state, health_reason)
		 VALUES ($1,$2,$3,$4)
		 ON CONFLICT (mailbox_id) DO UPDATE SET health_state = EXCLUDED.health_state,
		                                        health_reason = EXCLUDED.health_reason`,
		mailbox, f.ws, state, reason); err != nil {
		t.Fatalf("warmup participant %s: %v", state, err)
	}
}

// seedPlacement records SENDER-attributed inbox-vs-spam placement for one UTC day,
// the shape RecordWarmupSenderPlacementStat writes.
func (f *fixture) seedPlacement(t *testing.T, ctx context.Context, mailbox uuid.UUID, day time.Time, inbox, spam int) {
	t.Helper()
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO warmup_daily_stats (mailbox_id, workspace_id, day, inbox, spam)
		 VALUES ($1,$2,$3::date,$4,$5)
		 ON CONFLICT (mailbox_id, day) DO UPDATE SET inbox = EXCLUDED.inbox, spam = EXCLUDED.spam`,
		mailbox, f.ws, day.UTC().Format(time.DateOnly), inbox, spam); err != nil {
		t.Fatalf("placement: %v", err)
	}
}

// seedSendingDomain caches a DNS verdict for a domain, as the domainauth sweep does.
func (f *fixture) seedSendingDomain(t *testing.T, ctx context.Context, ws uuid.UUID, domain, state string, spf, dmarc bool) {
	t.Helper()
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO sending_domains (workspace_id, domain, state, spf_found, dmarc_found, checked_at)
		 VALUES ($1,$2,$3,$4,$5,now())
		 ON CONFLICT (workspace_id, domain) DO UPDATE SET state = EXCLUDED.state,
		     spf_found = EXCLUDED.spf_found, dmarc_found = EXCLUDED.dmarc_found`,
		ws, domain, state, spf, dmarc); err != nil {
		t.Fatalf("sending domain: %v", err)
	}
}

// seedSend inserts one 'sent' send for a fresh contact and returns both ids. The
// send is what `delivered` counts; sentAt places it inside or outside the window.
func (f *fixture) seedSend(t *testing.T, ctx context.Context, sentAt time.Time) (sendID, contactID uuid.UUID) {
	t.Helper()
	return seedSendFor(t, ctx, f.q, f.pool, f.ws, f.campaign, f.mailbox, f.list, sentAt)
}

func seedSendFor(
	t *testing.T, ctx context.Context, q *gen.Queries, pool *pgxpool.Pool,
	ws, campaign, mailbox, list uuid.UUID, sentAt time.Time,
) (sendID, contactID uuid.UUID) {
	t.Helper()
	email := uuid.NewString() + "@recipient.test"
	c, err := q.UpsertContact(ctx, gen.UpsertContactParams{
		WorkspaceID: ws, Email: email, FirstName: "C",
	})
	if err != nil {
		t.Fatalf("contact: %v", err)
	}
	if err := q.AddListMember(ctx, gen.AddListMemberParams{ListID: list, ContactID: c.ID}); err != nil {
		t.Fatalf("member: %v", err)
	}
	var id uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO sends (workspace_id, campaign_id, contact_id, mailbox_id, to_email, status, sent_at)
		 VALUES ($1,$2,$3,$4,$5,'sent',$6) RETURNING id`,
		ws, campaign, c.ID, mailbox, email, sentAt).Scan(&id); err != nil {
		t.Fatalf("send: %v", err)
	}
	return id, c.ID
}

// seedBouncedEnrollment records the internal hard-bounce signal: an enrollment
// stopped 'bounced'. This is what the inbox poller produces, and the primary
// bounce source the breaker reads.
func (f *fixture) seedBouncedEnrollment(t *testing.T, ctx context.Context, contactID uuid.UUID, at time.Time) {
	t.Helper()
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO sequence_enrollments (workspace_id, campaign_id, contact_id, status, stop_reason, stopped_at)
		 VALUES ($1,$2,$3,'stopped','bounced',$4)`,
		f.ws, f.campaign, contactID, at); err != nil {
		t.Fatalf("bounced enrollment: %v", err)
	}
}

// seedCampaign builds a campaign with `delivered` sends of which `bounced` bounced,
// all inside the rolling window.
//
// The sends are back-dated (now - i minutes); seedTenant has already put the
// supervision floor 90 days back so they all count.
func (f *fixture) seedCampaign(t *testing.T, ctx context.Context, delivered, bounced int) {
	t.Helper()
	now := time.Now()
	for i := range delivered {
		_, contactID := f.seedSend(t, ctx, now.Add(-time.Duration(i)*time.Minute))
		if i < bounced {
			f.seedBouncedEnrollment(t, ctx, contactID, now)
		}
	}
}

func (f *fixture) status(t *testing.T, ctx context.Context) string {
	t.Helper()
	var status string
	if err := f.pool.QueryRow(ctx,
		`SELECT status FROM campaigns WHERE id = $1 AND workspace_id = $2`, f.campaign, f.ws).Scan(&status); err != nil {
		t.Fatalf("status: %v", err)
	}
	return status
}

func (f *fixture) service() *Service { return NewService(f.store) }

// senderDomain is the domain every seeded mailbox sends from — shared across the
// tenants a test creates, deliberately: two workspaces sending from one domain must
// keep separate verdicts, and sharing it here is what proves they do.
const senderDomain = "sender.test"

// component finds one score component by key.
func component(t *testing.T, s deliverability.Score, key string) deliverability.Component {
	t.Helper()
	for _, c := range s.Components {
		if c.Key == key {
			return c
		}
	}
	t.Fatalf("score has no %q component", key)
	return deliverability.Component{}
}

// The non-rate signals, end to end against Postgres. Every other integration test
// leaves warmup_participants, warmup_daily_stats and sending_domains EMPTY, so the
// signals query only ever returns its COALESCE defaults — which means the SUM
// scoping, the worst-state CASE ordering, and the domain-from-mailbox-email
// subquery would all be unproven. sqlc vet cannot catch a query that PREPAREs and
// still answers the wrong thing.
//
// It also pins the dual scope: a signal on a mailbox OUTSIDE the campaign's sender
// pool must reach the workspace rollup and must NOT reach the campaign's score.
func TestWarmupAndDomainSignalsReachTheScore(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	today := time.Now().UTC()

	// The campaign's own sender: healthy, with 10% of its warmup mail in spam.
	f.seedWarmupParticipant(t, ctx, f.mailbox, "healthy", "")
	f.seedPlacement(t, ctx, f.mailbox, today, 90, 10)
	// A mailbox the campaign does NOT send from: paused, and all its mail spammed.
	outsider := f.seedMailbox(t, ctx, senderDomain)
	f.seedWarmupParticipant(t, ctx, outsider, "paused", "spam placement 61% over 7 days")
	f.seedPlacement(t, ctx, outsider, today, 0, 50)
	// And the domain both send from is failing authentication.
	f.seedSendingDomain(t, ctx, f.ws, senderDomain, "failing", false, false)

	// --- campaign scope: only the campaign's own sender counts ---
	rep, err := f.service().CampaignReport(ctx, f.ws, f.campaign)
	if err != nil {
		t.Fatalf("CampaignReport: %v", err)
	}
	warmup := component(t, rep.Score, deliverability.KeyWarmup)
	if !warmup.Measured || warmup.Penalty != 0 || warmup.Detail != "healthy" {
		t.Errorf("campaign warmup component = %+v, want measured, unpenalised, healthy — the "+
			"paused OUTSIDER is not in this campaign's pool", warmup)
	}
	spam := component(t, rep.Score, deliverability.KeySpamPlacement)
	if !spam.Measured || spam.Rate == nil || *spam.Rate != 10 {
		t.Errorf("campaign spam placement = %+v, want a measured 10%% (10 of 100 observed)", spam)
	}
	// 10% of the 40% saturation point costs a quarter of the ceiling.
	if want := deliverability.SpamPlacementPenalty / 4; spam.Penalty != want {
		t.Errorf("campaign spam penalty = %d, want %d", spam.Penalty, want)
	}
	domain := component(t, rep.Score, deliverability.KeyDomainAuth)
	if !domain.Measured || domain.Penalty != deliverability.DomainAuthPenalty {
		t.Errorf("campaign domain component = %+v, want a failing verdict penalised %d",
			domain, deliverability.DomainAuthPenalty)
	}
	if wantScore := 100 - deliverability.SpamPlacementPenalty/4 - deliverability.DomainAuthPenalty; rep.Score.Value != wantScore {
		t.Errorf("campaign score = %d, want %d", rep.Score.Value, wantScore)
	}

	// --- workspace scope: every mailbox counts, worst health wins ---
	roll, err := f.service().Report(ctx, f.ws)
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	wsWarmup := component(t, roll.Score, deliverability.KeyWarmup)
	if wsWarmup.Detail != deliverability.WarmupPaused || wsWarmup.Penalty != deliverability.WarmupPausedPenalty {
		t.Errorf("workspace warmup component = %+v, want the WORST state (paused) penalised %d — "+
			"averaging a degraded sender away is how it stays invisible",
			wsWarmup, deliverability.WarmupPausedPenalty)
	}
	// 60 spam of 150 observed = 40%, exactly the saturation point.
	wsSpam := component(t, roll.Score, deliverability.KeySpamPlacement)
	if wsSpam.Rate == nil || *wsSpam.Rate != 40 || wsSpam.Penalty != deliverability.SpamPlacementPenalty {
		t.Errorf("workspace spam placement = %+v, want 40%% at the full ceiling", wsSpam)
	}
	// 50 + 40 + 20 = 110 of penalty against 100, so the score floors at 0 rather
	// than going negative.
	if roll.Score.Value != 0 {
		t.Errorf("workspace score = %d, want 0 (the floor)", roll.Score.Value)
	}

	// --- the at-risk lists, whose row mapping nothing else exercises ---
	if len(roll.AtRiskMailboxes) != 1 {
		t.Fatalf("at-risk mailboxes = %+v, want only the paused outsider (a healthy "+
			"participant is not at risk)", roll.AtRiskMailboxes)
	}
	risk := roll.AtRiskMailboxes[0]
	if risk.Label == "" || !strings.HasPrefix(risk.Reason, "paused: ") {
		t.Errorf("at-risk mailbox = %+v, want the engine's own recorded reason", risk)
	}
	if len(roll.AtRiskDomains) != 1 || roll.AtRiskDomains[0].Label != senderDomain {
		t.Fatalf("at-risk domains = %+v, want %q", roll.AtRiskDomains, senderDomain)
	}
	// The reason names the missing records, because publishing one is the next
	// action. DKIM is deliberately absent: not-found means "no probed selector
	// matched", not "unsigned".
	if got := roll.AtRiskDomains[0].Reason; got != "no SPF or DMARC record" {
		t.Errorf("at-risk domain reason = %q, want %q", got, "no SPF or DMARC record")
	}
}

// An 'unknown' domain verdict is a lookup that did not complete, not a
// misconfiguration: it must be neither penalised nor listed as at risk, or
// operators get sent editing DNS that was already correct.
func TestUnknownDomainVerdictIsNotPenalisedOrListed(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	f.seedSendingDomain(t, ctx, f.ws, senderDomain, "unknown", false, false)

	roll, err := f.service().Report(ctx, f.ws)
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if c := component(t, roll.Score, deliverability.KeyDomainAuth); c.Penalty != 0 {
		t.Errorf("domain component = %+v, want no penalty for an incomplete lookup", c)
	}
	if len(roll.AtRiskDomains) != 0 {
		t.Errorf("at-risk domains = %+v, want none for an 'unknown' verdict", roll.AtRiskDomains)
	}
}

// Two workspaces sending from the SAME domain keep separate verdicts, and a
// disabled participant stops contributing health at all — nothing would ever clear
// a frozen 'paused'.
func TestDomainVerdictsAndHealthAreWorkspaceScoped(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	foreignWS, _, _, _ := seedTenant(t, ctx, f.q, f.pool)

	// The foreign tenant's copy of the shared domain is failing; ours is not checked.
	f.seedSendingDomain(t, ctx, foreignWS, senderDomain, "failing", false, false)
	f.seedWarmupParticipant(t, ctx, f.mailbox, "throttled", "spam placement 24%")

	roll, err := f.service().Report(ctx, f.ws)
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if c := component(t, roll.Score, deliverability.KeyDomainAuth); c.Measured || c.Penalty != 0 {
		t.Errorf("domain component = %+v, want unmeasured — the failing verdict is another "+
			"tenant's row for the same domain name", c)
	}
	if len(roll.AtRiskDomains) != 0 {
		t.Errorf("at-risk domains = %+v, want none (the failing row is the foreign tenant's)", roll.AtRiskDomains)
	}
	if c := component(t, roll.Score, deliverability.KeyWarmup); c.Penalty != deliverability.WarmupThrottledPenalty {
		t.Errorf("warmup component = %+v, want throttled penalised %d", c, deliverability.WarmupThrottledPenalty)
	}

	// Warmup switched off means no live health signal.
	if _, err := f.pool.Exec(ctx,
		`UPDATE warmup_participants SET enabled = false WHERE mailbox_id = $1`, f.mailbox); err != nil {
		t.Fatalf("disable warmup: %v", err)
	}
	off, err := f.service().Report(ctx, f.ws)
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if c := component(t, off.Score, deliverability.KeyWarmup); c.Measured || c.Penalty != 0 {
		t.Errorf("warmup component = %+v, want unmeasured once the participant is disabled", c)
	}
	if len(off.AtRiskMailboxes) != 0 {
		t.Errorf("at-risk mailboxes = %+v, want none once the participant is disabled", off.AtRiskMailboxes)
	}
}

// A brand-new campaign has the breaker ON at the documented defaults. That is the
// column defaults from migration 000037, read back through the real query.
func TestGuardrailDefaultsAreOnAtTheDocumentedThresholds(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	cfg, err := f.store.CampaignConfig(ctx, f.ws, f.campaign)
	if err != nil {
		t.Fatalf("CampaignConfig: %v", err)
	}
	if !cfg.AutoPauseEnabled {
		t.Error("auto-pause is off by default; a safeguard nobody enables protects nobody")
	}
	if cfg.BouncePausePct != deliverability.DefaultBouncePausePct {
		t.Errorf("bounce_pause_pct = %v, want %v", cfg.BouncePausePct, deliverability.DefaultBouncePausePct)
	}
	if cfg.ComplaintPausePct != deliverability.DefaultComplaintPausePct {
		t.Errorf("complaint_pause_pct = %v, want %v", cfg.ComplaintPausePct, deliverability.DefaultComplaintPausePct)
	}
}

// setGuardrailsEnabledAt backdates the supervision floor, standing in for a
// campaign that came under supervision at some earlier moment — which after
// migration 000037 is every campaign that already existed.
func (f *fixture) setGuardrailsEnabledAt(t *testing.T, ctx context.Context, at time.Time) {
	t.Helper()
	if _, err := f.pool.Exec(ctx,
		`UPDATE campaigns SET guardrails_enabled_at = $3 WHERE id = $1 AND workspace_id = $2`,
		f.campaign, f.ws, at); err != nil {
		t.Fatalf("set guardrails_enabled_at: %v", err)
	}
}

func (f *fixture) guardrailsEnabledAt(t *testing.T, ctx context.Context) time.Time {
	t.Helper()
	var at time.Time
	if err := f.pool.QueryRow(ctx,
		`SELECT guardrails_enabled_at FROM campaigns WHERE id = $1 AND workspace_id = $2`,
		f.campaign, f.ws).Scan(&at); err != nil {
		t.Fatalf("guardrails_enabled_at: %v", err)
	}
	return at
}

// The migration-day hazard, against a real database.
//
// 000037 adds auto_pause_enabled DEFAULT TRUE, so applying it ARMS every campaign
// that already exists — nobody opted in. guardrails_enabled_at DEFAULT now() is what
// makes that safe: the widest sample the breaker can reach is "since the migration",
// so bounces from before the feature existed cannot stop anything.
//
// The campaign here is the worst case: slow enough to fall back (under the minimum
// in the 7-day window) and terrible over its lifetime.
func TestPreSupervisionBouncesCannotPauseACampaign(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	now := time.Now()

	// Supervision began an hour ago, as it would for an existing campaign on the
	// deploy that runs 000037.
	enabledAt := now.Add(-time.Hour)
	f.setGuardrailsEnabledAt(t, ctx, enabledAt)

	// A lifetime of disaster, all of it BEFORE supervision: 200 delivered, all
	// bounced, spread over the days leading up to the migration. Inside the 7-day
	// rolling window, so only the floor excludes it.
	for i := range 200 {
		at := enabledAt.Add(-time.Duration(i+1) * time.Minute)
		_, contactID := f.seedSend(t, ctx, at)
		f.seedBouncedEnrollment(t, ctx, contactID, at)
	}
	// And a handful of clean sends since, too few to judge on.
	for i := range 5 {
		f.seedSend(t, ctx, enabledAt.Add(time.Duration(i+1)*time.Minute))
	}

	out, err := f.service().EvaluateBreaker(ctx, f.ws, f.campaign)
	if err != nil {
		t.Fatalf("EvaluateBreaker: %v", err)
	}
	if out.Paused {
		t.Fatalf("paused on pre-supervision bounces (verdict %+v)", out.Verdict)
	}
	if got := f.status(t, ctx); got != "running" {
		t.Errorf("status = %q, want running", got)
	}
	events, err := f.store.PauseEvents(ctx, f.ws, f.campaign)
	if err != nil {
		t.Fatalf("PauseEvents: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("%d pause events on deploy day", len(events))
	}

	// The converse: bounces AFTER supervision began DO stop it, so the floor is a
	// floor and not a mute.
	for i := range deliverability.MinDelivered {
		at := enabledAt.Add(time.Duration(i+10) * time.Minute)
		_, contactID := f.seedSend(t, ctx, at)
		if i < 20 {
			f.seedBouncedEnrollment(t, ctx, contactID, at)
		}
	}
	after, err := f.service().EvaluateBreaker(ctx, f.ws, f.campaign)
	if err != nil {
		t.Fatalf("EvaluateBreaker: %v", err)
	}
	if !after.Paused {
		t.Fatalf("post-supervision bounces did not stop the campaign (verdict %+v)", after.Verdict)
	}
	// And the recorded sample excludes the pre-supervision 200: it was judged on
	// what happened under supervision, not on the lifetime.
	events, err = f.store.PauseEvents(ctx, f.ws, f.campaign)
	if err != nil {
		t.Fatalf("PauseEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("%d pause events, want 1", len(events))
	}
	if events[0].Delivered > deliverability.MinDelivered+5 {
		t.Errorf("judged on %d delivered, which reaches past the supervision floor "+
			"(only %d sends exist under supervision)", events[0].Delivered, deliverability.MinDelivered+5)
	}
}

// A new campaign is supervised from creation, so the column is never zero and the
// floor never has to be defended against a NULL.
func TestNewCampaignIsSupervisedFromCreation(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	// A campaign created through the ordinary path, NOT the fixture's (which
	// backdates the floor on purpose) — so this asserts the column DEFAULT.
	fresh, err := f.q.CreateCampaign(ctx, gen.CreateCampaignParams{
		WorkspaceID: f.ws, Name: "fresh", MailboxID: f.mailbox, ListID: f.list,
		Subject: "Hi", BodyText: "b",
	})
	if err != nil {
		t.Fatalf("campaign: %v", err)
	}
	var at time.Time
	if err := f.pool.QueryRow(ctx,
		`SELECT guardrails_enabled_at FROM campaigns WHERE id = $1`, fresh.ID).Scan(&at); err != nil {
		t.Fatalf("guardrails_enabled_at: %v", err)
	}
	if at.IsZero() {
		t.Fatal("guardrails_enabled_at is zero on a new campaign")
	}
	if time.Since(at) > time.Minute {
		t.Errorf("guardrails_enabled_at = %v, want ~now for a campaign just created", at)
	}
	cfg, err := f.store.CampaignConfig(ctx, f.ws, fresh.ID)
	if err != nil {
		t.Fatalf("CampaignConfig: %v", err)
	}
	if !cfg.EnabledAt.Equal(at) {
		t.Errorf("CampaignConfig.EnabledAt = %v, want the stored %v", cfg.EnabledAt, at)
	}
}

// Switching auto-pause back ON re-stamps the floor, or the same trap reopens for
// anyone who enables it later: the breaker would act on bounces from while it was
// off. Switching it off, or saving with it already on, must NOT move the floor.
func TestEnablingAutoPauseRestampsTheSupervisionFloor(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	svc := f.service()

	// Truncated to microseconds because that is timestamptz's resolution: an
	// untruncated Go instant does not survive the round trip, and the assertions
	// below are about whether the floor MOVED, not about clock precision.
	old := time.Now().AddDate(0, 0, -30).Truncate(time.Microsecond)
	f.setGuardrailsEnabledAt(t, ctx, old)

	// Saving with auto-pause already ON leaves the floor alone: an operator editing a
	// threshold has not started a new supervision period.
	if _, err := svc.SetGuardrails(ctx, f.ws, f.campaign, Guardrails{
		AutoPauseEnabled: true, BouncePausePct: 9, ComplaintPausePct: 1.5,
	}); err != nil {
		t.Fatalf("SetGuardrails (on -> on): %v", err)
	}
	if got := f.guardrailsEnabledAt(t, ctx); !got.Equal(old) {
		t.Errorf("on->on moved the floor to %v, want the original %v", got, old)
	}

	// Turning it OFF does not move it either.
	if _, err := svc.SetGuardrails(ctx, f.ws, f.campaign, Guardrails{
		AutoPauseEnabled: false, BouncePausePct: 9, ComplaintPausePct: 1.5,
	}); err != nil {
		t.Fatalf("SetGuardrails (on -> off): %v", err)
	}
	if got := f.guardrailsEnabledAt(t, ctx); !got.Equal(old) {
		t.Errorf("on->off moved the floor to %v, want the original %v", got, old)
	}

	// Turning it back ON starts a fresh supervision period.
	if _, err := svc.SetGuardrails(ctx, f.ws, f.campaign, Guardrails{
		AutoPauseEnabled: true, BouncePausePct: 9, ComplaintPausePct: 1.5,
	}); err != nil {
		t.Fatalf("SetGuardrails (off -> on): %v", err)
	}
	restamped := f.guardrailsEnabledAt(t, ctx)
	if !restamped.After(old) {
		t.Fatalf("off->on left the floor at %v; bounces from while it was off remain actionable", restamped)
	}
	if time.Since(restamped) > time.Minute {
		t.Errorf("re-stamped floor = %v, want ~now", restamped)
	}
}

// End to end: an operator turns the breaker off, the campaign bounces badly, they
// turn it back on. The bounces from the unsupervised period must not stop it.
func TestBouncesFromWhileDisabledCannotPauseAfterReEnabling(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	svc := f.service()

	if _, err := svc.SetGuardrails(ctx, f.ws, f.campaign, Guardrails{
		AutoPauseEnabled: false, BouncePausePct: 8, ComplaintPausePct: 1.5,
	}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	// A disaster while unsupervised.
	now := time.Now()
	for i := range 200 {
		at := now.Add(-time.Duration(i+1) * time.Minute)
		_, contactID := f.seedSend(t, ctx, at)
		f.seedBouncedEnrollment(t, ctx, contactID, at)
	}
	if _, err := svc.SetGuardrails(ctx, f.ws, f.campaign, Guardrails{
		AutoPauseEnabled: true, BouncePausePct: 8, ComplaintPausePct: 1.5,
	}); err != nil {
		t.Fatalf("re-enable: %v", err)
	}

	out, err := svc.EvaluateBreaker(ctx, f.ws, f.campaign)
	if err != nil {
		t.Fatalf("EvaluateBreaker: %v", err)
	}
	if out.Paused {
		t.Fatalf("paused on bounces from while the breaker was OFF (verdict %+v)", out.Verdict)
	}
	if got := f.status(t, ctx); got != "running" {
		t.Errorf("status = %q, want running", got)
	}
}

func TestGuardrailsRoundTripThroughPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	svc := f.service()

	want := Guardrails{AutoPauseEnabled: false, BouncePausePct: 12.5, ComplaintPausePct: 0.25}
	if _, err := svc.SetGuardrails(ctx, f.ws, f.campaign, want); err != nil {
		t.Fatalf("SetGuardrails: %v", err)
	}
	got, err := svc.Guardrails(ctx, f.ws, f.campaign)
	if err != nil {
		t.Fatalf("Guardrails: %v", err)
	}
	if got != want {
		t.Errorf("round-trip = %+v, want %+v", got, want)
	}
}

// A threshold the service rejects must also be unrepresentable in the column, so
// no code path (or hand-written SQL) can persist one.
func TestDatabaseRejectsAnOutOfRangeThreshold(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	for _, bad := range []float64{0, -1, 100.5} {
		if _, err := f.pool.Exec(ctx,
			`UPDATE campaigns SET bounce_pause_pct = $3 WHERE id = $1 AND workspace_id = $2`,
			f.campaign, f.ws, bad); err == nil {
			t.Errorf("the database accepted bounce_pause_pct = %v", bad)
		}
	}
}

// Invariant 1 against a real database: 49 delivered, ALL of them bounced, and the
// campaign keeps running with no pause event. This is the case that would make
// on-by-default worse than nothing.
func TestCampaignUnderTheMinimumSampleStaysRunning(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	under := deliverability.MinDelivered - 1
	f.seedCampaign(t, ctx, under, under)

	out, err := f.service().EvaluateBreaker(ctx, f.ws, f.campaign)
	if err != nil {
		t.Fatalf("EvaluateBreaker: %v", err)
	}
	if out.Paused {
		t.Fatalf("a campaign with %d delivered at 100%% bounce was paused", under)
	}
	if got := f.status(t, ctx); got != "running" {
		t.Errorf("status = %q, want running", got)
	}
	events, err := f.store.PauseEvents(ctx, f.ws, f.campaign)
	if err != nil {
		t.Fatalf("PauseEvents: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("%d pause events recorded below the sample floor", len(events))
	}
}

// The auto-pause: status flips, the reason is recorded, and repeated evaluation
// changes nothing (the status='running' guard is the exactly-once mechanism).
func TestAutoPauseFlipsStatusAndRecordsTheReasonExactlyOnce(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	// 50 delivered, 10 bounced = 20%, comfortably over the 8% default.
	f.seedCampaign(t, ctx, deliverability.MinDelivered, 10)
	svc := f.service()

	first, err := svc.EvaluateBreaker(ctx, f.ws, f.campaign)
	if err != nil {
		t.Fatalf("EvaluateBreaker: %v", err)
	}
	if !first.Paused {
		t.Fatalf("a 20%% bounce rate over %d delivered did not pause the campaign (verdict %+v)",
			deliverability.MinDelivered, first.Verdict)
	}
	if got := f.status(t, ctx); got != "paused" {
		t.Fatalf("status = %q, want paused", got)
	}

	// Three more evaluations. None may flip anything or add an event.
	for i := range 3 {
		out, err := svc.EvaluateBreaker(ctx, f.ws, f.campaign)
		if err != nil {
			t.Fatalf("re-evaluation %d: %v", i, err)
		}
		if out.Paused {
			t.Fatalf("re-evaluation %d claimed a second pause", i)
		}
	}
	events, err := f.store.PauseEvents(ctx, f.ws, f.campaign)
	if err != nil {
		t.Fatalf("PauseEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("%d pause events after four evaluations, want 1", len(events))
	}
	ev := events[0]
	if ev.Reason != deliverability.ReasonBounceSpike || ev.Metric != deliverability.MetricBounceRate {
		t.Errorf("event reason/metric = %q/%q", ev.Reason, ev.Metric)
	}
	if ev.Threshold != deliverability.DefaultBouncePausePct {
		t.Errorf("threshold = %v, want %v", ev.Threshold, deliverability.DefaultBouncePausePct)
	}
	if ev.Delivered < deliverability.MinDelivered {
		t.Errorf("delivered = %d, below the minimum sample the breaker may act on", ev.Delivered)
	}
	if ev.Value < deliverability.DefaultBouncePausePct {
		t.Errorf("recorded value %v is below the threshold it supposedly crossed", ev.Value)
	}
	// The stored NUMERICs survive the float8 round trip: 10/50 is exactly 20%.
	if ev.Value != 20 {
		t.Errorf("value = %v, want 20", ev.Value)
	}
}

// A campaign with auto-pause turned off keeps running however bad its rates, and
// records nothing — the operator turned off the ACTION, not the measurement.
func TestDisabledGuardrailLeavesTheCampaignRunning(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	f.seedCampaign(t, ctx, deliverability.MinDelivered, deliverability.MinDelivered)
	svc := f.service()
	if _, err := svc.SetGuardrails(ctx, f.ws, f.campaign, Guardrails{
		AutoPauseEnabled: false, BouncePausePct: 8, ComplaintPausePct: 1.5,
	}); err != nil {
		t.Fatalf("SetGuardrails: %v", err)
	}

	out, err := svc.EvaluateBreaker(ctx, f.ws, f.campaign)
	if err != nil {
		t.Fatalf("EvaluateBreaker: %v", err)
	}
	if out.Paused || f.status(t, ctx) != "running" {
		t.Fatalf("paused=%v status=%q with auto-pause disabled", out.Paused, f.status(t, ctx))
	}
	// The verdict is still computed and reported.
	if out.Verdict.State != deliverability.VerdictPause {
		t.Errorf("verdict = %q, want the breach still reported", out.Verdict.State)
	}
}

// A replayed webhook must not inflate the rate. Delivering the SAME
// provider_event_id twenty times leaves one row, so the complaint count the
// breaker reads is 1 — and the campaign that would pause on 20 does not.
func TestReplayedIngestDoesNotInflateTheRate(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	f.seedCampaign(t, ctx, 100, 0)
	sendID, _ := f.seedSend(t, ctx, time.Now())
	svc := f.service()

	event := EventInput{
		Kind: "complaint", Email: "reporter@recipient.test",
		ProviderEventID: "ses-replay-1", SendID: &sendID,
	}
	first, err := svc.Ingest(ctx, f.ws, event)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if first.Duplicate {
		t.Error("the first delivery was reported as a duplicate")
	}
	for i := range 19 {
		res, err := svc.Ingest(ctx, f.ws, event)
		if err != nil {
			t.Fatalf("replay %d: %v", i, err)
		}
		if !res.Duplicate {
			t.Fatalf("replay %d was accepted as new", i)
		}
	}

	var rows int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM deliverability_events WHERE workspace_id = $1 AND provider_event_id = $2`,
		f.ws, event.ProviderEventID).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Fatalf("%d event rows after 20 deliveries, want 1", rows)
	}
	// One complaint over 101 delivered is ~0.99%, below the 1.5% threshold. Twenty
	// would have been ~19.8% and stopped the campaign.
	if f.status(t, ctx) != "running" {
		t.Errorf("status = %q; a replayed webhook stopped the campaign", f.status(t, ctx))
	}
}

// The idempotency guarantee has to hold under a genuine RACE, not just under
// sequential replay. A webhook pipeline retrying in parallel — several deliveries
// of one event arriving at once across processes — is the realistic shape, and
// ON CONFLICT DO NOTHING is what makes it safe: exactly one insert wins, so the
// rate the breaker reads cannot be inflated by concurrency either.
//
// Run under -race as part of the integration suite. What it proves that
// TestReplayedIngestDoesNotInflateTheRate cannot: that no read-then-write window
// exists in the service between "is this new?" and "record it".
func TestConcurrentIngestOfOneEventRecordsItOnce(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	// A big clean sample, so a duplicated complaint would move the rate measurably
	// without tripping the breaker on the legitimate single one.
	f.seedCampaign(t, ctx, 200, 0)
	sendID, _ := f.seedSend(t, ctx, time.Now())
	svc := f.service()

	const goroutines = 16
	event := EventInput{
		Kind: "complaint", Email: "racer@recipient.test",
		ProviderEventID: "ses-race-1", SendID: &sendID,
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		accepted int
		dupes    int
		errs     []error
		start    = make(chan struct{})
	)
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release them together, so they actually contend
			res, err := svc.Ingest(ctx, f.ws, event)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err != nil:
				errs = append(errs, err)
			case res.Duplicate:
				dupes++
			default:
				accepted++
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(errs) != 0 {
		t.Fatalf("concurrent ingest errored: %v", errs)
	}
	// EXACTLY one caller may be told it accepted a new event; the rest must see a
	// duplicate. Two "accepted" would mean two rows, or a lost update.
	if accepted != 1 || dupes != goroutines-1 {
		t.Errorf("accepted=%d duplicate=%d, want 1 and %d", accepted, dupes, goroutines-1)
	}
	var rows int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM deliverability_events WHERE workspace_id = $1 AND provider_event_id = $2`,
		f.ws, event.ProviderEventID).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Fatalf("%d event rows from %d concurrent deliveries, want 1", rows, goroutines)
	}
	// The rate is unmoved: one complaint, not sixteen.
	counts, err := f.store.CampaignCounts(ctx, f.ws, f.campaign, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("CampaignCounts: %v", err)
	}
	if counts.Complained != 1 {
		t.Errorf("complained = %d, want 1", counts.Complained)
	}
	// And the suppression is single too (it is idempotent, so this is about the
	// address being suppressed exactly once, not about a constraint violation).
	var suppressions int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM suppression WHERE workspace_id = $1 AND lower(email) = lower($2)`,
		f.ws, event.Email).Scan(&suppressions); err != nil {
		t.Fatalf("suppressions: %v", err)
	}
	if suppressions != 1 {
		t.Errorf("%d suppression rows, want 1", suppressions)
	}
	// 1 complaint over 201 delivered is ~0.5%, under the 1.5% default. Sixteen
	// would have been ~8% and stopped the campaign.
	if got := f.status(t, ctx); got != "running" {
		t.Errorf("status = %q; concurrent replays stopped the campaign", got)
	}
}

// A complaint suppresses the address workspace-wide, through the existing
// suppression table, so no future campaign in this workspace emails it again.
func TestComplaintSuppressesTheAddress(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	email := "complainer-" + uuid.NewString()[:8] + "@recipient.test"

	if _, err := f.service().Ingest(ctx, f.ws, EventInput{
		Kind: "complaint", Email: email, ProviderEventID: "ses-supp-1",
	}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	suppressed, err := f.q.IsSuppressed(ctx, gen.IsSuppressedParams{WorkspaceID: f.ws, Lower: email})
	if err != nil {
		t.Fatalf("IsSuppressed: %v", err)
	}
	if !suppressed {
		t.Fatal("a complaint did not suppress the address")
	}
	// Under its own reason, not folded into 'unsubscribe': "they reported us as
	// spam" and "they asked to stop" are different things to see in a list.
	var reason string
	if err := f.pool.QueryRow(ctx,
		`SELECT reason FROM suppression WHERE workspace_id = $1 AND lower(email) = lower($2)`,
		f.ws, email).Scan(&reason); err != nil {
		t.Fatalf("reason: %v", err)
	}
	if reason != suppressionReasonComplaint {
		t.Errorf("reason = %q, want %q", reason, suppressionReasonComplaint)
	}
	// And it is idempotent: a second complaint from the same address is not a
	// constraint violation.
	if _, err := f.service().Ingest(ctx, f.ws, EventInput{
		Kind: "complaint", Email: email, ProviderEventID: "ses-supp-2",
	}); err != nil {
		t.Fatalf("second complaint: %v", err)
	}
}

// An ingested BOUNCE counts toward the rate but does not suppress: provider bounce
// feeds include soft bounces, and suppressing forever on a temporary failure is
// not something an operator can undo.
func TestIngestedBounceCountsButDoesNotSuppress(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	sendID, _ := f.seedSend(t, ctx, time.Now())
	email := "full-" + uuid.NewString()[:8] + "@recipient.test"

	if _, err := f.service().Ingest(ctx, f.ws, EventInput{
		Kind: "bounce", Email: email, ProviderEventID: "ses-soft-1", SendID: &sendID,
	}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	suppressed, err := f.q.IsSuppressed(ctx, gen.IsSuppressedParams{WorkspaceID: f.ws, Lower: email})
	if err != nil {
		t.Fatalf("IsSuppressed: %v", err)
	}
	if suppressed {
		t.Error("an ingested bounce suppressed the address")
	}
	counts, err := f.store.CampaignCounts(ctx, f.ws, f.campaign, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("CampaignCounts: %v", err)
	}
	if counts.Bounced != 1 {
		t.Errorf("bounced = %d, want the ingested bounce counted", counts.Bounced)
	}
}

// The two bounce feeds must not double-count. A contact whose enrollment the inbox
// poller stopped 'bounced' AND whose send the provider also reported is ONE bounce,
// not two — otherwise the rate the breaker acts on is twice the real one.
func TestBothBounceFeedsForOneContactCountOnce(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	now := time.Now()
	sendID, contactID := f.seedSend(t, ctx, now)
	f.seedBouncedEnrollment(t, ctx, contactID, now)
	if _, err := f.service().Ingest(ctx, f.ws, EventInput{
		Kind: "bounce", Email: "dup@recipient.test", ProviderEventID: "ses-dup-1", SendID: &sendID,
	}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	counts, err := f.store.CampaignCounts(ctx, f.ws, f.campaign, now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("CampaignCounts: %v", err)
	}
	if counts.Bounced != 1 {
		t.Errorf("bounced = %d, want 1 (both feeds reporting the same contact)", counts.Bounced)
	}
}

// Only the rolling window is counted: evidence older than it does not reach the
// breaker, so a campaign that bounced badly and was then fixed is not judged on
// its history.
func TestRollingWindowExcludesOlderEvidence(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	now := time.Now()
	stale := now.AddDate(0, 0, -(deliverability.WindowDays + 2))

	// Ancient disaster: 60 delivered, all bounced.
	for range 60 {
		_, contactID := f.seedSend(t, ctx, stale)
		f.seedBouncedEnrollment(t, ctx, contactID, stale)
	}
	// Recent, clean, and big enough to judge on.
	for i := range deliverability.MinDelivered + 10 {
		f.seedSend(t, ctx, now.Add(-time.Duration(i)*time.Minute))
	}

	out, err := f.service().EvaluateBreaker(ctx, f.ws, f.campaign)
	if err != nil {
		t.Fatalf("EvaluateBreaker: %v", err)
	}
	if out.Paused {
		t.Fatalf("paused on evidence outside the %d-day window (verdict %+v)",
			deliverability.WindowDays, out.Verdict)
	}
	if f.status(t, ctx) != "running" {
		t.Errorf("status = %q, want running", f.status(t, ctx))
	}
}

// A campaign an operator restarted must not be re-paused on the evidence they
// already overrode. Both windows are bounded below by the last pause, so a
// restart with no NEW bad sends leaves it running.
func TestRestartedCampaignIsNotRePausedOnTheSameEvidence(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	f.seedCampaign(t, ctx, deliverability.MinDelivered, deliverability.MinDelivered)
	svc := f.service()

	if out, err := svc.EvaluateBreaker(ctx, f.ws, f.campaign); err != nil || !out.Paused {
		t.Fatalf("setup: paused=%v err=%v, want an auto-pause", out.Paused, err)
	}
	// The operator looks at it and restarts it by hand.
	if _, err := f.pool.Exec(ctx,
		`UPDATE campaigns SET status = 'running' WHERE id = $1 AND workspace_id = $2`,
		f.campaign, f.ws); err != nil {
		t.Fatalf("restart: %v", err)
	}

	out, err := svc.EvaluateBreaker(ctx, f.ws, f.campaign)
	if err != nil {
		t.Fatalf("EvaluateBreaker: %v", err)
	}
	if out.Paused {
		t.Fatal("the campaign was re-paused on evidence the operator had already acted on")
	}
	if f.status(t, ctx) != "running" {
		t.Errorf("status = %q, want running", f.status(t, ctx))
	}

	// New bad evidence after the restart DOES stop it again.
	now := time.Now()
	for range deliverability.MinDelivered {
		_, contactID := f.seedSend(t, ctx, now)
		f.seedBouncedEnrollment(t, ctx, contactID, now)
	}
	again, err := svc.EvaluateBreaker(ctx, f.ws, f.campaign)
	if err != nil {
		t.Fatalf("EvaluateBreaker: %v", err)
	}
	if !again.Paused {
		t.Fatal("fresh bad evidence after a restart did not stop the campaign")
	}
	events, err := f.store.PauseEvents(ctx, f.ws, f.campaign)
	if err != nil {
		t.Fatalf("PauseEvents: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("%d pause events, want 2 (the original and the re-pause)", len(events))
	}
}

// Cross-tenant isolation: one workspace's sends, bounces and ingested events never
// reach another's counts, scores, pause history or guardrails.
func TestScoresAndEventsAreWorkspacePinned(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	foreignWS, foreignMailbox, foreignList, foreignCampaign := seedTenant(t, ctx, f.q, f.pool)
	now := time.Now()

	// The foreign tenant is a disaster: 60 delivered, all bounced, plus complaints.
	for range 60 {
		sendID, contactID := seedSendFor(t, ctx, f.q, f.pool, foreignWS, foreignCampaign, foreignMailbox, foreignList, now)
		if _, err := f.pool.Exec(ctx,
			`INSERT INTO sequence_enrollments (workspace_id, campaign_id, contact_id, status, stop_reason, stopped_at)
			 VALUES ($1,$2,$3,'stopped','bounced',$4)`,
			foreignWS, foreignCampaign, contactID, now); err != nil {
			t.Fatalf("foreign bounce: %v", err)
		}
		if _, err := f.pool.Exec(ctx,
			`INSERT INTO deliverability_events (workspace_id, kind, email, send_id, provider_event_id)
			 VALUES ($1,'complaint','x@y.test',$2,$3)`,
			foreignWS, sendID, uuid.NewString()); err != nil {
			t.Fatalf("foreign complaint: %v", err)
		}
	}
	// Our tenant is clean, with enough sample to be judged.
	for i := range deliverability.MinDelivered + 10 {
		f.seedSend(t, ctx, now.Add(-time.Duration(i)*time.Minute))
	}
	svc := f.service()

	counts, err := f.store.CampaignCounts(ctx, f.ws, f.campaign, now.AddDate(0, 0, -deliverability.WindowDays))
	if err != nil {
		t.Fatalf("CampaignCounts: %v", err)
	}
	if counts.Bounced != 0 || counts.Complained != 0 {
		t.Errorf("our counts = %+v; the foreign tenant's events leaked in", counts)
	}
	// And the complaint FEED flag is per-workspace too: the foreign tenant has one,
	// we do not, so our complaint component stays unmeasured rather than borrowing
	// theirs.
	if counts.ComplaintFeed {
		t.Error("our workspace reports a complaint feed it never received an event on")
	}

	out, err := svc.EvaluateBreaker(ctx, f.ws, f.campaign)
	if err != nil {
		t.Fatalf("EvaluateBreaker: %v", err)
	}
	if out.Paused {
		t.Fatal("our clean campaign was paused by another tenant's bounces")
	}

	// A foreign campaign id is a 404, not another tenant's data.
	if _, err := svc.CampaignReport(ctx, f.ws, foreignCampaign); !errors.Is(err, ErrNotFound) {
		t.Errorf("CampaignReport(foreign campaign) error = %v, want ErrNotFound", err)
	}
	if _, err := svc.Guardrails(ctx, f.ws, foreignCampaign); !errors.Is(err, ErrNotFound) {
		t.Errorf("Guardrails(foreign campaign) error = %v, want ErrNotFound", err)
	}
	if _, err := svc.SetGuardrails(ctx, f.ws, foreignCampaign, Guardrails{
		AutoPauseEnabled: false, BouncePausePct: 99, ComplaintPausePct: 99,
	}); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetGuardrails(foreign campaign) error = %v, want ErrNotFound", err)
	}
	// The foreign campaign's settings survived the attempt untouched.
	var enabled bool
	if err := f.pool.QueryRow(ctx,
		`SELECT auto_pause_enabled FROM campaigns WHERE id = $1`, foreignCampaign).Scan(&enabled); err != nil {
		t.Fatalf("foreign settings: %v", err)
	}
	if !enabled {
		t.Error("a cross-tenant SetGuardrails disabled another workspace's breaker")
	}
	// The foreign workspace's own report still sees its own disaster, so the
	// isolation is a filter and not an accident of empty data.
	foreignScore, err := svc.Report(ctx, foreignWS)
	if err != nil {
		t.Fatalf("Report(foreign): %v", err)
	}
	if foreignScore.Score.Value >= 100 {
		t.Errorf("foreign workspace scores %d despite 100%% bounces", foreignScore.Score.Value)
	}
}

// An ingest naming ANOTHER tenant's send stores no send_id rather than failing or
// attributing to their campaign: the send is resolved by a workspace-pinned
// SELECT, so a foreign id simply resolves to nothing.
func TestIngestWithAForeignSendIDAttributesToNoCampaign(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	foreignWS, foreignMailbox, foreignList, foreignCampaign := seedTenant(t, ctx, f.q, f.pool)
	foreignSend, _ := seedSendFor(t, ctx, f.q, f.pool, foreignWS, foreignCampaign, foreignMailbox, foreignList, time.Now())

	if _, err := f.service().Ingest(ctx, f.ws, EventInput{
		Kind: "complaint", Email: "a@b.test", ProviderEventID: "cross-1", SendID: &foreignSend,
	}); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	var stored *uuid.UUID
	if err := f.pool.QueryRow(ctx,
		`SELECT send_id FROM deliverability_events WHERE workspace_id = $1 AND provider_event_id = 'cross-1'`,
		f.ws).Scan(&stored); err != nil {
		t.Fatalf("stored event: %v", err)
	}
	if stored != nil {
		t.Errorf("send_id = %v, want NULL for a foreign send", stored)
	}
	// The foreign campaign's counts are untouched.
	counts, err := f.store.CampaignCounts(ctx, foreignWS, foreignCampaign, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("CampaignCounts(foreign): %v", err)
	}
	if counts.Complained != 0 {
		t.Errorf("foreign complained = %d; a cross-tenant ingest attributed to their campaign", counts.Complained)
	}
}

// A complaint arriving with no new sends must be able to stop a campaign on its
// own: the ingest path triggers its own evaluation.
func TestIngestedComplaintSpikePausesTheCampaign(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	f.seedCampaign(t, ctx, deliverability.MinDelivered, 0)
	svc := f.service()

	// Each complaint needs its own send so it attributes to the campaign. One
	// complaint over 51 delivered is already 1.96%, over the 1.5% default, so the
	// FIRST ingest stops it and the remaining four find it already paused.
	for i := range 5 {
		sendID, _ := f.seedSend(t, ctx, time.Now())
		if _, err := svc.Ingest(ctx, f.ws, EventInput{
			Kind: "complaint", Email: uuid.NewString() + "@recipient.test",
			ProviderEventID: "spike-" + uuid.NewString(), SendID: &sendID,
		}); err != nil {
			t.Fatalf("Ingest %d: %v", i, err)
		}
	}

	if got := f.status(t, ctx); got != "paused" {
		t.Fatalf("status = %q, want paused by the complaint spike", got)
	}
	events, err := f.store.PauseEvents(ctx, f.ws, f.campaign)
	if err != nil {
		t.Fatalf("PauseEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("%d pause events, want 1", len(events))
	}
	if events[0].Reason != deliverability.ReasonComplaintSpike ||
		events[0].Metric != deliverability.MetricComplaintRate {
		t.Errorf("event = %+v, want a complaint_spike / complaint_rate", events[0])
	}
}

// The workspace rollup and the per-day series come back coherently from real SQL:
// the series covers every UTC day in the window, and its delivered counts sum to
// the score's sample.
func TestWorkspaceReportSeriesCoversTheWindow(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	now := time.Now().UTC()
	// One send today, one two days ago.
	f.seedSend(t, ctx, now.Add(-2*time.Hour))
	f.seedSend(t, ctx, now.AddDate(0, 0, -2))

	rep, err := f.service().Report(ctx, f.ws)
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	// generate_series from (today - 7) to today inclusive.
	if len(rep.Series) != deliverability.WindowDays+1 {
		t.Errorf("%d series points, want %d", len(rep.Series), deliverability.WindowDays+1)
	}
	total := 0
	for _, p := range rep.Series {
		total += p.Delivered
		if p.ComplaintMeasured {
			t.Errorf("series claims a complaint count with no feed: %+v", p)
		}
	}
	if total != rep.Score.Delivered {
		t.Errorf("series sums to %d delivered, score says %d", total, rep.Score.Delivered)
	}
	if rep.Score.Delivered != 2 {
		t.Errorf("delivered = %d, want 2", rep.Score.Delivered)
	}
}

// A campaign report's score and its verdict describe the same evidence, and the
// pause event carries everything the UI needs to explain the stop.
func TestCampaignReportExplainsAnAutoPause(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	f.seedCampaign(t, ctx, deliverability.MinDelivered, 10)
	svc := f.service()
	if out, err := svc.EvaluateBreaker(ctx, f.ws, f.campaign); err != nil || !out.Paused {
		t.Fatalf("setup: paused=%v err=%v", out.Paused, err)
	}

	rep, err := svc.CampaignReport(ctx, f.ws, f.campaign)
	if err != nil {
		t.Fatalf("CampaignReport: %v", err)
	}
	if rep.Verdict != verdictPaused {
		t.Errorf("verdict = %q, want %q", rep.Verdict, verdictPaused)
	}
	if len(rep.PauseEvents) != 1 {
		t.Fatalf("%d pause events, want 1", len(rep.PauseEvents))
	}
	ev := rep.PauseEvents[0]
	if ev.Value <= ev.Threshold-0.0001 || ev.Delivered < deliverability.MinDelivered || ev.CreatedAt.IsZero() {
		t.Errorf("pause event = %+v, want a value at/over the threshold on a valid sample", ev)
	}
	if !rep.Guardrails.AutoPauseEnabled {
		t.Error("guardrails report auto-pause off on a campaign the breaker just stopped")
	}
	// The report on a PAUSED campaign shows the evidence that stopped it, not an
	// empty since-the-restart window: the last-pause bound applies only while the
	// campaign is running, so an operator opening the page sees the real numbers.
	if rep.Score.Delivered != deliverability.MinDelivered {
		t.Errorf("score sample = %d, want the %d delivered that were judged",
			rep.Score.Delivered, deliverability.MinDelivered)
	}
	// 20% bounce is past the 10% saturation point, so the bounce component costs
	// its full ceiling.
	if rep.Score.Value != 100-deliverability.BouncePenalty {
		t.Errorf("score = %d, want %d", rep.Score.Value, 100-deliverability.BouncePenalty)
	}
}

// warmupCampaignHardBounces refreshes the workspace's warmup signal snapshot —
// the SAME statement the evaluation sweep runs — and returns what it computed for
// one mailbox. Reading the real snapshot, rather than asserting on the stored
// column, is the point: the defect this covers was a rate whose arm filtered on a
// column nothing ever populated, so a test that only checked the row would have
// passed throughout.
func (f *fixture) warmupCampaignHardBounces(t *testing.T, ctx context.Context, mailbox uuid.UUID) int32 {
	t.Helper()
	if _, err := f.q.UpsertWarmupSignalSnapshotsForWorkspace(ctx, f.ws); err != nil {
		t.Fatalf("refresh warmup snapshot: %v", err)
	}
	var n int32
	if err := f.pool.QueryRow(ctx,
		`SELECT campaign_asserted_hard_bounces FROM warmup_signal_snapshots
		  WHERE workspace_id = $1 AND mailbox_id = $2`, f.ws, mailbox).Scan(&n); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	return n
}

// TestOnlyAHardIngestedBounceFeedsTheWarmupHardBounceRate is the end-to-end proof
// that the classification actually arrives where it is read. The warmup snapshot
// filters bounce_class = 'hard'; before ingest populated the column every row was
// 'unknown', so that arm matched nothing and the rule above it could never fire.
//
// Soft and unclassified stay OUT of the numerator deliberately: counting a
// greylist or a full mailbox as permanent pauses a healthy sender for 72 hours,
// while under-counting only delays a true signal.
//
// The arm read here is campaign_ASSERTED_hard_bounces, which is where a
// feed-reported bounce belongs: it is evidence Inroad did not observe itself, so
// the policy caps it at watch and it can never contain a mailbox (invariant 40,
// TestAssertedBouncesAdviseButCannotContain). campaign_hard_bounces holds only the
// self-observed arm. What this test proves is that the classification survives
// ingest and reaches the snapshot — not that it can quarantine anything.
func TestOnlyAHardIngestedBounceFeedsTheWarmupHardBounceRate(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	f.seedWarmupParticipant(t, ctx, f.mailbox, "unknown", "seed")

	for _, class := range []string{BounceClassSoft, BounceClassUnknown, ""} {
		sendID, _ := f.seedSend(t, ctx, time.Now())
		if _, err := f.service().Ingest(ctx, f.ws, EventInput{
			Kind: "bounce", Email: uuid.NewString() + "@recipient.test",
			ProviderEventID: "ev-" + uuid.NewString(), SendID: &sendID, BounceClass: class,
		}); err != nil {
			t.Fatalf("ingest %q: %v", class, err)
		}
	}
	if got := f.warmupCampaignHardBounces(t, ctx, f.mailbox); got != 0 {
		t.Fatalf("soft/unclassified bounces counted as hard: %d, want 0", got)
	}

	sendID, _ := f.seedSend(t, ctx, time.Now())
	if _, err := f.service().Ingest(ctx, f.ws, EventInput{
		Kind: "bounce", Email: uuid.NewString() + "@recipient.test",
		ProviderEventID: "ev-" + uuid.NewString(), SendID: &sendID, BounceClass: BounceClassHard,
	}); err != nil {
		t.Fatalf("ingest hard: %v", err)
	}
	if got := f.warmupCampaignHardBounces(t, ctx, f.mailbox); got != 1 {
		t.Fatalf("hard bounce did not reach the warmup rate: got %d, want 1", got)
	}
}
