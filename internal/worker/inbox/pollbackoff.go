package inbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
)

// PollBackoffCore is the narrow coreapi capability the poll backoff needs:
// record one failed poll against one mailbox and be told when it may be tried
// again. Consumer-defined here rather than added to coreapi.Client — the same
// trade PendingSweepCore, ReplyCore and maintenance.Cleaner make, and for the
// same reason (that interface carries ~40 methods and a dozen test fakes
// implement it in full).
//
// ONE METHOD, AND IT TOUCHES TWO COLUMNS. That is the interface-segregation
// point and also the safety point: this seam is structurally incapable of
// writing mailboxes.status, so a backoff cannot become a deactivation however
// the calling code evolves. Writing status='error' would stop the mailbox
// SENDING as well as polling (MailboxExists gates on 'active'), which is the
// exact failure this whole change exists to prevent.
type PollBackoffCore interface {
	RecordInboxPollFailure(ctx context.Context, mailboxID, workspaceID string, ladder []time.Duration) (coreapi.InboxPollBackoff, error)
}

// PollBackoff is the schedule a failing mailbox is re-polled on.
//
// A struct rather than two loose slices for the reason StrandedPendingWindow is
// one: they are adjacent, interchangeable to the compiler, and swapping them
// changes behaviour silently instead of failing.
type PollBackoff struct {
	// Ladder is indexed by consecutive-failure count and CLAMPED to its last
	// rung, so the last rung is the cap.
	Ladder []time.Duration
	// Permanent is the single delay applied to a failure that cannot resolve on
	// its own — a refused credential, an SSRF-blocked host. It is sent as a
	// one-rung ladder, which is how the query is told "go straight to the cap".
	Permanent time.Duration
}

// DefaultPollBackoff is the schedule the poller ships with.
//
// # The rungs
//
// 3m → 6 → 12 → 24 → 48 → 60m, capped. The first rung is exactly
// inboxSweepInterval, so ONE failed poll changes nothing at all: a blip costs
// the mailbox no latency, and only a mailbox that keeps failing widens. From
// there each rung doubles, which takes an hour-long outage from ~20 fan-outs
// (60 dial+auth attempts, ~20 dead-letter rows) down to 5.
//
// # Why the cap matters more than the rungs
//
// "A server down for an hour must not cost the user their connection" is the
// requirement, and the cap is what delivers it: a backed-off mailbox is still
// polled, just hourly, so it RECOVERS ON ITS OWN the first time the server
// answers — the successful poll's cursor write clears the counter in the same
// statement. An uncapped exponential would reach a day and then a week, and a
// mailbox nobody thought to look at would stop detecting replies for good. That
// is a deactivation with extra steps.
//
// # Permanent
//
// One hour, the same ceiling, reached on the FIRST failure rather than the
// sixth. It is not longer than the cap: an operator who fixes a password should
// not wait a day, and pause→resume clears the counter for anyone who will not
// wait the hour (UpdateMailboxStatus).
var DefaultPollBackoff = PollBackoff{
	Ladder: []time.Duration{
		3 * time.Minute, 6 * time.Minute, 12 * time.Minute,
		24 * time.Minute, 48 * time.Minute, 60 * time.Minute,
	},
	Permanent: 60 * time.Minute,
}

// pollFailureRecordTimeout bounds the failure write. It is deliberately short:
// the write is best-effort, and a control plane that is not answering promptly
// is not a reason to hold a poll slot open.
const pollFailureRecordTimeout = 5 * time.Second

// rungsFor maps a classified failure onto the ladder to apply, or nil for
// "record nothing".
//
// FOUR SIGNALS, THREE ANSWERS:
//
//   - A refused connection, a reset, a black-holed server, a failed TLS
//     handshake and a provider 429/5xx are all TRANSPORT: the thing that
//     routinely fixes itself, so it gets the widening ladder and recovers on its
//     own.
//   - A refused credential and an SSRF-blocked host go STRAIGHT TO THE CAP. They
//     cannot fix themselves — no amount of retrying changes a wrong password —
//     and an auth retry is not free the way a dial retry is: it is another
//     rejected sign-in against the account, which is what makes a provider issue
//     a challenge or lock the mailbox. This package already applies that
//     reasoning one level down, where authenticateIMAP caps itself at two
//     attempts per connection for the same reason; backing off to hourly is the
//     same argument applied to the attempts themselves.
//   - UNKNOWN takes the widening ladder too. The conservative choice is not
//     "leave it alone": leaving it alone is precisely the behaviour that produced
//     60 attempts an hour, and an unrecognised failure that recurs is
//     indistinguishable from an outage in every way that matters here.
//   - A CANCELLED poll records nothing. It is our own shutdown, not the
//     server's fault, and counting it would back off a mailbox nothing is wrong
//     with every time a worker restarts.
func (b PollBackoff) rungsFor(kind mail.ConnectFailure) []time.Duration {
	switch kind {
	case mail.ConnectFailureTransport, mail.ConnectFailureUnknown:
		return b.Ladder
	case mail.ConnectFailureAuth, mail.ConnectFailurePolicy:
		return []time.Duration{b.Permanent}
	default: // ConnectFailureNone, ConnectFailureAborted
		return nil
	}
}

// pollBackoff is the poller's failure recorder, resolved ONCE at wiring time.
//
// Resolved once, not per poll, for the reason PollHandler resolves
// WarmupEvidenceClient once: a comma-ok assertion evaluated per message means a
// core that does not implement it degrades in silence, and the whole point of
// this change is that silence is expensive. A nil core here is a poller that
// behaves exactly as it did before this existed.
type pollBackoff struct {
	core   PollBackoffCore
	policy PollBackoff
}

// note is called with the error from a provider dial or fetch. It classifies
// the failure, records it when it is the provider's fault, and returns the error
// the handler should return.
//
// The poll error is ALWAYS returned, never swallowed: asynq's retry and
// dead-letter capture are what make a genuine failure visible, and a backoff
// that also hid the failure would trade one invisible problem for another. What
// changes is the FAN-OUT rate, which is where the 60-attempts-an-hour came from.
//
// A permanent failure is additionally marked asynq.SkipRetry, so the two
// in-task retries do not turn one wrong password into three rejected sign-ins
// per rung. It is still captured as a dead letter — SkipRetry is terminal, and
// queue.IsTerminalFailure treats it as such on the first attempt.
func (b pollBackoff) note(ctx context.Context, p queue.InboxPollPayload, provider string, err error) error {
	kind := mail.ClassifyConnectFailure(err)
	ladder := b.policy.rungsFor(kind)
	if ladder == nil {
		return err
	}
	b.record(ctx, p, provider, kind, ladder)
	if kind == mail.ConnectFailureAuth || kind == mail.ConnectFailurePolicy {
		return fmt.Errorf("inbox poll: %w (%w)", err, asynq.SkipRetry)
	}
	return err
}

// record writes the failure and logs what it decided.
//
// THE ERROR TEXT IS NOT LOGGED HERE and is not sent over the seam — only the
// classification. The handler's own error return is what carries the detail to
// asynq's log and to task_dead_letters; duplicating it here would put provider
// output, and on the auth path a server's response to a sign-in attempt, into a
// second place for no gain (docs/security.md, credential handling).
func (b pollBackoff) record(ctx context.Context, p queue.InboxPollPayload, provider string, kind mail.ConnectFailure, ladder []time.Duration) {
	if b.core == nil {
		return
	}
	// The record must OUTLIVE the failure that caused it. The most expensive
	// failure this exists to stop — a server that accepts the connection and
	// then says nothing — is exactly the one that can exhaust the poll's own
	// deadline, and a record cancelled along with it would leave the mailbox on
	// its old, tighter schedule and keep the hammering going. WithoutCancel
	// keeps the trace values and drops only the cancellation; the deadline is
	// then ours, chosen, not inherited.
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pollFailureRecordTimeout)
	defer cancel()

	out, err := b.core.RecordInboxPollFailure(recordCtx, p.MailboxID, p.WorkspaceID, ladder)
	if err != nil {
		// FAIL OPEN. The mailbox keeps its old eligibility and is polled again on
		// the next sweep — today's behaviour. A recording failure must never be
		// able to widen a backoff or, worse, stop one being cleared.
		if errors.Is(err, coreapi.ErrCrossTenant) {
			// The mailbox was deleted (or never belonged to this workspace)
			// between the fan-out and now. There is nothing to back off.
			slog.Info("inbox_poll_backoff_no_mailbox", "mailbox_id", p.MailboxID, "workspace_id", p.WorkspaceID)
			return
		}
		slog.Warn("inbox_poll_backoff_not_recorded", "mailbox_id", p.MailboxID,
			"workspace_id", p.WorkspaceID, "reason", kind.String(), "error", err)
		return
	}
	slog.Warn("inbox_poll_failed", "mailbox_id", p.MailboxID, "workspace_id", p.WorkspaceID,
		"provider", provider, "reason", kind.String(),
		"consecutive_failures", out.Failures, "retry_after", out.RetryAfter)
}
