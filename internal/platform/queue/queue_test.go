package queue

import (
	"encoding/json"
	"testing"
	"time"
)

func TestWarmupTickPayloadRoundTrip(t *testing.T) {
	p := WarmupTickPayload{MailboxID: "mb-123", WorkspaceID: "ws-1"}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got WarmupTickPayload
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.MailboxID != "mb-123" || got.WorkspaceID != "ws-1" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if TaskWarmupTick != "warmup:tick" || TaskWarmupSweep != "warmup:sweep" {
		t.Errorf("task name drift: %q %q", TaskWarmupTick, TaskWarmupSweep)
	}
}

// TestWarmupTickTaskID proves the dedup key collapses two ticks whose due times
// fall in the same whole second to one key (a sweep re-seed racing the lazy
// chain), while a later second yields a distinct key (a genuinely new tick).
func TestWarmupTickTaskID(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	a := warmupTickTaskID("mb-1", base)
	b := warmupTickTaskID("mb-1", base.Add(500*time.Millisecond))
	c := warmupTickTaskID("mb-1", base.Add(time.Second))
	if a != "warmup:mb-1:1700000000" {
		t.Fatalf("task id = %q, want warmup:mb-1:1700000000", a)
	}
	if a != b {
		t.Fatalf("sub-second ticks must share a key: %q != %q", a, b)
	}
	if a == c {
		t.Fatalf("a later-second tick must get a distinct key: %q == %q", a, c)
	}
	if warmupTickTaskID("mb-2", base) == a {
		t.Fatalf("different mailboxes must get distinct keys")
	}
}

func TestAdvancePayloadRoundTrip(t *testing.T) {
	b, err := json.Marshal(AdvancePayload{EnrollmentID: "e1", WorkspaceID: "w1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got AdvancePayload
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.EnrollmentID != "e1" || got.WorkspaceID != "w1" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if TaskSequenceAdvance != "sequence:advance" || TaskSweepEnrollments != "sequence:sweep_stuck_enrollments" {
		t.Errorf("task name drift: %q %q", TaskSequenceAdvance, TaskSweepEnrollments)
	}
}

func TestInboxPollPayloadRoundTrip(t *testing.T) {
	b, err := json.Marshal(InboxPollPayload{MailboxID: "mb-1", WorkspaceID: "w1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got InboxPollPayload
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.MailboxID != "mb-1" || got.WorkspaceID != "w1" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if TaskInboxSweep != "inbox:sweep" || TaskInboxPoll != "inbox:poll" {
		t.Errorf("task name drift: %q %q", TaskInboxSweep, TaskInboxPoll)
	}
}

// TestQueuePriorities proves an ordered queue list maps to descending asynq
// weights (earlier = higher priority) with duplicates and empty names dropped,
// so a worker prefers its own per-IP queue over the shared default (spec §15)
// without starving it.
func TestQueuePriorities(t *testing.T) {
	got := queuePriorities([]string{"w:node-a", "default"})
	if got["w:node-a"] != 2 || got["default"] != 1 {
		t.Fatalf("weights = %v, want w:node-a=2 default=1", got)
	}

	// Duplicates and empty names are dropped, leaving one entry each; the earlier
	// queue keeps a strictly higher weight so it stays preferred.
	got = queuePriorities([]string{"w:node-a", "", "w:node-a", "default"})
	if len(got) != 2 {
		t.Fatalf("deduped map = %v, want 2 entries", got)
	}
	if got["w:node-a"] <= got["default"] || got["default"] < 1 {
		t.Fatalf("weights = %v, want w:node-a > default >= 1", got)
	}

	// An empty list yields an empty map so NewServer leaves asynq on its default.
	if m := queuePriorities(nil); len(m) != 0 {
		t.Fatalf("queuePriorities(nil) = %v, want empty", m)
	}
}
