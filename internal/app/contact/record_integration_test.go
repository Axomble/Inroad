//go:build integration

package contact

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// recordFixture is one workspace with a full outreach history for a single
// contact, plus a second workspace holding a decoy contact. Rows are written
// directly so timestamps are deterministic; nothing here goes through a service
// method that owns state machine invariants.
type recordFixture struct {
	pool      *pgxpool.Pool
	svc       *Service
	ws        uuid.UUID
	other     uuid.UUID
	contactID uuid.UUID
	companyID uuid.UUID
	campaign  uuid.UUID
	mailbox   uuid.UUID
	sentAt    time.Time
}

func recordSetup(t *testing.T, ctx context.Context) recordFixture {
	t.Helper()
	pool := connect(t, ctx)
	f := recordFixture{
		pool:   pool,
		ws:     newWorkspace(t, ctx, pool, "Record"),
		other:  newWorkspace(t, ctx, pool, "Record Other"),
		sentAt: time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC),
	}
	f.svc = NewService(NewPgStore(pool), dbListChecker{pool: pool}, NewPgFieldStore(gen.New(pool)))

	f.companyID = f.company(t, ctx, f.ws, "Acme", "acme.test")
	f.contactID = f.contact(t, ctx, f.ws, "dana@acme.test", &f.companyID)
	f.mailbox = f.scalar(t, ctx, `INSERT INTO mailboxes(workspace_id,email,secret_ciphertext)
	 VALUES($1,'seller@inroad.test','sealed') RETURNING id`, f.ws)
	list := f.scalar(t, ctx, `INSERT INTO lists(workspace_id,name) VALUES($1,'Prospects') RETURNING id`, f.ws)
	f.campaign = f.scalar(t, ctx, `INSERT INTO campaigns(workspace_id,name,mailbox_id,list_id,subject,status)
	 VALUES($1,'Q2 outbound',$2,$3,'A quick question','running') RETURNING id`, f.ws, f.mailbox, list)
	return f
}

func (f recordFixture) scalar(t *testing.T, ctx context.Context, sql string, args ...any) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := f.pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
		t.Fatalf("seed (%s): %v", sql, err)
	}
	return id
}

func (f recordFixture) company(t *testing.T, ctx context.Context, ws uuid.UUID, name, domain string) uuid.UUID {
	t.Helper()
	return f.scalar(t, ctx,
		`INSERT INTO companies(workspace_id,name,domain) VALUES($1,$2,$3) RETURNING id`, ws, name, domain)
}

func (f recordFixture) contact(t *testing.T, ctx context.Context, ws uuid.UUID, email string, company *uuid.UUID) uuid.UUID {
	t.Helper()
	return f.scalar(t, ctx,
		`INSERT INTO contacts(workspace_id,email,first_name,last_name,job_title,company_id)
		 VALUES($1,$2,'Dana','Customer','VP Ops',$3) RETURNING id`, ws, email, company)
}

// defaultStage returns a stage of the workspace's seeded default pipeline.
func (f recordFixture) defaultStage(t *testing.T, ctx context.Context, ws uuid.UUID) (uuid.UUID, uuid.UUID) {
	t.Helper()
	var pipeline, stage uuid.UUID
	err := f.pool.QueryRow(ctx,
		`SELECT s.pipeline_id, s.id FROM pipeline_stages s
		 JOIN pipelines p ON p.id = s.pipeline_id AND p.workspace_id = s.workspace_id
		 WHERE s.workspace_id = $1 AND p.is_default ORDER BY s.position LIMIT 1`, ws).Scan(&pipeline, &stage)
	if err != nil {
		t.Fatalf("default stage: %v", err)
	}
	return pipeline, stage
}

func (f recordFixture) deal(t *testing.T, ctx context.Context, ws, company, contact uuid.UUID, name string) uuid.UUID {
	t.Helper()
	pipeline, stage := f.defaultStage(t, ctx, ws)
	return f.scalar(t, ctx,
		`INSERT INTO deals(workspace_id,pipeline_id,stage_id,company_id,primary_contact_id,name,created_by_actor)
		 VALUES($1,$2,$3,$4,$5,$6,'{"type":"system"}'::jsonb) RETURNING id`,
		ws, pipeline, stage, company, contact, name)
}

// send writes one send at the given step and returns its id.
func (f recordFixture) send(t *testing.T, ctx context.Context, step int, status string, sentAt *time.Time) uuid.UUID {
	t.Helper()
	return f.scalar(t, ctx,
		`INSERT INTO sends(workspace_id,campaign_id,contact_id,mailbox_id,to_email,status,step_order,sent_at)
		 VALUES($1,$2,$3,$4,'dana@acme.test',$5,$6,$7) RETURNING id`,
		f.ws, f.campaign, f.contactID, f.mailbox, status, step, sentAt)
}

// track seeds a tracking event directly, so it must supply the HUMAN/MACHINE
// verdict the tracking service's classifier (platform/botfilter) would have
// assigned at write time. The exclusion of prefetches is no longer re-derived
// by the reading query from the UA and timestamp -- it reads the stored
// column -- so a fixture that omits the verdict is claiming every seeded event
// was a genuine human one.
func (f recordFixture) track(t *testing.T, ctx context.Context, sendID uuid.UUID, kind, userAgent string, at time.Time) {
	t.Helper()
	f.trackAs(t, ctx, sendID, kind, userAgent, at, "")
}

// trackAs seeds an event with an explicit machine reason ("" = a human event).
func (f recordFixture) trackAs(t *testing.T, ctx context.Context, sendID uuid.UUID, kind, userAgent string, at time.Time, machineReason string) {
	t.Helper()
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO tracking_events(workspace_id,campaign_id,send_id,kind,user_agent,created_at,is_machine,machine_reason)
		 VALUES($1,$2,$3,$4::tracking_event_kind,$5,$6,$7,$8)`,
		f.ws, f.campaign, sendID, kind, userAgent, at, machineReason != "", machineReason); err != nil {
		t.Fatalf("tracking event: %v", err)
	}
}

// The record page's relational half, read out of Postgres: the company link
// resolves to the real companies row and the deals carry their stage fields.
func TestRecordReadsCompanyAndDeals(t *testing.T) {
	ctx := context.Background()
	f := recordSetup(t, ctx)
	dealID := f.deal(t, ctx, f.ws, f.companyID, f.contactID, "Acme rollout")
	// A deal on the same company but a different contact must NOT appear.
	other := f.contact(t, ctx, f.ws, "sam@acme.test", &f.companyID)
	f.deal(t, ctx, f.ws, f.companyID, other, "Someone else's deal")

	got, err := f.svc.Record(ctx, f.ws, f.contactID)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if got.Email != "dana@acme.test" || got.JobTitle != "VP Ops" {
		t.Fatalf("record = %+v", got)
	}
	if got.Company == nil || got.Company.ID != f.companyID || got.Company.Name != "Acme" || got.Company.Domain != "acme.test" {
		t.Fatalf("company = %+v, want the linked Acme row", got.Company)
	}
	if len(got.Deals) != 1 || got.Deals[0].ID != dealID {
		t.Fatalf("deals = %+v, want only the contact's own deal", got.Deals)
	}
	if got.Deals[0].StageLabel != "Lead" || got.Deals[0].StageColor == "" {
		t.Fatalf("deal stage not joined: %+v", got.Deals[0])
	}
	if got.DealsTruncated {
		t.Fatal("deals_truncated on a one-deal contact")
	}
}

// A contact with no company link reads as no company, not as an empty one.
func TestRecordWithoutCompany(t *testing.T) {
	ctx := context.Background()
	f := recordSetup(t, ctx)
	solo := f.contact(t, ctx, f.ws, "solo@nowhere.test", nil)

	got, err := f.svc.Record(ctx, f.ws, solo)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if got.Company != nil {
		t.Fatalf("company = %+v, want nil", got.Company)
	}
	if len(got.Deals) != 0 {
		t.Fatalf("deals = %+v, want none", got.Deals)
	}
}

// The cap is enforced by the SQL LIMIT, not only by the service: a contact with
// more deals than the cap returns exactly DealCap of them and says so.
func TestRecordDealCapHoldsAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := recordSetup(t, ctx)
	for i := 0; i <= DealCap; i++ {
		f.deal(t, ctx, f.ws, f.companyID, f.contactID, "Deal")
	}

	got, err := f.svc.Record(ctx, f.ws, f.contactID)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if len(got.Deals) != DealCap || !got.DealsTruncated {
		t.Fatalf("deals = %d (truncated %v), want %d and true", len(got.Deals), got.DealsTruncated, DealCap)
	}
}

// The engagement rollup end to end: sent counts, the machine-open exclusion
// that makes an "indicative" open indicative, clicks, stop-reason outcomes, and
// the rates those imply.
func TestEngagementRollupAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := recordSetup(t, ctx)

	first := f.send(t, ctx, 1, "sent", &f.sentAt)
	secondAt := f.sentAt.Add(48 * time.Hour)
	second := f.send(t, ctx, 2, "sent", &secondAt)
	// A queued send is not a sent one and must not enter the denominator.
	f.send(t, ctx, 3, "queued", nil)

	// Gmail's image proxy fetches the pixel on receipt: classified machine by
	// UA at write time, so excluded from the indicative count.
	f.trackAs(t, ctx, first, "open", "GoogleImageProxy/1.0", f.sentAt.Add(time.Second), "proxy_user_agent")
	// A fetch within two seconds of the send is the same prefetch behaviour,
	// UA-agnostic (this is the Apple MPP shape): excluded too.
	f.trackAs(t, ctx, second, "open", "Mozilla/5.0", secondAt.Add(time.Second), "prefetch_window")
	// A real open, well after the send.
	lastEvent := f.sentAt.Add(72 * time.Hour)
	f.track(t, ctx, first, "open", "Mozilla/5.0", lastEvent)
	// Two clicks on one send still count as one engaged send.
	f.track(t, ctx, first, "click", "Mozilla/5.0", lastEvent)
	f.track(t, ctx, first, "click", "Mozilla/5.0", lastEvent)

	if _, err := f.pool.Exec(ctx, `INSERT INTO sequence_enrollments
	 (workspace_id,campaign_id,contact_id,status,stop_reason,stopped_at,enrolled_at,last_sent_at)
	 VALUES($1,$2,$3,'stopped','replied',$4,$4,$4)`,
		f.ws, f.campaign, f.contactID, f.sentAt); err != nil {
		t.Fatalf("enrollment: %v", err)
	}

	got, err := f.svc.Engagement(ctx, f.ws, f.contactID)
	if err != nil {
		t.Fatalf("Engagement: %v", err)
	}
	checks := []struct {
		name string
		got  int64
		want int64
	}{
		{"emails_sent", got.EmailsSent, 2},
		{"opens_indicative", got.OpensIndicative, 1},
		{"clicks", got.Clicks, 1},
		{"replies", got.Replies, 1},
		{"bounces", got.Bounces, 0},
		{"unsubscribes", got.Unsubscribes, 0},
		{"campaigns_enrolled", got.CampaignsEnrolled, 1},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
	if got.OpenRate != 0.5 || got.ClickRate != 0.5 {
		t.Errorf("open/click rate = %v/%v, want 0.5/0.5", got.OpenRate, got.ClickRate)
	}
	if got.LastActivityAt == nil || !got.LastActivityAt.Equal(lastEvent) {
		t.Errorf("last_activity_at = %v, want the last tracking event %v", got.LastActivityAt, lastEvent)
	}
	if len(got.Campaigns) != 1 || got.Campaigns[0].CampaignName != "Q2 outbound" ||
		got.Campaigns[0].StopReason == nil || *got.Campaigns[0].StopReason != stopReasonReplied {
		t.Errorf("campaigns = %+v", got.Campaigns)
	}
}

// A contact who has never been sent to reads as zeroes out of a real database,
// where the aggregates come back as SQL NULLs rather than as Go zero values.
func TestEngagementWithNoHistoryAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := recordSetup(t, ctx)

	got, err := f.svc.Engagement(ctx, f.ws, f.contactID)
	if err != nil {
		t.Fatalf("Engagement: %v", err)
	}
	if got.EmailsSent != 0 || got.OpensIndicative != 0 || got.Clicks != 0 || got.CampaignsEnrolled != 0 {
		t.Fatalf("counts = %+v, want all zero", got)
	}
	if got.OpenRate != 0 || got.ClickRate != 0 {
		t.Fatalf("rates = %v/%v, want 0/0", got.OpenRate, got.ClickRate)
	}
	if got.LastActivityAt != nil {
		t.Fatalf("last_activity_at = %v, want nil", got.LastActivityAt)
	}
	if len(got.Campaigns) != 0 {
		t.Fatalf("campaigns = %+v, want none", got.Campaigns)
	}
}

// The enrollment list is capped by SQL too, and the counts stay exact past it.
func TestEngagementCampaignCapHoldsAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := recordSetup(t, ctx)
	list := f.scalar(t, ctx, `INSERT INTO lists(workspace_id,name) VALUES($1,'More') RETURNING id`, f.ws)
	total := CampaignCap + 1
	if _, err := f.pool.Exec(ctx, `
		WITH extra AS (
		  INSERT INTO campaigns(workspace_id,name,mailbox_id,list_id,subject)
		  SELECT $1, 'Campaign ' || n, $2, $3, 'Subject'
		  FROM generate_series(1, $4) AS n
		  RETURNING id
		)
		INSERT INTO sequence_enrollments(workspace_id,campaign_id,contact_id)
		SELECT $1, id, $5 FROM extra`,
		f.ws, f.mailbox, list, total, f.contactID); err != nil {
		t.Fatalf("seed enrollments: %v", err)
	}

	got, err := f.svc.Engagement(ctx, f.ws, f.contactID)
	if err != nil {
		t.Fatalf("Engagement: %v", err)
	}
	if len(got.Campaigns) != CampaignCap || !got.CampaignsTruncated {
		t.Fatalf("campaigns = %d (truncated %v), want %d and true",
			len(got.Campaigns), got.CampaignsTruncated, CampaignCap)
	}
	if got.CampaignsEnrolled != int64(total) {
		t.Fatalf("campaigns_enrolled = %d, want the exact %d despite truncation", got.CampaignsEnrolled, total)
	}
}

// Tenant isolation, asserted against the database rather than a fake: the same
// contact id read from another workspace is ErrNotFound, and the decoy contact
// that workspace really does own never leaks either direction.
func TestRecordAndEngagementAreWorkspacePinned(t *testing.T) {
	ctx := context.Background()
	f := recordSetup(t, ctx)
	f.deal(t, ctx, f.ws, f.companyID, f.contactID, "Acme rollout")
	f.send(t, ctx, 1, "sent", &f.sentAt)
	decoy := f.contact(t, ctx, f.other, "dana@acme.test", nil)

	if _, err := f.svc.Record(ctx, f.other, f.contactID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace Record err = %v, want ErrNotFound", err)
	}
	if _, err := f.svc.Engagement(ctx, f.other, f.contactID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace Engagement err = %v, want ErrNotFound", err)
	}
	if _, err := f.svc.Record(ctx, f.ws, decoy); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign contact visible from its neighbour: %v", err)
	}

	// The owning workspace still sees everything, so the isolation above is a
	// tenancy filter and not a broken query.
	got, err := f.svc.Record(ctx, f.ws, f.contactID)
	if err != nil || len(got.Deals) != 1 {
		t.Fatalf("owner Record = %+v, %v", got, err)
	}
	engagement, err := f.svc.Engagement(ctx, f.ws, f.contactID)
	if err != nil || engagement.EmailsSent != 1 {
		t.Fatalf("owner Engagement = %+v, %v", engagement, err)
	}
}

// The aggregates must not pick up another workspace's rows even when the ids
// line up: a send belonging to workspace B whose tracking event carries B's
// workspace_id can only be reached through B's contact.
func TestEngagementIgnoresAnotherWorkspacesSends(t *testing.T) {
	ctx := context.Background()
	f := recordSetup(t, ctx)
	f.send(t, ctx, 1, "sent", &f.sentAt)

	// Build a complete second-workspace outreach history for its own contact.
	decoy := f.contact(t, ctx, f.other, "decoy@other.test", nil)
	mailbox := f.scalar(t, ctx, `INSERT INTO mailboxes(workspace_id,email,secret_ciphertext)
	 VALUES($1,'other@inroad.test','sealed') RETURNING id`, f.other)
	list := f.scalar(t, ctx, `INSERT INTO lists(workspace_id,name) VALUES($1,'Other') RETURNING id`, f.other)
	campaign := f.scalar(t, ctx, `INSERT INTO campaigns(workspace_id,name,mailbox_id,list_id,subject,status)
	 VALUES($1,'Other outbound',$2,$3,'Hi','running') RETURNING id`, f.other, mailbox, list)
	send := f.scalar(t, ctx, `INSERT INTO sends
	 (workspace_id,campaign_id,contact_id,mailbox_id,to_email,status,step_order,sent_at)
	 VALUES($1,$2,$3,$4,'decoy@other.test','sent',1,$5) RETURNING id`,
		f.other, campaign, decoy, mailbox, f.sentAt)
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO tracking_events(workspace_id,campaign_id,send_id,kind,user_agent,created_at)
		 VALUES($1,$2,$3,'click','Mozilla/5.0',$4)`,
		f.other, campaign, send, f.sentAt.Add(time.Hour)); err != nil {
		t.Fatalf("other tracking event: %v", err)
	}

	got, err := f.svc.Engagement(ctx, f.ws, f.contactID)
	if err != nil {
		t.Fatalf("Engagement: %v", err)
	}
	if got.EmailsSent != 1 || got.Clicks != 0 {
		t.Fatalf("engagement = sent %d clicks %d, want 1 and 0 — another workspace's rows leaked",
			got.EmailsSent, got.Clicks)
	}
}

// suppress adds an address to the workspace's suppression list.
func (f recordFixture) suppress(t *testing.T, ctx context.Context, ws uuid.UUID, email, reason string) {
	t.Helper()
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO suppression(workspace_id,email,reason) VALUES($1,$2,$3)`, ws, email, reason); err != nil {
		t.Fatalf("suppress %s: %v", email, err)
	}
}

// Every reason literal the suppression CHECK allows must survive to the record
// unchanged. Collapsing 'complaint' into 'unsubscribe' would lose the difference
// between "they asked to stop" and "they reported us as spam", which is exactly
// what migration 000037 added the reason for.
func TestRecordSuppressionCarriesEveryReasonLiteral(t *testing.T) {
	ctx := context.Background()
	f := recordSetup(t, ctx)

	for i, reason := range []string{
		SuppressionUnsubscribe, SuppressionBounce, SuppressionComplaint, SuppressionManual,
	} {
		t.Run(reason, func(t *testing.T) {
			// A workspace of its own per reason: suppression is unique per
			// (workspace, address).
			ws := newWorkspace(t, ctx, f.pool, "Suppress")
			email := "target" + string(rune('a'+i)) + "@acme.test"
			id := f.contact(t, ctx, ws, email, nil)
			f.suppress(t, ctx, ws, email, reason)

			got, err := f.svc.Record(ctx, ws, id)
			if err != nil {
				t.Fatalf("Record: %v", err)
			}
			if got.Suppression == nil {
				t.Fatal("suppression = nil for a suppressed contact")
			}
			if got.Suppression.Reason != reason {
				t.Fatalf("reason = %q, want %q", got.Suppression.Reason, reason)
			}
			if got.Suppression.Email != email || !got.Suppression.IsPrimaryEmail {
				t.Fatalf("suppression = %+v, want the primary address %s flagged", got.Suppression, email)
			}
			if got.Suppression.SuppressedAt.IsZero() {
				t.Fatal("suppressed_at is the zero time")
			}
		})
	}
}

// A contact whose address is not on the list may be emailed, and says so by
// reporting no suppression at all rather than an empty struct.
func TestRecordSuppressionAbsentWhenEmailable(t *testing.T) {
	ctx := context.Background()
	f := recordSetup(t, ctx)
	// Another contact in the same workspace IS suppressed, so a passing result
	// cannot come from an empty suppression table.
	noisy := f.contact(t, ctx, f.ws, "blocked@acme.test", nil)
	f.suppress(t, ctx, f.ws, "blocked@acme.test", SuppressionBounce)

	got, err := f.svc.Record(ctx, f.ws, f.contactID)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if got.Suppression != nil {
		t.Fatalf("suppression = %+v, want nil for an emailable contact", got.Suppression)
	}
	blocked, err := f.svc.Record(ctx, f.ws, noisy)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if blocked.Suppression == nil || blocked.Suppression.Reason != SuppressionBounce {
		t.Fatalf("suppression = %+v, want the bounce", blocked.Suppression)
	}
}

// The lookup spans the contact's whole alias set, not just contacts.email. A
// suppressed SECONDARY alias is reported with is_primary_email false: sending
// works today, but promoting that alias would silently stop it. That distinction
// is the point of returning an object rather than a boolean.
func TestRecordSuppressionFindsSecondaryAliases(t *testing.T) {
	ctx := context.Background()
	f := recordSetup(t, ctx)
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO contact_emails(workspace_id,contact_id,email,is_primary) VALUES($1,$2,$3,false)`,
		f.ws, f.contactID, "d.customer@acme.test"); err != nil {
		t.Fatalf("alias: %v", err)
	}
	f.suppress(t, ctx, f.ws, "d.customer@acme.test", SuppressionUnsubscribe)

	got, err := f.svc.Record(ctx, f.ws, f.contactID)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if got.Suppression == nil {
		t.Fatal("a suppressed alias was not reported")
	}
	if got.Suppression.Email != "d.customer@acme.test" || got.Suppression.IsPrimaryEmail {
		t.Fatalf("suppression = %+v, want the secondary alias with is_primary_email false", got.Suppression)
	}

	// Now suppress the primary too: the answer must switch to the address the
	// send path actually resolves, because that is the one that blocks sending.
	f.suppress(t, ctx, f.ws, "dana@acme.test", SuppressionComplaint)
	got, err = f.svc.Record(ctx, f.ws, f.contactID)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if got.Suppression.Email != "dana@acme.test" || !got.Suppression.IsPrimaryEmail ||
		got.Suppression.Reason != SuppressionComplaint {
		t.Fatalf("suppression = %+v, want the primary address's complaint to win", got.Suppression)
	}
}

// Suppression is workspace state: the same address suppressed in a neighbouring
// workspace must not mark this workspace's contact as unemailable.
func TestRecordSuppressionIsWorkspacePinned(t *testing.T) {
	ctx := context.Background()
	f := recordSetup(t, ctx)
	// Identical address, suppressed only in the OTHER workspace.
	f.contact(t, ctx, f.other, "dana@acme.test", nil)
	f.suppress(t, ctx, f.other, "dana@acme.test", SuppressionComplaint)

	got, err := f.svc.Record(ctx, f.ws, f.contactID)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if got.Suppression != nil {
		t.Fatalf("suppression = %+v — another workspace's suppression list leaked", got.Suppression)
	}
}

// Suppression is matched case-insensitively, the way every other lookup on this
// column is (idx_suppression_ws_email is built on lower(email)).
func TestRecordSuppressionIsCaseInsensitive(t *testing.T) {
	ctx := context.Background()
	f := recordSetup(t, ctx)
	ws := newWorkspace(t, ctx, f.pool, "Suppress Case")
	id := f.contact(t, ctx, ws, "Mixed.Case@Acme.test", nil)
	f.suppress(t, ctx, ws, "mixed.case@acme.test", SuppressionUnsubscribe)

	got, err := f.svc.Record(ctx, ws, id)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if got.Suppression == nil || got.Suppression.Reason != SuppressionUnsubscribe {
		t.Fatalf("suppression = %+v, want the unsubscribe matched case-insensitively", got.Suppression)
	}
}

// deal_count is counted by SQL independently of the LIMIT, so it stays true past
// the cap. Deriving it from the returned rows would cap the number too.
func TestRecordDealCountIsCountedPastTheCap(t *testing.T) {
	ctx := context.Background()
	f := recordSetup(t, ctx)
	total := DealCap + 3
	for i := 0; i < total; i++ {
		f.deal(t, ctx, f.ws, f.companyID, f.contactID, "Deal")
	}
	// A deal on another contact must not inflate the count.
	other := f.contact(t, ctx, f.ws, "sam@acme.test", &f.companyID)
	f.deal(t, ctx, f.ws, f.companyID, other, "Not theirs")

	got, err := f.svc.Record(ctx, f.ws, f.contactID)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if got.DealCount != int64(total) {
		t.Fatalf("deal_count = %d, want the true total %d", got.DealCount, total)
	}
	if len(got.Deals) != DealCap || !got.DealsTruncated {
		t.Fatalf("deals = %d (truncated %v), want the cap %d and true",
			len(got.Deals), got.DealsTruncated, DealCap)
	}
}

// tracking_enabled comes off the campaign row, so a campaign with tracking off
// is distinguishable from one that simply had no opens.
func TestEngagementReportsTrackingEnabledPerCampaign(t *testing.T) {
	ctx := context.Background()
	f := recordSetup(t, ctx)
	list := f.scalar(t, ctx, `INSERT INTO lists(workspace_id,name) VALUES($1,'Untracked') RETURNING id`, f.ws)
	untracked := f.scalar(t, ctx, `INSERT INTO campaigns
	 (workspace_id,name,mailbox_id,list_id,subject,status,tracking_enabled)
	 VALUES($1,'No tracking',$2,$3,'S','running',false) RETURNING id`, f.ws, f.mailbox, list)
	for _, campaign := range []uuid.UUID{f.campaign, untracked} {
		if _, err := f.pool.Exec(ctx,
			`INSERT INTO sequence_enrollments(workspace_id,campaign_id,contact_id) VALUES($1,$2,$3)`,
			f.ws, campaign, f.contactID); err != nil {
			t.Fatalf("enrollment: %v", err)
		}
	}

	got, err := f.svc.Engagement(ctx, f.ws, f.contactID)
	if err != nil {
		t.Fatalf("Engagement: %v", err)
	}
	if len(got.Campaigns) != 2 {
		t.Fatalf("campaigns = %d, want 2", len(got.Campaigns))
	}
	flags := map[string]bool{}
	for _, c := range got.Campaigns {
		flags[c.CampaignName] = c.TrackingEnabled
	}
	if !flags["Q2 outbound"] {
		t.Error("the tracked campaign reports tracking_enabled false")
	}
	if flags["No tracking"] {
		t.Error("the untracked campaign reports tracking_enabled true")
	}
}

// campaignWithTracking creates a campaign and returns its id.
func (f recordFixture) campaignWithTracking(t *testing.T, ctx context.Context, name string, tracking bool) uuid.UUID {
	t.Helper()
	list := f.scalar(t, ctx, `INSERT INTO lists(workspace_id,name) VALUES($1,$2) RETURNING id`, f.ws, name)
	return f.scalar(t, ctx, `INSERT INTO campaigns
	 (workspace_id,name,mailbox_id,list_id,subject,status,tracking_enabled)
	 VALUES($1,$2,$3,$4,'S','running',$5) RETURNING id`, f.ws, name, f.mailbox, list, tracking)
}

// sendFor writes a send for an arbitrary campaign at a given step.
func (f recordFixture) sendFor(t *testing.T, ctx context.Context, campaign uuid.UUID, step int, status string, sentAt *time.Time) {
	t.Helper()
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO sends(workspace_id,campaign_id,contact_id,mailbox_id,to_email,status,step_order,sent_at)
		 VALUES($1,$2,$3,$4,'dana@acme.test',$5,$6,$7)`,
		f.ws, campaign, f.contactID, f.mailbox, status, step, sentAt); err != nil {
		t.Fatalf("send: %v", err)
	}
}

// opens_measurable reflects the whole send history, not the visible enrollment
// window. This is the exact shape that makes a client-side inference wrong: more
// enrollments than the cap, every one in the newest CampaignCap untracked, and a
// single OLDER tracked campaign that actually sent. A `some(tracking_enabled)`
// over the returned rows says false; the truth is true.
func TestEngagementOpensMeasurableSeesPastTheCampaignCap(t *testing.T) {
	ctx := context.Background()
	f := recordSetup(t, ctx)

	// The oldest enrollment: tracked, and it really sent.
	tracked := f.campaignWithTracking(t, ctx, "Tracked old", true)
	oldest := f.sentAt.Add(-365 * 24 * time.Hour)
	if _, err := f.pool.Exec(ctx, `INSERT INTO sequence_enrollments
	 (workspace_id,campaign_id,contact_id,enrolled_at) VALUES($1,$2,$3,$4)`,
		f.ws, tracked, f.contactID, oldest); err != nil {
		t.Fatalf("tracked enrollment: %v", err)
	}
	f.sendFor(t, ctx, tracked, 1, "sent", &oldest)

	// Then CampaignCap newer, untracked enrollments, which is all a client sees.
	for i := 0; i < CampaignCap; i++ {
		untracked := f.campaignWithTracking(t, ctx, fmt.Sprintf("Untracked %d", i), false)
		at := f.sentAt.Add(time.Duration(i) * time.Hour)
		if _, err := f.pool.Exec(ctx, `INSERT INTO sequence_enrollments
		 (workspace_id,campaign_id,contact_id,enrolled_at) VALUES($1,$2,$3,$4)`,
			f.ws, untracked, f.contactID, at); err != nil {
			t.Fatalf("untracked enrollment %d: %v", i, err)
		}
	}

	got, err := f.svc.Engagement(ctx, f.ws, f.contactID)
	if err != nil {
		t.Fatalf("Engagement: %v", err)
	}
	if !got.CampaignsTruncated || len(got.Campaigns) != CampaignCap {
		t.Fatalf("campaigns = %d (truncated %v), want the cap %d and true — the "+
			"scenario needs the tracked campaign pushed out of the window",
			len(got.Campaigns), got.CampaignsTruncated, CampaignCap)
	}
	for _, c := range got.Campaigns {
		if c.TrackingEnabled {
			t.Fatalf("campaign %q is tracked and visible; the tracked one must be "+
				"outside the window for this test to mean anything", c.CampaignName)
		}
	}
	if !got.OpensMeasurable {
		t.Fatal("opens_measurable = false, but an older tracked campaign did send — " +
			"a client inferring this from campaigns[] would explain away a real zero")
	}
}

// A contact only ever sent to through untracked campaigns reports false, so the
// flag is not simply hardcoded true.
func TestEngagementOpensMeasurableFalseWhenNothingWasTracked(t *testing.T) {
	ctx := context.Background()
	f := recordSetup(t, ctx)
	untracked := f.campaignWithTracking(t, ctx, "Untracked only", false)
	f.sendFor(t, ctx, untracked, 1, "sent", &f.sentAt)

	got, err := f.svc.Engagement(ctx, f.ws, f.contactID)
	if err != nil {
		t.Fatalf("Engagement: %v", err)
	}
	if got.EmailsSent != 1 {
		t.Fatalf("emails_sent = %d, want 1", got.EmailsSent)
	}
	if got.OpensMeasurable {
		t.Fatal("opens_measurable = true with no tracked send")
	}
}

// A tracked campaign the contact was enrolled in but never SENT to cannot have
// produced an open, so it must not make the zero look measured.
func TestEngagementOpensMeasurableIgnoresUnsentTrackedCampaigns(t *testing.T) {
	ctx := context.Background()
	f := recordSetup(t, ctx)
	tracked := f.campaignWithTracking(t, ctx, "Tracked never sent", true)
	// Queued, not sent.
	f.sendFor(t, ctx, tracked, 1, "queued", nil)
	// And a real send on an untracked campaign, so emails_sent is non-zero.
	untracked := f.campaignWithTracking(t, ctx, "Untracked sent", false)
	f.sendFor(t, ctx, untracked, 1, "sent", &f.sentAt)

	got, err := f.svc.Engagement(ctx, f.ws, f.contactID)
	if err != nil {
		t.Fatalf("Engagement: %v", err)
	}
	if got.EmailsSent != 1 {
		t.Fatalf("emails_sent = %d, want only the untracked send", got.EmailsSent)
	}
	if got.OpensMeasurable {
		t.Fatal("a queued send on a tracked campaign made opens look measurable")
	}
}

// companyRoster runs the EXACT query crm.PgStore.ListCompanyContacts runs, via the
// shared generated package. The crm domain's store cannot be called from here
// (app packages do not import each other), but the SQL is the real seam between
// this write and that read — so driving the generated query directly proves the
// round trip QA had to do by hand, without duplicating the statement.
func (f recordFixture) companyRoster(t *testing.T, ctx context.Context, ws, companyID uuid.UUID) []string {
	t.Helper()
	rows, err := gen.New(f.pool).ListCompanyContacts(ctx, gen.ListCompanyContactsParams{
		WorkspaceID: ws,
		CompanyID:   pgtype.UUID{Bytes: companyID, Valid: true},
		PageLimit:   50,
	})
	if err != nil {
		t.Fatalf("company roster: %v", err)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Email)
	}
	return out
}

// The end-to-end gap QA found: before this write path existed, contacts.company_id
// was readable in three places and writable in none, so the company roster and
// ContactDetail.company were structurally always empty. This test fails on the
// code as it shipped.
func TestSetCompanyMakesTheContactVisibleToBothReads(t *testing.T) {
	ctx := context.Background()
	f := recordSetup(t, ctx)
	// A contact inserted with company_id NULL, so the precondition is established
	// by SQL rather than by the code under test — using SetCompany to set up
	// "unlinked" would make the mutation that breaks SetCompany break the fixture
	// instead of the assertion, and the test would stop testing the round trip.
	contactID := f.contact(t, ctx, f.ws, "unlinked@acme.test", nil)
	if roster := f.companyRoster(t, ctx, f.ws, f.companyID); len(roster) != 1 {
		t.Fatalf("roster = %v, want only recordSetup's own contact before the link", roster)
	}

	linked, err := f.svc.SetCompany(ctx, f.ws, contactID, &f.companyID)
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	// Read 1: the contact record's own company field.
	if linked.Company == nil || linked.Company.ID != f.companyID || linked.Company.Name != "Acme" {
		t.Fatalf("company = %+v, want the linked Acme row", linked.Company)
	}
	// Re-read through GET rather than trusting the write's return value.
	fetched, err := f.svc.Record(ctx, f.ws, contactID)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if fetched.Company == nil || fetched.Company.ID != f.companyID {
		t.Fatalf("re-read company = %+v, want the link to have persisted", fetched.Company)
	}
	// Read 2: the company's contact roster, via the crm domain's own SQL. This is
	// the read that could never return this contact before the write path existed.
	roster := f.companyRoster(t, ctx, f.ws, f.companyID)
	if !slices.Contains(roster, "unlinked@acme.test") {
		t.Fatalf("roster = %v, want it to include the newly linked contact", roster)
	}

	// And unlinking removes it from both again.
	if _, err := f.svc.SetCompany(ctx, f.ws, contactID, nil); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	after, err := f.svc.Record(ctx, f.ws, contactID)
	if err != nil {
		t.Fatalf("Record after unlink: %v", err)
	}
	if after.Company != nil {
		t.Fatalf("company = %+v, want nil after unlinking", after.Company)
	}
	if roster := f.companyRoster(t, ctx, f.ws, f.companyID); slices.Contains(roster, "unlinked@acme.test") {
		t.Fatalf("roster = %v, want the unlinked contact gone", roster)
	}
}

// A company from another workspace is refused, and the tenant FK is never even
// reached — the pre-check turns it into a clean 404 instead of a 23503.
func TestSetCompanyRefusesAnotherWorkspacesCompany(t *testing.T) {
	ctx := context.Background()
	f := recordSetup(t, ctx)
	foreign := f.company(t, ctx, f.other, "Foreign Inc", "foreign.test")

	_, err := f.svc.SetCompany(ctx, f.ws, f.contactID, &foreign)
	if !errors.Is(err, ErrCompanyNotFound) {
		t.Fatalf("err = %v, want ErrCompanyNotFound", err)
	}
	// The contact's original link is untouched.
	got, err := f.svc.Record(ctx, f.ws, f.contactID)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if got.Company == nil || got.Company.ID != f.companyID {
		t.Fatalf("company = %+v, want the original link intact", got.Company)
	}
	// And the foreign company's roster never saw this contact.
	if roster := f.companyRoster(t, ctx, f.other, foreign); len(roster) != 0 {
		t.Fatalf("foreign roster = %v, want empty", roster)
	}
}

// Writing a contact from the wrong workspace affects zero rows and is a 404, not
// a silent no-op that reports success.
func TestSetCompanyIsWorkspacePinnedOnTheContact(t *testing.T) {
	ctx := context.Background()
	f := recordSetup(t, ctx)

	if _, err := f.svc.SetCompany(ctx, f.other, f.contactID, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace write err = %v, want ErrNotFound", err)
	}
	// The contact still belongs to its company: nothing was written.
	got, err := f.svc.Record(ctx, f.ws, f.contactID)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if got.Company == nil {
		t.Fatal("a cross-workspace write unlinked the contact")
	}
}
