package sendingdomain

import (
	"context"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/dnsauth"
)

// Service holds the business rules: which domains a workspace has, and what an
// on-demand check is allowed to do. The resolver is injected so tests never
// touch real DNS.
type Service struct {
	store    Store
	resolver dnsauth.Resolver
}

func NewService(store Store, resolver dnsauth.Resolver) *Service {
	return &Service{store: store, resolver: resolver}
}

// List returns every domain the workspace sends from with its last known state.
func (s *Service) List(ctx context.Context, workspaceID uuid.UUID) ([]Domain, error) {
	return s.store.List(ctx, workspaceID)
}

// Check re-runs the DNS lookups for one domain now, so an operator who has just
// fixed a record does not wait for the daily sweep.
//
// The workspace's own mailboxes are consulted FIRST: a domain this workspace
// does not send from is ErrNotFound and NO lookup happens, so the endpoint
// cannot be used to resolve arbitrary names through our resolver.
//
// A result of `unknown` (an authoritative lookup failed for a non-NXDOMAIN
// reason) is NOT persisted. Two consequences, both deliberate:
//   - a previously known-good verdict is never overwritten by a resolver blip,
//   - checked_at keeps meaning "when a check last completed", so the sweep
//     retries this domain on its next tick rather than waiting out the window.
//
// The returned Domain then carries the STORED detail with State forced to
// unknown: the caller learns this attempt did not complete without being shown
// records we did not actually observe.
func (s *Service) Check(ctx context.Context, workspaceID uuid.UUID, domain string) (Domain, error) {
	domain = dnsauth.Normalize(domain)
	if domain == "" {
		return Domain{}, ErrNotFound
	}
	current, err := s.store.Get(ctx, workspaceID, domain)
	if err != nil {
		return Domain{}, err
	}

	res := dnsauth.Check(ctx, s.resolver, domain, nil)
	if res.State() == dnsauth.StateUnknown {
		current.State = dnsauth.StateUnknown
		return current, nil
	}

	checkedAt, err := s.store.Record(ctx, workspaceID, res)
	if err != nil {
		return Domain{}, err
	}
	return Domain{
		Domain:       res.Domain,
		MailboxCount: current.MailboxCount,
		State:        res.State(),
		SPFFound:     res.SPF.Found,
		SPFRecord:    res.SPF.Value,
		DKIMFound:    res.DKIM.Found,
		DKIMSelector: res.DKIM.Selector,
		DMARCFound:   res.DMARC.Found,
		DMARCPolicy:  res.DMARC.Policy,
		CheckedAt:    &checkedAt,
	}, nil
}
