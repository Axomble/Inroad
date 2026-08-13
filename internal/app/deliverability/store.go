// Package deliverability is the control-plane half of the deliverability
// guardrails: it gathers the measured evidence, hands it to
// platform/deliverability for the ONE computation that produces both the score
// and the breaker's verdict, and — when the breaker says pause — stops the
// campaign with a recorded reason.
//
// The scoring arithmetic lives in platform/deliverability and nothing here
// duplicates it: this package's job is which rows to read and what to do with
// the answer, never what the answer means.
package deliverability

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// ErrNotFound means the campaign (or send) is not this workspace's. It is the
// 404, and it is what a cross-tenant id produces: every statement is
// workspace-pinned, so a foreign id matches zero rows rather than another
// tenant's data.
var ErrNotFound = errors.New("deliverability: not found")

// suppressionReasonComplaint is the suppression reason a complaint is recorded
// under. It reuses the EXISTING workspace-scoped, idempotent suppression table —
// the same path a hard bounce and a reply-unsubscribe take — with its own reason
// literal, because "they reported us as spam" and "they asked to stop" are
// different things to see in a list. Mirrored by the 000037 CHECK constraint.
const suppressionReasonComplaint = "complaint"

// pauseEventLimit bounds the pause history returned with a campaign. A campaign
// paused more than a handful of times is a campaign nobody should be restarting,
// and the card renders the recent ones.
const pauseEventLimit = 20

// Counts is the rate evidence for one campaign or workspace over a window.
// ComplaintFeed is the invariant-4 discriminator: false means no complaint feed
// has ever reported for this workspace, so Complained is NOT MEASURED rather
// than measured-as-zero.
type Counts struct {
	Delivered     int
	Bounced       int
	Complained    int
	ComplaintFeed bool
}

// Signals is the non-rate evidence: warmup placement (sender-attributed), the
// worst warmup health state, and the worst sending-domain verdict. Empty strings
// mean "no signal at all", which the score treats as unmeasured, not healthy.
type Signals struct {
	InboxPlaced int
	SpamPlaced  int
	WarmupState string
	DomainState string
}

// Guardrails is one campaign's circuit-breaker configuration, as stored and as
// the PUT endpoint round-trips it.
type Guardrails struct {
	AutoPauseEnabled  bool
	BouncePausePct    float64
	ComplaintPausePct float64
}

// CampaignConfig pairs the guardrails with the campaign's current status: the
// breaker only pauses a running campaign, and the report needs the status to tell
// "the breaker has fired" from "a rate is trending bad".
type CampaignConfig struct {
	Guardrails
	Status string
	// EnabledAt is when this campaign came under automatic supervision
	// (campaigns.guardrails_enabled_at): migration time for a campaign that already
	// existed, creation time for a new one, re-stamped when an operator switches
	// auto-pause back on. It is the lower bound on evidence the breaker may act on,
	// so bounces from before the operator could have seen them can never stop a
	// campaign. Never zero — the column is NOT NULL DEFAULT now().
	EnabledAt time.Time
}

// PauseEvent is one recorded automatic pause. Value and Threshold are
// percentages; Delivered is the sample the judgement was made on, so an operator
// can confirm the minimum-sample rule held.
type PauseEvent struct {
	Reason    string
	Metric    string
	Value     float64
	Threshold float64
	Delivered int
	CreatedAt time.Time
}

// Point is one UTC day of the series.
//
// The store fills the four counts; the SERVICE sets the two Measured flags from
// the score's own components, so the series can never imply a complaint or
// placement rate the score itself declined to claim (invariant 4). The DTO
// serializes an unmeasured count as JSON null rather than 0.
type Point struct {
	Day        time.Time
	Delivered  int
	Bounced    int
	Complained int
	SpamPlaced int
	// ComplaintMeasured / PlacementMeasured mirror the corresponding score
	// component's Measured flag. False means "no feed / nothing observed", not
	// "zero".
	ComplaintMeasured bool
	PlacementMeasured bool
}

// Risk is one at-risk mailbox or domain for the dashboard's lists.
type Risk struct {
	Label  string
	Reason string
}

// EventInput is one ingested deliverability event. SendID is optional: most feeds
// report an address, not our internal id, and an event without one still counts
// at workspace scope — it simply attributes to no campaign.
//
// BounceClass discriminates a permanent failure from a transient one. Provider
// feeds mix them (a full mailbox and a greylisting deferral arrive on the same
// webhook as a nonexistent address), and ONLY 'hard' feeds the warmup
// hard-bounce rate — counting a greylist as permanent pauses a healthy sender
// for 72 hours. The service normalizes an omitted value to BounceClassUnknown,
// which is excluded from that rate: under-counting is the safe direction.
type EventInput struct {
	Kind            string
	Email           string
	ProviderEventID string
	SendID          *uuid.UUID
	BounceClass     string
}

// Store is the repository interface this domain depends on, defined here by the
// consumer so the service is unit-testable against a fake with no database.
// Every method takes the workspace explicitly; it comes from the JWT at the
// handler or the task payload in the worker, never from a request body.
type Store interface {
	// CampaignCounts returns one campaign's rate evidence over [since, now).
	CampaignCounts(ctx context.Context, ws, campaignID uuid.UUID, since time.Time) (Counts, error)
	// WorkspaceCounts returns the workspace rollup's rate evidence over [since, now).
	WorkspaceCounts(ctx context.Context, ws uuid.UUID, since time.Time) (Counts, error)
	// Signals returns the non-rate evidence for a set of mailboxes; an empty
	// mailboxIDs means every mailbox in the workspace.
	Signals(ctx context.Context, ws uuid.UUID, mailboxIDs []uuid.UUID, since time.Time) (Signals, error)
	// CampaignSenderMailboxes returns the mailboxes a campaign actually sends
	// from: its sender pool, or the single fallback mailbox when it has no pool.
	CampaignSenderMailboxes(ctx context.Context, ws, campaignID uuid.UUID) ([]uuid.UUID, error)
	// Series returns one workspace-wide row per UTC day in the window, including
	// days with no activity — a gap in the chart has to read as a gap.
	Series(ctx context.Context, ws uuid.UUID, since time.Time) ([]Point, error)
	// AtRiskMailboxes returns mailboxes the warmup engine has judged degraded.
	AtRiskMailboxes(ctx context.Context, ws uuid.UUID) ([]Risk, error)
	// AtRiskDomains returns sending domains whose last completed check failed.
	AtRiskDomains(ctx context.Context, ws uuid.UUID) ([]Risk, error)
	// CampaignConfig returns the campaign's guardrails and status, or
	// ErrNotFound when it is not this workspace's.
	CampaignConfig(ctx context.Context, ws, campaignID uuid.UUID) (CampaignConfig, error)
	// SetGuardrails persists the three settings, ErrNotFound on zero rows.
	SetGuardrails(ctx context.Context, ws, campaignID uuid.UUID, g Guardrails) error
	// LastPausedAt is when the breaker last paused this campaign, or the zero
	// time when it never has.
	LastPausedAt(ctx context.Context, ws, campaignID uuid.UUID) (time.Time, error)
	// PauseEvents returns the campaign's pause history, newest first.
	PauseEvents(ctx context.Context, ws, campaignID uuid.UUID) ([]PauseEvent, error)
	// PauseForBreach flips a RUNNING campaign to paused and records the event, in
	// one transaction. It reports false when the campaign was not running — which
	// is what makes repeated evaluation idempotent: no second flip, no second
	// event.
	PauseForBreach(ctx context.Context, ws, campaignID uuid.UUID, ev PauseEvent) (bool, error)
	// Ingest records an event idempotently on (workspace, provider_event_id) and
	// reports whether the row was NEW. False means a replay: nothing was written,
	// so nothing downstream should happen either.
	Ingest(ctx context.Context, ws uuid.UUID, in EventInput) (recorded bool, err error)
	// SendCampaign resolves the campaign a send belongs to, ErrNotFound when the
	// send is not this workspace's.
	SendCampaign(ctx context.Context, ws, sendID uuid.UUID) (uuid.UUID, error)
	// Suppress adds the address to the workspace suppression list. Idempotent.
	Suppress(ctx context.Context, ws uuid.UUID, email, reason string) error
}

// PgStore implements Store over the sqlc-generated queries. It is the only place
// in this domain that knows about gen.Queries. The pool backs PauseForBreach's
// transaction; every other method flows through the pool-bound *gen.Queries.
type PgStore struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

func NewPgStore(pool *pgxpool.Pool) *PgStore {
	return &PgStore{pool: pool, q: gen.New(pool)}
}

func (s *PgStore) CampaignCounts(ctx context.Context, ws, campaignID uuid.UUID, since time.Time) (Counts, error) {
	r, err := s.q.GetCampaignDeliverabilityCounts(ctx, gen.GetCampaignDeliverabilityCountsParams{
		WorkspaceID: ws, CampaignID: campaignID, Since: stamp(since),
	})
	if err != nil {
		return Counts{}, err
	}
	return Counts{
		Delivered: int(r.Delivered), Bounced: int(r.Bounced),
		Complained: int(r.Complained), ComplaintFeed: r.ComplaintFeed,
	}, nil
}

func (s *PgStore) WorkspaceCounts(ctx context.Context, ws uuid.UUID, since time.Time) (Counts, error) {
	r, err := s.q.GetWorkspaceDeliverabilityCounts(ctx, gen.GetWorkspaceDeliverabilityCountsParams{
		WorkspaceID: ws, Since: stamp(since),
	})
	if err != nil {
		return Counts{}, err
	}
	return Counts{
		Delivered: int(r.Delivered), Bounced: int(r.Bounced),
		Complained: int(r.Complained), ComplaintFeed: r.ComplaintFeed,
	}, nil
}

func (s *PgStore) Signals(ctx context.Context, ws uuid.UUID, mailboxIDs []uuid.UUID, since time.Time) (Signals, error) {
	r, err := s.q.GetDeliverabilitySignals(ctx, gen.GetDeliverabilitySignalsParams{
		WorkspaceID: ws, Since: day(since), MailboxIds: nonNil(mailboxIDs),
	})
	if err != nil {
		return Signals{}, err
	}
	return Signals{
		InboxPlaced: int(r.InboxPlaced), SpamPlaced: int(r.SpamPlaced),
		WarmupState: r.WarmupState, DomainState: r.DomainState,
	}, nil
}

func (s *PgStore) CampaignSenderMailboxes(ctx context.Context, ws, campaignID uuid.UUID) ([]uuid.UUID, error) {
	return s.q.ListCampaignSenderMailboxes(ctx, gen.ListCampaignSenderMailboxesParams{
		CampaignID: campaignID, WorkspaceID: ws,
	})
}

func (s *PgStore) Series(ctx context.Context, ws uuid.UUID, since time.Time) ([]Point, error) {
	rows, err := s.q.ListDeliverabilitySeries(ctx, gen.ListDeliverabilitySeriesParams{
		WorkspaceID: ws, Since: day(since),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Point, len(rows))
	for i, r := range rows {
		out[i] = Point{
			Day: r.Day.Time, Delivered: int(r.Delivered), Bounced: int(r.Bounced),
			Complained: int(r.Complained), SpamPlaced: int(r.SpamPlaced),
		}
	}
	return out, nil
}

func (s *PgStore) AtRiskMailboxes(ctx context.Context, ws uuid.UUID) ([]Risk, error) {
	rows, err := s.q.ListAtRiskMailboxes(ctx, ws)
	if err != nil {
		return nil, err
	}
	out := make([]Risk, len(rows))
	for i, r := range rows {
		out[i] = Risk{Label: r.Email, Reason: mailboxRiskReason(r.HealthState, r.HealthReason)}
	}
	return out, nil
}

// mailboxRiskReason prefers the warmup engine's own recorded reason and falls
// back to the bare state: the engine sets health_reason on a transition, but a
// row written before that existed would otherwise render an empty explanation.
func mailboxRiskReason(state, reason string) string {
	if reason != "" {
		return state + ": " + reason
	}
	return state
}

func (s *PgStore) AtRiskDomains(ctx context.Context, ws uuid.UUID) ([]Risk, error) {
	rows, err := s.q.ListAtRiskDomains(ctx, ws)
	if err != nil {
		return nil, err
	}
	out := make([]Risk, len(rows))
	for i, r := range rows {
		out[i] = Risk{Label: r.Domain, Reason: domainRiskReason(r.SpfFound, r.DmarcFound)}
	}
	return out, nil
}

// domainRiskReason names the missing records rather than repeating "failing",
// because the operator's next action is to publish one of them. DKIM is
// deliberately absent: dkim_found=false means "none of the probed selectors
// matched", not "unsigned", so it must not be reported as a missing record.
func domainRiskReason(spf, dmarc bool) string {
	switch {
	case !spf && !dmarc:
		return "no SPF or DMARC record"
	case !spf:
		return "no SPF record"
	case !dmarc:
		return "no DMARC record"
	default:
		return "authentication failing"
	}
}

func (s *PgStore) CampaignConfig(ctx context.Context, ws, campaignID uuid.UUID) (CampaignConfig, error) {
	r, err := s.q.GetCampaignGuardrails(ctx, gen.GetCampaignGuardrailsParams{
		CampaignID: campaignID, WorkspaceID: ws,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CampaignConfig{}, ErrNotFound
		}
		return CampaignConfig{}, err
	}
	return CampaignConfig{
		Guardrails: Guardrails{
			AutoPauseEnabled:  r.AutoPauseEnabled,
			BouncePausePct:    r.BouncePausePct,
			ComplaintPausePct: r.ComplaintPausePct,
		},
		Status:    r.Status,
		EnabledAt: r.GuardrailsEnabledAt.Time,
	}, nil
}

func (s *PgStore) SetGuardrails(ctx context.Context, ws, campaignID uuid.UUID, g Guardrails) error {
	n, err := s.q.SetCampaignGuardrails(ctx, gen.SetCampaignGuardrailsParams{
		CampaignID: campaignID, WorkspaceID: ws,
		AutoPauseEnabled: g.AutoPauseEnabled,
		BouncePausePct:   g.BouncePausePct, ComplaintPausePct: g.ComplaintPausePct,
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PgStore) LastPausedAt(ctx context.Context, ws, campaignID uuid.UUID) (time.Time, error) {
	ts, err := s.q.LatestCampaignPauseAt(ctx, gen.LatestCampaignPauseAtParams{
		WorkspaceID: ws, CampaignID: campaignID,
	})
	if err != nil {
		return time.Time{}, err
	}
	if !ts.Valid {
		return time.Time{}, nil
	}
	return ts.Time, nil
}

func (s *PgStore) PauseEvents(ctx context.Context, ws, campaignID uuid.UUID) ([]PauseEvent, error) {
	rows, err := s.q.ListCampaignPauseEvents(ctx, gen.ListCampaignPauseEventsParams{
		WorkspaceID: ws, CampaignID: campaignID, RowLimit: pauseEventLimit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]PauseEvent, len(rows))
	for i, r := range rows {
		out[i] = PauseEvent{
			Reason: r.Reason, Metric: r.Metric, Value: r.Value, Threshold: r.Threshold,
			Delivered: int(r.Delivered), CreatedAt: r.CreatedAt.Time,
		}
	}
	return out, nil
}

// PauseForBreach flips the campaign to paused and records why, in ONE
// transaction: a paused campaign must never be found without its explanation
// (invariant 3), and an explanation must never exist for a campaign that was not
// actually stopped.
//
// The status='running' guard inside the UPDATE is the exactly-once mechanism. A
// second evaluation — a retried task, two workers finalising sends at the same
// instant — updates zero rows, so this returns false and writes no event. The
// guard is in SQL rather than in a read-then-write here precisely so two
// concurrent evaluations cannot both pass it.
func (s *PgStore) PauseForBreach(ctx context.Context, ws, campaignID uuid.UUID, ev PauseEvent) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed
	qtx := s.q.WithTx(tx)

	n, err := qtx.PauseCampaignForBreach(ctx, gen.PauseCampaignForBreachParams{
		CampaignID: campaignID, WorkspaceID: ws,
	})
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	if err := qtx.InsertCampaignPauseEvent(ctx, gen.InsertCampaignPauseEventParams{
		WorkspaceID: ws, CampaignID: campaignID,
		Reason: ev.Reason, Metric: ev.Metric, Value: ev.Value, Threshold: ev.Threshold,
		Delivered: int32(ev.Delivered), //nolint:gosec // a delivered count above int32 is not a thing this product reaches
	}); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (s *PgStore) Ingest(ctx context.Context, ws uuid.UUID, in EventInput) (bool, error) {
	n, err := s.q.InsertDeliverabilityEvent(ctx, gen.InsertDeliverabilityEventParams{
		WorkspaceID: ws, Kind: in.Kind, Email: in.Email,
		SendID: optionalUUID(in.SendID), ProviderEventID: in.ProviderEventID,
		BounceClass: in.BounceClass,
	})
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *PgStore) SendCampaign(ctx context.Context, ws, sendID uuid.UUID) (uuid.UUID, error) {
	id, err := s.q.GetSendCampaign(ctx, gen.GetSendCampaignParams{SendID: sendID, WorkspaceID: ws})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrNotFound
		}
		return uuid.Nil, err
	}
	return id, nil
}

func (s *PgStore) Suppress(ctx context.Context, ws uuid.UUID, email, reason string) error {
	return s.q.AddSuppression(ctx, gen.AddSuppressionParams{WorkspaceID: ws, Email: email, Reason: reason})
}

func stamp(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

// day narrows an instant to the UTC calendar day the warmup stats and the series
// are keyed on (warmup_daily_stats.day is a DATE written against CURRENT_DATE
// with the session in UTC, so the window bound has to be the UTC day).
func day(t time.Time) pgtype.Date {
	u := t.UTC()
	return pgtype.Date{Time: time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC), Valid: true}
}

// optionalUUID maps an absent id to SQL NULL. nil is meaningful in both places it
// is used: "the whole workspace, not one campaign", and "the provider reported no
// send id".
func optionalUUID(id *uuid.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}

// nonNil guarantees a non-nil slice for the uuid[] parameter: pgx encodes a nil
// slice as SQL NULL, and `cardinality(NULL) = 0` is NULL rather than true, which
// would make the "empty means every mailbox" branch silently match nothing.
func nonNil(ids []uuid.UUID) []uuid.UUID {
	if ids == nil {
		return []uuid.UUID{}
	}
	return ids
}
