package inprocess

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/esp"
)

// recipientDomainSweepLimit bounds how many domains one sweep tick claims. The
// fan-out is already narrowed to actively-enrolled, not-yet-pinned contacts, but
// a single large import can still put tens of thousands of domains in that set
// at once — and each one costs a DNS round trip. Whatever is left over stays
// stale and the next tick takes it, so the only cost of the bound is latency on
// a cache that is an optimisation to begin with.
const recipientDomainSweepLimit = 500

// ListStaleRecipientDomains returns the recipient domains due a (re)lookup:
// never classified, or last completed longer ago than staleAfter.
//
// The cutoff is computed here, in the control plane, rather than taken from the
// worker — same reason as ListStaleSendingDomains: a worker with a skewed clock
// could otherwise decide every domain is fresh (none ever rechecked) or that all
// of them are stale (a DNS flood every tick).
func (c client) ListStaleRecipientDomains(ctx context.Context, staleAfter time.Duration) ([]coreapi.RecipientDomainRef, error) {
	rows, err := c.q.ListStaleRecipientDomains(ctx, gen.ListStaleRecipientDomainsParams{
		Cutoff:   pgtype.Timestamptz{Time: time.Now().Add(-staleAfter), Valid: true},
		RowLimit: recipientDomainSweepLimit,
	})
	if err != nil {
		return nil, fmt.Errorf("list stale recipient domains: %w", err)
	}
	out := make([]coreapi.RecipientDomainRef, len(rows))
	for i, r := range rows {
		out[i] = coreapi.RecipientDomainRef{WorkspaceID: r.WorkspaceID.String(), Domain: r.Domain}
	}
	return out, nil
}

// RecordRecipientDomainESP persists one COMPLETED classification.
//
// An "unknown" state is dropped rather than written, for the same reason
// RecordSendingDomainAuth drops one: it means the lookup did not complete, and
// the write stamps checked_at — which would both overwrite a good answer and
// hide the domain from the next sweep for a full staleness window. The sweep
// handler already skips these; the guard is repeated here so the rule holds for
// any future caller.
//
// The domain is only lower-cased (and trimmed, which a value straight off the
// fan-out query never needs): the send path's point lookup keys on exactly what
// ListStaleRecipientDomains projected — lower(split_part(email,'@',2)), which is
// what esp.Domain reproduces in Go — so any further normalisation here would
// write a key no read ever seeks.
func (c client) RecordRecipientDomainESP(ctx context.Context, in coreapi.RecipientDomainESP) error {
	if !esp.Valid(in.ESP) || esp.ESP(in.ESP) == esp.Unknown {
		return nil
	}
	ws, err := uuid.Parse(in.WorkspaceID)
	if err != nil {
		return fmt.Errorf("record recipient domain esp: workspace id: %w", err)
	}
	domain := strings.ToLower(strings.TrimSpace(in.Domain))
	if domain == "" {
		return fmt.Errorf("record recipient domain esp: empty domain")
	}
	if err := c.q.UpsertRecipientDomain(ctx, gen.UpsertRecipientDomainParams{
		WorkspaceID: ws, Domain: domain, Esp: in.ESP, MxHost: in.MXHost,
	}); err != nil {
		return fmt.Errorf("record recipient domain esp: %w", err)
	}
	return nil
}

// PurgeExpiredRecipientDomains drops rows nothing has mailed since the cutoff —
// the eviction half of this cache's retention policy, and the reason it can be
// sized by the contact list rather than bounded by it.
//
// Safe because only actively-enrolled domains are ever refreshed: a domain that
// stops being mailed stops being touched and ages out here. Deleting a row that
// IS still wanted costs one DNS lookup on the next sweep and nothing else — a
// miss reads as unknown, which skips matching and falls back to the full pool.
//
// The cutoff is computed control-plane side for the same clock-skew reason as
// the staleness cutoff above.
func (c client) PurgeExpiredRecipientDomains(ctx context.Context, retention time.Duration) (int64, error) {
	n, err := c.q.DeleteExpiredRecipientDomains(ctx,
		pgtype.Timestamptz{Time: time.Now().Add(-retention), Valid: true})
	if err != nil {
		return 0, fmt.Errorf("purge expired recipient domains: %w", err)
	}
	return n, nil
}
