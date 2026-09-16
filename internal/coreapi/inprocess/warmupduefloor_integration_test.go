//go:build integration

package inprocess

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

)

// The spacing floor, end to end against real Postgres.
//
// warmup.NextDue's floor is table-tested in isolation; what this file pins is the
// WIRING, which is where the bug actually lived. NextWarmupDue computed a plan
// from a participant row and today's count and never asked when the mailbox last
// sent, so the ramp's inter-send gap existed only as the delay on the chained
// tick — and warmup:sweep, which fans a tick out to every enabled participant
// every five minutes at `now`, pre-empted it. The visible symptom was a worker
// restart emptying the day's whole quota back-to-back.
//
// A unit test cannot catch a regression here: reverting the query, the
// GetLastWarmupSentAt call, or the `lastSent.Valid` mapping leaves every pure
// policy test passing.

// markSent writes a 'sent' warmup_sends row for (from -> to) at t, which is what
// GetLastWarmupSentAt reads. Written through raw SQL: MarkWarmupSent stamps
// now(), and this needs to place a send at a CHOSEN instant.
func markSent(t *testing.T, ctx context.Context, f warmupFixture, from, to uuid.UUID, at time.Time) {
	t.Helper()
	if _, err := f.raw.Exec(ctx,
		`INSERT INTO warmup_sends (id, workspace_id, thread_id, from_mailbox, to_mailbox, status, token, sent_at)
		 VALUES ($1, $2, $3, $4, $5, 'sent', $6, $7)`,
		uuid.New(), f.ws1, newFloorThread(t, ctx, f, from, to), from, to, "tok-"+uuid.NewString(), at,
	); err != nil {
		t.Fatalf("insert sent warmup send: %v", err)
	}
}

// newFloorThread creates the warmup_threads row a send must reference.
func newFloorThread(t *testing.T, ctx context.Context, f warmupFixture, from, to uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := f.raw.Exec(ctx,
		`INSERT INTO warmup_threads (id, workspace_id, sender_mailbox, partner_mailbox, subject, content_key)
		 VALUES ($1, $2, $3, $4, 'floor fixture', $5)`,
		id, f.ws1, from, to, "key-"+uuid.NewString(),
	); err != nil {
		t.Fatalf("insert warmup thread: %v", err)
	}
	return id
}

func TestNextWarmupDueHonoursTheSpacingFloor(t *testing.T) {
	ctx, f := setupWarmup(t)

	// Nothing sent yet: the first warmup mail must not be held back.
	_, sendNow, err := f.core.NextWarmupDue(ctx, f.a.String(), f.ws1.String())
	if err != nil {
		t.Fatalf("NextWarmupDue (never sent): %v", err)
	}
	if !sendNow {
		t.Fatal("a mailbox that has never sent must be allowed its first warmup send")
	}

	// A send one minute ago is well inside any plausible floor (the gap is the
	// waking window divided by a daily target in the single digits, so hours).
	now := warmupNow(t, f)
	markSent(t, ctx, f, f.a, f.b, now.Add(-time.Minute))

	due, sendNow, err := f.core.NextWarmupDue(ctx, f.a.String(), f.ws1.String())
	if err != nil {
		t.Fatalf("NextWarmupDue (just sent): %v", err)
	}
	if sendNow {
		t.Fatal("a mailbox that sent one minute ago must not send again now — this is the bug a restart exposed")
	}
	if !due.After(now) {
		t.Fatalf("NextDue = %s must be in the future so the chain resumes on its own (now %s)", due, now)
	}

	// A send long enough ago clears the floor. A day is unambiguously past any
	// spacing while staying inside the same pinned day for the quota gate.
	markSent(t, ctx, f, f.a, f.b, now.Add(-12*time.Hour))
	if _, sendNow, err = f.core.NextWarmupDue(ctx, f.a.String(), f.ws1.String()); err != nil {
		t.Fatalf("NextWarmupDue (floor elapsed): %v", err)
	}
	// Still false: the most recent send is what counts, and that is the one a
	// minute ago. MAX(sent_at) must not be confused with "any old send".
	if sendNow {
		t.Fatal("the floor must measure from the MOST RECENT send, not the oldest")
	}
}

// warmupNow reads the clock the fixture pinned, so the test reasons about the
// same instant the coreapi client does.
func warmupNow(t *testing.T, f warmupFixture) time.Time {
	t.Helper()
	impl, ok := f.core.(client)
	if !ok {
		t.Fatalf("fixture core is %T, want inprocess.client", f.core)
	}
	return impl.now().UTC()
}
