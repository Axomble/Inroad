package inprocess

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/dnsauth"
)

// ListStaleSendingDomains scans the derived domain set for anything whose last
// completed check predates now-staleAfter. The cutoff is computed here, in the
// control plane, rather than taken from the worker: a worker with a skewed clock
// could otherwise decide every domain is fresh (and none ever gets rechecked) or
// that all of them are stale.
func (c client) ListStaleSendingDomains(ctx context.Context, staleAfter time.Duration) ([]coreapi.SendingDomainRef, error) {
	cutoff := pgtype.Timestamptz{Time: time.Now().Add(-staleAfter), Valid: true}
	rows, err := c.q.ListStaleSendingDomains(ctx, cutoff)
	if err != nil {
		return nil, fmt.Errorf("list stale sending domains: %w", err)
	}
	out := make([]coreapi.SendingDomainRef, len(rows))
	for i, r := range rows {
		out[i] = coreapi.SendingDomainRef{WorkspaceID: r.WorkspaceID.String(), Domain: r.Domain}
	}
	return out, nil
}

// RecordSendingDomainAuth persists one completed check.
//
// An "unknown" state is dropped rather than written: it means a lookup failed
// for a non-NXDOMAIN reason, and persisting it would both overwrite a
// known-good verdict and stamp checked_at, hiding the domain from the next
// sweep for a full staleness window. The sweep handler already skips these; the
// guard is repeated here so the rule holds for any future caller.
func (c client) RecordSendingDomainAuth(ctx context.Context, in coreapi.SendingDomainAuth) error {
	if dnsauth.State(in.State) == dnsauth.StateUnknown {
		return nil
	}
	ws, err := uuid.Parse(in.WorkspaceID)
	if err != nil {
		return fmt.Errorf("record sending domain auth: workspace id: %w", err)
	}
	domain := dnsauth.Normalize(in.Domain)
	if domain == "" {
		return fmt.Errorf("record sending domain auth: empty domain")
	}
	if _, err := c.q.UpsertSendingDomain(ctx, gen.UpsertSendingDomainParams{
		WorkspaceID:  ws,
		Domain:       domain,
		State:        in.State,
		SpfFound:     in.SPFFound,
		SpfRecord:    in.SPFRecord,
		DkimFound:    in.DKIMFound,
		DkimSelector: in.DKIMSelector,
		DmarcFound:   in.DMARCFound,
		DmarcPolicy:  in.DMARCPolicy,
	}); err != nil {
		return fmt.Errorf("record sending domain auth: %w", err)
	}
	return nil
}
