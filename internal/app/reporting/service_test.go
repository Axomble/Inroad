package reporting

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

type fakeStore struct {
	rows []gen.ListCampaignPerformanceRow
	err  error
	got  uuid.UUID
}

func (f *fakeStore) CampaignPerformance(_ context.Context, ws uuid.UUID) ([]gen.ListCampaignPerformanceRow, error) {
	f.got = ws
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

func row(name string, sent, enrolled, opens, clicks, replies, bounces, unsubs int64) gen.ListCampaignPerformanceRow {
	return gen.ListCampaignPerformanceRow{
		ID: uuid.New(), Name: name, Status: "running",
		Sent: sent, Enrolled: enrolled, Opens: opens, Clicks: clicks,
		Replies: replies, Bounces: bounces, Unsubscribes: unsubs,
	}
}

func near(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s = %v, want %v", label, got, want)
	}
}

func TestCampaignPerformanceComputesRatesOnTwoDenominators(t *testing.T) {
	store := &fakeStore{rows: []gen.ListCampaignPerformanceRow{
		row("Outbound Q3", 200, 100, 50, 20, 10, 4, 2),
	}}
	ws := uuid.New()

	report, err := NewService(store).CampaignPerformance(context.Background(), ws)
	if err != nil {
		t.Fatalf("CampaignPerformance: %v", err)
	}
	if store.got != ws {
		t.Errorf("store called with workspace %s, want %s", store.got, ws)
	}
	if len(report.Campaigns) != 1 {
		t.Fatalf("campaigns = %d, want 1", len(report.Campaigns))
	}

	c := report.Campaigns[0]
	// Opens and clicks divide by SENDS (200): a message was opened.
	near(t, "open rate", c.OpenRate, 0.25)
	near(t, "click rate", c.ClickRate, 0.10)
	// Replies, bounces and unsubscribes divide by ENROLLED CONTACTS (100): a
	// person replied, however many messages they received. Dividing these by
	// sends would halve every one of them here.
	near(t, "reply rate", c.ReplyRate, 0.10)
	near(t, "bounce rate", c.BounceRate, 0.04)
	near(t, "unsub rate", c.UnsubRate, 0.02)
}

// A workspace's rate is its summed counts over its summed denominator, not the
// mean of its campaigns' rates — otherwise a 2-send campaign that got 1 reply
// would drag the workspace figure around as hard as a 100,000-send one.
func TestTotalsWeightByVolumeRatherThanAveragingRates(t *testing.T) {
	store := &fakeStore{rows: []gen.ListCampaignPerformanceRow{
		row("Tiny", 2, 2, 0, 0, 1, 0, 0),             // 50% reply rate, 2 contacts
		row("Huge", 10_000, 10_000, 0, 0, 100, 0, 0), // 1% reply rate, 10k contacts
	}}

	report, err := NewService(store).CampaignPerformance(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("CampaignPerformance: %v", err)
	}

	if report.Totals.Replies != 101 || report.Totals.Enrolled != 10_002 {
		t.Fatalf("totals = %+v, want 101 replies over 10002 enrolled", report.Totals)
	}
	near(t, "workspace reply rate", report.Totals.ReplyRate, 101.0/10_002.0)
	// The naive mean would be (0.5 + 0.01) / 2 = 0.255 — 25x the truth.
	if report.Totals.ReplyRate > 0.02 {
		t.Errorf("reply rate %v looks like an average of rates, not a weighted total", report.Totals.ReplyRate)
	}
}

// 0/0 must be 0, not NaN: NaN does not survive JSON encoding, so a campaign
// that has sent nothing would break the whole response rather than one cell.
func TestZeroDenominatorsYieldZeroRatesNotNaN(t *testing.T) {
	store := &fakeStore{rows: []gen.ListCampaignPerformanceRow{row("Draft", 0, 0, 0, 0, 0, 0, 0)}}

	report, err := NewService(store).CampaignPerformance(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("CampaignPerformance: %v", err)
	}

	c := report.Campaigns[0]
	for label, rate := range map[string]float64{
		"open": c.OpenRate, "click": c.ClickRate, "reply": c.ReplyRate,
		"bounce": c.BounceRate, "unsub": c.UnsubRate,
	} {
		if math.IsNaN(rate) || rate != 0 {
			t.Errorf("%s rate = %v, want 0", label, rate)
		}
	}
	if math.IsNaN(report.Totals.ReplyRate) {
		t.Error("workspace reply rate is NaN")
	}
}

// The empty state renders from this payload, so it must be an empty list and a
// zeroed total — never a nil slice that encodes as JSON null.
func TestNoCampaignsSerializesAsEmptyNotNil(t *testing.T) {
	report, err := NewService(&fakeStore{}).CampaignPerformance(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("CampaignPerformance: %v", err)
	}
	if report.Campaigns == nil {
		t.Error("Campaigns is nil; it must encode as [] rather than null")
	}
	if len(report.Campaigns) != 0 || report.Totals.Sent != 0 {
		t.Errorf("report = %+v, want empty", report)
	}
}

func TestStoreErrorIsWrappedNotSwallowed(t *testing.T) {
	sentinel := errors.New("connection reset")
	_, err := NewService(&fakeStore{err: sentinel}).CampaignPerformance(context.Background(), uuid.New())
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want it to wrap %v", err, sentinel)
	}
}
