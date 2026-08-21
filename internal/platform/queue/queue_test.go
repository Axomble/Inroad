package queue

import (
	"encoding/json"
	"strings"
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

// TestInboxPollTaskID proves the dedup key collapses every replica's sweep within
// one interval to a single poll, while a later interval still gets its own key.
//
// Both halves matter. Without the first, N replicas each open a real IMAP
// connection per mailbox per interval — a provider rate-limit problem for zero
// gain, since the extra polls read the same messages. Without the second, a
// mailbox that has been polled once would never be polled again.
func TestInboxPollTaskID(t *testing.T) {
	// A bucket boundary, so the truncation is exercised rather than accidentally
	// landing mid-interval.
	base := time.Unix(1_700_000_000, 0).Truncate(inboxSweepInterval)
	a := inboxPollTaskID("mb-1", base)

	// Every instant inside the interval — start, middle, and the last moment before
	// the next bucket — shares one key.
	for _, offset := range []time.Duration{0, inboxSweepInterval / 2, inboxSweepInterval - time.Nanosecond} {
		if got := inboxPollTaskID("mb-1", base.Add(offset)); got != a {
			t.Fatalf("offset %s must share the bucket key: %q != %q", offset, got, a)
		}
	}
	// The next interval is a genuinely new poll, so the dedup window is bounded.
	if got := inboxPollTaskID("mb-1", base.Add(inboxSweepInterval)); got == a {
		t.Fatalf("the next interval must get a distinct key: %q == %q", got, a)
	}
	// Different mailboxes never suppress each other.
	if inboxPollTaskID("mb-2", base) == a {
		t.Fatal("different mailboxes must get distinct keys")
	}
	// The key is namespaced, so it cannot collide with warmup/testsend/reply keys
	// in asynq's shared task-id space.
	if !strings.HasPrefix(a, "inbox-poll:mb-1:") {
		t.Fatalf("task id = %q, want an inbox-poll:mb-1: prefix", a)
	}
}

func TestTestSendPayloadRoundTrip(t *testing.T) {
	p := TestSendPayload{CampaignID: "c1", StepID: "s1", MailboxID: "m1", To: "preview@example.com", WorkspaceID: "w1"}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got TestSendPayload
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != p {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, p)
	}
	if TaskTestSend != "testsend:send" {
		t.Errorf("task name drift: %q", TaskTestSend)
	}
}

func TestInboxReplySendPayloadRoundTrip(t *testing.T) {
	p := InboxReplySendPayload{ThreadID: "t1", BodyText: "thanks!", WorkspaceID: "w1", TaskID: "inboxreply:t1:1700000000"}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got InboxReplySendPayload
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != p {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, p)
	}
	if TaskInboxReplySend != "inbox:reply_send" {
		t.Errorf("task name drift: %q", TaskInboxReplySend)
	}
}

// TestTestSendTaskID proves the dedup key collapses two enqueues for the SAME
// (campaign, step, mailbox) within the same second (a double-submitted form),
// while a later second yields a distinct key (a genuinely new test-send); a
// different mailbox always yields a distinct key.
func TestTestSendTaskID(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	a := testSendTaskID("c1", "s1", "m1", base)
	if a != "testsend:c1:s1:m1:1700000000" {
		t.Fatalf("task id = %q, want testsend:c1:s1:m1:1700000000", a)
	}
	if got := testSendTaskID("c1", "s1", "m1", base.Add(500*time.Millisecond)); got != a {
		t.Fatalf("sub-second re-submits must share a key: %q != %q", got, a)
	}
	if got := testSendTaskID("c1", "s1", "m1", base.Add(time.Second)); got == a {
		t.Fatalf("a later-second test-send must get a distinct key: %q == %q", got, a)
	}
	if testSendTaskID("c1", "s1", "m2", base) == a {
		t.Fatal("different mailboxes must get distinct keys")
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
