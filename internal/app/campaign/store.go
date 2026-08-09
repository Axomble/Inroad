package campaign

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/sendcap"
)

// CreateInput carries the fields needed to create a new campaign. Timezone is
// optional (empty = UTC) and sets the zone the default Mon–Fri 09:00–17:00 send
// window is interpreted in.
type CreateInput struct {
	Name, Subject, BodyText, BodyHTML string
	MailboxID, ListID                 uuid.UUID
	Timezone                          string
}

// Enrollment is a newly created enrollment returned by EnrollTx: its id and the
// staggered next_due_at the DB assigned at insert time. Launch enqueues each
// advance at exactly this NextDueAt so the scheduled task and the enrollment's
// due cursor stay aligned by construction (Postgres doesn't guarantee RETURNING
// row order, so the value must travel with the id rather than be recomputed).
type Enrollment struct {
	ID        uuid.UUID
	NextDueAt time.Time
}

// Store is the repository interface this domain depends on. It is defined
// here (by the consumer), not by the persistence layer, so the service can
// be unit-tested against a fake without a database.
type Store interface {
	Create(ctx context.Context, ws uuid.UUID, in CreateInput) (gen.Campaign, error)
	Get(ctx context.Context, ws, id uuid.UUID) (gen.Campaign, error)
	List(ctx context.Context, ws uuid.UUID) ([]gen.Campaign, error)
	Stats(ctx context.Context, ws, id uuid.UUID) (map[string]int64, error)
	// CountSteps returns how many sequence_steps the campaign has. Launch
	// requires ≥1 (backfill/Create seed step 1 for the single-message flow).
	CountSteps(ctx context.Context, ws, campaignID uuid.UUID) (int64, error)
	// EnrollTx materializes one sequence_enrollment per (campaign, list member)
	// AND transitions the campaign to running, atomically. Returns the new
	// enrollments (id + the staggered next_due_at the DB assigned). Either both
	// writes commit or neither does.
	EnrollTx(ctx context.Context, ws, campaignID uuid.UUID) ([]Enrollment, error)
	// Reschedule re-stamps an active enrollment's next_due_at (launch stagger).
	Reschedule(ctx context.Context, ws, enrollmentID uuid.UUID, at time.Time) error
	// RescheduleBatch re-stamps many active enrollments at once (the launch
	// cadence spread), one round trip for the whole batch. Only 'active' rows are
	// touched, so a concurrent stop still wins.
	RescheduleBatch(ctx context.Context, ws uuid.UUID, due map[uuid.UUID]time.Time) error
	// ListWindows returns the campaign's open sending intervals for the week,
	// ordered by weekday then start minute.
	ListWindows(ctx context.Context, ws, campaignID uuid.UUID) ([]SendWindow, error)
	// ReplaceSchedule swaps the campaign's timezone, its whole set of windows and
	// its campaign-wide daily limit atomically, so the campaign is never observed
	// window-less nor with half of a saved plan applied.
	ReplaceSchedule(ctx context.Context, ws, campaignID uuid.UUID, plan Plan) error
	// ListSenders returns the campaign's sender-pool members (including disabled
	// ones — the panel edits them), ordered by mailbox email.
	ListSenders(ctx context.Context, ws, campaignID uuid.UUID) ([]Sender, error)
	// FallbackSender projects campaigns.mailbox_id as a one-member pool, for a
	// campaign that has no campaign_senders rows and therefore sends from it.
	FallbackSender(ctx context.Context, ws, campaignID uuid.UUID) (Sender, error)
	// ReplaceSenders swaps the campaign's rotation mode and its whole pool
	// atomically, PRESERVING assigned_count/last_assigned_at for mailboxes that
	// stay in the pool so an edit doesn't reset the rotation.
	ReplaceSenders(ctx context.Context, ws, campaignID uuid.UUID, mode string, senders []SenderInput) error
	// ListSteps returns the campaign's ordered steps (for the detail view).
	ListSteps(ctx context.Context, ws, campaignID uuid.UUID) ([]gen.SequenceStep, error)
	// ListStepVariants returns the campaign's A/B variants keyed by step id.
	//
	// It reads a table the sequencestep app package owns, through sqlc, rather
	// than calling that domain's service: app packages do not import each other,
	// and routing a read through another domain's HTTP-shaped service would be
	// the worse coupling (the same reasoning as the contact record-page queries).
	ListStepVariants(ctx context.Context, ws, campaignID uuid.UUID) (map[uuid.UUID][]PreflightVariant, error)
	// EnrollmentCounts returns enrollment counts grouped by status.
	EnrollmentCounts(ctx context.Context, ws, campaignID uuid.UUID) (map[string]int64, error)
	// EngagementCounts returns (opensIndicative, clicks) sourced from
	// tracking_events: opens via the human-open filter (CountHumanOpens),
	// clicks via CountEngagedSendsByKind.
	EngagementCounts(ctx context.Context, ws, campaignID uuid.UUID) (opens, clicks int64, err error)
	// StopReasonCounts returns terminal-enrollment counts keyed by stop_reason
	// (replied/bounced/suppressed/manual/failed) for the reply/bounce/unsub
	// metrics rollup. Distinct from EnrollmentCounts, which groups by
	// lifecycle status.
	StopReasonCounts(ctx context.Context, ws, campaignID uuid.UUID) (map[string]int64, error)
	// SetTracking flips the campaign's tracking_enabled flag.
	SetTracking(ctx context.Context, ws, campaignID uuid.UUID, enabled bool) error
	// ListEnrollments returns per-contact reply status for the campaign's
	// enrollments (contact email/name plus reply class/source/replied_at),
	// workspace-pinned, most-recently-replied first, paginated by limit/offset.
	ListEnrollments(ctx context.Context, ws, campaignID uuid.UUID, limit, offset int32) ([]gen.ListCampaignEnrollmentsRow, error)
	// SetStatus updates the campaign's lifecycle status (Pause/Resume). It never
	// touches launched_at -- SetCampaignStatus's COALESCE keeps whatever EnrollTx
	// already stamped there at launch.
	SetStatus(ctx context.Context, ws, id uuid.UUID, status CampaignStatus) error
	// Rename replaces the campaign's name, workspace-scoped, and returns the
	// updated row.
	Rename(ctx context.Context, ws, id uuid.UUID, name string) (gen.Campaign, error)
	// DeleteDraft removes a draft campaign and its dependents (send windows,
	// campaign_senders, sequence_steps, sequence_enrollments) in one
	// transaction. Guarded on status='draft' in SQL as defense in depth on top
	// of the service's own check.
	DeleteDraft(ctx context.Context, ws, id uuid.UUID) error
	// CountUnsuppressedAudience returns how many of the campaign's list members
	// are NOT on the workspace suppression list -- the preflight "audience"
	// check's evidence (0 = fail).
	CountUnsuppressedAudience(ctx context.Context, ws, campaignID uuid.UUID) (int64, error)
}

// Checker validates cross-domain references belong to the workspace.
// Implemented in cmd/inroad wiring by a small adapter over the mailbox and
// list stores (Task 9).
type Checker interface {
	MailboxActive(ctx context.Context, ws, mailboxID uuid.UUID) (bool, error)
	ListExists(ctx context.Context, ws, listID uuid.UUID) (bool, error)
}

// PgStore implements Store by wrapping sqlc-generated queries.
type PgStore struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

// NewPgStore constructs a PgStore backed by the given connection pool. The
// pool is used for LaunchTx's transaction; every other method flows through
// the pool-bound *gen.Queries.
func NewPgStore(pool *pgxpool.Pool) *PgStore {
	return &PgStore{pool: pool, q: gen.New(pool)}
}

// Create persists the campaign AND seeds step 1 from its inline subject/body
// in one transaction, so the single-message POST /campaigns → launch flow
// yields a one-step sequence (spec §2 backward compat). Multi-step callers add
// further steps via the steps API.
func (s *PgStore) Create(ctx context.Context, ws uuid.UUID, in CreateInput) (gen.Campaign, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return gen.Campaign{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed
	qtx := s.q.WithTx(tx)

	c, err := qtx.CreateCampaign(ctx, gen.CreateCampaignParams{
		WorkspaceID: ws, Name: in.Name, MailboxID: in.MailboxID, ListID: in.ListID,
		Subject: in.Subject, BodyText: in.BodyText, BodyHtml: in.BodyHTML,
	})
	if err != nil {
		return gen.Campaign{}, err
	}
	if _, err := qtx.CreateStep(ctx, gen.CreateStepParams{
		WorkspaceID: ws, CampaignID: c.ID, StepOrder: 1, DelaySeconds: 0,
		Subject: in.Subject, BodyText: in.BodyText, BodyHtml: in.BodyHTML,
	}); err != nil {
		return gen.Campaign{}, err
	}
	// Seed the default sending schedule in the same transaction: a campaign must
	// never exist without a window, since an empty week means "no valid send
	// instant exists" to the cadence engine. Migration 000031 backfills the same
	// default for campaigns created before this existed.
	sched := DefaultSchedule(in.Timezone)
	if in.Timezone != "" {
		if err := qtx.SetCampaignTimezone(ctx, gen.SetCampaignTimezoneParams{
			ID: c.ID, WorkspaceID: ws, Timezone: sched.Timezone,
		}); err != nil {
			return gen.Campaign{}, err
		}
		c.Timezone = sched.Timezone
	}
	if err := insertWindows(ctx, qtx, ws, c.ID, sched.Windows); err != nil {
		return gen.Campaign{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return gen.Campaign{}, err
	}
	return c, nil
}

func (s *PgStore) CountSteps(ctx context.Context, ws, campaignID uuid.UUID) (int64, error) {
	return s.q.CountStepsByCampaign(ctx, gen.CountStepsByCampaignParams{CampaignID: campaignID, WorkspaceID: ws})
}

func (s *PgStore) Reschedule(ctx context.Context, ws, enrollmentID uuid.UUID, at time.Time) error {
	return s.q.SetEnrollmentDue(ctx, gen.SetEnrollmentDueParams{
		ID: enrollmentID, WorkspaceID: ws, NextDueAt: pgtype.Timestamptz{Time: at, Valid: true},
	})
}

// RescheduleBatch stamps a whole launch's cadence-computed due times in one
// pipelined batch. A per-statement error is returned rather than swallowed: the
// launcher counts it and the enrollment sweeper reconciles the row on its next
// tick, but a silent partial write would leave enrollments due immediately —
// exactly the burst the cadence spread exists to prevent.
func (s *PgStore) RescheduleBatch(ctx context.Context, ws uuid.UUID, due map[uuid.UUID]time.Time) error {
	if len(due) == 0 {
		return nil
	}
	params := make([]gen.SetEnrollmentDueBatchParams, 0, len(due))
	for id, at := range due {
		params = append(params, gen.SetEnrollmentDueBatchParams{
			ID: id, WorkspaceID: ws, NextDueAt: pgtype.Timestamptz{Time: at, Valid: true},
		})
	}
	var firstErr error
	res := s.q.SetEnrollmentDueBatch(ctx, params)
	res.Exec(func(_ int, err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	})
	if cerr := res.Close(); cerr != nil && firstErr == nil {
		firstErr = cerr
	}
	return firstErr
}

func (s *PgStore) ListWindows(ctx context.Context, ws, campaignID uuid.UUID) ([]SendWindow, error) {
	rows, err := s.q.ListSendWindows(ctx, gen.ListSendWindowsParams{CampaignID: campaignID, WorkspaceID: ws})
	if err != nil {
		return nil, err
	}
	out := make([]SendWindow, len(rows))
	for i, r := range rows {
		out[i] = SendWindow{
			Weekday: int(r.Weekday), StartMinute: int(r.StartMinute), EndMinute: int(r.EndMinute),
		}
	}
	return out, nil
}

// ReplaceSchedule swaps the timezone, the whole window set and both daily
// ceilings in one transaction: delete-then-insert would otherwise leave a window
// between the two statements where the campaign has no schedule at all, which the
// cadence engine reads as "no valid send instant exists". The limits ride along
// because they are saved by the same panel: committing the windows without them
// would leave the plan half-applied.
func (s *PgStore) ReplaceSchedule(ctx context.Context, ws, campaignID uuid.UUID, plan Plan) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed
	qtx := s.q.WithTx(tx)

	if err := qtx.SetCampaignTimezone(ctx, gen.SetCampaignTimezoneParams{
		ID: campaignID, WorkspaceID: ws, Timezone: plan.Timezone,
	}); err != nil {
		return err
	}
	if err := qtx.SetCampaignDailyLimit(ctx, gen.SetCampaignDailyLimitParams{
		ID: campaignID, WorkspaceID: ws, DailyLimit: storedOptionalInt(plan.DailyLimit),
	}); err != nil {
		return err
	}
	if err := qtx.SetCampaignMaxNewLeads(ctx, gen.SetCampaignMaxNewLeadsParams{
		ID: campaignID, WorkspaceID: ws, MaxNewLeadsPerDay: storedOptionalInt(plan.MaxNewLeadsPerDay),
	}); err != nil {
		return err
	}
	if err := qtx.DeleteSendWindows(ctx, gen.DeleteSendWindowsParams{
		CampaignID: campaignID, WorkspaceID: ws,
	}); err != nil {
		return err
	}
	if err := insertWindows(ctx, qtx, ws, campaignID, plan.Windows); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// storedOptionalInt narrows a validated *int limit to the column's *int32 type;
// nil clears it. Shared by every nullable per-UTC-day campaign ceiling
// (daily_limit, max_new_leads_per_day) ReplaceSchedule writes.
func storedOptionalInt(limit *int) *int32 {
	if limit == nil {
		return nil
	}
	n := int32(*limit) //nolint:gosec // SetSchedule bounds every caller to [1, 1_000_000], well inside int32
	return &n
}

// insertWindows queues one insert per interval as a single batch. Shared by
// ReplaceSchedule and Create so the default schedule and an edited one take the
// identical path (and hit the identical overlap constraint).
func insertWindows(ctx context.Context, q *gen.Queries, ws, campaignID uuid.UUID, windows []SendWindow) error {
	if len(windows) == 0 {
		return nil
	}
	params := make([]gen.CreateSendWindowsParams, len(windows))
	for i, w := range windows {
		params[i] = gen.CreateSendWindowsParams{
			WorkspaceID: ws, CampaignID: campaignID,
			Weekday:     int16(w.Weekday),     //nolint:gosec // 0..6, validated by Schedule.Compile
			StartMinute: int32(w.StartMinute), //nolint:gosec // 0..1439, CHECK-constrained
			EndMinute:   int32(w.EndMinute),   //nolint:gosec // 1..1440, CHECK-constrained
		}
	}
	var firstErr error
	res := q.CreateSendWindows(ctx, params)
	res.Exec(func(_ int, err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	})
	if cerr := res.Close(); cerr != nil && firstErr == nil {
		firstErr = cerr
	}
	return firstErr
}

func (s *PgStore) ListSenders(ctx context.Context, ws, campaignID uuid.UUID) ([]Sender, error) {
	rows, err := s.q.ListCampaignSenders(ctx, gen.ListCampaignSendersParams{CampaignID: campaignID, WorkspaceID: ws})
	if err != nil {
		return nil, err
	}
	out := make([]Sender, len(rows))
	for i, r := range rows {
		out[i] = Sender{
			MailboxID: r.MailboxID, Email: r.Email, Provider: r.Provider, Status: r.Status,
			Weight: int(r.Weight), Enabled: r.Enabled,
			AssignedCount: r.AssignedCount, LastAssignedAt: nullableTime(r.LastAssignedAt),
		}
		senderCapacity{
			dailyCap: r.DailyCap, rampStartCap: r.RampStartCap, rampDays: r.RampDays,
			rampEnabled: r.RampEnabled, mailboxCreatedAt: r.MailboxCreatedAt,
			mailboxStatus: r.Status, poolEnabled: r.Enabled,
			healthState: r.HealthState, sentToday: r.SentToday,
		}.fill(&out[i])
	}
	return out, nil
}

// FallbackSender reports campaigns.mailbox_id in the pool's shape. Weight 1 and
// enabled true describe how it actually behaves — it is the campaign's only
// sender — and the rotation state is genuinely absent, since no row tracks it. Its
// health and capacity are real, though: the fallback mailbox is ramped and
// health-gated exactly like a pool member.
func (s *PgStore) FallbackSender(ctx context.Context, ws, campaignID uuid.UUID) (Sender, error) {
	r, err := s.q.GetCampaignFallbackSender(ctx, gen.GetCampaignFallbackSenderParams{ID: campaignID, WorkspaceID: ws})
	if err != nil {
		return Sender{}, err
	}
	out := Sender{
		MailboxID: r.MailboxID, Email: r.Email, Provider: r.Provider, Status: r.Status,
		Weight: defaultSenderWeight, Enabled: true,
	}
	senderCapacity{
		dailyCap: r.DailyCap, rampStartCap: r.RampStartCap, rampDays: r.RampDays,
		rampEnabled: r.RampEnabled, mailboxCreatedAt: r.MailboxCreatedAt,
		mailboxStatus: r.Status, poolEnabled: true,
		healthState: r.HealthState, sentToday: r.SentToday,
	}.fill(&out)
	return out, nil
}

// senderCapacity is the health-and-capacity slice of a pool row, shared by the
// pool listing and the fallback projection so both report the identical cap_today
// for the identical mailbox. The two sqlc row types are distinct structs with the
// same columns, so the shared logic takes the columns rather than either row.
type senderCapacity struct {
	dailyCap, rampStartCap, rampDays int32
	rampEnabled                      bool
	mailboxCreatedAt                 pgtype.Timestamptz
	mailboxStatus                    string
	poolEnabled                      bool
	healthState                      string
	sentToday                        int64
}

// fill sets s's reported health and capacity. cap_today is the SAME arithmetic the
// send path applies (platform/sendcap: ramp, then warmup health), so an operator
// reading the panel sees the cap that will actually be enforced. A paused mailbox
// therefore reports a cap of 0 and sending=false, which is exactly what it does.
func (c senderCapacity) fill(s *Sender) {
	ageDays := int(time.Since(c.mailboxCreatedAt.Time).Hours() / 24)
	ramped := sendcap.Effective(int(c.dailyCap), int(c.rampStartCap), int(c.rampDays), c.rampEnabled, ageDays)
	s.CapToday = sendcap.Cold(ramped, c.healthState)
	s.SentToday = int(c.sentToday)
	s.Sending = c.poolEnabled && c.mailboxStatus == mailboxStatusActive && c.healthState != sendcap.HealthPaused
	if c.healthState != "" {
		state := c.healthState
		s.HealthState = &state
	}
}

// ReplaceSenders upserts every requested member then deletes the ones left out,
// in one transaction and in that order: the reverse would leave a window where the
// pool is empty, which the read and send paths both interpret as "never
// configured" and answer with campaigns.mailbox_id. The upsert touches only
// weight/enabled, so assigned_count/last_assigned_at survive for a retained
// mailbox.
func (s *PgStore) ReplaceSenders(ctx context.Context, ws, campaignID uuid.UUID, mode string, senders []SenderInput) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed
	qtx := s.q.WithTx(tx)

	if err := qtx.SetCampaignRotationMode(ctx, gen.SetCampaignRotationModeParams{
		ID: campaignID, WorkspaceID: ws, RotationMode: mode,
	}); err != nil {
		return err
	}
	params := make([]gen.UpsertCampaignSenderParams, len(senders))
	keep := make([]uuid.UUID, len(senders))
	for i, sender := range senders {
		params[i] = gen.UpsertCampaignSenderParams{
			WorkspaceID: ws, CampaignID: campaignID, MailboxID: sender.MailboxID,
			Weight:  int32(sender.Weight), //nolint:gosec // 1..100, validated by SetSenders and CHECK-constrained
			Enabled: sender.Enabled,
		}
		keep[i] = sender.MailboxID
	}
	var firstErr error
	res := qtx.UpsertCampaignSender(ctx, params)
	res.Exec(func(_ int, err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	})
	if cerr := res.Close(); cerr != nil && firstErr == nil {
		firstErr = cerr
	}
	if firstErr != nil {
		return firstErr
	}
	if err := qtx.DeleteCampaignSendersExcept(ctx, gen.DeleteCampaignSendersExceptParams{
		CampaignID: campaignID, WorkspaceID: ws, MailboxIds: keep,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// nullableTime turns a NULL-able timestamptz column into an optional Go time, so
// the response DTO can carry JSON null rather than a zero timestamp.
func nullableTime(ts pgtype.Timestamptz) *time.Time {
	if !ts.Valid {
		return nil
	}
	at := ts.Time
	return &at
}

func (s *PgStore) ListStepVariants(ctx context.Context, ws, campaignID uuid.UUID) (map[uuid.UUID][]PreflightVariant, error) {
	rows, err := s.q.ListVariantsByCampaign(ctx, gen.ListVariantsByCampaignParams{CampaignID: campaignID, WorkspaceID: ws})
	if err != nil {
		return nil, fmt.Errorf("list campaign variants: %w", err)
	}
	out := make(map[uuid.UUID][]PreflightVariant, len(rows))
	for _, r := range rows {
		out[r.StepID] = append(out[r.StepID], PreflightVariant{
			Label: r.Label, Weight: int(r.Weight),
			Subject: r.Subject, BodyText: r.BodyText, BodyHTML: r.BodyHtml,
		})
	}
	return out, nil
}

func (s *PgStore) ListSteps(ctx context.Context, ws, campaignID uuid.UUID) ([]gen.SequenceStep, error) {
	return s.q.ListStepsByCampaign(ctx, gen.ListStepsByCampaignParams{CampaignID: campaignID, WorkspaceID: ws})
}

func (s *PgStore) EnrollmentCounts(ctx context.Context, ws, campaignID uuid.UUID) (map[string]int64, error) {
	rows, err := s.q.CountEnrollmentsByStatus(ctx, gen.CountEnrollmentsByStatusParams{CampaignID: campaignID, WorkspaceID: ws})
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		out[r.Status] = r.N
	}
	return out, nil
}

// EngagementCounts aggregates the two tracking-event-derived metrics: opens
// (human-open filtered) and clicks (the reliable signal). CountEngagedSendsByKind
// returns a row per kind present in the campaign's events; a campaign with no
// clicks yet simply has no 'click' row, so the loop leaves clicks at 0.
func (s *PgStore) EngagementCounts(ctx context.Context, ws, campaignID uuid.UUID) (int64, int64, error) {
	opens, err := s.q.CountHumanOpens(ctx, gen.CountHumanOpensParams{CampaignID: campaignID, WorkspaceID: ws})
	if err != nil {
		return 0, 0, err
	}
	rows, err := s.q.CountEngagedSendsByKind(ctx, gen.CountEngagedSendsByKindParams{CampaignID: campaignID, WorkspaceID: ws})
	if err != nil {
		return 0, 0, err
	}
	var clicks int64
	for _, r := range rows {
		if r.Kind == gen.TrackingEventKindClick {
			clicks = r.N
		}
	}
	return opens, clicks, nil
}

func (s *PgStore) StopReasonCounts(ctx context.Context, ws, campaignID uuid.UUID) (map[string]int64, error) {
	rows, err := s.q.CountEnrollmentsByStopReason(ctx, gen.CountEnrollmentsByStopReasonParams{CampaignID: campaignID, WorkspaceID: ws})
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		// stop_reason is nullable in the DB (CHECK allows NULL); StopEnrollment
		// always sets one, so a nil here would be a data anomaly rather than
		// normal flow. Skip it rather than panic on the dereference.
		if r.StopReason == nil {
			continue
		}
		out[*r.StopReason] = r.N
	}
	return out, nil
}

func (s *PgStore) SetTracking(ctx context.Context, ws, campaignID uuid.UUID, enabled bool) error {
	return s.q.SetCampaignTracking(ctx, gen.SetCampaignTrackingParams{
		ID: campaignID, WorkspaceID: ws, TrackingEnabled: enabled,
	})
}

func (s *PgStore) ListEnrollments(ctx context.Context, ws, campaignID uuid.UUID, limit, offset int32) ([]gen.ListCampaignEnrollmentsRow, error) {
	return s.q.ListCampaignEnrollments(ctx, gen.ListCampaignEnrollmentsParams{
		CampaignID: campaignID, WorkspaceID: ws, Limit: limit, Offset: offset,
	})
}

func (s *PgStore) Get(ctx context.Context, ws, id uuid.UUID) (gen.Campaign, error) {
	return s.q.GetCampaign(ctx, gen.GetCampaignParams{ID: id, WorkspaceID: ws})
}
func (s *PgStore) List(ctx context.Context, ws uuid.UUID) ([]gen.Campaign, error) {
	return s.q.ListCampaigns(ctx, ws)
}
func (s *PgStore) Stats(ctx context.Context, ws, id uuid.UUID) (map[string]int64, error) {
	rows, err := s.q.CountSendsByStatus(ctx, gen.CountSendsByStatusParams{CampaignID: id, WorkspaceID: ws})
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for _, r := range rows {
		out[r.Status] = r.N
	}
	return out, nil
}

// EnrollTx materializes one enrollment per list member and flips status to
// running in a single transaction. If either write fails the transaction rolls
// back, leaving the campaign draft with no enrollments. An empty target list
// commits nothing and returns no ids (service maps that to ErrEmptyList).
func (s *PgStore) EnrollTx(ctx context.Context, ws, campaignID uuid.UUID) ([]Enrollment, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed

	qtx := s.q.WithTx(tx)
	rows, err := qtx.EnrollListMembers(ctx, gen.EnrollListMembersParams{ID: campaignID, WorkspaceID: ws})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	if err := qtx.SetCampaignStatus(ctx, gen.SetCampaignStatusParams{
		ID:          campaignID,
		WorkspaceID: ws,
		Status:      string(StatusRunning),
		LaunchedAt:  pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	enrollments := make([]Enrollment, len(rows))
	for i, r := range rows {
		enrollments[i] = Enrollment{ID: r.ID, NextDueAt: r.NextDueAt.Time}
	}
	return enrollments, nil
}

// SetStatus flips the campaign's lifecycle status for Pause/Resume. The
// LaunchedAt argument is left zero-valued (Valid: false): SetCampaignStatus's
// COALESCE(launched_at, $4) then keeps whatever value is already stored,
// since pause/resume never launches a campaign.
func (s *PgStore) SetStatus(ctx context.Context, ws, id uuid.UUID, status CampaignStatus) error {
	return s.q.SetCampaignStatus(ctx, gen.SetCampaignStatusParams{
		ID: id, WorkspaceID: ws, Status: string(status),
	})
}

func (s *PgStore) Rename(ctx context.Context, ws, id uuid.UUID, name string) (gen.Campaign, error) {
	return s.q.RenameCampaign(ctx, gen.RenameCampaignParams{ID: id, WorkspaceID: ws, Name: name})
}

// DeleteDraft removes a draft campaign and its dependents in one transaction:
// sequence_enrollments, sequence_steps and campaign_senders, then the send
// windows, then the campaign row itself. Every one of those FKs is already
// ON DELETE CASCADE, but the deletes are explicit here rather than relied on,
// per this task's brief -- and it lets the final DeleteDraftCampaign guard
// re-check status='draft' in SQL (defense in depth) without also depending on
// cascade ordering.
//
// If DeleteDraftCampaign matches zero rows (the status changed between the
// service's check and this transaction -- a concurrent launch, say), the
// transaction rolls back and ErrNotDraft is returned so the caller sees the
// same typed 409 the pre-check would have given a moment earlier.
func (s *PgStore) DeleteDraft(ctx context.Context, ws, id uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed
	qtx := s.q.WithTx(tx)

	if err := qtx.DeleteEnrollmentsByCampaign(ctx, gen.DeleteEnrollmentsByCampaignParams{
		CampaignID: id, WorkspaceID: ws,
	}); err != nil {
		return err
	}
	if err := qtx.DeleteStepsByCampaign(ctx, gen.DeleteStepsByCampaignParams{
		CampaignID: id, WorkspaceID: ws,
	}); err != nil {
		return err
	}
	if err := qtx.DeleteCampaignSendersByCampaign(ctx, gen.DeleteCampaignSendersByCampaignParams{
		CampaignID: id, WorkspaceID: ws,
	}); err != nil {
		return err
	}
	if err := qtx.DeleteSendWindows(ctx, gen.DeleteSendWindowsParams{
		CampaignID: id, WorkspaceID: ws,
	}); err != nil {
		return err
	}
	n, err := qtx.DeleteDraftCampaign(ctx, gen.DeleteDraftCampaignParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotDraft
	}
	return tx.Commit(ctx)
}

func (s *PgStore) CountUnsuppressedAudience(ctx context.Context, ws, campaignID uuid.UUID) (int64, error) {
	return s.q.CountUnsuppressedAudience(ctx, gen.CountUnsuppressedAudienceParams{ID: campaignID, WorkspaceID: ws})
}
