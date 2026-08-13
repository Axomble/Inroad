package campaign_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/campaign"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// --- ComputePreflight: pure-function matrix -------------------------------
//
// ComputePreflight does no I/O, so every branch below is exercised with a
// hand-built PreflightInput. healthyInput is a baseline that passes every
// check; each test below flips exactly ONE field off the baseline and asserts
// only the check that field drives.

func healthyInput() campaign.PreflightInput {
	capToday := 50
	return campaign.PreflightInput{
		Steps:   []campaign.PreflightStep{{BodyText: "hello {{first_name}}"}},
		Windows: []campaign.SendWindow{{Weekday: 1, StartMinute: 540, EndMinute: 1020}},
		Senders: []campaign.Sender{
			{Email: "sender@x.test", Enabled: true, Status: "active", CapToday: capToday},
		},
		AudienceCount: 10,
		DomainAuth: map[string]campaign.DomainAuthVerdict{
			"x.test": {Checked: true, SPFFound: true, DMARCFound: true},
		},
		TrackingEnabled: true,
		DailyLimit:      nil,
	}
}

// findCheck locates one check by id, failing the test if it is absent --
// every one of the documented ids must always be present.
func findCheck(t *testing.T, report campaign.PreflightReport, id string) campaign.PreflightCheck {
	t.Helper()
	for _, c := range report.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("check %q missing from report: %+v", id, report.Checks)
	return campaign.PreflightCheck{}
}

func TestComputePreflightHealthyInputIsReadyAndEveryCheckPasses(t *testing.T) {
	report := campaign.ComputePreflight(healthyInput())
	if !report.Ready {
		t.Fatalf("ready = false, want true: %+v", report.Checks)
	}
	ids := []string{
		campaign.CheckSequenceSteps, campaign.CheckEmptyBodies, campaign.CheckScheduleWindows,
		campaign.CheckSenderPool, campaign.CheckAudience, campaign.CheckDomainAuth,
		campaign.CheckTracking, campaign.CheckDailyLimit, campaign.CheckWarmupHealth, campaign.CheckTokens, campaign.CheckVariantWeights,
	}
	if len(report.Checks) != len(ids) {
		t.Fatalf("checks = %d, want %d: %+v", len(report.Checks), len(ids), report.Checks)
	}
	for _, id := range ids {
		c := findCheck(t, report, id)
		if c.Severity != campaign.SeverityPass {
			t.Errorf("check %q severity = %q, want pass: %+v", id, c.Severity, c)
		}
	}
}

func TestComputePreflightZeroStepsFails(t *testing.T) {
	in := healthyInput()
	in.Steps = nil
	report := campaign.ComputePreflight(in)
	if report.Ready {
		t.Error("ready = true, want false")
	}
	if c := findCheck(t, report, campaign.CheckSequenceSteps); c.Severity != campaign.SeverityFail {
		t.Errorf("sequence_steps severity = %q, want fail", c.Severity)
	}
}

func TestComputePreflightEmptyStepBodyWarns(t *testing.T) {
	in := healthyInput()
	in.Steps = []campaign.PreflightStep{{BodyText: "hi"}, {}}
	report := campaign.ComputePreflight(in)
	if !report.Ready {
		t.Error("a warn-only check must not flip ready to false")
	}
	c := findCheck(t, report, campaign.CheckEmptyBodies)
	if c.Severity != campaign.SeverityWarn {
		t.Errorf("empty_bodies severity = %q, want warn", c.Severity)
	}
}

func TestComputePreflightNoBothBodiesEmptyStillPasses(t *testing.T) {
	in := healthyInput()
	in.Steps = []campaign.PreflightStep{{BodyHTML: "<p>hi</p>"}}
	report := campaign.ComputePreflight(in)
	if c := findCheck(t, report, campaign.CheckEmptyBodies); c.Severity != campaign.SeverityPass {
		t.Errorf("a step with only an HTML body must not warn: severity = %q", c.Severity)
	}
}

func TestComputePreflightEmptyWeekFails(t *testing.T) {
	in := healthyInput()
	in.Windows = nil
	report := campaign.ComputePreflight(in)
	if report.Ready {
		t.Error("ready = true, want false")
	}
	if c := findCheck(t, report, campaign.CheckScheduleWindows); c.Severity != campaign.SeverityFail {
		t.Errorf("schedule_windows severity = %q, want fail", c.Severity)
	}
}

func TestComputePreflightNoEnabledActiveSenderFails(t *testing.T) {
	cases := []struct {
		name    string
		senders []campaign.Sender
	}{
		{"disabled only", []campaign.Sender{{Email: "a@x.test", Enabled: false, Status: "active"}}},
		{"inactive only", []campaign.Sender{{Email: "a@x.test", Enabled: true, Status: "paused"}}},
		{"no senders", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := healthyInput()
			in.Senders = tc.senders
			report := campaign.ComputePreflight(in)
			if report.Ready {
				t.Error("ready = true, want false")
			}
			if c := findCheck(t, report, campaign.CheckSenderPool); c.Severity != campaign.SeverityFail {
				t.Errorf("sender_pool severity = %q, want fail", c.Severity)
			}
		})
	}
}

func TestComputePreflightMixedPoolWithOneEligibleSenderPasses(t *testing.T) {
	in := healthyInput()
	in.Senders = []campaign.Sender{
		{Email: "disabled@x.test", Enabled: false, Status: "active"},
		{Email: "ok@x.test", Enabled: true, Status: "active", CapToday: 10},
	}
	report := campaign.ComputePreflight(in)
	if c := findCheck(t, report, campaign.CheckSenderPool); c.Severity != campaign.SeverityPass {
		t.Errorf("sender_pool severity = %q, want pass with one eligible member", c.Severity)
	}
}

func TestComputePreflightZeroAudienceFails(t *testing.T) {
	in := healthyInput()
	in.AudienceCount = 0
	report := campaign.ComputePreflight(in)
	if report.Ready {
		t.Error("ready = true, want false")
	}
	if c := findCheck(t, report, campaign.CheckAudience); c.Severity != campaign.SeverityFail {
		t.Errorf("audience severity = %q, want fail", c.Severity)
	}
}

func TestComputePreflightDomainAuthFailingSPFOrDMARCWarnsButStaysReady(t *testing.T) {
	in := healthyInput()
	in.DomainAuth = map[string]campaign.DomainAuthVerdict{
		"x.test": {Checked: true, SPFFound: false, DMARCFound: true},
	}
	report := campaign.ComputePreflight(in)
	if !report.Ready {
		t.Error("domain_auth is informational -- it must never fail preflight")
	}
	c := findCheck(t, report, campaign.CheckDomainAuth)
	if c.Severity != campaign.SeverityWarn {
		t.Errorf("domain_auth severity = %q, want warn", c.Severity)
	}
	if !strings.Contains(c.Detail, "x.test") {
		t.Errorf("detail = %q, want it to name the failing domain", c.Detail)
	}
}

func TestComputePreflightDomainAuthUncheckedWarnsWithRecheckRemedy(t *testing.T) {
	in := healthyInput()
	in.DomainAuth = map[string]campaign.DomainAuthVerdict{} // no verdict at all for x.test
	report := campaign.ComputePreflight(in)
	c := findCheck(t, report, campaign.CheckDomainAuth)
	if c.Severity != campaign.SeverityWarn {
		t.Errorf("domain_auth severity = %q, want warn", c.Severity)
	}
	if !strings.Contains(strings.ToLower(c.Remedy), "recheck") {
		t.Errorf("remedy = %q, want it to mention rechecking", c.Remedy)
	}
}

// When a campaign's pool spans domains in BOTH states at once (one failing
// SPF/DMARC, another never checked), the single domain_auth row must name
// both -- not silently drop one in favor of the other.
func TestComputePreflightDomainAuthMentionsBothFailingAndUncheckedWhenBothExist(t *testing.T) {
	in := healthyInput()
	in.Senders = []campaign.Sender{
		{Email: "a@failing.test", Enabled: true, Status: "active", CapToday: 10},
		{Email: "b@unchecked.test", Enabled: true, Status: "active", CapToday: 10},
	}
	in.DomainAuth = map[string]campaign.DomainAuthVerdict{
		"failing.test": {Checked: true, SPFFound: false, DMARCFound: true},
		// "unchecked.test" deliberately absent from the map.
	}
	report := campaign.ComputePreflight(in)
	if !report.Ready {
		t.Error("domain_auth is informational -- it must never fail preflight")
	}
	c := findCheck(t, report, campaign.CheckDomainAuth)
	if c.Severity != campaign.SeverityWarn {
		t.Errorf("domain_auth severity = %q, want warn", c.Severity)
	}
	if !strings.Contains(c.Detail, "failing.test") {
		t.Errorf("detail = %q, want it to name the failing domain", c.Detail)
	}
	if !strings.Contains(c.Detail, "unchecked.test") {
		t.Errorf("detail = %q, want it to ALSO name the unchecked domain, not just the failing one", c.Detail)
	}
	if !strings.Contains(strings.ToLower(c.Remedy), "recheck") {
		t.Errorf("remedy = %q, want it to still mention rechecking for the unchecked domain", c.Remedy)
	}
	if !strings.Contains(strings.ToLower(c.Remedy), "publish") {
		t.Errorf("remedy = %q, want it to still mention publishing records for the failing domain", c.Remedy)
	}
}

func TestComputePreflightNoSenderDomainsSkipsDomainAuthCleanly(t *testing.T) {
	in := healthyInput()
	in.Senders = []campaign.Sender{{Email: "not-an-email", Enabled: true, Status: "active"}}
	report := campaign.ComputePreflight(in)
	// No domain to check at all -> nothing failing, nothing unchecked -> pass.
	if c := findCheck(t, report, campaign.CheckDomainAuth); c.Severity != campaign.SeverityPass {
		t.Errorf("domain_auth severity = %q, want pass when no sender has a resolvable domain", c.Severity)
	}
}

func TestComputePreflightTrackingDisabledWarnsButStaysReady(t *testing.T) {
	in := healthyInput()
	in.TrackingEnabled = false
	report := campaign.ComputePreflight(in)
	if !report.Ready {
		t.Error("tracking is informational -- it must never fail preflight")
	}
	if c := findCheck(t, report, campaign.CheckTracking); c.Severity != campaign.SeverityWarn {
		t.Errorf("tracking severity = %q, want warn", c.Severity)
	}
}

func TestComputePreflightDailyLimitAboveCapacityWarns(t *testing.T) {
	in := healthyInput()
	in.Senders = []campaign.Sender{{Email: "a@x.test", Enabled: true, Status: "active", CapToday: 20}}
	limit := 100
	in.DailyLimit = &limit
	report := campaign.ComputePreflight(in)
	if !report.Ready {
		t.Error("daily_limit is informational -- it must never fail preflight")
	}
	c := findCheck(t, report, campaign.CheckDailyLimit)
	if c.Severity != campaign.SeverityWarn {
		t.Errorf("daily_limit severity = %q, want warn", c.Severity)
	}
}

func TestComputePreflightDailyLimitWithinCapacityPasses(t *testing.T) {
	in := healthyInput()
	in.Senders = []campaign.Sender{{Email: "a@x.test", Enabled: true, Status: "active", CapToday: 200}}
	limit := 100
	in.DailyLimit = &limit
	report := campaign.ComputePreflight(in)
	if c := findCheck(t, report, campaign.CheckDailyLimit); c.Severity != campaign.SeverityPass {
		t.Errorf("daily_limit severity = %q, want pass", c.Severity)
	}
}

func TestComputePreflightDailyLimitCountsOnlyEnabledSenderCapacity(t *testing.T) {
	in := healthyInput()
	in.Senders = []campaign.Sender{
		{Email: "enabled@x.test", Enabled: true, Status: "active", CapToday: 10},
		{Email: "disabled@x.test", Enabled: false, Status: "active", CapToday: 1000},
	}
	limit := 50
	in.DailyLimit = &limit
	report := campaign.ComputePreflight(in)
	// 50 > 10 (the disabled sender's huge cap must not count) -> warn.
	if c := findCheck(t, report, campaign.CheckDailyLimit); c.Severity != campaign.SeverityWarn {
		t.Errorf("daily_limit severity = %q, want warn (disabled sender capacity must not count)", c.Severity)
	}
}

func TestComputePreflightNilDailyLimitPasses(t *testing.T) {
	in := healthyInput()
	in.DailyLimit = nil
	if c := findCheck(t, campaign.ComputePreflight(in), campaign.CheckDailyLimit); c.Severity != campaign.SeverityPass {
		t.Errorf("daily_limit severity = %q, want pass when no campaign-wide limit is set", c.Severity)
	}
}

func TestComputePreflightDegradedHealthWarns(t *testing.T) {
	unknown, watch, throttled, paused := "unknown", "watch", "throttled", "paused"
	cases := []*string{&unknown, &watch, &throttled, &paused}
	for _, state := range cases {
		t.Run(*state, func(t *testing.T) {
			in := healthyInput()
			in.Senders = []campaign.Sender{
				{Email: "a@x.test", Enabled: true, Status: "active", CapToday: 10, HealthState: state},
			}
			report := campaign.ComputePreflight(in)
			if !report.Ready {
				t.Error("a degraded HEALTH state only reduces capacity -- it must not fail preflight")
			}
			if c := findCheck(t, report, campaign.CheckWarmupHealth); c.Severity != campaign.SeverityWarn {
				t.Errorf("warmup_health severity = %q, want warn for state %q", c.Severity, *state)
			}
		})
	}
}

func TestComputePreflightHealthyMailboxDoesNotWarnWarmupHealth(t *testing.T) {
	healthy := "healthy"
	in := healthyInput()
	in.Senders = []campaign.Sender{{Email: "a@x.test", Enabled: true, Status: "active", CapToday: 10, HealthState: &healthy}}
	if c := findCheck(t, campaign.ComputePreflight(in), campaign.CheckWarmupHealth); c.Severity != campaign.SeverityPass {
		t.Errorf("warmup_health severity = %q, want pass for a non-throttled/paused state", c.Severity)
	}
}

func TestComputePreflightMultipleFailuresAllSurfaceAndReadyIsFalse(t *testing.T) {
	in := healthyInput()
	in.Steps = nil
	in.AudienceCount = 0
	report := campaign.ComputePreflight(in)
	if report.Ready {
		t.Fatal("ready = true, want false")
	}
	if c := findCheck(t, report, campaign.CheckSequenceSteps); c.Severity != campaign.SeverityFail {
		t.Errorf("sequence_steps severity = %q, want fail", c.Severity)
	}
	if c := findCheck(t, report, campaign.CheckAudience); c.Severity != campaign.SeverityFail {
		t.Errorf("audience severity = %q, want fail", c.Severity)
	}
	// A fail elsewhere must not mask an unrelated check's own pass.
	if c := findCheck(t, report, campaign.CheckTracking); c.Severity != campaign.SeverityPass {
		t.Errorf("tracking severity = %q, want pass (unaffected)", c.Severity)
	}
}

// --- Service.Preflight: the thin loader ------------------------------------

// fakeCampaignStore implements campaign.Store with just enough behaviour to
// drive Preflight/TestSend. Every remaining method is a zero-value stub, the
// same pattern lifecycle_test.go's fakeStore uses for the methods it doesn't
// need.
type fakeCampaignStore struct {
	campaigns map[[2]uuid.UUID]gen.Campaign

	steps   []gen.SequenceStep
	windows []campaign.SendWindow

	stepVariants map[uuid.UUID][]campaign.PreflightVariant

	senders     []campaign.Sender
	sendersErr  error
	fallback    campaign.Sender
	fallbackErr error

	audience    int64
	audienceErr error

	stepsErr, windowsErr error
}

func (f *fakeCampaignStore) Get(_ context.Context, ws, id uuid.UUID) (gen.Campaign, error) {
	c, ok := f.campaigns[[2]uuid.UUID{ws, id}]
	if !ok {
		return gen.Campaign{}, errNotFound
	}
	return c, nil
}
func (f *fakeCampaignStore) ListSteps(context.Context, uuid.UUID, uuid.UUID) ([]gen.SequenceStep, error) {
	return f.steps, f.stepsErr
}

// stepVariants is nil in every test that is not about A/B variants -- a campaign
// with no variants at all, which is how the vast majority of campaigns run.
func (f *fakeCampaignStore) ListStepVariants(context.Context, uuid.UUID, uuid.UUID) (map[uuid.UUID][]campaign.PreflightVariant, error) {
	return f.stepVariants, nil
}
func (f *fakeCampaignStore) ListWindows(context.Context, uuid.UUID, uuid.UUID) ([]campaign.SendWindow, error) {
	return f.windows, f.windowsErr
}
func (f *fakeCampaignStore) ListSenders(context.Context, uuid.UUID, uuid.UUID) ([]campaign.Sender, error) {
	return f.senders, f.sendersErr
}
func (f *fakeCampaignStore) FallbackSender(context.Context, uuid.UUID, uuid.UUID) (campaign.Sender, error) {
	return f.fallback, f.fallbackErr
}
func (f *fakeCampaignStore) CountUnsuppressedAudience(context.Context, uuid.UUID, uuid.UUID) (int64, error) {
	return f.audience, f.audienceErr
}

// Every remaining Store method is unused by these tests.
func (f *fakeCampaignStore) Create(context.Context, uuid.UUID, campaign.CreateInput) (gen.Campaign, error) {
	return gen.Campaign{}, nil
}
func (f *fakeCampaignStore) List(context.Context, uuid.UUID) ([]gen.Campaign, error) { return nil, nil }
func (f *fakeCampaignStore) Stats(context.Context, uuid.UUID, uuid.UUID) (map[string]int64, error) {
	return nil, nil
}
func (f *fakeCampaignStore) CountSteps(context.Context, uuid.UUID, uuid.UUID) (int64, error) {
	return int64(len(f.steps)), nil
}
func (f *fakeCampaignStore) EnrollTx(context.Context, uuid.UUID, uuid.UUID) ([]campaign.Enrollment, error) {
	return nil, nil
}
func (f *fakeCampaignStore) Reschedule(context.Context, uuid.UUID, uuid.UUID, time.Time) error {
	return nil
}
func (f *fakeCampaignStore) RescheduleBatch(context.Context, uuid.UUID, map[uuid.UUID]time.Time) error {
	return nil
}
func (f *fakeCampaignStore) ReplaceSchedule(context.Context, uuid.UUID, uuid.UUID, campaign.Plan) error {
	return nil
}
func (f *fakeCampaignStore) ReplaceSenders(context.Context, uuid.UUID, uuid.UUID, string, []campaign.SenderInput) error {
	return nil
}
func (f *fakeCampaignStore) EnrollmentCounts(context.Context, uuid.UUID, uuid.UUID) (map[string]int64, error) {
	return nil, nil
}
func (f *fakeCampaignStore) EngagementCounts(context.Context, uuid.UUID, uuid.UUID) (int64, int64, error) {
	return 0, 0, nil
}
func (f *fakeCampaignStore) StopReasonCounts(context.Context, uuid.UUID, uuid.UUID) (map[string]int64, error) {
	return nil, nil
}
func (f *fakeCampaignStore) SetTracking(context.Context, uuid.UUID, uuid.UUID, bool) error {
	return nil
}
func (f *fakeCampaignStore) ListEnrollments(context.Context, uuid.UUID, uuid.UUID, int32, int32) ([]gen.ListCampaignEnrollmentsRow, error) {
	return nil, nil
}
func (f *fakeCampaignStore) SetStatus(context.Context, uuid.UUID, uuid.UUID, campaign.CampaignStatus) error {
	return nil
}
func (f *fakeCampaignStore) Rename(context.Context, uuid.UUID, uuid.UUID, string) (gen.Campaign, error) {
	return gen.Campaign{}, nil
}
func (f *fakeCampaignStore) DeleteDraft(context.Context, uuid.UUID, uuid.UUID) error { return nil }

// fakeDomainAuthReader returns a fixed verdict map, or an error when errOn is
// set.
type fakeDomainAuthReader struct {
	verdicts map[string]campaign.DomainAuthVerdict
	err      error
}

func (f fakeDomainAuthReader) DomainAuth(context.Context, uuid.UUID) (map[string]campaign.DomainAuthVerdict, error) {
	return f.verdicts, f.err
}

// fakeCustomFieldReader supplies the live {{custom.*}} keys the
// personalization_tokens check resolves against.
type fakeCustomFieldReader struct {
	keys []string
	err  error
}

func (f fakeCustomFieldReader) CustomFieldKeys(context.Context, uuid.UUID) ([]string, error) {
	return f.keys, f.err
}

// withFields is the option every loader test needs, because an unwired reader
// deliberately fails the request rather than degrading (see WithCustomFields).
// The default key set is empty: a sequence with no {{custom.*}} tokens passes
// the check either way, so tests that are not about custom fields say nothing
// about them.
func withFields(keys ...string) campaign.ServiceOption {
	return campaign.WithCustomFields(fakeCustomFieldReader{keys: keys})
}

func TestPreflightCrossTenantIsNotFound(t *testing.T) {
	ctx := context.Background()
	owner, id := uuid.New(), uuid.New()
	store := &fakeCampaignStore{campaigns: map[[2]uuid.UUID]gen.Campaign{{owner, id}: {ID: id, WorkspaceID: owner}}}
	svc := campaign.NewService(store, noopChecker{}, withFields())

	if _, err := svc.Preflight(ctx, uuid.New(), id); !errors.Is(err, campaign.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestPreflightFallsBackToTheCampaignMailboxWhenThePoolIsEmpty(t *testing.T) {
	ctx := context.Background()
	ws, id := uuid.New(), uuid.New()
	store := &fakeCampaignStore{
		campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws}},
		steps:     []gen.SequenceStep{{ID: uuid.New(), BodyText: "hi"}},
		windows:   []campaign.SendWindow{{Weekday: 1, StartMinute: 0, EndMinute: 60}},
		senders:   nil, // never configured
		fallback:  campaign.Sender{Email: "solo@x.test", Enabled: true, Status: "active", CapToday: 10},
		audience:  1,
	}
	svc := campaign.NewService(store, noopChecker{}, withFields())

	report, err := svc.Preflight(ctx, ws, id)
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if c := findCheck(t, report, campaign.CheckSenderPool); c.Severity != campaign.SeverityPass {
		t.Errorf("sender_pool severity = %q, want pass via the fallback mailbox", c.Severity)
	}
}

func TestPreflightWithoutADomainAuthReaderTreatsEveryDomainAsUnchecked(t *testing.T) {
	ctx := context.Background()
	ws, id := uuid.New(), uuid.New()
	store := &fakeCampaignStore{
		campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws}},
		steps:     []gen.SequenceStep{{ID: uuid.New(), BodyText: "hi"}},
		windows:   []campaign.SendWindow{{Weekday: 1, StartMinute: 0, EndMinute: 60}},
		senders:   []campaign.Sender{{Email: "a@x.test", Enabled: true, Status: "active", CapToday: 10}},
		audience:  1,
	}
	// No campaign.WithDomainAuth option: the loader must not fail, and the
	// check must degrade to "not checked" rather than silently reporting pass.
	svc := campaign.NewService(store, noopChecker{}, withFields())

	report, err := svc.Preflight(ctx, ws, id)
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	c := findCheck(t, report, campaign.CheckDomainAuth)
	if c.Severity != campaign.SeverityWarn {
		t.Errorf("domain_auth severity = %q, want warn (unchecked) with no reader wired", c.Severity)
	}
}

func TestPreflightUsesTheWiredDomainAuthReader(t *testing.T) {
	ctx := context.Background()
	ws, id := uuid.New(), uuid.New()
	store := &fakeCampaignStore{
		campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws}},
		steps:     []gen.SequenceStep{{ID: uuid.New(), BodyText: "hi"}},
		windows:   []campaign.SendWindow{{Weekday: 1, StartMinute: 0, EndMinute: 60}},
		senders:   []campaign.Sender{{Email: "a@x.test", Enabled: true, Status: "active", CapToday: 10}},
		audience:  1,
	}
	reader := fakeDomainAuthReader{verdicts: map[string]campaign.DomainAuthVerdict{
		"x.test": {Checked: true, SPFFound: true, DMARCFound: true},
	}}
	svc := campaign.NewService(store, noopChecker{}, campaign.WithDomainAuth(reader), withFields())

	report, err := svc.Preflight(ctx, ws, id)
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if c := findCheck(t, report, campaign.CheckDomainAuth); c.Severity != campaign.SeverityPass {
		t.Errorf("domain_auth severity = %q, want pass with a passing wired verdict", c.Severity)
	}
}

func TestPreflightPropagatesStoreErrors(t *testing.T) {
	ctx := context.Background()
	ws, id := uuid.New(), uuid.New()
	boom := errNotFound // any sentinel; the point is it's not silently swallowed
	store := &fakeCampaignStore{
		campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws}},
		stepsErr:  boom,
	}
	svc := campaign.NewService(store, noopChecker{}, withFields())

	if _, err := svc.Preflight(ctx, ws, id); err == nil {
		t.Fatal("err = nil, want the propagated store error")
	}
}

// A lane that may not take new leads is the one warmup condition that STOPS a
// launch. Those mailboxes are withheld from the pool entirely, so launching would
// either send nothing or silently concentrate the whole campaign on the senders
// that remain.
func TestComputePreflightWithheldLaneFailsPreflight(t *testing.T) {
	for _, lane := range []string{"quarantine", "blocked"} {
		t.Run(lane, func(t *testing.T) {
			lane := lane
			healthy := "healthy"
			in := healthyInput()
			in.Senders = []campaign.Sender{
				{Email: "a@x.test", Enabled: true, Status: "active", CapToday: 10, HealthState: &healthy, Lane: &lane},
			}
			report := campaign.ComputePreflight(in)
			if report.Ready {
				t.Error("a withheld lane must fail preflight even when health looks fine")
			}
			c := findCheck(t, report, campaign.CheckWarmupHealth)
			if c.Severity != campaign.SeverityFail {
				t.Fatalf("warmup_health severity = %q, want fail for lane %q", c.Severity, lane)
			}
			if !strings.Contains(c.Detail, lane) {
				t.Errorf("detail %q does not name the lane that caused the failure", c.Detail)
			}
			if c.Remedy == "" {
				t.Error("a failing check must tell the operator what clears it")
			}
		})
	}
}

// Evidence-gathering lanes send at a bounded volume but are not withheld, so they
// warn rather than fail: the campaign can launch, just slower.
func TestComputePreflightEvidenceGatheringLanesWarn(t *testing.T) {
	for _, lane := range []string{"probation", "recovery"} {
		t.Run(lane, func(t *testing.T) {
			lane := lane
			healthy := "healthy"
			in := healthyInput()
			in.Senders = []campaign.Sender{
				{Email: "a@x.test", Enabled: true, Status: "active", CapToday: 10, HealthState: &healthy, Lane: &lane},
			}
			report := campaign.ComputePreflight(in)
			if !report.Ready {
				t.Errorf("lane %q gathers evidence at reduced volume -- it must not fail preflight", lane)
			}
			if c := findCheck(t, report, campaign.CheckWarmupHealth); c.Severity != campaign.SeverityWarn {
				t.Errorf("warmup_health severity = %q, want warn for lane %q", c.Severity, lane)
			}
		})
	}
}

// security.md invariant 39: "Nothing on the send path reads sending_domains — an
// advisory that turns out to be wrong must not be able to stop a campaign."
// pending_auth is derived from that advisory, so it warns rather than fails, and a
// spoofed or merely un-swept DNS answer cannot veto a launch. Warmup itself still
// refuses to send unauthenticated mail; only the campaign veto is withheld.
func TestComputePreflightPendingAuthWarnsRatherThanBlocking(t *testing.T) {
	lane, healthy := "pending_auth", "healthy"
	in := healthyInput()
	in.Senders = []campaign.Sender{
		{Email: "a@x.test", Enabled: true, Status: "active", CapToday: 10, HealthState: &healthy, Lane: &lane},
	}
	report := campaign.ComputePreflight(in)
	if !report.Ready {
		t.Error("an advisory DNS check must not stop a campaign (security.md invariant 39)")
	}
	c := findCheck(t, report, campaign.CheckWarmupHealth)
	if c.Severity != campaign.SeverityWarn {
		t.Fatalf("warmup_health severity = %q, want warn for pending_auth", c.Severity)
	}
	if !strings.Contains(c.Detail, "a@x.test") {
		t.Errorf("detail %q does not name the affected sender", c.Detail)
	}
}
