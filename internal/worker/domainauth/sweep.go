// Package domainauth runs the periodic SPF/DKIM/DMARC check over every domain
// the deployment sends from. It reaches relational data only through coreapi —
// it asks for the stale domains and reports the answers back — so the
// control⇄execution seam holds: DNS is the only thing this package talks to
// directly.
package domainauth

import (
	"context"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/dnsauth"
)

// DefaultStaleAfter is how old a completed check may be before the sweep redoes
// it. DNS records change rarely and by hand, so a day is plenty; the on-demand
// endpoint covers the one case where waiting is unacceptable (an operator who
// has just edited a record and wants to see it land).
const DefaultStaleAfter = 24 * time.Hour

// Core is the narrow coreapi capability this sweep needs. Defined here, by the
// consumer, so the handler can be tested against a two-method fake instead of
// the whole coreapi.Client surface — the same shape as maintenance.Cleaner.
// coreapi.Client satisfies it, which is what the composition root passes.
type Core interface {
	ListStaleSendingDomains(ctx context.Context, staleAfter time.Duration) ([]coreapi.SendingDomainRef, error)
	RecordSendingDomainAuth(ctx context.Context, in coreapi.SendingDomainAuth) error
}

// SweepHandler returns an asynq handler for domainauth:sweep tasks. It asks
// coreapi which derived domains are stale, performs the lookups, and reports
// each COMPLETED check back through coreapi.
//
// A domain whose check comes back `unknown` is skipped rather than reported: a
// resolver timeout is not evidence about anyone's DNS, so it must neither
// overwrite a known-good verdict nor stamp checked_at (which would hide the
// domain from this sweep for a full staleness window). Skipping leaves it stale,
// so the next tick simply tries again.
//
// One domain's failure never aborts the pass — the rest of the deployment's
// domains are independent — and nothing is lost when the tick ends early
// (shutdown, or a very large domain set): an unreported domain stays stale and
// is picked up next time.
func SweepHandler(core Core, resolver dnsauth.Resolver, staleAfter time.Duration) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, _ *asynq.Task) error {
		stale, err := core.ListStaleSendingDomains(ctx, staleAfter)
		if err != nil {
			return err
		}
		var checked, unknown, failures int
		for _, d := range stale {
			if cancelled(ctx) {
				break
			}
			res := dnsauth.Check(ctx, resolver, d.Domain, nil)
			if res.State() == dnsauth.StateUnknown {
				unknown++
				continue
			}
			if err := core.RecordSendingDomainAuth(ctx, coreapi.SendingDomainAuth{
				WorkspaceID:  d.WorkspaceID,
				Domain:       res.Domain,
				State:        string(res.State()),
				SPFFound:     res.SPF.Found,
				SPFRecord:    res.SPF.Value,
				DKIMFound:    res.DKIM.Found,
				DKIMSelector: res.DKIM.Selector,
				DMARCFound:   res.DMARC.Found,
				DMARCPolicy:  res.DMARC.Policy,
			}); err != nil {
				// Logged with the domain (public DNS data, not tenant content)
				// so an operator can tell a persistent write failure from a
				// resolver problem.
				slog.ErrorContext(ctx, "domainauth_record_failed", "domain", res.Domain, "err", err)
				failures++
				continue
			}
			checked++
		}
		slog.InfoContext(ctx, "domainauth_sweep",
			"stale", len(stale), "checked", checked, "unknown", unknown, "record_failures", failures)
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
