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
