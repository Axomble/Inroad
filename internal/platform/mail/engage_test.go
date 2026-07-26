package mail

import (
	"context"
	"errors"
	"testing"
)

// TestMultiEngagerM365Unsupported proves the Graph/M365 provider is a graceful
// ErrEngageUnsupported for both actions (v1 follow-up), returned WITHOUT touching a
// sub-engager — so the warmup:engage handler logs a clean skip.
func TestMultiEngagerM365Unsupported(t *testing.T) {
	m := NewMultiEngager(nil, nil) // nil sub-engagers: the m365 branch must not touch them
	ctx := context.Background()
	tgt := EngageTarget{Provider: "m365", MessageID: "<a@b>"}

	if err := m.MarkRead(ctx, tgt); !errors.Is(err, ErrEngageUnsupported) {
		t.Fatalf("MarkRead(m365) = %v, want ErrEngageUnsupported", err)
	}
	if err := m.Rescue(ctx, tgt); !errors.Is(err, ErrEngageUnsupported) {
		t.Fatalf("Rescue(m365) = %v, want ErrEngageUnsupported", err)
	}
}

// TestMultiEngagerRoutesByProvider proves gmail routes to the Gmail engager and the
// default (smtp) routes to the IMAP engager. The Gmail leg uses a network-free seam;
// the smtp leg is proven by the vetAddr rejection it returns (it reached the IMAP
// engager and tried to dial a disallowed host).
func TestMultiEngagerRoutesByProvider(t *testing.T) {
	rec := &gmailEngageRecorder{}
	m := NewMultiEngager(NewNetEngager(false), newFakeGmailEngager(rec, []string{"m1"}))
	ctx := context.Background()

	if err := m.MarkRead(ctx, EngageTarget{Provider: "gmail", MessageID: "<a@b>"}); err != nil {
		t.Fatalf("gmail MarkRead: %v", err)
	}
	if len(rec.modifiedIDs) != 1 {
		t.Fatalf("gmail leg not exercised: %v", rec.modifiedIDs)
	}

	// smtp/default → IMAP engager. A loopback host is rejected by the SSRF guard,
	// proving the call reached the vetted IMAP dial path (not the Gmail leg).
	err := m.MarkRead(ctx, EngageTarget{Provider: "smtp", IMAPHost: "127.0.0.1", IMAPPort: 993, MessageID: "<a@b>"})
	if !errors.Is(err, ErrHostNotPermitted) {
		t.Fatalf("smtp MarkRead err = %v, want ErrHostNotPermitted (reached the SSRF-vetted IMAP path)", err)
	}
}
