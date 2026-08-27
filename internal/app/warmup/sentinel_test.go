package warmup

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
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
