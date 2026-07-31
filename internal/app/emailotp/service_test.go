package emailotp

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"
)

var codeInEmail = regexp.MustCompile(`\d{6}`)

// sentCode pulls the 6-digit code out of the most recently delivered email (the
// raw code is never persisted, so a test reads it the way a user would).
func sentCode(t *testing.T, s *captureSender) string {
	t.Helper()
	m, ok := s.last()
	if !ok {
		t.Fatal("no login-code email was sent")
	}
	code := codeInEmail.FindString(m.TextBody)
	if code == "" {
		t.Fatalf("no code found in email body %q", m.TextBody)
	}
	return code
}

func TestStartThenVerifySuccess(t *testing.T) {
	store := newFakeStore()
	uid := store.addUser("user@example.test")
	sender := &captureSender{}
	svc := newTestService(store, sender, time.Now())

	if err := svc.Start(context.Background(), "user@example.test"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := store.activeCodeCount(uid); got != 1 {
		t.Fatalf("active codes after start: got %d, want 1", got)
	}
	code := sentCode(t, sender)

	gotUID, err := svc.Verify(context.Background(), "user@example.test", code)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if gotUID != uid {
		t.Fatalf("Verify uid: got %s, want %s", gotUID, uid)
	}
	if got := store.activeCodeCount(uid); got != 0 {
		t.Fatalf("active codes after successful verify: got %d, want 0 (consumed)", got)
	}
}

func TestVerifyWrongCodeRejected(t *testing.T) {
	store := newFakeStore()
	store.addUser("user@example.test")
	sender := &captureSender{}
	svc := newTestService(store, sender, time.Now())
	_ = svc.Start(context.Background(), "user@example.test")
	realCode := sentCode(t, sender)

	wrong := "000000"
	if wrong == realCode {
		wrong = "111111"
	}
	if _, err := svc.Verify(context.Background(), "user@example.test", wrong); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("wrong code: got %v, want ErrInvalidCode", err)
	}
	// The real code still works (a wrong guess burned an attempt but didn't kill it).
	if _, err := svc.Verify(context.Background(), "user@example.test", realCode); err != nil {
		t.Fatalf("real code after one wrong guess: %v", err)
	}
}

func TestVerifySingleUse(t *testing.T) {
	store := newFakeStore()
	store.addUser("user@example.test")
	sender := &captureSender{}
	svc := newTestService(store, sender, time.Now())
	_ = svc.Start(context.Background(), "user@example.test")
	code := sentCode(t, sender)

	if _, err := svc.Verify(context.Background(), "user@example.test", code); err != nil {
		t.Fatalf("first verify: %v", err)
	}
	if _, err := svc.Verify(context.Background(), "user@example.test", code); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("replay of consumed code: got %v, want ErrInvalidCode", err)
	}
}

func TestVerifyExpiredCode(t *testing.T) {
	store := newFakeStore()
	store.addUser("user@example.test")
	sender := &captureSender{}
	t0 := time.Now()
	svc := newTestService(store, sender, t0)
	_ = svc.Start(context.Background(), "user@example.test")
	code := sentCode(t, sender)

	// Advance the service clock past the code TTL.
	svc.now = func() time.Time { return t0.Add(codeTTL + time.Minute) }
	if _, err := svc.Verify(context.Background(), "user@example.test", code); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("expired code: got %v, want ErrInvalidCode", err)
	}
}

// TestVerifyExpiredCodePaysCompare documents the timing-side-channel fix: the
// expired/consumed branch must pay exactly one argon2 compare before returning
// ErrInvalidCode, so it is wall-clock-indistinguishable from a real wrong-code
// compare (an early return would leak that this address had a just-expired code).
func TestVerifyExpiredCodePaysCompare(t *testing.T) {
	store := newFakeStore()
	store.addUser("user@example.test")
	sender := &captureSender{}
	t0 := time.Now()
	svc := newTestService(store, sender, t0)

	// Count compares by wrapping the (in-package) compare seam; delegate to the
	// real argon2 comparison so behavior is otherwise unchanged.
	var compares int
	baseCompare := svc.compare
	svc.compare = func(hash, presented string) bool {
		compares++
		return baseCompare(hash, presented)
	}

	_ = svc.Start(context.Background(), "user@example.test")
	code := sentCode(t, sender)

	// Advance the service clock past the code TTL so the expired branch is taken.
	svc.now = func() time.Time { return t0.Add(codeTTL + time.Minute) }
	if _, err := svc.Verify(context.Background(), "user@example.test", code); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("expired code: got %v, want ErrInvalidCode", err)
	}
	if compares != 1 {
		t.Fatalf("expired verify performed %d argon2 compares, want exactly 1 (timing-equalized)", compares)
	}
}

func TestVerifyAttemptCapExhaustion(t *testing.T) {
	store := newFakeStore()
	store.addUser("user@example.test")
	sender := &captureSender{}
	svc := newTestService(store, sender, time.Now())
	_ = svc.Start(context.Background(), "user@example.test")
	code := sentCode(t, sender)

	wrong := "000000"
	if wrong == code {
		wrong = "111111"
	}
	for i := int32(0); i < maxAttempts; i++ {
		if _, err := svc.Verify(context.Background(), "user@example.test", wrong); !errors.Is(err, ErrInvalidCode) {
			t.Fatalf("wrong guess %d: got %v, want ErrInvalidCode", i, err)
		}
	}
	// The code is now dead: even the CORRECT code no longer verifies.
	if _, err := svc.Verify(context.Background(), "user@example.test", code); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("correct code after cap exhaustion: got %v, want ErrInvalidCode", err)
	}
}

func TestStartUnknownEmailIsNoop(t *testing.T) {
	store := newFakeStore()
	sender := &captureSender{}
	svc := newTestService(store, sender, time.Now())

	if err := svc.Start(context.Background(), "ghost@example.test"); err != nil {
		t.Fatalf("Start unknown email: %v", err)
	}
	if _, ok := sender.last(); ok {
		t.Fatal("an email was sent for a non-existent account (enumeration oracle)")
	}
}

// TestStartLookupFailureIsSilentNoop covers the anti-enumeration branch at
// service.go where a non-NoRows DB error from the user lookup must be logged and
// still return nil (no error surfaced, no email dispatched) — a lookup outage must
// not become an account-existence oracle by behaving differently from an unknown
// address.
func TestStartLookupFailureIsSilentNoop(t *testing.T) {
	store := newFakeStore()
	store.addUser("user@example.test") // a real account exists…
	store.failLookup = errors.New("db down")
	sender := &captureSender{}
	svc := newTestService(store, sender, time.Now())

	if err := svc.Start(context.Background(), "user@example.test"); err != nil {
		t.Fatalf("Start on lookup failure: got %v, want nil (no error surfaced)", err)
	}
	if _, ok := sender.last(); ok {
		t.Fatal("an email was dispatched despite the lookup failing (should be a silent no-op)")
	}
}

func TestStartReplacesPriorCode(t *testing.T) {
	store := newFakeStore()
	uid := store.addUser("user@example.test")
	sender := &captureSender{}
	svc := newTestService(store, sender, time.Now())

	_ = svc.Start(context.Background(), "user@example.test")
	first := sentCode(t, sender)
	_ = svc.Start(context.Background(), "user@example.test")
	second := sentCode(t, sender)

	if got := store.activeCodeCount(uid); got != 1 {
		t.Fatalf("active codes after second start: got %d, want 1", got)
	}
	// The superseded first code no longer verifies.
	if first != second {
		if _, err := svc.Verify(context.Background(), "user@example.test", first); !errors.Is(err, ErrInvalidCode) {
			t.Fatalf("superseded code: got %v, want ErrInvalidCode", err)
		}
	}
	if _, err := svc.Verify(context.Background(), "user@example.test", second); err != nil {
		t.Fatalf("current code: %v", err)
	}
}

func TestVerifyUnknownEmailRejected(t *testing.T) {
	store := newFakeStore()
	sender := &captureSender{}
	svc := newTestService(store, sender, time.Now())
	if _, err := svc.Verify(context.Background(), "ghost@example.test", "123456"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("verify unknown email: got %v, want ErrInvalidCode", err)
	}
}
