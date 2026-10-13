//go:build integration

package inbox

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/replyclassify"
)

// The end-to-end proof, over the REAL inprocess coreapi and a real Postgres:
// an unreachable server backs the mailbox off the poll fan-out, the mailbox
// stays sendable throughout, and it comes back on its own.
//
// The unit tests above prove the classification and the ladder; this proves the
// three pieces are actually wired to each other — the handler to the coreapi
// capability, the capability to the columns, the columns to the fan-out query.
// Each of those seams is a type assertion or a generated query, and the unit
// tests are blind to all three.
func TestAnUnreachableServerBacksOffWithoutDeactivatingTheMailbox(t *testing.T) {
	ctx := context.Background()
	pool, q, closeFn := connect(t)
	defer closeFn()
	fx := seedActiveEnrollment(t, ctx, pool, q, newSealer(t), "<backoff-step1@acme.test>")

	fannedOut := func() bool {
		t.Helper()
		rows, err := fx.core.ListActiveMailboxes(ctx)
		if err != nil {
			t.Fatalf("ListActiveMailboxes: %v", err)
		}
		for _, r := range rows {
			if r.ID == fx.mailboxID.String() {
				return true
			}
		}
		return false
	}
	poll := func(reader *fakeReader) error {
		t.Helper()
		return PollHandler(fx.core, reader, nil, nil, replyclassify.New(nil), nil, noopEngageEnqueuer{})(
			ctx, pollTaskFor(t, fx.mailboxID.String(), fx.ws.String()))
	}

	if !fannedOut() {
		t.Fatal("a healthy mailbox is not in the poll fan-out")
	}

	// The server goes away. Three sweeps' worth of polls; the ladder widens under
	// each one.
	unreachable := &fakeReader{stateErr: fmt.Errorf("imap dial: %w", syscall.ECONNREFUSED)}
	var previous int
	for attempt := 1; attempt <= 3; attempt++ {
		if err := poll(unreachable); !errors.Is(err, syscall.ECONNREFUSED) {
			t.Fatalf("attempt %d: poll error = %v, want the dial failure", attempt, err)
		}
		failures, retryAfter := mailboxBackoff(t, ctx, pool, fx.mailboxID)
		if failures != attempt {
			t.Fatalf("attempt %d recorded failure count %d", attempt, failures)
		}
		if retryAfter <= previous {
			t.Errorf("attempt %d scheduled the retry %ds out, no further than attempt %d's %ds",
				attempt, retryAfter, attempt-1, previous)
		}
		previous = retryAfter
		if fannedOut() {
			t.Errorf("attempt %d left the mailbox in the fan-out; the hammering is unchanged", attempt)
		}
	}

	// THE ASSERTION THIS TASK EXISTS FOR. The mailbox is suppressed from POLLING
	// and from nothing else — it is still 'active', so it still SENDS.
	if got := mailboxStatus(t, ctx, pool, fx.mailboxID); got != "active" {
		t.Fatalf("status = %q after three failed polls; the backoff became a deactivation", got)
	}
	if ok, err := q.MailboxExists(ctx, fx.mailboxID); err != nil || !ok {
		t.Fatalf("MailboxExists = (%v, %v); a backed-off mailbox must still send", ok, err)
	}

	// And it is polled again once due, with no operator action — the cap is what
	// guarantees this happens at all.
	if _, err := pool.Exec(ctx,
		`UPDATE mailboxes SET inbox_poll_retry_after = now() - interval '1 second' WHERE id = $1`,
		fx.mailboxID); err != nil {
		t.Fatalf("expire the backoff: %v", err)
	}
	if !fannedOut() {
		t.Fatal("a mailbox past its retry time is still suppressed")
	}

	// The server comes back. One successful poll clears everything, in the same
	// statement that advances the cursor.
	if err := poll(&fakeReader{uidValidity: 7, uidNext: 30}); err != nil {
		t.Fatalf("recovery poll: %v", err)
	}
	if failures, retryAfter := mailboxBackoff(t, ctx, pool, fx.mailboxID); failures != 0 || retryAfter != 0 {
		t.Errorf("after a successful poll the backoff is (failures=%d, retry_after=+%ds), want cleared", failures, retryAfter)
	}
	if !fannedOut() {
		t.Error("a recovered mailbox is still suppressed")
	}
}

// A refused credential is a different signal: it goes straight to the cap on the
// first failure, because retrying it is another rejected sign-in — and it still
// must not touch the mailbox's status.
func TestARejectedCredentialCapsImmediatelyAndStillSends(t *testing.T) {
	ctx := context.Background()
	pool, q, closeFn := connect(t)
	defer closeFn()
	fx := seedActiveEnrollment(t, ctx, pool, q, newSealer(t), "<backoff-auth-step1@acme.test>")

	reader := &fakeReader{stateErr: fmt.Errorf("%w: imap login: bad credentials", mail.ErrAuthRejected)}
	if err := PollHandler(fx.core, reader, nil, nil, replyclassify.New(nil), nil, noopEngageEnqueuer{})(
		ctx, pollTaskFor(t, fx.mailboxID.String(), fx.ws.String())); err == nil {
		t.Fatal("a rejected credential did not fail the poll")
	}

	failures, retryAfter := mailboxBackoff(t, ctx, pool, fx.mailboxID)
	if failures != 1 {
		t.Fatalf("failure count = %d, want 1", failures)
	}
	// The cap, on the FIRST failure — not the 3-minute first rung.
	if want := int(DefaultPollBackoff.Permanent.Seconds()); retryAfter < want-60 || retryAfter > want+60 {
		t.Errorf("retry scheduled +%ds out, want ~%ds (the cap, immediately)", retryAfter, want)
	}
	if got := mailboxStatus(t, ctx, pool, fx.mailboxID); got != "active" {
		t.Errorf("status = %q; a wrong password must not deactivate a mailbox", got)
	}
	if ok, err := q.MailboxExists(ctx, fx.mailboxID); err != nil || !ok {
		t.Errorf("MailboxExists = (%v, %v); a mailbox with a stale IMAP password still SENDS over SMTP", ok, err)
	}

	// The operator's reset: pause→resume clears the counter, so nobody has to
	// wait out the hour after fixing the password.
	for _, status := range []string{"paused", "active"} {
		if _, err := q.UpdateMailboxStatus(ctx, gen.UpdateMailboxStatusParams{
			ID: fx.mailboxID, WorkspaceID: fx.ws, Status: status,
		}); err != nil {
			t.Fatalf("%s: %v", status, err)
		}
	}
	if failures, retryAfter := mailboxBackoff(t, ctx, pool, fx.mailboxID); failures != 0 || retryAfter != 0 {
		t.Errorf("after resume the backoff is (failures=%d, retry_after=+%ds), want cleared", failures, retryAfter)
	}
}

// mailboxBackoff reads the two columns, measuring retry_after as SECONDS FROM
// THE DATABASE'S OWN now(). Comparing against a time this process built would
// make a few ms of host↔DB clock skew decide the assertion, and this test is all
// time arithmetic. 0 means NULL (no backoff).
func mailboxBackoff(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (int, int) {
	t.Helper()
	var failures int
	var secs *float64
	if err := pool.QueryRow(ctx,
		`SELECT inbox_poll_failures, extract(epoch FROM inbox_poll_retry_after - now())
		   FROM mailboxes WHERE id = $1`, id).Scan(&failures, &secs); err != nil {
		t.Fatalf("backoff state: %v", err)
	}
	if secs == nil {
		return failures, 0
	}
	return failures, int(*secs)
}

func mailboxStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) string {
	t.Helper()
	var s string
	if err := pool.QueryRow(ctx, `SELECT status FROM mailboxes WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatalf("status: %v", err)
	}
	return s
}
