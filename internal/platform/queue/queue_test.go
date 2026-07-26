package queue

import (
	"encoding/json"
	"testing"
)

func TestWarmupTickPayloadRoundTrip(t *testing.T) {
	p := WarmupTickPayload{MailboxID: "mb-123"}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got WarmupTickPayload
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.MailboxID != "mb-123" {
		t.Errorf("MailboxID = %q, want mb-123", got.MailboxID)
	}
	if TaskWarmupTick != "warmup:tick" {
		t.Errorf("TaskWarmupTick = %q", TaskWarmupTick)
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
