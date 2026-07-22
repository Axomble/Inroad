package mail

import (
	"testing"
	"time"
)

// SSRF guard test — mirrors TestIMAPRejectsPrivateWhenDisallowed: needs no
// network since the literal IP resolves without DNS and vetAddr rejects it
// before any dial is attempted.

func TestInboxReaderRejectsPrivateWhenDisallowed(t *testing.T) {
	reader := &NetInboxReader{Timeout: time.Second} // AllowPrivate false
	_, _, err := reader.Fetch(IMAPConfig{Host: "10.0.0.5", Port: 993}, 0, 50)
	if err != ErrHostNotPermitted {
		t.Fatalf("expected ErrHostNotPermitted for private IP, got %v", err)
	}
}

func TestInboxReaderRejectsNonPositiveMaxN(t *testing.T) {
	reader := &NetInboxReader{Timeout: time.Second}
	if _, _, err := reader.Fetch(IMAPConfig{Host: "mail.example.com", Port: 993}, 0, 0); err == nil {
		t.Fatal("expected error for maxN == 0, got nil")
	}
	if _, _, err := reader.Fetch(IMAPConfig{Host: "mail.example.com", Port: 993}, 0, -1); err == nil {
		t.Fatal("expected error for maxN < 0, got nil")
	}
}

// uidRangeSeqSet is the protocol-level bound behind Fetch's IMAP request: the
// range must have an explicit upper bound (never "*"), or an unbounded
// backlog above sinceUID would be pulled over the wire in one shot.

func TestUidRangeSeqSetBoundsUpperLimit(t *testing.T) {
	got := uidRangeSeqSet(5, 200).String()
	if want := "6:205"; got != want {
		t.Fatalf("expected seqset %q, got %q", want, got)
	}
}

func TestUidRangeSeqSetFirstPoll(t *testing.T) {
	got := uidRangeSeqSet(0, 50).String()
	if want := "1:50"; got != want {
		t.Fatalf("expected seqset %q, got %q", want, got)
	}
}

// capToLowestUIDs is the pure sort/truncate helper behind Fetch's cap. It's
// tested directly (without a server) because the correctness requirement —
// the cap must return the LOWEST UIDs, never skip mail — is easy to get
// backwards with a naive slice truncation on unsorted input.

func TestCapToLowestUIDsSortsAscendingAndTruncates(t *testing.T) {
	msgs := []InboundMessage{{UID: 30}, {UID: 10}, {UID: 20}, {UID: 5}}
	got := capToLowestUIDs(msgs, 2)
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
	if got[0].UID != 5 || got[1].UID != 10 {
		t.Fatalf("expected lowest UIDs [5 10], got [%d %d]", got[0].UID, got[1].UID)
	}
}

func TestCapToLowestUIDsNoTruncationWhenUnderCap(t *testing.T) {
	msgs := []InboundMessage{{UID: 3}, {UID: 1}, {UID: 2}}
	got := capToLowestUIDs(msgs, 10)
	if len(got) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(got))
	}
	if got[0].UID != 1 || got[1].UID != 2 || got[2].UID != 3 {
		t.Fatalf("expected ascending [1 2 3], got %v", got)
	}
}

func TestCapToLowestUIDsZeroCapMeansUnbounded(t *testing.T) {
	msgs := []InboundMessage{{UID: 3}, {UID: 1}, {UID: 2}}
	got := capToLowestUIDs(msgs, 0)
	if len(got) != 3 {
		t.Fatalf("expected 3 messages (n<=0 leaves msgs untouched beyond sort), got %d", len(got))
	}
}
