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
