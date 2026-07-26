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

	upsertCalls int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		participants:   map[uuid.UUID]Participant{},
		ownedMailboxes: map[uuid.UUID]uuid.UUID{},
		sentToday:      map[uuid.UUID]int32{},
		dailyStats:     map[uuid.UUID][]DayStat{},
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
	p, ok := s.participants[mailboxID]
	if !ok || p.WorkspaceID != workspaceID {
		return Participant{}, pgx.ErrNoRows
	}
	return p, nil
}

func (s *fakeStore) ListParticipants(_ context.Context, workspaceID uuid.UUID) ([]Participant, error) {
	var out []Participant
	for _, p := range s.participants {
		if p.WorkspaceID == workspaceID {
			out = append(out, p)
		}
	}
	return out, nil
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

func (s *fakeStore) PlacementRates7d(_ context.Context, _ uuid.UUID) ([]PlacementRate, error) {
	return nil, nil
}

func (s *fakeStore) SentToday(_ context.Context, _, mailboxID uuid.UUID) (int32, error) {
	return s.sentToday[mailboxID], nil
}

func (s *fakeStore) ListOverviewRows(_ context.Context, _ uuid.UUID) ([]OverviewRow, error) {
	return s.overviewRows, nil
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
	if m.InboxRate7d != 0.8 || m.SpamRate7d != 0.2 { // 8/10, 2/10
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

// TestOverviewZeroPlacementRate proves an empty 7-day window yields rate 0 (no
// divide-by-zero) and a paused row reports today_target 0.
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
	if m.InboxRate7d != 0 || m.SpamRate7d != 0 {
		t.Fatalf("empty window must be 0 rate: %+v", m)
	}
	if m.TodayTarget != 0 {
		t.Fatalf("paused today_target: got %d want 0", m.TodayTarget)
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
