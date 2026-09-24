package inbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"syscall"
	"testing"
	"time"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/platform/replyclassify"
)

// recordedFailure is one RecordInboxPollFailure the poller made.
type recordedFailure struct {
	mailboxID   string
	workspaceID string
	ladder      []time.Duration
	// ctxLive is whether the context the call arrived on was still usable. It is
	// captured rather than asserted here because the whole point of one test
	// below is that a poll killed by its own deadline can still record.
	ctxLive bool
}

// backoffCore is a stubCore that ALSO carries the optional backoff capability,
// which plain stubCore deliberately does not — so every pre-existing poll test
// keeps exercising the no-capability path.
type backoffCore struct {
	*stubCore
	recorded []recordedFailure
	out      coreapi.InboxPollBackoff
	err      error
}

func (b *backoffCore) RecordInboxPollFailure(ctx context.Context, mailboxID, workspaceID string, ladder []time.Duration) (coreapi.InboxPollBackoff, error) {
	b.recorded = append(b.recorded, recordedFailure{
		mailboxID: mailboxID, workspaceID: workspaceID,
		ladder: append([]time.Duration(nil), ladder...), ctxLive: ctx.Err() == nil,
	})
	return b.out, b.err
}

func newBackoffCore(job coreapi.InboxPollJob) *backoffCore {
	return &backoffCore{stubCore: &stubCore{job: job}}
}

// a mailbox that has been polled before, so the poll takes the fetch path rather
// than the first-poll baseline.
func polledBefore() coreapi.InboxPollJob {
	return coreapi.InboxPollJob{UIDValidity: 7, LastSeenUID: 10}
}

// A transport failure is the case this whole change exists for: the server is
// unreachable, and the mailbox must be scheduled further out instead of re-dialed
// every sweep.
func TestTransportFailureRecordsTheWideningLadder(t *testing.T) {
	core := newBackoffCore(polledBefore())
	reader := &fakeReader{stateErr: fmt.Errorf("imap dial: %w", syscall.ECONNREFUSED)}

	err := runPoll(t, core, reader)
	if !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("poll error = %v, want the dial failure propagated", err)
	}
	// Still retried in-task: a refused connection may well succeed on the next
	// attempt, and only the FAN-OUT rate is what was hammering.
	if errors.Is(err, asynq.SkipRetry) {
		t.Error("a transport failure was marked SkipRetry; a refused dial is worth retrying")
	}
	if len(core.recorded) != 1 {
		t.Fatalf("recorded %d failures, want 1", len(core.recorded))
	}
	got := core.recorded[0]
	if !sameLadder(got.ladder, DefaultPollBackoff.Ladder) {
		t.Errorf("ladder = %v, want the widening ladder %v", got.ladder, DefaultPollBackoff.Ladder)
	}
}

// The Fetch leg dials too, so a failure there counts the same as one at
// CurrentState.
func TestTransportFailureAtFetchAlsoRecords(t *testing.T) {
	core := newBackoffCore(polledBefore())
	reader := &fakeReader{uidValidity: 7, uidNext: 20, fetchErr: fmt.Errorf("imap fetch: %w", io.EOF)}

	if err := runPoll(t, core, reader); !errors.Is(err, io.EOF) {
		t.Fatalf("poll error = %v, want the fetch failure propagated", err)
	}
	if len(core.recorded) != 1 {
		t.Fatalf("recorded %d failures, want 1", len(core.recorded))
	}
}

// A refused credential goes STRAIGHT to the cap and is not retried in-task:
// every repeat is another rejected sign-in, which is what makes a provider lock
// an account.
func TestAuthFailureGoesStraightToTheCapAndSkipsRetry(t *testing.T) {
	core := newBackoffCore(polledBefore())
	reader := &fakeReader{stateErr: fmt.Errorf("%w: imap login: bad credentials", mail.ErrAuthRejected)}

	err := runPoll(t, core, reader)
	if !errors.Is(err, mail.ErrAuthRejected) {
		t.Fatalf("poll error = %v, want the auth failure propagated (never swallowed)", err)
	}
	if !errors.Is(err, asynq.SkipRetry) {
		t.Error("an auth failure was left retryable; two more rejected sign-ins per rung is the cost")
	}
	if len(core.recorded) != 1 {
		t.Fatalf("recorded %d failures, want 1", len(core.recorded))
	}
	ladder := core.recorded[0].ladder
	if len(ladder) != 1 || ladder[0] != DefaultPollBackoff.Permanent {
		t.Errorf("ladder = %v, want the one-rung cap [%v]", ladder, DefaultPollBackoff.Permanent)
	}
}

// Our own SSRF refusal never opened a socket and will never succeed until an
// operator edits the mailbox — same one-rung treatment, for a different reason.
func TestPolicyRefusalGoesStraightToTheCap(t *testing.T) {
	core := newBackoffCore(polledBefore())
	reader := &fakeReader{stateErr: fmt.Errorf("imap: %w", mail.ErrHostNotPermitted)}

	if err := runPoll(t, core, reader); !errors.Is(err, mail.ErrHostNotPermitted) {
		t.Fatalf("poll error = %v, want the guard's refusal propagated", err)
	}
	if len(core.recorded) != 1 || len(core.recorded[0].ladder) != 1 {
		t.Fatalf("recorded = %+v, want one failure on a one-rung ladder", core.recorded)
	}
}

// A poll that reaches the server records nothing — the cursor write is what
// clears the counter, and it must not be preceded by a failure the poll did not
// have.
func TestSuccessfulPollRecordsNoFailure(t *testing.T) {
	core := newBackoffCore(polledBefore())
	reader := &fakeReader{uidValidity: 7, uidNext: 20}

	if err := runPoll(t, core, reader); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(core.recorded) != 0 {
		t.Errorf("a successful poll recorded %d failures", len(core.recorded))
	}
	if !core.cursorSet {
		t.Error("a successful poll did not advance the cursor, which is what clears the backoff")
	}
}

// A failure that is OURS, not the provider's, must not back off polling: the
// mailbox is fine, and delaying reply detection over our own bug helps nobody.
func TestNonProviderFailureRecordsNoBackoff(t *testing.T) {
	core := newBackoffCore(polledBefore())
	core.cursorErr = errors.New("control plane unavailable")
	reader := &fakeReader{uidValidity: 7, uidNext: 20}

	if err := runPoll(t, core, reader); err == nil {
		t.Fatal("a failed cursor write did not fail the poll")
	}
	if len(core.recorded) != 0 {
		t.Errorf("a control-plane failure recorded %d poll failures against the mailbox", len(core.recorded))
	}
}

// A core without the capability polls exactly as it did before this existed: no
// panic, no swallowed error, no backoff. stubCore is that core.
func TestPollWithoutTheBackoffCapabilityIsUnchanged(t *testing.T) {
	core := &stubCore{job: polledBefore()}
	reader := &fakeReader{stateErr: fmt.Errorf("imap dial: %w", syscall.ECONNREFUSED)}

	if err := runPoll(t, core, reader); !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("poll error = %v, want the dial failure propagated", err)
	}
}

// A control plane that cannot record the failure must not change the poll's
// outcome. Failing open here means the mailbox keeps its old eligibility — the
// behaviour that shipped before the backoff — rather than a hiccup deciding a
// mailbox stops being polled.
func TestABackoffThatCannotBeRecordedStillFailsThePollAndNothingElse(t *testing.T) {
	core := newBackoffCore(polledBefore())
	core.err = errors.New("control plane unavailable")
	reader := &fakeReader{stateErr: fmt.Errorf("imap dial: %w", syscall.ECONNREFUSED)}

	if err := runPoll(t, core, reader); !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("poll error = %v, want the dial failure, not the recording failure", err)
	}
}

// A deleted mailbox (or a workspace that never owned it) surfaces as
// ErrCrossTenant from the workspace-pinned UPDATE. There is nothing to back off,
// and it must not change the poll's error either.
func TestABackoffForAMailboxThatIsGoneIsNotAnError(t *testing.T) {
	core := newBackoffCore(polledBefore())
	core.err = coreapi.ErrCrossTenant
	reader := &fakeReader{stateErr: fmt.Errorf("imap dial: %w", syscall.ECONNREFUSED)}

	if err := runPoll(t, core, reader); !errors.Is(err, syscall.ECONNREFUSED) {
		t.Fatalf("poll error = %v, want the dial failure", err)
	}
}

// The record must OUTLIVE the failure that caused it. A server that accepts the
// connection and then stalls is exactly the failure that can burn the poll's own
// deadline, and a record cancelled along with it would leave the mailbox on its
// old, tighter schedule — the hammering this exists to stop, at its most
// expensive.
func TestTheFailureRecordSurvivesTheCancelledPoll(t *testing.T) {
	core := newBackoffCore(polledBefore())
	b := pollBackoff{core: core, policy: DefaultPollBackoff}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	p := queue.InboxPollPayload{MailboxID: "m1", WorkspaceID: "ws1"}
	stalled := fmt.Errorf("imap dial: %w", context.DeadlineExceeded)
	if err := b.note(ctx, p, "smtp", stalled); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("note returned %v, want the stall propagated", err)
	}
	if len(core.recorded) != 1 {
		t.Fatalf("recorded %d failures, want 1", len(core.recorded))
	}
	if !core.recorded[0].ctxLive {
		t.Error("the failure record arrived on a cancelled context; it would never have been written")
	}
	if core.recorded[0].mailboxID != "m1" || core.recorded[0].workspaceID != "ws1" {
		t.Errorf("recorded against %+v, want (m1, ws1)", core.recorded[0])
	}
}

// The classification-to-schedule map, stated once as a table. Four signals,
// three answers.
func TestRungsForEveryClassification(t *testing.T) {
	for _, tc := range []struct {
		kind mail.ConnectFailure
		want []time.Duration
		why  string
	}{
		{mail.ConnectFailureTransport, DefaultPollBackoff.Ladder, "an unreachable server routinely fixes itself"},
		{mail.ConnectFailureUnknown, DefaultPollBackoff.Ladder, "an unrecognised failure that recurs is an outage in every way that matters"},
		{mail.ConnectFailureAuth, []time.Duration{DefaultPollBackoff.Permanent}, "a rejected sign-in cannot fix itself and is not free to repeat"},
		{mail.ConnectFailurePolicy, []time.Duration{DefaultPollBackoff.Permanent}, "our own refusal will not change without an operator"},
		{mail.ConnectFailureAborted, nil, "our shutdown is not the server's fault"},
		{mail.ConnectFailureNone, nil, "there is no failure"},
	} {
		t.Run(tc.kind.String(), func(t *testing.T) {
			if got := DefaultPollBackoff.rungsFor(tc.kind); !sameLadder(got, tc.want) {
				t.Errorf("rungsFor(%v) = %v, want %v — %s", tc.kind, got, tc.want, tc.why)
			}
		})
	}
}

// The schedule's own properties, which the SQL and the integration test both
// depend on: it widens, it is capped, and the first rung costs a healthy mailbox
// nothing.
func TestDefaultPollBackoffLadder(t *testing.T) {
	l := DefaultPollBackoff.Ladder
	if len(l) == 0 {
		t.Fatal("an empty ladder disables the backoff entirely")
	}
	// inboxSweepInterval, which is unexported in platform/queue. One failed poll
	// must therefore change nothing at all: a blip costs no latency.
	if l[0] != 3*time.Minute {
		t.Errorf("first rung = %v, want the 3m sweep interval so one blip costs nothing", l[0])
	}
	for i := 1; i < len(l); i++ {
		if l[i] <= l[i-1] {
			t.Errorf("rung %d (%v) does not widen on rung %d (%v)", i+1, l[i], i, l[i-1])
		}
	}
	ceiling := l[len(l)-1]
	if ceiling > time.Hour {
		t.Errorf("cap = %v; a server down for an hour must not cost the user their connection", ceiling)
	}
	if DefaultPollBackoff.Permanent != ceiling {
		t.Errorf("permanent rung = %v, cap = %v; a fixable password must not wait longer than an outage does",
			DefaultPollBackoff.Permanent, ceiling)
	}
	if err := coreapi.ValidateBackoffLadder(l); err != nil {
		t.Errorf("the shipped ladder does not pass the seam's own validation: %v", err)
	}
}

// The API transports reach the same machinery through a different error shape.
func TestAPIPollFailureBacksOff(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		wantCap  bool
		skipsNow bool
	}{
		{"graph 401 is the API transport's wrong password", &mail.APIError{Provider: "m365", Op: "inbox delta", Status: 401}, true, true},
		{"graph 503 is come back later", &mail.APIError{Provider: "m365", Op: "inbox delta", Status: 503}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core := &backoffCore{stubCore: &stubCore{job: coreapi.InboxPollJob{Provider: "m365"}}}
			err := PollHandler(core, nil, nil, failingGraph{tc.err}, replyclassify.New(nil), nil, noopEngageEnqueuer{})(t.Context(), pollTask(t))
			if !errors.Is(err, tc.err) {
				t.Fatalf("poll error = %v, want the provider failure propagated", err)
			}
			if errors.Is(err, asynq.SkipRetry) != tc.skipsNow {
				t.Errorf("SkipRetry = %v, want %v", errors.Is(err, asynq.SkipRetry), tc.skipsNow)
			}
			if len(core.recorded) != 1 {
				t.Fatalf("recorded %d failures, want 1", len(core.recorded))
			}
			atCap := len(core.recorded[0].ladder) == 1
			if atCap != tc.wantCap {
				t.Errorf("ladder = %v, wantCap = %v", core.recorded[0].ladder, tc.wantCap)
			}
		})
	}
}

// failingGraph is a GraphFetcher whose every fetch is the provider's answer.
type failingGraph struct{ err error }

func (f failingGraph) Fetch(context.Context, string, string, int) ([]mail.InboundMessage, string, error) {
	return nil, "", f.err
}

func sameLadder(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
