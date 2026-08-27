package warmup

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	pwarmup "github.com/inroad/inroad/internal/platform/warmup"
)

// TestParticipantDTOReportsSentinelDesignation proves the read surface carries
// whether a mailbox is a measurement sentinel, under the contract's field name.
//
// It is a THIRD, ORTHOGONAL fact and not a value of either axis: the participant
// below is reputation-healthy, in the healthy lane, and a sentinel at the same
// time — the combination the design requires stay representable, since a sentinel
// that starts degrading is exactly the case a lane-valued "sentinel" would make
// impossible to express.
func TestParticipantDTOReportsSentinelDesignation(t *testing.T) {
	ws, mb := uuid.New(), uuid.New()
	store := newFakeStore()
	store.ownedMailboxes[mb] = ws
	store.participants[mb] = Participant{
		MailboxID:   mb,
		WorkspaceID: ws,
		Enabled:     true,
		HealthState: "healthy",
		IsSentinel:  true,
	}

	detail, err := NewService(store).GetWarmupDetail(context.Background(), ws, mb)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if !detail.Participant.IsSentinel {
		t.Fatalf("want the participant reported as a sentinel, got %+v", detail.Participant)
	}

	body, err := json.Marshal(detail.Participant)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The contract's own name, and PRESENT on every participant: a client that has
	// to tell "not a sentinel" from "this build does not report designation" cannot
	// do it if the field is omitted when false.
	if got, ok := wire["is_sentinel"]; !ok || got != true {
		t.Fatalf("want is_sentinel true on the wire, got %v (present=%v): %s", got, ok, body)
	}
}

// TestOrdinaryParticipantIsNotASentinel proves the ordinary case is reported as
// the explicit false it is, rather than omitted.
func TestOrdinaryParticipantIsNotASentinel(t *testing.T) {
	ws, mb := uuid.New(), uuid.New()
	store := newFakeStore()
	store.ownedMailboxes[mb] = ws
	store.participants[mb] = Participant{MailboxID: mb, WorkspaceID: ws, Enabled: true, HealthState: "healthy"}

	detail, err := NewService(store).GetWarmupDetail(context.Background(), ws, mb)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	body, err := json.Marshal(detail.Participant)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got, ok := wire["is_sentinel"]; !ok || got != false {
		t.Fatalf("want is_sentinel false on the wire, got %v (present=%v): %s", got, ok, body)
	}
}

// TestUpdatingRampSettingsKeepsTheDesignation proves a settings update does not
// silently undesignate a sentinel.
//
// The upsert's ON CONFLICT arm names the columns it writes (the ramp settings and
// enabled) and is_sentinel is not among them, so designation survives an update —
// the same shape the lane's carry-forward has, and for the same reason: a write
// that answers one question must not quietly answer another. Note this does NOT
// extend across a disable, which deletes the row: a re-enabled mailbox comes back
// undesignated, which is the safe direction (it is not silently re-exposed to
// degrading senders).
func TestUpdatingRampSettingsKeepsTheDesignation(t *testing.T) {
	ws, mb := uuid.New(), uuid.New()
	store := newFakeStore()
	store.ownedMailboxes[mb] = ws
	store.participants[mb] = Participant{
		MailboxID:     mb,
		WorkspaceID:   ws,
		Enabled:       true,
		HealthState:   "healthy",
		StartVolume:   4,
		MaxVolume:     40,
		RampIncrement: 2,
		ReplyRate:     0.3,
		IsSentinel:    true,
	}

	maxVolume := int32(60)
	got, err := NewService(store).EnableWarmup(context.Background(), ws, mb, WarmupSettings{MaxVolume: &maxVolume})
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !got.IsSentinel {
		t.Fatalf("a ramp update cleared the sentinel designation: %+v", got)
	}
}

// The server actually EMITS the sentinel fields the contract declares.
//
// This is the test that was missing, and its absence is why the feature shipped dead.
// `evidence_confidence` and `sentinel_count` were declared in openapi.yaml, generated
// into the client, and read by the UI — while nothing in Go ever set them. Both are
// optional, so the UI's undefined branch rendered permanently, and that branch has
// careful copy about "a build that does not report sentinels". Which was true of every
// build. It looked handled.
//
// Asserted through the JSON rather than the struct, because a Go field that is never
// populated and a JSON key that is never sent are the same defect from a client's side,
// and only one of them is visible in a field-by-field comparison.
func TestOverviewEmitsTheSentinelFieldsTheContractDeclares(t *testing.T) {
	store := newFakeStore()
	store.enabledCount = 2
	store.overviewRows = []OverviewRow{
		{
			MailboxID: uuid.New(), Enabled: true, Email: "sentinel@acme.test",
			HealthState: "healthy", Lane: "healthy",
			StartVolume: 4, MaxVolume: 40, RampIncrement: 2, ReplyRate: 0.3,
			StartedAt:  pgtype.Timestamptz{Time: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), Valid: true},
			IsSentinel: true,
			// Its own evidence came from a sentinel observer.
			SentinelObservations7d: 12, Inbox7d: 30, Spam7d: 1,
		},
		{
			MailboxID: uuid.New(), Enabled: true, Email: "peer@acme.test",
			HealthState: "healthy", Lane: "healthy",
			StartVolume: 4, MaxVolume: 40, RampIncrement: 2, ReplyRate: 0.3,
			StartedAt:  pgtype.Timestamptz{Time: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), Valid: true},
			IsSentinel: false,
			// Measured only by its own lane-mates.
			SentinelObservations7d: 0, Inbox7d: 28, Spam7d: 2,
		},
	}
	svc := withNow(NewService(store), time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC))

	ov, err := svc.GetOverview(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	body, err := json.Marshal(ov)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(body)

	for _, want := range []string{
		`"sentinel_count":1`,
		`"sentinel_pool_oversized":false`, // one of two is exactly the share, not over it
		`"evidence_confidence":"sentinel_corroborated"`,
		`"evidence_confidence":"peer_only"`,
		`"is_sentinel":true`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("overview JSON is missing %s\ngot: %s", want, got)
		}
	}
}

// And the confidence label comes from the policy, not a comparison spelled out in the
// service — so the two cannot drift about what "corroborated" means.
func TestEvidenceConfidenceTracksThePolicy(t *testing.T) {
	for _, n := range []int64{0, 1, 5} {
		store := newFakeStore()
		store.enabledCount = 1
		store.overviewRows = []OverviewRow{{
			MailboxID: uuid.New(), Enabled: true, Email: "a@acme.test",
			HealthState: "healthy", Lane: "healthy",
			StartVolume: 4, MaxVolume: 40, RampIncrement: 2, ReplyRate: 0.3,
			StartedAt:              pgtype.Timestamptz{Time: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), Valid: true},
			SentinelObservations7d: n,
		}}
		svc := withNow(NewService(store), time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC))

		ov, err := svc.GetOverview(context.Background(), uuid.New())
		if err != nil {
			t.Fatalf("GetOverview: %v", err)
		}
		want := string(pwarmup.ConfidenceOf(int(n)))
		if got := ov.Mailboxes[0].EvidenceConfidence; got != want {
			t.Errorf("with %d sentinel observations: confidence = %q, want %q", n, got, want)
		}
	}
}

// The advisory cap fires, and only when actually exceeded.
//
// Split from the emission test because that fixture sits at EXACTLY the share — one
// sentinel of two — where the honest answer is false. A revert hardcoding false
// therefore passed it, which is a test agreeing with a bug.
//
// Advisory throughout: exceeded, it is reported and nothing is refused. Refusing to
// pair would stop warmup rather than tell the operator that measurement has started
// to become the network.
func TestSentinelPoolOversizedIsReportedNotEnforced(t *testing.T) {
	row := func(sentinel bool) OverviewRow {
		return OverviewRow{
			MailboxID: uuid.New(), Enabled: true, Email: "m@acme.test",
			HealthState: "healthy", Lane: "healthy",
			StartVolume: 4, MaxVolume: 40, RampIncrement: 2, ReplyRate: 0.3,
			StartedAt:  pgtype.Timestamptz{Time: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), Valid: true},
			IsSentinel: sentinel,
		}
	}
	tests := []struct {
		name      string
		rows      []OverviewRow
		wantCount int
		wantOver  bool
	}{
		// Two of three is 67%, past the advisory half.
		{"two sentinels of three", []OverviewRow{row(true), row(true), row(false)}, 2, true},
		// One of two is exactly half, which is not past it.
		{"one of two is at the share, not over", []OverviewRow{row(true), row(false)}, 1, false},
		// A pool of nothing but a sentinel is oversized AND measures nothing.
		{"a lone sentinel", []OverviewRow{row(true)}, 1, true},
		{"no sentinels", []OverviewRow{row(false), row(false)}, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			store.enabledCount = int64(len(tc.rows))
			store.overviewRows = tc.rows
			svc := withNow(NewService(store), time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC))

			ov, err := svc.GetOverview(context.Background(), uuid.New())
			if err != nil {
				t.Fatalf("GetOverview: %v", err)
			}
			if ov.SentinelCount != tc.wantCount {
				t.Errorf("sentinel_count = %d, want %d", ov.SentinelCount, tc.wantCount)
			}
			if ov.SentinelPoolOversized != tc.wantOver {
				t.Errorf("sentinel_pool_oversized = %v, want %v", ov.SentinelPoolOversized, tc.wantOver)
			}
			// Nothing is withheld either way: every mailbox is still returned.
			if len(ov.Mailboxes) != len(tc.rows) {
				t.Errorf("returned %d mailboxes of %d — the cap must report, never refuse",
					len(ov.Mailboxes), len(tc.rows))
			}
		})
	}
}
