package pulse

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// fakeStore returns canned aggregate rows; err (when set) is returned by
// every method so error propagation is provable.
type fakeStore struct {
	mb       gen.GetPulseMailboxCountsRow
	wu       gen.GetPulseWarmupCountsRow
	cam      gen.GetPulseCampaignCountsRow
	contacts int64
	sent     int64
	caps     []gen.ListPulseSenderCapacityRow
	dmarc    gen.GetPulseDmarcAttentionRow
	err      error
}

func (f *fakeStore) MailboxCounts(context.Context, uuid.UUID) (gen.GetPulseMailboxCountsRow, error) {
	return f.mb, f.err
}
func (f *fakeStore) WarmupCounts(context.Context, uuid.UUID) (gen.GetPulseWarmupCountsRow, error) {
	return f.wu, f.err
}
func (f *fakeStore) CampaignCounts(context.Context, uuid.UUID) (gen.GetPulseCampaignCountsRow, error) {
	return f.cam, f.err
}
func (f *fakeStore) ContactCount(context.Context, uuid.UUID) (int64, error) {
	return f.contacts, f.err
}
func (f *fakeStore) SentToday(context.Context, uuid.UUID) (int64, error) { return f.sent, f.err }
func (f *fakeStore) SenderCapacities(context.Context, uuid.UUID) ([]gen.ListPulseSenderCapacityRow, error) {
	return f.caps, f.err
}
func (f *fakeStore) DmarcAttention(context.Context, uuid.UUID) (gen.GetPulseDmarcAttentionRow, error) {
	return f.dmarc, f.err
}

// testNow is the pinned clock every test's service runs on, so ramp-age
// arithmetic is deterministic.
var testNow = time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

// newService builds a service over store with the clock pinned to testNow.
func newService(store Store) *Service {
	s := NewService(store)
	s.now = func() time.Time { return testNow }
	return s
}

// capRow builds a sender-capacity row created ageDays before the pinned clock.
func capRow(dailyCap int32, rampEnabled bool, health string, ageDays int) gen.ListPulseSenderCapacityRow {
	return gen.ListPulseSenderCapacityRow{
		ID: uuid.New(), DailyCap: dailyCap,
		RampEnabled: rampEnabled, RampStartCap: 5, RampDays: 30,
		CreatedAt:   pgtype.Timestamptz{Time: testNow.AddDate(0, 0, -ageDays), Valid: true},
		HealthState: health,
	}
}

// TestEmptyWorkspacePayload proves a brand-new workspace serializes to the
// exact frozen contract: every aggregate zero, inbox literal zeros, and
// attention as [] — never null, which would break the card's healthy state.
func TestEmptyWorkspacePayload(t *testing.T) {
	svc := newService(&fakeStore{})
	p, err := svc.Get(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	body, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"mailboxes":{"total":0,"active":0,"paused":0,"error":0},` +
		`"warmup":{"pool":0,"healthy":0,"watch":0,"at_risk":0},` +
		`"campaigns":{"total":0,"running":0,"draft":0,"paused":0},` +
		`"contacts":{"total":0},` +
		`"sending":{"sent_today":0,"daily_cap":0},` +
		`"inbox":{"unread":0,"interested":0},` +
		`"attention":[]}`
	if string(body) != want {
		t.Errorf("contract drift:\n got %s\nwant %s", body, want)
	}
}

// TestAggregatesMapThrough proves each store row lands in its payload section
// without renaming or cross-wiring.
func TestAggregatesMapThrough(t *testing.T) {
	svc := newService(&fakeStore{
		mb:       gen.GetPulseMailboxCountsRow{Total: 12, Active: 9, Paused: 1, Error: 2, ErrorReason: "auth failed"},
		wu:       gen.GetPulseWarmupCountsRow{Pool: 6, Healthy: 4, Watch: 1, AtRisk: 1},
		cam:      gen.GetPulseCampaignCountsRow{Total: 8, Running: 3, Draft: 1, Paused: 1},
		contacts: 1243,
		sent:     247,
		caps:     []gen.ListPulseSenderCapacityRow{capRow(600, false, "", 40)},
	})
	p, err := svc.Get(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if p.Mailboxes != (MailboxCounts{Total: 12, Active: 9, Paused: 1, Error: 2}) {
		t.Errorf("mailboxes = %+v", p.Mailboxes)
	}
	if p.Warmup != (WarmupCounts{Pool: 6, Healthy: 4, Watch: 1, AtRisk: 1}) {
		t.Errorf("warmup = %+v", p.Warmup)
	}
	if p.Campaigns != (CampaignCounts{Total: 8, Running: 3, Draft: 1, Paused: 1}) {
		t.Errorf("campaigns = %+v", p.Campaigns)
	}
	if p.Contacts.Total != 1243 {
		t.Errorf("contacts = %+v", p.Contacts)
	}
	if p.Sending != (SendingStatus{SentToday: 247, DailyCap: 600}) {
		t.Errorf("sending = %+v", p.Sending)
	}
	if p.Inbox != (InboxCounts{}) {
		t.Errorf("inbox must be literal zeros until the read-model ships: %+v", p.Inbox)
	}
}

// TestDailyCapUsesSendcapArithmetic proves the meter's daily_cap is the
// health-scaled ramped cap the send path enforces: a ramping mailbox reports
// its ramped (not configured) cap, throttled halves, paused contributes zero.
func TestDailyCapUsesSendcapArithmetic(t *testing.T) {
	svc := newService(&fakeStore{caps: []gen.ListPulseSenderCapacityRow{
		capRow(100, false, "", 40),         // plain: 100
		capRow(100, true, "", 15),          // ramping day 15/30 from 5: 5+(95*15/30)=52
		capRow(40, false, "throttled", 40), // health-halved: 20
		capRow(50, false, "paused", 40),    // cannot send: 0
	}})
	p, err := svc.Get(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if want := int64(100 + 52 + 20 + 0); p.Sending.DailyCap != want {
		t.Errorf("daily_cap = %d, want %d", p.Sending.DailyCap, want)
	}
}

// TestMailboxErrorProducer proves the danger row: present with the stored
// reason, present with the truthful fallback when no last_error was recorded,
// absent with zero erroring mailboxes.
func TestMailboxErrorProducer(t *testing.T) {
	svc := newService(&fakeStore{
		mb: gen.GetPulseMailboxCountsRow{Total: 3, Error: 2, ErrorReason: "auth failed"},
	})
	p, err := svc.Get(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(p.Attention) != 1 {
		t.Fatalf("attention = %+v, want one mailbox_error row", p.Attention)
	}
	got := p.Attention[0]
	want := Attention{Kind: "mailbox_error", Severity: "danger", Count: 2,
		Reason: "auth failed", Href: "/app/mailboxes?status=error"}
	if got != want {
		t.Errorf("row = %+v, want %+v", got, want)
	}

	svc = newService(&fakeStore{mb: gen.GetPulseMailboxCountsRow{Total: 1, Error: 1}})
	p, err = svc.Get(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Get (no stored reason): %v", err)
	}
	if len(p.Attention) != 1 || p.Attention[0].Reason != "mailbox in error state" {
		t.Errorf("fallback reason: %+v", p.Attention)
	}

	svc = newService(&fakeStore{mb: gen.GetPulseMailboxCountsRow{Total: 3, Active: 3}})
	p, err = svc.Get(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Get (healthy): %v", err)
	}
	if len(p.Attention) != 0 {
		t.Errorf("healthy workspace must emit no rows: %+v", p.Attention)
	}
}

// TestSendersGatedProducer proves the warn row counts only health-gated
// senders (watch/throttled/paused) and narrates the states; all-healthy or
// not-warming mailboxes emit nothing.
func TestSendersGatedProducer(t *testing.T) {
	svc := newService(&fakeStore{caps: []gen.ListPulseSenderCapacityRow{
		capRow(50, false, "healthy", 40),
		capRow(50, false, "", 40),
		capRow(50, false, "watch", 40),
		capRow(50, false, "throttled", 40),
		capRow(50, false, "paused", 40),
	}})
	p, err := svc.Get(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(p.Attention) != 1 {
		t.Fatalf("attention = %+v, want one senders_gated row", p.Attention)
	}
	got := p.Attention[0]
	if got.Kind != "senders_gated" || got.Severity != "warn" || got.Count != 3 {
		t.Errorf("row = %+v, want senders_gated/warn/3", got)
	}
	if want := "warmup health limiting sending: 1 on watch, 1 throttled, 1 paused"; got.Reason != want {
		t.Errorf("reason = %q, want %q", got.Reason, want)
	}

	svc = newService(&fakeStore{caps: []gen.ListPulseSenderCapacityRow{
		capRow(50, false, "healthy", 40),
	}})
	p, err = svc.Get(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Get (healthy pool): %v", err)
	}
	if len(p.Attention) != 0 {
		t.Errorf("healthy pool must emit nothing: %+v", p.Attention)
	}
}

// TestDmarcProducer proves the warn row and both reason forms (one domain,
// several); the empty case is covered by TestEmptyWorkspacePayload.
func TestDmarcProducer(t *testing.T) {
	svc := newService(&fakeStore{dmarc: gen.GetPulseDmarcAttentionRow{Count: 1, SampleDomain: "acme.com"}})
	p, err := svc.Get(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(p.Attention) != 1 {
		t.Fatalf("attention = %+v, want one dmarc_failing row", p.Attention)
	}
	got := p.Attention[0]
	want := Attention{Kind: "dmarc_failing", Severity: "warn", Count: 1,
		Reason: "acme.com has no verified DMARC record", Href: "/app/mailboxes"}
	if got != want {
		t.Errorf("row = %+v, want %+v", got, want)
	}

	svc = newService(&fakeStore{dmarc: gen.GetPulseDmarcAttentionRow{Count: 3, SampleDomain: "acme.com"}})
	p, err = svc.Get(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Get (several): %v", err)
	}
	if want := "acme.com and 2 more domains have no verified DMARC record"; p.Attention[0].Reason != want {
		t.Errorf("reason = %q, want %q", p.Attention[0].Reason, want)
	}
}

// TestCapConsumedProducer proves the info row's boundary: fires at exactly
// 90% consumed, not at 89%, and never when the workspace has no capacity at
// all (0/0 is not "cap consumed", it is "nothing to consume").
func TestCapConsumedProducer(t *testing.T) {
	cases := []struct {
		name string
		cap  int32
		sent int64
		want bool
	}{
		{"at threshold", 100, 90, true},
		{"below threshold", 100, 89, false},
		{"over cap", 100, 120, true},
		{"zero capacity never fires", 0, 5, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{sent: tc.sent}
			if tc.cap > 0 {
				store.caps = []gen.ListPulseSenderCapacityRow{capRow(tc.cap, false, "", 40)}
			}
			p, err := newService(store).Get(context.Background(), uuid.New())
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got := len(p.Attention) == 1; got != tc.want {
				t.Fatalf("fired = %v, want %v (attention %+v)", got, tc.want, p.Attention)
			}
			if tc.want {
				row := p.Attention[0]
				if row.Kind != "cap_consumed" || row.Severity != "info" || row.Href != "/app/campaigns" {
					t.Errorf("row = %+v", row)
				}
				if !strings.HasPrefix(row.Reason, "daily cap ") || !strings.HasSuffix(row.Reason, "% used") {
					t.Errorf("reason = %q, want 'daily cap N%% used'", row.Reason)
				}
			}
		})
	}
}

// TestAttentionSeverityOrdering proves worst-first: with every producer
// firing, rows come back danger, then the warns, then info.
func TestAttentionSeverityOrdering(t *testing.T) {
	svc := newService(&fakeStore{
		mb:    gen.GetPulseMailboxCountsRow{Total: 4, Error: 1, ErrorReason: "auth failed"},
		dmarc: gen.GetPulseDmarcAttentionRow{Count: 1, SampleDomain: "acme.com"},
		sent:  95,
		caps: []gen.ListPulseSenderCapacityRow{
			capRow(100, false, "throttled", 40), // gated, contributes 50
			capRow(50, false, "", 40),           // 50 → total cap 100, sent 95 = 95%
		},
	})
	p, err := svc.Get(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	kinds := make([]string, len(p.Attention))
	sevs := make([]string, len(p.Attention))
	for i, a := range p.Attention {
		kinds[i], sevs[i] = a.Kind, a.Severity
	}
	wantKinds := []string{"mailbox_error", "senders_gated", "dmarc_failing", "cap_consumed"}
	wantSevs := []string{"danger", "warn", "warn", "info"}
	for i := range wantKinds {
		if i >= len(kinds) || kinds[i] != wantKinds[i] || sevs[i] != wantSevs[i] {
			t.Fatalf("attention order = %v / %v, want %v / %v", kinds, sevs, wantKinds, wantSevs)
		}
	}
}

// TestStoreErrorPropagates proves a failing aggregate read surfaces (wrapped)
// rather than producing a silently-zero payload the card would render as
// "all healthy".
func TestStoreErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	svc := newService(&fakeStore{err: boom})
	if _, err := svc.Get(context.Background(), uuid.New()); !errors.Is(err, boom) {
		t.Fatalf("Get error = %v, want wrapped %v", err, boom)
	}
}
