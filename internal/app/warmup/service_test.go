package warmup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	pwarmup "github.com/inroad/inroad/internal/platform/warmup"
)

// fakeStore is an in-memory Store for unit-testing Service without a database.
// It enforces the same workspace scoping a Postgres-backed Store would: a
// (mailbox, workspace) pair only matches when both agree, so cross-tenant reads
// miss exactly as the self-enforcing SQL does.
type fakeStore struct {
	participants map[uuid.UUID]Participant // by mailbox id
	// ownedMailboxes gates UpsertParticipant: only these (mailbox->workspace)
	// pairs "belong" to a workspace, modeling the store's self-enforcing insert.
	ownedMailboxes map[uuid.UUID]uuid.UUID
	sentToday      map[uuid.UUID]int32
	dailyStats     map[uuid.UUID][]DayStat
	overviewRows   []OverviewRow
	enabledCount   int64
	// routes is the per-destination breakdown by mailbox, already ordered as the
	// query returns it (the ORDER BY is asserted against Postgres, not here).
	routes map[uuid.UUID][]RouteRow

	// getParticipantErr, when non-nil, is returned by GetParticipant to model a
	// transient read failure (a NON-ErrNoRows error) on the merge-base read.
	getParticipantErr error

	// incidentParticipants is the workspace's pool already projected onto the fold's
	// input shape, which is what the real store hands over: whether each mailbox is
	// degraded, and the dimension values it carries. incidentParticipantsErr models a
	// read failure, which must degrade to "no incidents" rather than failing the read.
	incidentParticipants    []pwarmup.IncidentInput
	incidentParticipantsErr error

	// observerStats is the workspace's per-observer reporting record, already in the
	// trust rule's input shape. observerStatsErr models a read failure, which must
	// degrade to "no observer discounted" rather than failing the overview — the same
	// direction the WRITE side takes when the read breaks.
	observerStats    []pwarmup.ObserverStats
	observerStatsErr error

	// transitions is the decision history per mailbox, newest first.
	// transitionWorkspace, when set, is the only workspace those rows belong to,
	// modeling the query's workspace pin. transitionLimits records the limit each
	// call received, so a test can prove the cap reached the store.
	transitions         map[uuid.UUID][]Transition
	transitionWorkspace uuid.UUID
	transitionLimits    []int32

	upsertCalls         int
	contentVersionStats []pwarmup.ContentVersionStat
	contentVersionErr   error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		participants:   map[uuid.UUID]Participant{},
		ownedMailboxes: map[uuid.UUID]uuid.UUID{},
		sentToday:      map[uuid.UUID]int32{},
		dailyStats:     map[uuid.UUID][]DayStat{},
		transitions:    map[uuid.UUID][]Transition{},
		routes:         map[uuid.UUID][]RouteRow{},
	}
}

func (s *fakeStore) UpsertParticipant(_ context.Context, arg UpsertParams) (Participant, error) {
	s.upsertCalls++
	if s.ownedMailboxes[arg.MailboxID] != arg.WorkspaceID {
		return Participant{}, ErrMailboxNotInWorkspace
	}
	existing, ok := s.participants[arg.MailboxID]
	started := existing.StartedAt
	if !ok {
		started = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
	}
	p := Participant{
		MailboxID:     arg.MailboxID,
		WorkspaceID:   arg.WorkspaceID,
		Enabled:       true,
		StartVolume:   arg.StartVolume,
		MaxVolume:     arg.MaxVolume,
		RampIncrement: arg.RampIncrement,
		ReplyRate:     arg.ReplyRate,
		StartedAt:     started,
		HealthState:   "healthy",
	}
	if ok {
		p.HealthState = existing.HealthState
		// The ON CONFLICT arm names the columns it writes and is_sentinel is not
		// among them, so an update to the ramp settings leaves a designation alone.
		// A fake that dropped it would let the service silently undesignate every
		// sentinel whose settings were edited, and the test would still pass.
		p.IsSentinel = existing.IsSentinel
	}
	s.participants[arg.MailboxID] = p
	return p, nil
}

func (s *fakeStore) GetParticipant(_ context.Context, workspaceID, mailboxID uuid.UUID) (Participant, error) {
	if s.getParticipantErr != nil {
		return Participant{}, s.getParticipantErr
	}
	p, ok := s.participants[mailboxID]
	if !ok || p.WorkspaceID != workspaceID {
		return Participant{}, pgx.ErrNoRows
	}
	return p, nil
}

func (s *fakeStore) DisableParticipant(_ context.Context, workspaceID, mailboxID uuid.UUID) (int64, error) {
	p, ok := s.participants[mailboxID]
	if !ok || p.WorkspaceID != workspaceID {
		return 0, nil
	}
	delete(s.participants, mailboxID)
	return 1, nil
}

func (s *fakeStore) CountEnabledParticipants(_ context.Context, _ uuid.UUID) (int64, error) {
	return s.enabledCount, nil
}

func (s *fakeStore) DailyStats(_ context.Context, workspaceID, mailboxID uuid.UUID) ([]DayStat, error) {
	return s.dailyStats[mailboxID], nil
}

func (s *fakeStore) SentToday(_ context.Context, _, mailboxID uuid.UUID) (int32, error) {
	return s.sentToday[mailboxID], nil
}

// contentVersionStats and its error let a test drive both the reported list and the
// degrade-to-empty path, which is the behaviour that keeps the overview from going
// dark when one advisory rollup fails.
func (s *fakeStore) ListContentVersionStats(_ context.Context, _ uuid.UUID) ([]pwarmup.ContentVersionStat, error) {
	return s.contentVersionStats, s.contentVersionErr
}

func (s *fakeStore) ListOverviewRows(_ context.Context, _ uuid.UUID) ([]OverviewRow, error) {
	return s.overviewRows, nil
}

// ListRoutes models the workspace pin the SQL enforces: a mailbox read under the
// wrong workspace has no routes, exactly as the WHERE clause returns none.
func (s *fakeStore) ListRoutes(_ context.Context, workspaceID, mailboxID uuid.UUID) ([]RouteRow, error) {
	p, ok := s.participants[mailboxID]
	if !ok || p.WorkspaceID != workspaceID {
		return nil, nil
	}
	return s.routes[mailboxID], nil
}

func (s *fakeStore) ListIncidentParticipants(_ context.Context, _ uuid.UUID) ([]pwarmup.IncidentInput, error) {
	if s.incidentParticipantsErr != nil {
		return nil, s.incidentParticipantsErr
	}
	return s.incidentParticipants, nil
}

func (s *fakeStore) ListObserverStats(_ context.Context, _ uuid.UUID) ([]pwarmup.ObserverStats, error) {
	if s.observerStatsErr != nil {
		return nil, s.observerStatsErr
	}
	return s.observerStats, nil
}

func (s *fakeStore) MailboxInWorkspace(_ context.Context, workspaceID, mailboxID uuid.UUID) (bool, error) {
	owner, ok := s.ownedMailboxes[mailboxID]
	return ok && owner == workspaceID, nil
}

// ListTransitions models the SQL: workspace-pinned, newest first, capped at the
// limit the service resolved. The fake stores rows newest-first already.
func (s *fakeStore) ListTransitions(_ context.Context, workspaceID, mailboxID uuid.UUID, limit int32) ([]Transition, error) {
	s.transitionLimits = append(s.transitionLimits, limit)
	rows := s.transitions[mailboxID]
	if s.transitionWorkspace != uuid.Nil && s.transitionWorkspace != workspaceID {
		return nil, nil
	}
	if int32(len(rows)) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func ptrI32(v int32) *int32     { return &v }
func ptrF32(v float32) *float32 { return &v }

// fixedNow pins the service clock for deterministic ramp math.
func withNow(svc *Service, t time.Time) *Service {
	svc.now = func() time.Time { return t }
	return svc
}

// TestEnableWarmupDefaultsAndTodayTargetDay0 proves a first enable with an empty
// body seeds the package defaults and that today's target on day 0 equals
// start_volume (the ramp base).
func TestEnableWarmupDefaultsAndTodayTargetDay0(t *testing.T) {
	ws, mb := uuid.New(), uuid.New()
	store := newFakeStore()
	store.ownedMailboxes[mb] = ws
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	// started_at will be set to time.Now() by the fake on insert; pin the clock
	// close so daysWarming reads 0. The fake stamps started_at at real now, so we
	// also pin the service clock to real now for this day-0 assertion.
	svc := withNow(NewService(store), time.Now().UTC())
	_ = now

	got, err := svc.EnableWarmup(context.Background(), ws, mb, WarmupSettings{})
	if err != nil {
		t.Fatalf("EnableWarmup: %v", err)
	}
	if got.StartVolume != defaultStartVolume || got.MaxVolume != defaultMaxVolume ||
		got.RampIncrement != defaultRampIncrement || got.ReplyRate != defaultReplyRate {
		t.Fatalf("defaults not applied: %+v", got)
	}
	if !got.Enabled {
		t.Fatalf("want enabled")
	}
	if got.TodayTarget != defaultStartVolume {
		t.Fatalf("day-0 target = start_volume: got %d want %d", got.TodayTarget, defaultStartVolume)
	}
}

// TestTodayTargetRampAndCap proves today_target ramps by increment per whole UTC
// day and is capped at max_volume.
func TestTodayTargetRampAndCap(t *testing.T) {
	started := pgtype.Timestamptz{Time: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), Valid: true}
	p := Participant{StartVolume: 4, MaxVolume: 40, RampIncrement: 2, StartedAt: started, HealthState: "healthy"}

	// day 5 -> 4 + 5*2 = 14
	day5 := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	svc := withNow(NewService(newFakeStore()), day5)
	if got := svc.participantDTO(p, 0).TodayTarget; got != 14 {
		t.Fatalf("day 5 target: got %d want 14", got)
	}

	// day 100 -> capped at max 40
	day100 := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	svc = withNow(NewService(newFakeStore()), day100)
	if got := svc.participantDTO(p, 0).TodayTarget; got != 40 {
		t.Fatalf("day 100 target: got %d want 40 (capped)", got)
	}
}

// TestTodayTargetPausedIsZero proves a paused participant reports today_target 0
// regardless of ramp.
func TestTodayTargetPausedIsZero(t *testing.T) {
	started := pgtype.Timestamptz{Time: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), Valid: true}
	p := Participant{StartVolume: 4, MaxVolume: 40, RampIncrement: 2, StartedAt: started, HealthState: "paused"}
	svc := withNow(NewService(newFakeStore()), time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC))
	if got := svc.participantDTO(p, 0).TodayTarget; got != 0 {
		t.Fatalf("paused target: got %d want 0", got)
	}
}

// TestEnablePartialUpdateKeepsExisting proves an update with only one field set
// keeps the participant's other current values.
func TestEnablePartialUpdateKeepsExisting(t *testing.T) {
	ws, mb := uuid.New(), uuid.New()
	store := newFakeStore()
	store.ownedMailboxes[mb] = ws
	svc := withNow(NewService(store), time.Now().UTC())

	if _, err := svc.EnableWarmup(context.Background(), ws, mb, WarmupSettings{
		StartVolume: ptrI32(10), MaxVolume: ptrI32(80), RampIncrement: ptrI32(5), ReplyRate: ptrF32(0.5),
	}); err != nil {
		t.Fatalf("initial enable: %v", err)
	}
	// Update only max_volume; everything else must persist.
	got, err := svc.EnableWarmup(context.Background(), ws, mb, WarmupSettings{MaxVolume: ptrI32(120)})
	if err != nil {
		t.Fatalf("partial update: %v", err)
	}
	if got.StartVolume != 10 || got.RampIncrement != 5 || got.ReplyRate != 0.5 {
		t.Fatalf("partial update lost existing values: %+v", got)
	}
	if got.MaxVolume != 120 {
		t.Fatalf("partial update did not apply max_volume: got %d", got.MaxVolume)
	}
}

// TestEnablePartialUpdatePropagatesReadError proves a transient (NON-ErrNoRows)
// read failure on the merge-base read of an EXISTING participant is propagated,
// not swallowed to defaults. If it were swallowed, the partial update would merge
// over defaults and the upsert would overwrite the live start/max/increment/reply
// settings back to defaults (silent data corruption). The upsert must NOT run.
func TestEnablePartialUpdatePropagatesReadError(t *testing.T) {
	ws, mb := uuid.New(), uuid.New()
	store := newFakeStore()
	store.ownedMailboxes[mb] = ws
	// Model an existing participant whose current settings the merge would keep.
	store.participants[mb] = Participant{
		MailboxID: mb, WorkspaceID: ws, Enabled: true,
		StartVolume: 10, MaxVolume: 80, RampIncrement: 5, ReplyRate: 0.5, HealthState: "healthy",
	}
	readErr := errors.New("transient read failure")
	store.getParticipantErr = readErr
	svc := NewService(store)

	// Partial update (only max_volume) — the merge base MUST come from a good read.
	_, err := svc.EnableWarmup(context.Background(), ws, mb, WarmupSettings{MaxVolume: ptrI32(120)})
	if !errors.Is(err, readErr) {
		t.Fatalf("partial update read failure: want wrapped %v, got %v", readErr, err)
	}
	if store.upsertCalls != 0 {
		t.Fatalf("a failed merge-base read must not reach the upsert: upsertCalls=%d", store.upsertCalls)
	}
}

// TestEnableValidation covers the boundary rules: start>max, out-of-range
// reply_rate, max over ceiling, and increment < 1 all reject with ErrValidation
// and never reach the store.
func TestEnableValidation(t *testing.T) {
	ws, mb := uuid.New(), uuid.New()
	cases := []struct {
		name string
		s    WarmupSettings
	}{
		{"start over max", WarmupSettings{StartVolume: ptrI32(50), MaxVolume: ptrI32(40)}},
		{"reply rate over 1", WarmupSettings{ReplyRate: ptrF32(1.5)}},
		{"reply rate below 0", WarmupSettings{ReplyRate: ptrF32(-0.1)}},
		{"max over ceiling", WarmupSettings{MaxVolume: ptrI32(500)}},
		{"start below 1", WarmupSettings{StartVolume: ptrI32(0)}},
		{"increment below 1", WarmupSettings{RampIncrement: ptrI32(0)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := newFakeStore()
			store.ownedMailboxes[mb] = ws
			svc := NewService(store)
			_, err := svc.EnableWarmup(context.Background(), ws, mb, c.s)
			if !errors.Is(err, ErrValidation) {
				t.Fatalf("want ErrValidation, got %v", err)
			}
			if store.upsertCalls != 0 {
				t.Fatalf("validation must reject before the store: upsertCalls=%d", store.upsertCalls)
			}
		})
	}
}

// TestEnableCrossTenantIsNotFound proves a mailbox not owned by the workspace
// surfaces the store's ErrMailboxNotInWorkspace as a domain ErrNotFound (handler
// 404), never binding a foreign mailbox.
func TestEnableCrossTenantIsNotFound(t *testing.T) {
	ws, other, mb := uuid.New(), uuid.New(), uuid.New()
	store := newFakeStore()
	store.ownedMailboxes[mb] = other // owned by a different workspace
	svc := NewService(store)

	_, err := svc.EnableWarmup(context.Background(), ws, mb, WarmupSettings{})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant enable: want ErrNotFound, got %v", err)
	}
}

// TestGetDetailNotParticipant proves a mailbox that is not a warmup participant
// (store miss -> pgx.ErrNoRows) is ErrNotFound.
func TestGetDetailNotParticipant(t *testing.T) {
	ws, mb := uuid.New(), uuid.New()
	svc := NewService(newFakeStore())
	_, err := svc.GetWarmupDetail(context.Background(), ws, mb)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("detail for non-participant: want ErrNotFound, got %v", err)
	}
}

// TestGetDetailSeries proves the detail returns the participant plus its day
// series mapped to the wire shape.
func TestGetDetailSeries(t *testing.T) {
	ws, mb := uuid.New(), uuid.New()
	store := newFakeStore()
	store.participants[mb] = Participant{
		MailboxID: mb, WorkspaceID: ws, Enabled: true,
		StartVolume: 4, MaxVolume: 40, RampIncrement: 2, ReplyRate: 0.3, HealthState: "healthy",
		StartedAt: pgtype.Timestamptz{Time: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), Valid: true},
	}
	store.sentToday[mb] = 3
	store.dailyStats[mb] = []DayStat{
		{Day: pgtype.Date{Time: time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC), Valid: true}, Sent: 5, Received: 4, Inbox: 3, Spam: 1, Replies: 2},
	}
	svc := withNow(NewService(store), time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC))

	d, err := svc.GetWarmupDetail(context.Background(), ws, mb)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if d.Participant.TodaySent != 3 {
		t.Fatalf("today_sent: got %d want 3", d.Participant.TodaySent)
	}
	if d.Participant.TodayTarget != 14 { // day 5
		t.Fatalf("today_target: got %d want 14", d.Participant.TodayTarget)
	}
	if len(d.Series) != 1 || d.Series[0].Day != "2026-07-25" || d.Series[0].Inbox != 3 {
		t.Fatalf("series mapping wrong: %+v", d.Series)
	}
}

// TestOverviewActiveThreshold proves active is false below 2 participants and
// true at >= 2, and that placement rates and today_target are computed per row.
func TestOverviewActiveThreshold(t *testing.T) {
	ws := uuid.New()
	mb := uuid.New()
	started := pgtype.Timestamptz{Time: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), Valid: true}
	row := OverviewRow{
		MailboxID: mb, Enabled: true, StartVolume: 4, MaxVolume: 40, RampIncrement: 2,
		ReplyRate: 0.3, StartedAt: started, HealthState: "healthy", Email: "a@example.com",
		Inbox7d: 8, Spam7d: 2, TodaySent: 6,
	}

	// pool of 1 -> inactive
	store := newFakeStore()
	store.enabledCount = 1
	store.overviewRows = []OverviewRow{row}
	svc := withNow(NewService(store), time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC))
	ov, err := svc.GetOverview(context.Background(), ws)
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if ov.PoolSize != 1 || ov.Active {
		t.Fatalf("pool 1 must be inactive: %+v", ov)
	}
	if len(ov.Mailboxes) != 1 {
		t.Fatalf("want 1 mailbox, got %d", len(ov.Mailboxes))
	}
	m := ov.Mailboxes[0]
	if m.Email != "a@example.com" || m.TodaySent != 6 {
		t.Fatalf("mailbox fields wrong: %+v", m)
	}
	if m.TodayTarget != 14 { // day 5 ramp
		t.Fatalf("today_target: got %d want 14", m.TodayTarget)
	}
	if m.InboxRate7d == nil || m.SpamRate7d == nil || *m.InboxRate7d != 0.8 || *m.SpamRate7d != 0.2 || m.PlacementSample7d != 10 { // 8/10, 2/10
		t.Fatalf("placement rates: got inbox=%v spam=%v", m.InboxRate7d, m.SpamRate7d)
	}

	// pool of 2 -> active
	store.enabledCount = 2
	ov, err = svc.GetOverview(context.Background(), ws)
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if ov.PoolSize != 2 || !ov.Active {
		t.Fatalf("pool 2 must be active: %+v", ov)
	}
}

// TestOverviewZeroPlacementRate proves an empty 7-day window yields no measured
// rate (not a fabricated zero) and a paused row reports today_target 0.
func TestOverviewZeroPlacementRateAndPaused(t *testing.T) {
	ws := uuid.New()
	row := OverviewRow{
		MailboxID: uuid.New(), Enabled: true, StartVolume: 4, MaxVolume: 40, RampIncrement: 2,
		StartedAt:   pgtype.Timestamptz{Time: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), Valid: true},
		HealthState: "paused", Email: "p@example.com", Inbox7d: 0, Spam7d: 0,
	}
	store := newFakeStore()
	store.enabledCount = 3
	store.overviewRows = []OverviewRow{row}
	svc := withNow(NewService(store), time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC))
	ov, err := svc.GetOverview(context.Background(), ws)
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	m := ov.Mailboxes[0]
	if m.InboxRate7d != nil || m.SpamRate7d != nil || m.PlacementSample7d != 0 {
		t.Fatalf("empty window must be unmeasured: %+v", m)
	}
	if m.TodayTarget != 0 {
		t.Fatalf("paused today_target: got %d want 0", m.TodayTarget)
	}
}

// The tabbed rate is reported over ITS OWN denominator: the placements whose reader
// could have named a tab. Here 3 of 12 tab-capable observations were tabbed (25%)
// while the mailbox has 40 inbox-side placements in total — the 8% that pooling them
// would produce is the bounce-denominator defect applied to a new signal.
//
// The inbox rate is asserted alongside on purpose: tabbed landings stay on the inbox
// side of it, so widening the vocabulary must not move a number the operator was
// already reading.
func TestOverviewTabbedRateUsesTheTabCapableDenominator(t *testing.T) {
	ws := uuid.New()
	row := OverviewRow{
		MailboxID: uuid.New(), Enabled: true, StartVolume: 4, MaxVolume: 40, RampIncrement: 2,
		StartedAt:   pgtype.Timestamptz{Time: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), Valid: true},
		HealthState: "healthy", Email: "t@example.com",
		Inbox7d: 40, Spam7d: 10, Tabbed7d: 3, TabCapable7d: 12,
	}
	store := newFakeStore()
	store.enabledCount = 2
	store.overviewRows = []OverviewRow{row}
	svc := withNow(NewService(store), time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC))

	ov, err := svc.GetOverview(context.Background(), ws)
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	m := ov.Mailboxes[0]
	if m.TabCapableSample7d != 12 {
		t.Fatalf("tab_capable_sample_7d = %d, want 12", m.TabCapableSample7d)
	}
	if m.TabbedRate7d == nil {
		t.Fatal("tabbed_rate_7d is null with 12 tab-capable observations to measure")
	}
	if *m.TabbedRate7d != 0.25 {
		t.Fatalf("tabbed_rate_7d = %v, want 0.25 (3 of 12 tab-capable, not 3 of 50)", *m.TabbedRate7d)
	}
	if m.InboxRate7d == nil || *m.InboxRate7d != 0.8 || m.PlacementSample7d != 50 {
		t.Fatalf("the inbox-side numbers moved: inbox=%v sample=%d, want 0.8 / 50",
			m.InboxRate7d, m.PlacementSample7d)
	}
}

// Null, never 0.0, when nothing observing the mailbox could report a category. A
// zero would read as a confident clean rate for a mailbox whose tabs are simply
// invisible — which is every SMTP mailbox, i.e. most of a self-hosted pool.
func TestOverviewTabbedRateIsNullWhenNothingCouldReportATab(t *testing.T) {
	ws := uuid.New()
	row := OverviewRow{
		MailboxID: uuid.New(), Enabled: true, StartVolume: 4, MaxVolume: 40, RampIncrement: 2,
		StartedAt:   pgtype.Timestamptz{Time: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), Valid: true},
		HealthState: "healthy", Email: "imap@example.com",
		Inbox7d: 30, Spam7d: 2, Tabbed7d: 0, TabCapable7d: 0,
	}
	store := newFakeStore()
	store.enabledCount = 2
	store.overviewRows = []OverviewRow{row}
	svc := withNow(NewService(store), time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC))

	ov, err := svc.GetOverview(context.Background(), ws)
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	m := ov.Mailboxes[0]
	if m.TabbedRate7d != nil {
		t.Fatalf("tabbed_rate_7d = %v, want null: a rate over no measurable population is not zero", *m.TabbedRate7d)
	}
	if m.TabCapableSample7d != 0 {
		t.Fatalf("tab_capable_sample_7d = %d, want 0", m.TabCapableSample7d)
	}
	// The placement rates a whole provider class CAN report are unaffected.
	if m.InboxRate7d == nil || m.PlacementSample7d != 32 {
		t.Fatalf("inbox-side numbers wrong: inbox=%v sample=%d", m.InboxRate7d, m.PlacementSample7d)
	}
}

// TestDisableIdempotent proves disabling a never-enrolled mailbox is a clean
// success (matches the 204 contract).
func TestDisableIdempotent(t *testing.T) {
	ws, mb := uuid.New(), uuid.New()
	svc := NewService(newFakeStore())
	if err := svc.DisableWarmup(context.Background(), ws, mb); err != nil {
		t.Fatalf("disable of non-participant should be nil, got %v", err)
	}
}

// strPtr is a local helper for the nullable lane fields.
func strPtr(s string) *string { return &s }

// TestListTransitionsForeignMailboxIs404 proves the ownership gate: a mailbox
// that is not this workspace's is absent, not empty. Without the gate the caller
// would get a 200 and an empty page for any uuid, which leaks nothing but does
// tell a probe that the endpoint exists for ids it does not own.
func TestListTransitionsForeignMailboxIs404(t *testing.T) {
	ws, other, mb := uuid.New(), uuid.New(), uuid.New()
	store := newFakeStore()
	store.ownedMailboxes[mb] = other
	svc := NewService(store)

	if _, err := svc.ListTransitions(context.Background(), ws, mb, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound for a foreign mailbox, got %v", err)
	}
}

// TestListTransitionsOwnedMailboxWithNoHistoryIsEmptyPage proves the 404 test is
// OWNERSHIP, not participation: a mailbox in the workspace that never warmed up
// returns an empty page, so the UI renders "no changes yet" rather than "gone".
func TestListTransitionsOwnedMailboxWithNoHistoryIsEmptyPage(t *testing.T) {
	ws, mb := uuid.New(), uuid.New()
	store := newFakeStore()
	store.ownedMailboxes[mb] = ws
	page, err := NewService(store).ListTransitions(context.Background(), ws, mb, 0)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(page.Transitions) != 0 {
		t.Fatalf("want empty page, got %+v", page.Transitions)
	}
}

// TestListTransitionsLimitBounds proves the contract's bounds are applied where
// the store can see them: omitted resolves to 50, an oversized ask is capped at
// 200, and a value inside the range passes through.
func TestListTransitionsLimitBounds(t *testing.T) {
	ws, mb := uuid.New(), uuid.New()
	store := newFakeStore()
	store.ownedMailboxes[mb] = ws
	svc := NewService(store)

	for _, asked := range []int32{0, 1000, 25} {
		if _, err := svc.ListTransitions(context.Background(), ws, mb, asked); err != nil {
			t.Fatalf("ListTransitions(%d): %v", asked, err)
		}
	}
	want := []int32{defaultTransitionLimit, maxTransitionLimit, 25}
	if len(store.transitionLimits) != len(want) {
		t.Fatalf("limits recorded: got %v want %v", store.transitionLimits, want)
	}
	for i, w := range want {
		if store.transitionLimits[i] != w {
			t.Fatalf("limit %d: got %d want %d", i, store.transitionLimits[i], w)
		}
	}
}

// TestListTransitionsMapsEveryContractField proves the wire shape carries every
// required field, that a pre-lane row's lane fields serialize as null rather
// than "", and that a stored REAL is not surfaced with its float32 artefacts.
func TestListTransitionsMapsEveryContractField(t *testing.T) {
	ws, mb := uuid.New(), uuid.New()
	id := uuid.New()
	created := time.Date(2026, 8, 12, 9, 30, 0, 0, time.UTC)
	store := newFakeStore()
	store.ownedMailboxes[mb] = ws
	store.transitions[mb] = []Transition{
		{
			ID: id, CreatedAt: pgtype.Timestamptz{Time: created, Valid: true},
			FromState: "healthy", ToState: "watch",
			ReasonCode: "spam_watch", Reason: "spam placement rate above the watch threshold",
			FromLane: strPtr("healthy"), ToLane: strPtr("watch"),
			LaneReasonCode: strPtr("lane_watch"), LaneReason: strPtr("moved to watch"),
			PlacementSamples: 40, SpamRate: 0.15,
			BouncePopulation: strPtr("campaign"),
			BounceSamples:    200, BounceRate: 0.02,
			ComplaintSamples: 1000, ComplaintRate: 0.0003,
			InvalidTokens: 2, PolicyVersion: "warmup-phase1-v1",
		},
		// A row written before pool lanes existed: all four lane columns NULL, and
		// no bounce_population either — that row genuinely does not know which arm
		// spoke, and guessing would put a false claim in an append-only trail.
		{ID: uuid.New(), CreatedAt: pgtype.Timestamptz{Time: created.Add(-time.Hour), Valid: true},
			FromState: "unknown", ToState: "healthy", ReasonCode: "evidence_qualified",
			Reason: "qualified", PolicyVersion: "warmup-phase0-v1"},
	}

	page, err := NewService(store).ListTransitions(context.Background(), ws, mb, 0)
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(page.Transitions) != 2 {
		t.Fatalf("want 2 rows, got %d", len(page.Transitions))
	}
	got := page.Transitions[0]
	if got.ID != id.String() || got.CreatedAt != created.Format(time.RFC3339) {
		t.Fatalf("identity/timestamp wrong: %+v", got)
	}
	if got.FromState != "healthy" || got.ToState != "watch" ||
		got.ReasonCode != "spam_watch" || got.PolicyVersion != "warmup-phase1-v1" {
		t.Fatalf("health axis wrong: %+v", got)
	}
	if got.FromLane == nil || *got.FromLane != "healthy" || got.ToLane == nil || *got.ToLane != "watch" ||
		got.LaneReasonCode == nil || *got.LaneReasonCode != "lane_watch" {
		t.Fatalf("lane axis wrong: %+v", got)
	}
	if got.SpamRate != 0.15 || got.BounceRate != 0.02 || got.ComplaintRate != 0.0003 {
		t.Fatalf("rates carry float32 artefacts: %v %v %v", got.SpamRate, got.BounceRate, got.ComplaintRate)
	}
	if got.PlacementSamples != 40 || got.BounceSamples != 200 || got.ComplaintSamples != 1000 || got.InvalidTokens != 2 {
		t.Fatalf("samples wrong: %+v", got)
	}
	// The bounce pair is meaningless without the population it counted: 200 samples
	// at 2% is a claim about campaign mail or about warmup mail, never about
	// "bounces".
	if got.BouncePopulation == nil || *got.BouncePopulation != "campaign" {
		t.Fatalf("bounce_population = %v, want campaign — the bounce pair must name its arm", got.BouncePopulation)
	}
	if pre := page.Transitions[1]; pre.FromLane != nil || pre.ToLane != nil ||
		pre.LaneReasonCode != nil || pre.LaneReason != nil || pre.BouncePopulation != nil {
		t.Fatalf("a pre-lane row must serialize its lane and population fields as null: %+v", pre)
	}
}

// The identity block is emitted whole, from the latest observation that carried
// one, with observed_at so the reader can judge how stale it is. It is metadata:
// health_state, lane and every rate beside it must read identically to a row
// without it, which is asserted here rather than assumed.
func TestOverviewCarriesTheLatestObservedIdentity(t *testing.T) {
	ws := uuid.New()
	observed := time.Date(2026, 8, 14, 9, 30, 0, 0, time.UTC)
	row := OverviewRow{
		MailboxID: uuid.New(), Enabled: true, StartVolume: 4, MaxVolume: 40, RampIncrement: 2,
		StartedAt:   pgtype.Timestamptz{Time: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), Valid: true},
		HealthState: "healthy", Lane: "healthy", Email: "signed@example.com",
		Inbox7d: 40, Spam7d: 10,
		IdentityDKIMDomain: "acme.test", IdentityReturnPathDomain: "mail.acme.test",
		IdentitySPFResult: "pass", IdentityDKIMResult: "pass", IdentityDMARCResult: "fail",
		IdentityObservedAt: pgtype.Timestamptz{Time: observed, Valid: true},
	}
	store := newFakeStore()
	store.enabledCount = 2
	store.overviewRows = []OverviewRow{row}
	svc := withNow(NewService(store), time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC))

	ov, err := svc.GetOverview(context.Background(), ws)
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	m := ov.Mailboxes[0]
	if m.Identity == nil {
		t.Fatal("identity is null for a mailbox whose latest observation carried one")
	}
	want := WarmupIdentityDTO{
		DKIMDomain: "acme.test", ReturnPathDomain: "mail.acme.test",
		SPFResult: "pass", DKIMResult: "pass", DMARCResult: "fail",
		ObservedAt: "2026-08-14T09:30:00Z",
	}
	if *m.Identity != want {
		t.Errorf("identity = %+v, want %+v", *m.Identity, want)
	}
	// Metadata, not evidence: nothing an operator or the engine reads may move.
	if m.HealthState != "healthy" || m.Lane != "healthy" || m.PlacementSample7d != 50 {
		t.Errorf("recording an identity moved a decision input: health=%q lane=%q sample=%d",
			m.HealthState, m.Lane, m.PlacementSample7d)
	}
}

// Null, not a block of defaults. Two empty domains and three unknown verdicts is
// what every observation written before identity extraction carries, so emitting it would
// state "we looked and found nothing" about a message nobody ever looked at — and
// would leave the UI no way to distinguish an unsigned sender from an unobserved
// one. The presence of the timestamp is the only signal that says which.
func TestOverviewIdentityIsNullWhenNoObservationCarriedOne(t *testing.T) {
	ws := uuid.New()
	row := OverviewRow{
		MailboxID: uuid.New(), Enabled: true, StartVolume: 4, MaxVolume: 40, RampIncrement: 2,
		StartedAt:   pgtype.Timestamptz{Time: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), Valid: true},
		HealthState: "healthy", Lane: "healthy", Email: "quiet@example.com",
		Inbox7d: 30, Spam7d: 2,
		// What the LEFT JOIN miss produces: the column defaults, and no timestamp.
		IdentitySPFResult: "unknown", IdentityDKIMResult: "unknown", IdentityDMARCResult: "unknown",
	}
	store := newFakeStore()
	store.enabledCount = 2
	store.overviewRows = []OverviewRow{row}
	svc := withNow(NewService(store), time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC))

	ov, err := svc.GetOverview(context.Background(), ws)
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if got := ov.Mailboxes[0].Identity; got != nil {
		t.Fatalf("identity = %+v, want nil: no observation has carried identity facts, which is "+
			"not the same fact as an unsigned sender with no verdicts", *got)
	}
}

// THE denominator rule, for the third time in this subsystem (bounce populations,
// tab capability, and now routes): each route's rates are computed over ITS OWN
// sample count, never over the mailbox's pooled total.
//
// The fixture makes the two answers far apart on purpose. Per route the spam rates
// are 5% and 60%; pooled they would both read 26% — a number that describes neither
// route and would have an operator chasing a Google problem that does not exist
// while missing a Microsoft one that does.
func TestDetailRouteRatesUsePerRouteDenominators(t *testing.T) {
	ws, mb := uuid.New(), uuid.New()
	store := newFakeStore()
	store.participants[mb] = routeParticipant(ws, mb)
	store.routes[mb] = []RouteRow{
		{DestinationESP: "google", Inbox7d: 38, Spam7d: 2, Tabbed7d: 2, TabCapable7d: 20},
		{DestinationESP: "microsoft", Inbox7d: 10, Spam7d: 15},
	}
	svc := withNow(NewService(store), time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC))

	d, err := svc.GetWarmupDetail(context.Background(), ws, mb)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if len(d.Routes) != 2 {
		t.Fatalf("got %d routes, want 2: %+v", len(d.Routes), d.Routes)
	}

	google, microsoft := d.Routes[0], d.Routes[1]
	if google.PlacementSample7d != 40 || microsoft.PlacementSample7d != 25 {
		t.Fatalf("samples = %d/%d, want 40/25", google.PlacementSample7d, microsoft.PlacementSample7d)
	}
	assertRate(t, "google spam_rate_7d", google.SpamRate7d, 0.05)
	assertRate(t, "google inbox_rate_7d", google.InboxRate7d, 0.95)
	assertRate(t, "microsoft spam_rate_7d", microsoft.SpamRate7d, 0.60)
	assertRate(t, "microsoft inbox_rate_7d", microsoft.InboxRate7d, 0.40)

	// The tabbed rate keeps its OWN denominator inside the route, exactly as it does
	// on the overview: 2 of 20 categorisable landings, not 2 of the route's 40.
	assertRate(t, "google tabbed_rate_7d", google.TabbedRate7d, 0.1)
	if google.TabCapableSample7d != 20 {
		t.Errorf("google tab_capable_sample_7d = %d, want 20", google.TabCapableSample7d)
	}
	if microsoft.TabbedRate7d != nil {
		t.Errorf("microsoft tabbed_rate_7d = %v, want null: nothing that observed this route could "+
			"report a category, and 0.0 would read as a confident clean rate", *microsoft.TabbedRate7d)
	}
}

// Splitting a window by destination shrinks every cell, so the existing sample
// floor matters more here than anywhere it has been applied before — a four-route
// pool quarters every denominator. A route under it reports its sample and NO
// rates: absence of evidence is not a clean rate.
func TestDetailRouteBelowTheSampleFloorReportsNoRates(t *testing.T) {
	ws, mb := uuid.New(), uuid.New()
	// Derived from the EXISTING constant rather than a literal, so the fixture
	// cannot drift away from the floor it is testing — and so a per-route minimum
	// invented later fails here instead of quietly replacing this one.
	floor := int64(pwarmup.MinPlacementSamples)
	store := newFakeStore()
	store.participants[mb] = routeParticipant(ws, mb)
	store.routes[mb] = []RouteRow{
		// One sample short of the floor, and every one of them spam: the rate this
		// suppresses would be an alarming 100% computed from a handful of messages.
		{DestinationESP: "microsoft", Inbox7d: 0, Spam7d: floor - 1, TabCapable7d: floor - 1},
		{DestinationESP: "unknown", Inbox7d: floor, Spam7d: 0, TabCapable7d: floor},
	}
	svc := withNow(NewService(store), time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC))

	d, err := svc.GetWarmupDetail(context.Background(), ws, mb)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	thin, established := d.Routes[0], d.Routes[1]

	if thin.PlacementSample7d != floor-1 {
		t.Errorf("placement_sample_7d = %d, want %d: the count is reported even when the rates are not",
			thin.PlacementSample7d, floor-1)
	}
	for name, rate := range map[string]*float64{
		"inbox_rate_7d":  thin.InboxRate7d,
		"spam_rate_7d":   thin.SpamRate7d,
		"tabbed_rate_7d": thin.TabbedRate7d,
	} {
		if rate != nil {
			t.Errorf("%s = %v on a %d-sample route, want null: below MinPlacementSamples (%d) a route "+
				"is not established", name, *rate, thin.PlacementSample7d, floor)
		}
	}
	// The floor is the EXISTING constant, not a per-route invention: one more sample
	// than the thin route has is exactly what establishes the other one.
	if established.SpamRate7d == nil || established.InboxRate7d == nil {
		t.Fatalf("a %d-sample route reports no rates; MinPlacementSamples is %d, so it qualifies",
			established.PlacementSample7d, floor)
	}
}

// Always present, `[]` and never null: a client distinguishing "no rows" from
// "field missing" is a distinction with no meaning, and the absent form is the one
// that arrives as `undefined` and takes a UI down a fallback path.
func TestDetailRoutesAreAnEmptySliceWhenNothingWasObserved(t *testing.T) {
	ws, mb := uuid.New(), uuid.New()
	store := newFakeStore()
	store.participants[mb] = routeParticipant(ws, mb)
	svc := withNow(NewService(store), time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC))

	d, err := svc.GetWarmupDetail(context.Background(), ws, mb)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if d.Routes == nil {
		t.Fatal("routes is nil, want an empty slice: it serializes as null and the SPA reads undefined")
	}
	if len(d.Routes) != 0 {
		t.Fatalf("routes = %+v, want empty", d.Routes)
	}
}

// routeParticipant is the minimal enabled participant the route tests hang off.
func routeParticipant(ws, mb uuid.UUID) Participant {
	return Participant{
		MailboxID: mb, WorkspaceID: ws, Enabled: true,
		StartVolume: 4, MaxVolume: 40, RampIncrement: 2, ReplyRate: 0.3, HealthState: "healthy",
		StartedAt: pgtype.Timestamptz{Time: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), Valid: true},
	}
}

// The overview's incident view, which is the whole operator-visible half of the
// correlated-incident slice: the arithmetic that produced the finding, not a verdict.
//
// The fixture is the case the concentration test exists to FIND: four mailboxes sign
// as bad.test and three of them are degrading (75%), against a pool of twenty where
// five are (25%) — a lift of 3. The outside is deliberately not clean, so the test
// cannot pass on a division by an empty denominator.
func TestOverviewReportsACorrelatedIncidentWithItsArithmetic(t *testing.T) {
	ws := uuid.New()
	store := newFakeStore()
	store.enabledCount = 24
	store.incidentParticipants = incidentPool()

	svc := withNow(NewService(store), time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC))
	ov, err := svc.GetOverview(context.Background(), ws)
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if len(ov.Incidents) != 1 {
		t.Fatalf("incidents = %+v, want exactly one (the signing domain)", ov.Incidents)
	}
	got := ov.Incidents[0]
	if got.Dimension != "signing_domain" || got.Value != "bad.test" {
		t.Errorf("incident names %s=%q, want signing_domain=bad.test", got.Dimension, got.Value)
	}
	if got.CohortSize != 4 || got.DegradedInside != 3 || got.CohortOutside != 20 || got.DegradedOutside != 5 {
		t.Errorf("arithmetic = %+v, want cohort 4 / inside 3 / outside 20 / degraded outside 5", got)
	}
	if got.Lift != 3 {
		t.Errorf("lift = %v, want 3 (75%% inside against 25%% outside)", got.Lift)
	}
	if len(got.MemberMailboxIDs) != 3 {
		t.Errorf("member_mailbox_ids = %v, want the three degraded members", got.MemberMailboxIDs)
	}
}

// The wire contract, asserted as JSON because the frontend is built against these
// exact keys and a Go-side field rename would otherwise pass every other test here.
func TestOverviewIncidentSerializesTheAgreedContract(t *testing.T) {
	store := newFakeStore()
	store.incidentParticipants = incidentPool()
	svc := withNow(NewService(store), time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC))

	ov, err := svc.GetOverview(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if len(ov.Incidents) != 1 {
		t.Fatalf("incidents = %+v, want exactly one", ov.Incidents)
	}
	body, err := json.Marshal(ov.Incidents[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"dimension":"signing_domain","value":"bad.test",` +
		`"member_mailbox_ids":["degraded-1","degraded-2","degraded-3"],` +
		`"cohort_size":4,"degraded_inside":3,"cohort_outside":20,"degraded_outside":5,"lift":3}`
	if string(body) != want {
		t.Errorf("contract drift:\n got %s\nwant %s", body, want)
	}
}

// incidentPool is the fixture both incident tests read: one genuine signing-domain
// correlation (3 of 4 degraded) inside a pool of 20 others where 5 are degraded.
//
// Every member carries a DISTINCT organizational domain, so the sender_domain
// dimension cannot form a cohort of its own and the assertions above are about the
// signing domain alone.
func incidentPool() []pwarmup.IncidentInput {
	pool := []pwarmup.IncidentInput{}
	for i := 1; i <= 3; i++ {
		pool = append(pool, pwarmup.IncidentInput{
			MailboxID: fmt.Sprintf("degraded-%d", i), Email: fmt.Sprintf("m%d@inside%d.test", i, i),
			Degraded: true, SigningDomain: "bad.test",
		})
	}
	pool = append(pool, pwarmup.IncidentInput{
		MailboxID: "healthy-inside", Email: "ok@inside4.test", SigningDomain: "bad.test",
	})
	for i := 1; i <= 20; i++ {
		pool = append(pool, pwarmup.IncidentInput{
			MailboxID: fmt.Sprintf("outside-%d", i), Email: fmt.Sprintf("o%d@outside%d.test", i, i),
			Degraded: i <= 5, SigningDomain: fmt.Sprintf("own%d.test", i),
		})
	}
	return pool
}

// The two things the DTO mapping decides on its own, tested where they live rather
// than through a fixture that has to reach them: the members list is never null (a
// JSON null would break a client typed against string[]), and the lift is trimmed to
// two decimals so the wire never carries sixteen digits of confidence in an estimate
// over single-digit counts.
func TestIncidentDTOKeepsMembersNonNullAndTrimsTheLift(t *testing.T) {
	empty := incidentDTO(pwarmup.Incident{})
	if empty.MemberMailboxIDs == nil {
		t.Error("member_mailbox_ids is nil, and serializes as null: the contract says it is a list")
	}
	if len(empty.MemberMailboxIDs) != 0 {
		t.Errorf("member_mailbox_ids = %v, want empty", empty.MemberMailboxIDs)
	}
	if got := incidentDTO(pwarmup.Incident{Lift: 3.2666666666}).Lift; got != 3.27 {
		t.Errorf("lift = %v, want 3.27", got)
	}
}

// No incident is an ANSWER — "nothing shared was found" — so the key is always
// present and empty. The absent form arrives as undefined and takes a client down a
// path indistinguishable from "we did not look".
func TestOverviewIncidentsAreAnEmptyArrayWhenNoneWasFound(t *testing.T) {
	store := newFakeStore()
	store.enabledCount = 2
	svc := withNow(NewService(store), time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC))

	ov, err := svc.GetOverview(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	body, err := json.Marshal(ov)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(body), `"incidents":[]`) {
		t.Errorf("overview JSON = %s, want an empty incidents array", body)
	}
}

// Detection must never fail the read (design §9). The overview is the operator's
// window into a degrading pool: an inference that cannot be computed degrades to "no
// incidents", while the pool summary and every mailbox row still arrive.
func TestOverviewSurvivesAFailedIncidentDetection(t *testing.T) {
	ws, mb := uuid.New(), uuid.New()
	store := newFakeStore()
	store.enabledCount = 2
	store.incidentParticipantsErr = errors.New("detection exploded")
	store.overviewRows = []OverviewRow{{
		MailboxID: mb, Enabled: true, Email: "a@example.com", HealthState: "healthy",
		StartVolume: 4, MaxVolume: 40, RampIncrement: 2,
		StartedAt: pgtype.Timestamptz{Time: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), Valid: true},
	}}
	svc := withNow(NewService(store), time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC))

	ov, err := svc.GetOverview(context.Background(), ws)
	if err != nil {
		t.Fatalf("overview must not fail because an inference failed: %v", err)
	}
	if ov.Incidents == nil || len(ov.Incidents) != 0 {
		t.Errorf("incidents = %#v, want an empty slice", ov.Incidents)
	}
	if ov.PoolSize != 2 || len(ov.Mailboxes) != 1 {
		t.Errorf("overview lost its real content: %+v", ov)
	}
}

// assertRate compares a nullable wire rate against an exact expectation, within a
// tolerance that only absorbs float representation.
func assertRate(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Errorf("%s = null, want %v", name, want)
		return
	}
	if diff := *got - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("%s = %v, want %v", name, *got, want)
	}
}

// The overview publishes the pool floor detection needs, so a client can tell an
// empty incidents list that means "we looked and found no shared cause" from one
// that means "this pool is too small for concentration to exist". Both arrive as
// `[]` and they are different answers.
//
// It is served rather than left for the client to derive because it comes from a
// backend policy constant: a copy on the client would drift the moment that
// constant is recalibrated, leaving the UI claiming it searched a pool the server
// never examined.
func TestOverviewPublishesTheIncidentPoolFloor(t *testing.T) {
	store := newFakeStore()
	store.enabledCount = 1
	store.overviewRows = []OverviewRow{{
		MailboxID: uuid.New(), Enabled: true, StartVolume: 4, MaxVolume: 40, RampIncrement: 2,
		ReplyRate: 0.3, HealthState: "healthy", Email: "a@example.com",
		StartedAt: pgtype.Timestamptz{Time: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), Valid: true},
	}}
	svc := withNow(NewService(store), time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC))

	ov, err := svc.GetOverview(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	if ov.IncidentsMinPool != pwarmup.MinIncidentPool {
		t.Errorf("IncidentsMinPool = %d, want %d (warmup.MinIncidentPool)",
			ov.IncidentsMinPool, pwarmup.MinIncidentPool)
	}
	// A floor of zero would let a client conclude every pool is large enough,
	// which is the failure this field exists to prevent — so the zero value is
	// specifically wrong, not merely unset.
	if ov.IncidentsMinPool == 0 {
		t.Error("IncidentsMinPool is 0, which reads as 'every pool is big enough to search'")
	}
}

// The observer-trust view, which is the operator-visible half of the ONLY inference
// in this subsystem that gates. The exclusion removes evidence from every sender that
// mailed the discounted mailbox, so the whole sum has to ship with the finding: an
// operator who disagrees needs to see 44 of 50 against a cohort of 12%, not a badge.
//
// The fixture is the case the rule exists to catch and nothing weaker: a mailbox
// junking 88% of what it receives while its Microsoft peers junk 12%. The peers are
// deliberately NOT clean, so the lift is a real comparison rather than a division by
// the continuity correction.
func TestOverviewReportsADiscountedObserverWithItsArithmetic(t *testing.T) {
	store := newFakeStore()
	store.enabledCount = 4
	store.observerStats = hostileObserverPool()
	svc := withNow(NewService(store), time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC))

	ov, err := svc.GetOverview(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if len(ov.DiscountedObservers) != 1 {
		t.Fatalf("discounted_observers = %+v, want exactly one (the hostile mailbox)", ov.DiscountedObservers)
	}
	// Asserted as JSON because a client is built against these exact keys, and a
	// Go-side field rename would pass every other assertion here.
	body, err := json.Marshal(ov.DiscountedObservers[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"observer_mailbox_id":"hostile","cohort":"microsoft","spam":44,"total":50,` +
		`"spam_rate":0.88,"cohort_spam_rate":0.12,"lift":7.33}`
	if string(body) != want {
		t.Errorf("contract drift:\n got %s\nwant %s", body, want)
	}
}

// hostileObserverPool is one Microsoft mailbox reporting 88% of its mail as spam
// beside a peer reporting 12% — a lift of 7.33, comfortably past every gate — and a
// peer that must NOT be discounted for merely sharing the cohort.
func hostileObserverPool() []pwarmup.ObserverStats {
	return []pwarmup.ObserverStats{
		{ObserverMailboxID: "hostile", Cohort: "microsoft", Spam: 44, Total: 50},
		// Split across two mailboxes, combining to the same 6/50: a baseline has to be
		// more than one peer's opinion (MinObserverPeers), and the split leaves the
		// cohort rate and the lift this test pins exactly where they were.
		{ObserverMailboxID: "peer-a", Cohort: "microsoft", Spam: 3, Total: 25},
		{ObserverMailboxID: "peer-b", Cohort: "microsoft", Spam: 3, Total: 25},
	}
}

// A STRICT observer is not a hostile one. This mailbox junks 40% of what it receives
// — well past MinObserverSpamRate, and far more than the pool average — but its
// Microsoft peers junk 20%, so the lift is 2 and the reports stand. Excluding it would
// make every sender that mails it look cleaner than it is, which is the worse of the
// two failure modes, so `[]` here is the whole point of the rule rather than an empty
// state.
//
// Asserted through the JSON because the key must be PRESENT and empty: absent arrives
// as undefined and takes a client down a path indistinguishable from "the check did
// not run".
func TestOverviewDoesNotDiscountAStrictButNormalObserver(t *testing.T) {
	store := newFakeStore()
	store.enabledCount = 3
	store.observerStats = []pwarmup.ObserverStats{
		{ObserverMailboxID: "strict", Cohort: "microsoft", Spam: 20, Total: 50},
		{ObserverMailboxID: "peer-a", Cohort: "microsoft", Spam: 20, Total: 100},
		{ObserverMailboxID: "peer-b", Cohort: "microsoft", Spam: 20, Total: 100},
	}
	svc := withNow(NewService(store), time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC))

	ov, err := svc.GetOverview(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if len(ov.DiscountedObservers) != 0 {
		t.Fatalf("discounted_observers = %+v, want none: 40%% against a cohort of 20%% is a lift of 2, "+
			"under ObserverSpamLift (%v)", ov.DiscountedObservers, pwarmup.ObserverSpamLift)
	}
	body, err := json.Marshal(ov)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(body), `"discounted_observers":[]`) {
		t.Errorf("overview JSON = %s, want a present, empty discounted_observers array", body)
	}
}

// The detector already sorts worst-first and breaks ties on the mailbox id; the read
// layer must not form a second opinion. The fixture makes the two orders disagree:
// the WORSE observer sorts LAST by id, so any re-sort here flips them.
func TestOverviewKeepsTheDetectorsWorstFirstObserverOrder(t *testing.T) {
	store := newFakeStore()
	store.enabledCount = 4
	store.observerStats = []pwarmup.ObserverStats{
		{ObserverMailboxID: "b-worse", Cohort: "microsoft", Spam: 50, Total: 50},
		{ObserverMailboxID: "a-bad", Cohort: "microsoft", Spam: 30, Total: 50},
		{ObserverMailboxID: "c-peer", Cohort: "microsoft", Spam: 4, Total: 400},
	}
	svc := withNow(NewService(store), time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC))

	ov, err := svc.GetOverview(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if len(ov.DiscountedObservers) != 2 {
		t.Fatalf("discounted_observers = %+v, want the two outliers", ov.DiscountedObservers)
	}
	if got := []string{ov.DiscountedObservers[0].ObserverMailboxID, ov.DiscountedObservers[1].ObserverMailboxID}; got[0] != "b-worse" || got[1] != "a-bad" {
		t.Errorf("order = %v, want [b-worse a-bad] — the detector's worst-lift-first order, not the id order", got)
	}
	if ov.DiscountedObservers[0].Lift <= ov.DiscountedObservers[1].Lift {
		t.Errorf("lifts = %v then %v, want strictly descending",
			ov.DiscountedObservers[0].Lift, ov.DiscountedObservers[1].Lift)
	}
}

// The trust read must never fail the overview. It is the operator's window into a
// degrading pool, and losing the pool summary and every mailbox row because ONE
// inference could not be computed is worse than reporting no discounted observer —
// especially since the write side falls back to exactly the same empty list, so the
// two halves still agree about what happened.
func TestOverviewSurvivesAFailedObserverTrustRead(t *testing.T) {
	ws, mb := uuid.New(), uuid.New()
	store := newFakeStore()
	store.enabledCount = 2
	store.observerStatsErr = errors.New("observer stats exploded")
	store.overviewRows = []OverviewRow{{
		MailboxID: mb, Enabled: true, Email: "a@example.com", HealthState: "healthy",
		StartVolume: 4, MaxVolume: 40, RampIncrement: 2,
		StartedAt: pgtype.Timestamptz{Time: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), Valid: true},
	}}
	svc := withNow(NewService(store), time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC))

	ov, err := svc.GetOverview(context.Background(), ws)
	if err != nil {
		t.Fatalf("overview must not fail because the trust read failed: %v", err)
	}
	if ov.DiscountedObservers == nil || len(ov.DiscountedObservers) != 0 {
		t.Errorf("discounted_observers = %#v, want an empty slice", ov.DiscountedObservers)
	}
	if ov.PoolSize != 2 || len(ov.Mailboxes) != 1 {
		t.Errorf("overview lost its real content: %+v", ov)
	}
}

// The one thing the DTO mapping decides on its own: all THREE floats are trimmed to
// two decimals, not just the lift. A row that printed a lift to 2dp beside rates to
// 16 would invite a reader to check 0.8799999999/0.12 and conclude the engine's
// arithmetic is wrong.
func TestDiscountedObserverDTOTrimsEveryRate(t *testing.T) {
	got := discountedObserverDTO(pwarmup.DiscountedObserver{
		SpamRate: 0.876543, CohortSpamRate: 0.123456, Lift: 7.098765,
	})
	if got.SpamRate != 0.88 {
		t.Errorf("spam_rate = %v, want 0.88", got.SpamRate)
	}
	if got.CohortSpamRate != 0.12 {
		t.Errorf("cohort_spam_rate = %v, want 0.12", got.CohortSpamRate)
	}
	if got.Lift != 7.1 {
		t.Errorf("lift = %v, want 7.1", got.Lift)
	}
}
