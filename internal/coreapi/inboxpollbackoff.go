package coreapi

import (
	"errors"
	"fmt"
	"time"
)

// The inbox poll backoff's vocabulary: what the execution plane tells the
// control plane when a mailbox's server would not answer, and what it gets back.
//
// It is NOT a coreapi.Client method. Like PendingSweepCore and ReplyCore, the
// capability is consumed through a narrow interface defined by its consumer
// (internal/worker/inbox.PollBackoffCore) and satisfied by both clients via type
// assertion at registration. Client already carries ~40 methods that a dozen
// test fakes implement in full, and every method on it is one a remote worker
// must be able to call — a bar this clears, which is why both implementations
// carry it, but not a reason to widen the seam itself.

// ErrInvalidBackoffLadder rejects a schedule that could not produce a bounded
// retry time. It is a programming error rather than a runtime condition, but it
// crosses a process boundary on the remote transport, so it is validated there
// too: an empty ladder would leave inbox_poll_retry_after NULL and quietly
// disable the backoff, and a negative rung would schedule a retry in the past.
var ErrInvalidBackoffLadder = errors.New("coreapi: invalid inbox poll backoff ladder")

// maxBackoffRungs bounds a ladder arriving over the wire. Nothing legitimate
// needs more than a handful of rungs, and an unbounded array from a compromised
// worker would be an unbounded array parameter in a SQL statement.
const maxBackoffRungs = 32

// InboxPollBackoff is the control plane's answer after recording one failed
// poll: how many consecutive failures this mailbox has now had, and the earliest
// time the poll fan-out will consider it again.
//
// RetryAfter is the DATABASE's clock, not the worker's — it is compared against
// the database clock in ListActiveMailboxes, and a worker's few-ms skew must not
// be what decides whether a mailbox is due. It is returned for the LOG LINE and
// for tests; nothing branches on it.
//
// snake_case json tags for the reason the package doc gives: the remote
// transport encodes THIS type rather than a parallel wire struct.
type InboxPollBackoff struct {
	Failures   int       `json:"failures"`
	RetryAfter time.Time `json:"retry_after"`
}

// ValidateBackoffLadder checks a schedule before it reaches SQL.
//
// The ladder is indexed by consecutive-failure count and CLAMPED to its last
// rung, so the last rung is the cap — which is the whole reason a server down
// for an hour does not cost the user their connection. A one-rung ladder is
// therefore how a caller says "go straight to the cap"; that is a legitimate
// schedule, not a degenerate one.
func ValidateBackoffLadder(ladder []time.Duration) error {
	if len(ladder) == 0 {
		return fmt.Errorf("%w: empty", ErrInvalidBackoffLadder)
	}
	if len(ladder) > maxBackoffRungs {
		return fmt.Errorf("%w: %d rungs exceeds the %d maximum", ErrInvalidBackoffLadder, len(ladder), maxBackoffRungs)
	}
	for i, d := range ladder {
		if d <= 0 {
			return fmt.Errorf("%w: rung %d is %v", ErrInvalidBackoffLadder, i+1, d)
		}
	}
	return nil
}
