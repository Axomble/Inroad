package inprocess

import (
	"testing"

	"github.com/google/uuid"
)

// TestDeriveWarmupReplySendIDStableInReceipt proves the reply's warmup_sends id is a
// PURE function of the receipt id: the same receipt always derives the same id (so a
// post-send engage retry reclaims the same row instead of re-sending), and distinct
// receipts derive distinct ids (one receipt maps to one reply row).
func TestDeriveWarmupReplySendIDStableInReceipt(t *testing.T) {
	r1 := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	r2 := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	if got, again := deriveWarmupReplySendID(r1), deriveWarmupReplySendID(r1); got != again {
		t.Fatalf("same receipt derived different ids: %s vs %s (retry would re-send)", got, again)
	}
	if deriveWarmupReplySendID(r1) == deriveWarmupReplySendID(r2) {
		t.Fatalf("distinct receipts collided to the same reply id")
	}
}

// TestDeriveWarmupReplySendIDDistinctNamespace proves a reply id can NEVER collide with
// a normal due-send id: even fed the SAME underlying uuid bytes as a receipt id and as a
// (mailbox, day, index) tuple, the two derivations live in distinct namespaces and so
// differ — a reply can never silently no-op a tick send (or vice versa) at claim time.
func TestDeriveWarmupReplySendIDDistinctNamespace(t *testing.T) {
	id := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	if deriveWarmupReplySendID(id) == deriveWarmupSendID(id, "2026-07-27", 0) {
		t.Fatalf("reply and normal-send derivations collided (namespaces not distinct)")
	}
}
