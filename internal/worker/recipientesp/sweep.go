// Package recipientesp keeps the recipient-domain ESP cache fresh, so sender
// selection can pair a Google contact with a Google mailbox without ever
// resolving DNS on the send path.
//
// It exists BECAUSE of that constraint. resolveSender already runs three or four
// queries inside the send job; a lookup there would put a network round trip in
// front of every first send, and a slow resolver would then be a send outage. So
// the send path reads the cache only (a miss is `unknown`, which skips matching
// and falls back to the full pool) and all the DNS happens here, off the hot
// path, on a schedule.
//
// Like domainauth it reaches relational data only through coreapi — it asks for
// the stale domains and reports the answers back — so the control⇄execution seam
// holds: DNS is the only thing this package talks to directly.
package recipientesp

import (
	"context"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/esp"
)

// DefaultStaleAfter is how old a completed classification may be before the
// sweep redoes it. Long on purpose: a domain's MX records are edited by hand and
// almost never, so a shorter window would buy no accuracy and cost a DNS lookup
// per contact domain per window — and this table is sized by the contact list,
// not by the mailboxes a deployment owns.
const DefaultStaleAfter = 30 * 24 * time.Hour

// DefaultRetention is how long a row survives without being refreshed before it
// is evicted. It must be comfortably longer than DefaultStaleAfter, or a domain
// that is still being mailed would be deleted between refreshes and re-resolved
// from scratch every time; at 3x, only a domain that has gone at least two whole
// staleness windows without a single active enrollment is dropped.
//
// This is the half of the retention policy that keeps the table bounded. The
// other half is the fan-out query, which only ever CREATES rows for domains on
// an active, not-yet-pinned enrollment — so a domain that stops being mailed
// stops being refreshed and ages out here on its own.
const DefaultRetention = 90 * 24 * time.Hour

// Core is the narrow coreapi capability this sweep needs, defined here by the
// consumer so the handler is testable against a three-method fake instead of the
// whole coreapi.Client surface — the same shape as domainauth.Core and
// maintenance.Cleaner. It is deliberately NOT part of coreapi.Client: that
// interface is implemented in full by more than a dozen test fakes, and widening
// it to serve one sweep would break all of them for no gain.
type Core interface {
	ListStaleRecipientDomains(ctx context.Context, staleAfter time.Duration) ([]coreapi.RecipientDomainRef, error)
	RecordRecipientDomainESP(ctx context.Context, in coreapi.RecipientDomainESP) error
	PurgeExpiredRecipientDomains(ctx context.Context, retention time.Duration) (int64, error)
}

// SweepHandler returns an asynq handler for recipientesp:sweep tasks. Each tick
// evicts expired rows, then classifies whatever coreapi reports as stale.
//
// A lookup that does not COMPLETE (resolver timeout, opaque failure) is skipped
// rather than reported: it is not evidence about anyone's DNS, so it must
// neither overwrite a good answer nor stamp checked_at, which would hide the
// domain from the next sweep for a full staleness window. Skipping leaves it
// stale, so the next tick simply tries again.
//
// One domain's failure never aborts the pass, and nothing is lost when a tick
// ends early (shutdown, or a domain set larger than one tick's bound): an
// unreported domain stays stale and is picked up next time.
//
// Eviction runs FIRST and its failure is fatal to the tick. Everything else here
// is best-effort because a miss is harmless, but an eviction that silently stops
// working is the one failure mode this cache cannot absorb — the table is sized
// by the contact list, so unbounded growth is the actual risk being managed.
func SweepHandler(core Core, resolver esp.Resolver, staleAfter, retention time.Duration) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, _ *asynq.Task) error {
		evicted, err := core.PurgeExpiredRecipientDomains(ctx, retention)
		if err != nil {
			return err
		}
		stale, err := core.ListStaleRecipientDomains(ctx, staleAfter)
		if err != nil {
			return err
		}
		var classified, incomplete, failures int
		for _, d := range stale {
			if cancelled(ctx) {
				break
			}
			result, mxHost, ok := esp.Lookup(ctx, resolver, d.Domain)
			if !ok {
				incomplete++
				continue
			}
			if err := core.RecordRecipientDomainESP(ctx, coreapi.RecipientDomainESP{
				WorkspaceID: d.WorkspaceID,
				Domain:      d.Domain,
				ESP:         string(result),
				MXHost:      mxHost,
			}); err != nil {
				// Logged with the domain — a recipient's mail-server domain is
				// public DNS data, not tenant content — so an operator can tell a
				// persistent write failure from a resolver problem.
				slog.ErrorContext(ctx, "recipientesp_record_failed", "domain", d.Domain, "err", err)
				failures++
				continue
			}
			classified++
		}
		slog.InfoContext(ctx, "recipientesp_sweep",
			"evicted", evicted, "stale", len(stale),
			"classified", classified, "incomplete", incomplete, "record_failures", failures)
		return nil
	}
}

// cancelled reports whether the tick should stop early — a worker shutdown, or
// asynq's own task deadline. Stopping is not a failure: the domains not reached
// were never reported, so they are still stale and the next tick takes them.
func cancelled(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}
