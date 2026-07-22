package campaign

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// CreateInput carries the fields needed to create a new campaign.
type CreateInput struct {
	Name, Subject, BodyText, BodyHTML string
	MailboxID, ListID                 uuid.UUID
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
	// ListSteps returns the campaign's ordered steps (for the detail view).
	ListSteps(ctx context.Context, ws, campaignID uuid.UUID) ([]gen.SequenceStep, error)
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
	defer tx.Rollback(ctx)
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
	defer tx.Rollback(ctx)

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
