package warmup

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
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

	// getParticipantErr, when non-nil, is returned by GetParticipant to model a
	// transient read failure (a NON-ErrNoRows error) on the merge-base read.
	getParticipantErr error

	// transitions is the decision history per mailbox, newest first.
	// transitionWorkspace, when set, is the only workspace those rows belong to,
	// modeling the query's workspace pin. transitionLimits records the limit each
	// call received, so a test can prove the cap reached the store.
	transitions         map[uuid.UUID][]Transition
	transitionWorkspace uuid.UUID
	transitionLimits    []int32

	upsertCalls int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		participants:   map[uuid.UUID]Participant{},
		ownedMailboxes: map[uuid.UUID]uuid.UUID{},
		sentToday:      map[uuid.UUID]int32{},
		dailyStats:     map[uuid.UUID][]DayStat{},
		transitions:    map[uuid.UUID][]Transition{},
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

func (s *fakeStore) ListOverviewRows(_ context.Context, _ uuid.UUID) ([]OverviewRow, error) {
	return s.overviewRows, nil
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
