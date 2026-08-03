package deliverability

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/deliverability"
)

// fakeStore is an in-memory Store. It records what the service asked for — the
// window each count was gathered over, the pause events it wrote — because the
// interesting behaviour of this service IS which window it judged on.
type fakeStore struct {
	// counts answers CampaignCounts/WorkspaceCounts. Keyed by nothing: tests set
	// either one fixed answer or a per-window answer via countsFor.
	counts    Counts
	countsFor map[time.Time]Counts
	// askedSince records every `since` CampaignCounts was called with, in order.
	askedSince []time.Time

	signals       Signals
	signalsSince  time.Time
	senders       []uuid.UUID
	series        []Point
	seriesSince   time.Time
	atRiskMbx     []Risk
	atRiskDomains []Risk

	config     CampaignConfig
	configErr  error
	lastPause  time.Time
	events     []PauseEvent
	pauseCalls []PauseEvent
	// pauseWins is what PauseForBreach reports. A store whose campaign is already
	// paused reports false — the exactly-once behaviour the real UPDATE's
	// status='running' guard provides.
	pauseWins bool
	pauseErr  error

	saved       *Guardrails
	saveErr     error
	ingestNew   bool
	ingestErr   error
	ingested    []EventInput
	suppressed  []string
	suppressErr error
	sendCampaign
}

// sendCampaign is the send→campaign resolution half of the fake, split out so a
// test can set just it.
type sendCampaign struct {
	campaignForSend uuid.UUID
	sendCampaignErr error
}

func (f *fakeStore) CampaignCounts(_ context.Context, _, _ uuid.UUID, since time.Time) (Counts, error) {
	f.askedSince = append(f.askedSince, since)
	if c, ok := f.countsFor[since]; ok {
		return c, nil
	}
	return f.counts, nil
}

func (f *fakeStore) WorkspaceCounts(_ context.Context, _ uuid.UUID, _ time.Time) (Counts, error) {
	return f.counts, nil
}

func (f *fakeStore) Signals(_ context.Context, _ uuid.UUID, _ []uuid.UUID, since time.Time) (Signals, error) {
	f.signalsSince = since
	return f.signals, nil
}

func (f *fakeStore) CampaignSenderMailboxes(context.Context, uuid.UUID, uuid.UUID) ([]uuid.UUID, error) {
	return f.senders, nil
}

func (f *fakeStore) Series(_ context.Context, _ uuid.UUID, since time.Time) ([]Point, error) {
	f.seriesSince = since
	return f.series, nil
}

func (f *fakeStore) AtRiskMailboxes(context.Context, uuid.UUID) ([]Risk, error) {
	return f.atRiskMbx, nil
}

func (f *fakeStore) AtRiskDomains(context.Context, uuid.UUID) ([]Risk, error) {
	return f.atRiskDomains, nil
}

func (f *fakeStore) CampaignConfig(context.Context, uuid.UUID, uuid.UUID) (CampaignConfig, error) {
	return f.config, f.configErr
}

func (f *fakeStore) SetGuardrails(_ context.Context, _, _ uuid.UUID, g Guardrails) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = &g
	return nil
}

func (f *fakeStore) LastPausedAt(context.Context, uuid.UUID, uuid.UUID) (time.Time, error) {
	return f.lastPause, nil
}

func (f *fakeStore) PauseEvents(context.Context, uuid.UUID, uuid.UUID) ([]PauseEvent, error) {
	return f.events, nil
}

func (f *fakeStore) PauseForBreach(_ context.Context, _, _ uuid.UUID, ev PauseEvent) (bool, error) {
	f.pauseCalls = append(f.pauseCalls, ev)
	return f.pauseWins, f.pauseErr
}

func (f *fakeStore) Ingest(_ context.Context, _ uuid.UUID, in EventInput) (bool, error) {
	if f.ingestErr != nil {
		return false, f.ingestErr
	}
	f.ingested = append(f.ingested, in)
	return f.ingestNew, nil
}

func (f *fakeStore) SendCampaign(context.Context, uuid.UUID, uuid.UUID) (uuid.UUID, error) {
	return f.campaignForSend, f.sendCampaignErr
}

func (f *fakeStore) Suppress(_ context.Context, _ uuid.UUID, email, reason string) error {
	if f.suppressErr != nil {
		return f.suppressErr
	}
	f.suppressed = append(f.suppressed, email+":"+reason)
	return nil
}

// runningCampaign is a campaign with the on-by-default guardrails, supervised long
// enough that guardrails_enabled_at no longer binds the window. EnabledAt is set
// explicitly and never left zero: the column is NOT NULL DEFAULT now(), so a zero
// value is a shape production cannot produce, and a fake that used it would silently
// disable the floor in every test.
func runningCampaign() CampaignConfig {
	return CampaignConfig{
		Guardrails: Guardrails{
			AutoPauseEnabled:  true,
			BouncePausePct:    deliverability.DefaultBouncePausePct,
			ComplaintPausePct: deliverability.DefaultComplaintPausePct,
		},
		Status:    campaignStatusRunning,
		EnabledAt: fixedNow.AddDate(0, 0, -60),
	}
}

// fixedNow pins the clock so window arithmetic is assertable.
var fixedNow = time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

func newService(store Store) *Service {
	s := NewService(store)
	s.now = func() time.Time { return fixedNow }
	return s
}

var (
	testWS       = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	testCampaign = uuid.MustParse("22222222-2222-2222-2222-222222222222")
)

func evaluate(t *testing.T, store *fakeStore) BreakerOutcome {
	t.Helper()
	out, err := newService(store).EvaluateBreaker(context.Background(), testWS, testCampaign)
	if err != nil {
		t.Fatalf("EvaluateBreaker: %v", err)
	}
	return out
}

// The invariant-1 guard at the service level: a campaign under the minimum sample
// with a 100% bounce rate stays running and writes no pause event.
func TestBreakerDoesNotPauseUnderTheMinimumSample(t *testing.T) {
	for delivered := 1; delivered < deliverability.MinDelivered; delivered++ {
		store := &fakeStore{
			config:    runningCampaign(),
			counts:    Counts{Delivered: delivered, Bounced: delivered},
			pauseWins: true,
		}
		out := evaluate(t, store)
		if out.Paused {
			t.Fatalf("delivered=%d: campaign paused below the minimum sample", delivered)
		}
		if len(store.pauseCalls) != 0 {
			t.Fatalf("delivered=%d: %d pause writes attempted below the minimum sample",
				delivered, len(store.pauseCalls))
		}
	}
}

func TestBreakerPausesAndRecordsTheReason(t *testing.T) {
	store := &fakeStore{
		config:    runningCampaign(),
		counts:    Counts{Delivered: 200, Bounced: 20}, // 10% > 8%
		pauseWins: true,
	}
	out := evaluate(t, store)
	if !out.Paused {
		t.Fatal("campaign not paused at a 10% bounce rate over 200 delivered")
	}
	if len(store.pauseCalls) != 1 {
		t.Fatalf("%d pause writes, want 1", len(store.pauseCalls))
	}
	ev := store.pauseCalls[0]
	if ev.Reason != deliverability.ReasonBounceSpike || ev.Metric != deliverability.MetricBounceRate {
		t.Errorf("event reason/metric = %q/%q", ev.Reason, ev.Metric)
	}
	if ev.Value != 10 || ev.Threshold != deliverability.DefaultBouncePausePct || ev.Delivered != 200 {
		t.Errorf("event = %+v, want value 10 threshold 8 delivered 200", ev)
	}
}

// Repeated evaluation is idempotent: the store's status='running' guard reports
// false the second time, so nothing claims a second pause.
func TestRepeatedEvaluationPausesOnce(t *testing.T) {
	store := &fakeStore{
		config:    runningCampaign(),
		counts:    Counts{Delivered: 200, Bounced: 20},
		pauseWins: true,
	}
	svc := newService(store)
	first, err := svc.EvaluateBreaker(context.Background(), testWS, testCampaign)
	if err != nil {
		t.Fatal(err)
	}
	// The campaign is now paused, so the guarded UPDATE matches nothing.
	store.pauseWins = false
	store.config.Status = "paused"
	second, err := svc.EvaluateBreaker(context.Background(), testWS, testCampaign)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Paused || second.Paused {
		t.Fatalf("paused first=%v second=%v, want true then false", first.Paused, second.Paused)
	}
	// And the second evaluation does not even attempt the write: the campaign is
	// no longer running.
	if len(store.pauseCalls) != 1 {
		t.Fatalf("%d pause writes across two evaluations, want 1", len(store.pauseCalls))
	}
}

func TestBreakerRespectsTheDisabledToggle(t *testing.T) {
	store := &fakeStore{
		config:    runningCampaign(),
		counts:    Counts{Delivered: 200, Bounced: 100}, // 50% bounce
		pauseWins: true,
	}
	store.config.AutoPauseEnabled = false
	out := evaluate(t, store)
	if out.Paused || len(store.pauseCalls) != 0 {
		t.Fatalf("paused=%v writes=%d with auto-pause disabled", out.Paused, len(store.pauseCalls))
	}
	// The verdict is still computed and reported — the operator turned off the
	// ACTION, not the measurement.
	if out.Verdict.State != deliverability.VerdictPause {
		t.Errorf("verdict = %q, want pause reported even when not acted on", out.Verdict.State)
	}
}

// A campaign's own thresholds are honoured, not the defaults.
func TestBreakerUsesTheCampaignsOwnThresholds(t *testing.T) {
	store := &fakeStore{
		config:    runningCampaign(),
		counts:    Counts{Delivered: 200, Bounced: 6}, // 3%
		pauseWins: true,
	}
	store.config.BouncePausePct = 2
	if out := evaluate(t, store); !out.Paused {
		t.Fatal("3% bounce did not pause a campaign whose threshold is 2%")
	}
}

func TestRollingWindowIsPreferredWhenItHasEnoughSample(t *testing.T) {
	rolling := fixedNow.AddDate(0, 0, -deliverability.WindowDays)
	store := &fakeStore{
		config: runningCampaign(),
		countsFor: map[time.Time]Counts{
			// Recent evidence is clean and big enough to judge on.
			rolling: {Delivered: 100, Bounced: 0},
			// All-time evidence is terrible. It must not be consulted.
			{}: {Delivered: 10000, Bounced: 5000},
		},
		pauseWins: true,
	}
	out := evaluate(t, store)
	if out.Paused {
		t.Fatal("paused on history despite a clean, sufficient rolling window")
	}
	if len(store.askedSince) != 1 || !store.askedSince[0].Equal(rolling) {
		t.Fatalf("windows asked for = %v, want only the rolling one (%v)", store.askedSince, rolling)
	}
}

// The fallback: a campaign too slow to fill the 7-day window is judged on
// everything SINCE SUPERVISION BEGAN. Not on its lifetime — the floor is what the
// fallback reaches back to, never the zero time.
func TestFallbackReachesBackToTheSupervisionFloor(t *testing.T) {
	rolling := fixedNow.AddDate(0, 0, -deliverability.WindowDays)
	cfg := runningCampaign()
	store := &fakeStore{
		config: cfg,
		countsFor: map[time.Time]Counts{
			// A slow campaign: not enough recent mail to judge on...
			rolling: {Delivered: 5, Bounced: 5},
			// ...but plenty since supervision began, and it is badly broken.
			cfg.EnabledAt: {Delivered: 500, Bounced: 100},
			// A lifetime sample must never be reached; if it were, the assertion on
			// Verdict.Delivered below would see 9000.
			{}: {Delivered: 9000, Bounced: 8000},
		},
		pauseWins: true,
	}
	out := evaluate(t, store)
	if !out.Paused {
		t.Fatal("did not fall back to the wider sample when the rolling window was too small")
	}
	if out.Verdict.Delivered != 500 {
		t.Errorf("judged on %d delivered, want the wider sample's 500", out.Verdict.Delivered)
	}
	if len(store.askedSince) != 2 {
		t.Fatalf("asked for %d windows, want 2 (rolling then the fallback)", len(store.askedSince))
	}
	for _, since := range store.askedSince {
		if since.Before(cfg.EnabledAt) {
			t.Errorf("read evidence from %v, before supervision began at %v", since, cfg.EnabledAt)
		}
	}
}

// The fallback must not widen the reported period when there is nothing more to
// find: an equally small cumulative sample means the rolling window already held
// everything.
func TestFallbackKeepsTheRollingWindowWhenItIsNoSmaller(t *testing.T) {
	rolling := fixedNow.AddDate(0, 0, -deliverability.WindowDays)
	store := &fakeStore{
		config:    runningCampaign(),
		counts:    Counts{Delivered: 5, Bounced: 5}, // same answer for every window
		pauseWins: true,
	}
	svc := newService(store)
	if _, err := svc.EvaluateBreaker(context.Background(), testWS, testCampaign); err != nil {
		t.Fatal(err)
	}
	if !store.signalsSince.Equal(rolling) {
		t.Errorf("signals gathered over %v, want the rolling window %v", store.signalsSince, rolling)
	}
}

// The fallback's trap: a campaign with a bad history that an operator restarted
// must not be re-paused on the evidence they just overrode. Both windows are
// bounded below by the last pause, so a restart with no NEW bad evidence leaves
// the campaign running.
func TestRestartedCampaignIsNotRePausedOnAlreadyJudgedEvidence(t *testing.T) {
	lastPause := fixedNow.Add(-2 * time.Hour)
	store := &fakeStore{
		config:    runningCampaign(),
		lastPause: lastPause,
		countsFor: map[time.Time]Counts{
			// Since the restart: a handful of clean sends.
			lastPause: {Delivered: 5, Bounced: 0},
			// All time (never asked for): the disaster that paused it.
			{}: {Delivered: 5000, Bounced: 4000},
		},
		pauseWins: true,
	}
	out := evaluate(t, store)
	if out.Paused {
		t.Fatal("re-paused on evidence the operator had already acted on")
	}
	for _, since := range store.askedSince {
		if since.Before(lastPause) {
			t.Errorf("asked for evidence from %v, before the last pause at %v", since, lastPause)
		}
	}
}

// ...and new bad evidence after the restart still stops it.
func TestRestartedCampaignPausesOnFreshEvidence(t *testing.T) {
	lastPause := fixedNow.Add(-2 * time.Hour)
	store := &fakeStore{
		config:    runningCampaign(),
		lastPause: lastPause,
		countsFor: map[time.Time]Counts{
			lastPause: {Delivered: 100, Bounced: 30},
		},
		pauseWins: true,
	}
	if out := evaluate(t, store); !out.Paused {
		t.Fatal("a restarted campaign bouncing 30% over 100 fresh sends was not stopped")
	}
}

// The migration-day case. auto_pause_enabled defaults TRUE, so applying 000037
// arms every campaign that already exists; guardrails_enabled_at defaults to now(),
// so on the first tick afterwards the widest sample the breaker can reach is
// "since the migration" — NOT the campaign's lifetime.
//
// Without this floor, a slow campaign (under the minimum for the 7-day window, so
// it falls back) would be judged on bounces predating the feature, which nobody
// opted into and no dashboard ever showed, and would stop itself on deploy day.
func TestBreakerIgnoresEvidenceFromBeforeSupervisionBegan(t *testing.T) {
	enabledAt := fixedNow.Add(-time.Hour) // the migration ran an hour ago
	rolling := fixedNow.AddDate(0, 0, -deliverability.WindowDays)
	store := &fakeStore{
		config: runningCampaign(),
		countsFor: map[time.Time]Counts{
			// Since supervision began: a handful of clean sends, too few to judge.
			enabledAt: {Delivered: 5, Bounced: 0},
			// The last 7 days, and all time, are both disasters — and both predate
			// supervision, so neither may be reached.
			rolling: {Delivered: 4000, Bounced: 3000},
			{}:      {Delivered: 9000, Bounced: 8000},
		},
		pauseWins: true,
	}
	store.config.EnabledAt = enabledAt

	out := evaluate(t, store)
	if out.Paused {
		t.Fatalf("paused on pre-supervision evidence (verdict %+v)", out.Verdict)
	}
	if len(store.pauseCalls) != 0 {
		t.Errorf("%d pause writes on deploy day", len(store.pauseCalls))
	}
	// Nothing older than the floor may even be READ, or the score would report a
	// rate the breaker refuses to act on.
	for _, since := range store.askedSince {
		if since.Before(enabledAt) {
			t.Errorf("read evidence from %v, before supervision began at %v", since, enabledAt)
		}
	}
}

// ...and bounces AFTER supervision began do stop it, so the floor is a floor and
// not a mute.
func TestBreakerActsOnEvidenceFromAfterSupervisionBegan(t *testing.T) {
	enabledAt := fixedNow.Add(-time.Hour)
	store := &fakeStore{
		config: runningCampaign(),
		countsFor: map[time.Time]Counts{
			enabledAt: {Delivered: deliverability.MinDelivered, Bounced: 20},
		},
		pauseWins: true,
	}
	store.config.EnabledAt = enabledAt

	if out := evaluate(t, store); !out.Paused {
		t.Fatal("fresh post-supervision bounces did not stop the campaign")
	}
}

// The two floors compose: whichever is LATER wins, so a campaign restarted after an
// auto-pause is judged from the restart even though supervision began earlier.
func TestTheLaterOfTheTwoFloorsWins(t *testing.T) {
	enabledAt := fixedNow.AddDate(0, 0, -30)
	lastPause := fixedNow.Add(-time.Hour)
	store := &fakeStore{
		config:    runningCampaign(),
		lastPause: lastPause,
		countsFor: map[time.Time]Counts{
			lastPause: {Delivered: 5, Bounced: 0},
			enabledAt: {Delivered: 5000, Bounced: 4000},
		},
		pauseWins: true,
	}
	store.config.EnabledAt = enabledAt

	if out := evaluate(t, store); out.Paused {
		t.Fatalf("paused on evidence older than the restart (verdict %+v)", out.Verdict)
	}
	for _, since := range store.askedSince {
		if since.Before(lastPause) {
			t.Errorf("read evidence from %v, before the later floor at %v", since, lastPause)
		}
	}
}

// The supervision floor applies whatever the status, unlike the last-pause floor:
// a campaign that has never been supervised has no supervised evidence to REPORT
// either, so the dashboard must not show a rate the breaker would refuse to act on.
func TestSupervisionFloorAppliesToAPausedCampaignToo(t *testing.T) {
	enabledAt := fixedNow.Add(-time.Hour)
	store := &fakeStore{
		config: runningCampaign(),
		countsFor: map[time.Time]Counts{
			enabledAt: {Delivered: 5},
			fixedNow.AddDate(0, 0, -deliverability.WindowDays): {Delivered: 4000, Bounced: 3000},
		},
	}
	store.config.Status = "paused"
	store.config.EnabledAt = enabledAt

	rep, err := newService(store).CampaignReport(context.Background(), testWS, testCampaign)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Score.Delivered != 5 {
		t.Errorf("score sample = %d, want the 5 delivered since supervision began", rep.Score.Delivered)
	}
}

// The last-pause bound applies only while the campaign is RUNNING. A report on a
// still-paused campaign must show the evidence that stopped it, not an empty
// since-the-restart window.
func TestPausedCampaignIsReportedOnTheEvidenceThatStoppedIt(t *testing.T) {
	lastPause := fixedNow.Add(-time.Minute)
	store := &fakeStore{
		config:    runningCampaign(),
		lastPause: lastPause,
		countsFor: map[time.Time]Counts{
			// Everything that happened, unbounded — what the operator needs to see.
			fixedNow.AddDate(0, 0, -deliverability.WindowDays): {Delivered: 200, Bounced: 40},
			// Bounded to since-the-pause: nothing, which would read as a clean 100.
			lastPause: {Delivered: 0},
		},
		events: []PauseEvent{{Reason: deliverability.ReasonBounceSpike}},
	}
	store.config.Status = "paused"

	rep, err := newService(store).CampaignReport(context.Background(), testWS, testCampaign)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Score.Delivered != 200 {
		t.Errorf("score sample = %d, want the 200 delivered that were judged", rep.Score.Delivered)
	}
	for _, since := range store.askedSince {
		if since.Equal(lastPause) {
			t.Error("a paused campaign was judged on a window bounded by its own pause")
		}
	}
}

// Invariant 4 at the service seam: with no complaint feed the Inputs carry a NIL
// complaint count, so the score reports the component unmeasured rather than 0%.
func TestNoComplaintFeedIsUnmeasuredNotZero(t *testing.T) {
	store := &fakeStore{
		config: runningCampaign(),
		counts: Counts{Delivered: 200, Bounced: 0, Complained: 0, ComplaintFeed: false},
	}
	rep, err := newService(store).CampaignReport(context.Background(), testWS, testCampaign)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range rep.Score.Components {
		if c.Key != deliverability.KeyComplaint {
			continue
		}
		if c.Measured || c.Rate != nil {
			t.Fatalf("complaint component = %+v, want unmeasured with a nil rate", c)
		}
		return
	}
	t.Fatal("report has no complaint component")
}

// With a live feed, zero complaints IS a measurement.
func TestLiveComplaintFeedWithNoComplaintsIsMeasured(t *testing.T) {
	store := &fakeStore{
		config: runningCampaign(),
		counts: Counts{Delivered: 200, Complained: 0, ComplaintFeed: true},
	}
	rep, err := newService(store).CampaignReport(context.Background(), testWS, testCampaign)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range rep.Score.Components {
		if c.Key == deliverability.KeyComplaint {
			if !c.Measured || c.Rate == nil || *c.Rate != 0 {
				t.Fatalf("complaint component = %+v, want measured at 0%%", c)
			}
			return
		}
	}
	t.Fatal("report has no complaint component")
}

// The series must not claim a per-day count the score declined to claim.
func TestSeriesMasksUnmeasuredSignals(t *testing.T) {
	store := &fakeStore{
		counts: Counts{Delivered: 200, ComplaintFeed: false},
		series: []Point{{Day: fixedNow, Delivered: 10, Bounced: 1, Complained: 0, SpamPlaced: 0}},
	}
	rep, err := newService(store).Report(context.Background(), testWS)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Series) != 1 {
		t.Fatalf("%d points, want 1", len(rep.Series))
	}
	if rep.Series[0].ComplaintMeasured {
		t.Error("series claims a complaint count with no feed connected")
	}
	if rep.Series[0].PlacementMeasured {
		t.Error("series claims a placement count with nothing observed")
	}
}

func TestCampaignVerdict(t *testing.T) {
	cases := []struct {
		name   string
		counts Counts
		status string
		events []PauseEvent
		want   string
	}{
		{
			name:   "clean and running",
			counts: Counts{Delivered: 200, Bounced: 0},
			status: campaignStatusRunning,
			want:   verdictOk,
		},
		{
			name:   "in the warn band",
			counts: Counts{Delivered: 200, Bounced: 10}, // 5%, half of 8 is 4
			status: campaignStatusRunning,
			want:   verdictWarn,
		},
		{
			name:   "the breaker has fired",
			counts: Counts{Delivered: 200, Bounced: 20},
			status: "paused",
			events: []PauseEvent{{Reason: deliverability.ReasonBounceSpike}},
			want:   verdictPaused,
		},
		{
			// Over threshold but still sending, because the operator turned the
			// action off. Calling this 'paused' would be a lie the UI repeats.
			name:   "breaching but not stopped",
			counts: Counts{Delivered: 200, Bounced: 20},
			status: campaignStatusRunning,
			want:   verdictWarn,
		},
		{
			// A campaign paused BY HAND has no pause event, so the breaker has not
			// fired and the verdict describes its rates, not its status.
			name:   "manually paused with clean rates",
			counts: Counts{Delivered: 200, Bounced: 0},
			status: "paused",
			want:   verdictOk,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := &fakeStore{config: runningCampaign(), counts: c.counts, events: c.events}
			store.config.Status = c.status
			rep, err := newService(store).CampaignReport(context.Background(), testWS, testCampaign)
			if err != nil {
				t.Fatal(err)
			}
			if rep.Verdict != c.want {
				t.Errorf("verdict = %q, want %q", rep.Verdict, c.want)
			}
		})
	}
}

func TestSetGuardrailsValidation(t *testing.T) {
	cases := []struct {
		name    string
		in      Guardrails
		wantErr bool
	}{
		{name: "the defaults", in: Guardrails{true, 8, 1.5}},
		{name: "the extremes", in: Guardrails{false, deliverability.ThresholdMin, deliverability.ThresholdMax}},
		{name: "zero bounce threshold", in: Guardrails{true, 0, 1.5}, wantErr: true},
		{name: "zero complaint threshold", in: Guardrails{true, 8, 0}, wantErr: true},
		{name: "negative", in: Guardrails{true, -1, 1.5}, wantErr: true},
		{name: "over 100", in: Guardrails{true, 100.1, 1.5}, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := &fakeStore{}
			got, err := newService(store).SetGuardrails(context.Background(), testWS, testCampaign, c.in)
			if c.wantErr {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("err = %v, want ErrInvalid", err)
				}
				if store.saved != nil {
					t.Error("an invalid threshold was persisted")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.in || store.saved == nil || *store.saved != c.in {
				t.Errorf("round-trip = %+v / stored %+v, want %+v", got, store.saved, c.in)
			}
		})
	}
}

func TestSetGuardrailsPropagatesNotFound(t *testing.T) {
	store := &fakeStore{saveErr: ErrNotFound}
	_, err := newService(store).SetGuardrails(context.Background(), testWS, testCampaign, Guardrails{true, 8, 1.5})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestIngestValidation(t *testing.T) {
	cases := []struct {
		name string
		in   EventInput
	}{
		{name: "unknown kind", in: EventInput{Kind: "spam", Email: "a@b.test", ProviderEventID: "1"}},
		{name: "empty kind", in: EventInput{Email: "a@b.test", ProviderEventID: "1"}},
		{name: "no email", in: EventInput{Kind: "complaint", ProviderEventID: "1"}},
		{name: "blank email", in: EventInput{Kind: "complaint", Email: "  ", ProviderEventID: "1"}},
		{name: "no idempotency key", in: EventInput{Kind: "complaint", Email: "a@b.test"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := &fakeStore{ingestNew: true}
			if _, err := newService(store).Ingest(context.Background(), testWS, c.in); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
			if len(store.ingested) != 0 {
				t.Error("an invalid event was recorded")
			}
		})
	}
}

// A complaint suppresses the address workspace-wide, through the existing
// suppression table, under its own reason.
func TestIngestComplaintSuppressesTheAddress(t *testing.T) {
	store := &fakeStore{ingestNew: true}
	res, err := newService(store).Ingest(context.Background(), testWS, EventInput{
		Kind: "complaint", Email: "reporter@example.test", ProviderEventID: "ses-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Duplicate {
		t.Error("a first delivery reported as a duplicate")
	}
	want := "reporter@example.test:" + suppressionReasonComplaint
	if len(store.suppressed) != 1 || store.suppressed[0] != want {
		t.Errorf("suppressions = %v, want [%q]", store.suppressed, want)
	}
}

// An ingested bounce counts toward the rate but does NOT suppress: provider
// bounce feeds include soft bounces, and suppressing an address forever on a
// temporary failure is not something an operator can undo.
func TestIngestBounceDoesNotSuppress(t *testing.T) {
	store := &fakeStore{ingestNew: true}
	if _, err := newService(store).Ingest(context.Background(), testWS, EventInput{
		Kind: "bounce", Email: "full-mailbox@example.test", ProviderEventID: "ses-2",
	}); err != nil {
		t.Fatal(err)
	}
	if len(store.suppressed) != 0 {
		t.Errorf("an ingested bounce suppressed %v", store.suppressed)
	}
	if len(store.ingested) != 1 {
		t.Errorf("%d events recorded, want 1", len(store.ingested))
	}
}

// A replayed webhook must change nothing: no second suppression, no evaluation,
// and a 200-shaped result rather than a 202.
func TestIngestReplayIsInert(t *testing.T) {
	store := &fakeStore{ingestNew: false}
	res, err := newService(store).Ingest(context.Background(), testWS, EventInput{
		Kind: "complaint", Email: "reporter@example.test", ProviderEventID: "ses-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Duplicate {
		t.Error("a replay was not reported as a duplicate")
	}
	if len(store.suppressed) != 0 {
		t.Errorf("a replay suppressed %v", store.suppressed)
	}
	if len(store.pauseCalls) != 0 {
		t.Error("a replay triggered a breaker evaluation")
	}
}

// A failed suppression fails the whole ingest: honouring the opt-out is the
// load-bearing half of recording a complaint.
func TestIngestFailsWhenSuppressionFails(t *testing.T) {
	store := &fakeStore{ingestNew: true, suppressErr: errors.New("boom")}
	if _, err := newService(store).Ingest(context.Background(), testWS, EventInput{
		Kind: "complaint", Email: "a@b.test", ProviderEventID: "1",
	}); err == nil {
		t.Fatal("ingest succeeded despite the suppression write failing")
	}
}

// An ingest carrying a send_id re-evaluates that send's campaign, because a
// complaint spike can happen with no new sends at all.
func TestIngestWithASendTriggersEvaluation(t *testing.T) {
	sendID := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	store := &fakeStore{
		ingestNew:    true,
		config:       runningCampaign(),
		counts:       Counts{Delivered: 200, Complained: 20, ComplaintFeed: true}, // 10% > 1.5%
		pauseWins:    true,
		sendCampaign: sendCampaign{campaignForSend: testCampaign},
	}
	if _, err := newService(store).Ingest(context.Background(), testWS, EventInput{
		Kind: "complaint", Email: "a@b.test", ProviderEventID: "1", SendID: &sendID,
	}); err != nil {
		t.Fatal(err)
	}
	if len(store.pauseCalls) != 1 {
		t.Fatalf("%d pause writes, want 1 (the complaint spike)", len(store.pauseCalls))
	}
	if store.pauseCalls[0].Reason != deliverability.ReasonComplaintSpike {
		t.Errorf("pause reason = %q, want %q", store.pauseCalls[0].Reason, deliverability.ReasonComplaintSpike)
	}
}

// An event with no send attributes to no campaign, so there is nothing to
// evaluate — and that is not an error.
func TestIngestWithoutASendSkipsEvaluation(t *testing.T) {
	store := &fakeStore{ingestNew: true, config: runningCampaign(), pauseWins: true}
	if _, err := newService(store).Ingest(context.Background(), testWS, EventInput{
		Kind: "complaint", Email: "a@b.test", ProviderEventID: "1",
	}); err != nil {
		t.Fatal(err)
	}
	if len(store.pauseCalls) != 0 {
		t.Error("evaluated a campaign for an event attributed to none")
	}
}

// A scoring failure during ingest must not fail the request: the event is already
// committed and idempotent, so a retry would only be a duplicate.
func TestIngestSucceedsWhenEvaluationFails(t *testing.T) {
	sendID := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	store := &fakeStore{
		ingestNew:    true,
		configErr:    errors.New("boom"),
		sendCampaign: sendCampaign{campaignForSend: testCampaign},
	}
	res, err := newService(store).Ingest(context.Background(), testWS, EventInput{
		Kind: "complaint", Email: "a@b.test", ProviderEventID: "1", SendID: &sendID,
	})
	if err != nil {
		t.Fatalf("ingest failed on an evaluation error: %v", err)
	}
	if res.Duplicate {
		t.Error("a first delivery reported as a duplicate")
	}
	if len(store.suppressed) != 1 {
		t.Errorf("the suppression did not happen: %v", store.suppressed)
	}
}

func TestCampaignReportPropagatesNotFound(t *testing.T) {
	store := &fakeStore{configErr: ErrNotFound}
	if _, err := newService(store).CampaignReport(context.Background(), testWS, testCampaign); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestWorkspaceReportUsesThePlainRollingWindow(t *testing.T) {
	store := &fakeStore{
		counts:        Counts{Delivered: 1000, Bounced: 50},
		atRiskMbx:     []Risk{{Label: "a@b.test", Reason: "throttled"}},
		atRiskDomains: []Risk{{Label: "b.test", Reason: "no SPF record"}},
	}
	rep, err := newService(store).Report(context.Background(), testWS)
	if err != nil {
		t.Fatal(err)
	}
	want := fixedNow.AddDate(0, 0, -deliverability.WindowDays)
	if !store.seriesSince.Equal(want) || !store.signalsSince.Equal(want) {
		t.Errorf("windows = series %v signals %v, want %v", store.seriesSince, store.signalsSince, want)
	}
	// 5% bounce = half the bounce ceiling.
	if rep.Score.Value != 80 {
		t.Errorf("score = %d, want 80", rep.Score.Value)
	}
	if len(rep.AtRiskMailboxes) != 1 || len(rep.AtRiskDomains) != 1 {
		t.Errorf("at-risk lists = %d mailboxes, %d domains", len(rep.AtRiskMailboxes), len(rep.AtRiskDomains))
	}
}

// The score the campaign endpoint reports and the verdict the breaker acts on come
// from ONE resolution of ONE Inputs value (invariant 2). Asserted by driving both
// paths over the same store and checking they agree about the sample.
func TestReportAndBreakerJudgeTheSameEvidence(t *testing.T) {
	rolling := fixedNow.AddDate(0, 0, -deliverability.WindowDays)
	cfg := runningCampaign()
	store := &fakeStore{
		config: cfg,
		countsFor: map[time.Time]Counts{
			rolling:       {Delivered: 10, Bounced: 10},
			cfg.EnabledAt: {Delivered: 400, Bounced: 40}, // 10% over the fallback sample
		},
		pauseWins: true,
	}
	svc := newService(store)
	rep, err := svc.CampaignReport(context.Background(), testWS, testCampaign)
	if err != nil {
		t.Fatal(err)
	}
	out, err := svc.EvaluateBreaker(context.Background(), testWS, testCampaign)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Score.Delivered != out.Verdict.Delivered {
		t.Fatalf("report judged %d delivered, breaker judged %d — two computations",
			rep.Score.Delivered, out.Verdict.Delivered)
	}
	if rep.Verdict != verdictWarn && !out.Paused {
		t.Fatalf("report verdict %q disagrees with breaker outcome %+v", rep.Verdict, out)
	}
}
