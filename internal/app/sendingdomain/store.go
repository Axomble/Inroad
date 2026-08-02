// Package sendingdomain reports SPF/DKIM/DMARC authentication status for the
// domains a workspace sends from. It is informational: nothing here blocks a
// send, and nothing on the send path reads it.
//
// The domain list is DERIVED from mailboxes.email — the set Inroad cares about
// is exactly the set it sends from — so a domain exists as soon as a mailbox on
// it is connected, whether or not it has ever been checked.
package sendingdomain

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/dnsauth"
)

// ErrNotFound means this workspace has no mailbox on the requested domain. It
// is the 404 the check endpoint returns BEFORE any DNS lookup, so the endpoint
// cannot be used to resolve arbitrary names through our resolver.
var ErrNotFound = errors.New("sending domain not found")

// Domain is one sending domain and its last known authentication status.
// CheckedAt is nil until a check COMPLETES — a check that ended `unknown` is not
// a completed check.
type Domain struct {
	Domain       string
	MailboxCount int64
	State        dnsauth.State
	SPFFound     bool
	SPFRecord    string
	DKIMFound    bool
	DKIMSelector string
	DMARCFound   bool
	DMARCPolicy  string
	CheckedAt    *time.Time
}

// Store is the repository interface this domain depends on, defined here by the
// consumer so the service is unit-testable against a fake without a database.
// Every method takes the workspace explicitly; it comes from the JWT at the
// handler, never from a request body or path parameter.
type Store interface {
	// List returns one row per domain the workspace's mailboxes send from,
	// including never-checked domains (state unknown, nil CheckedAt).
	List(ctx context.Context, workspaceID uuid.UUID) ([]Domain, error)
	// Get returns one derived domain, or ErrNotFound when the workspace has no
	// mailbox on it.
	Get(ctx context.Context, workspaceID uuid.UUID, domain string) (Domain, error)
	// Record persists a COMPLETED check and returns the stored checked_at.
	// Callers must not pass an `unknown` result — see Service.Check.
	Record(ctx context.Context, workspaceID uuid.UUID, res dnsauth.Result) (time.Time, error)
}

// PgStore implements Store over the sqlc-generated queries. It is the only place
// in this domain that knows about gen.Queries.
type PgStore struct {
	q *gen.Queries
}

func NewPgStore(q *gen.Queries) *PgStore { return &PgStore{q: q} }

func (s *PgStore) List(ctx context.Context, workspaceID uuid.UUID) ([]Domain, error) {
	rows, err := s.q.ListSendingDomains(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]Domain, len(rows))
	for i, r := range rows {
		out[i] = Domain{
			Domain:       r.Domain,
			MailboxCount: r.MailboxCount,
			State:        dnsauth.State(r.State),
			SPFFound:     r.SpfFound,
			SPFRecord:    r.SpfRecord,
			DKIMFound:    r.DkimFound,
			DKIMSelector: r.DkimSelector,
			DMARCFound:   r.DmarcFound,
			DMARCPolicy:  r.DmarcPolicy,
			CheckedAt:    checkedAt(r.CheckedAt),
		}
	}
	return out, nil
}

func (s *PgStore) Get(ctx context.Context, workspaceID uuid.UUID, domain string) (Domain, error) {
	r, err := s.q.GetSendingDomain(ctx, gen.GetSendingDomainParams{WorkspaceID: workspaceID, Domain: domain})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Domain{}, ErrNotFound
		}
		return Domain{}, err
	}
	return Domain{
		Domain:       r.Domain,
		MailboxCount: r.MailboxCount,
		State:        dnsauth.State(r.State),
		SPFFound:     r.SpfFound,
		SPFRecord:    r.SpfRecord,
		DKIMFound:    r.DkimFound,
		DKIMSelector: r.DkimSelector,
		DMARCFound:   r.DmarcFound,
		DMARCPolicy:  r.DmarcPolicy,
		CheckedAt:    checkedAt(r.CheckedAt),
	}, nil
}

func (s *PgStore) Record(ctx context.Context, workspaceID uuid.UUID, res dnsauth.Result) (time.Time, error) {
	ts, err := s.q.UpsertSendingDomain(ctx, gen.UpsertSendingDomainParams{
		WorkspaceID:  workspaceID,
		Domain:       res.Domain,
		State:        string(res.State()),
		SpfFound:     res.SPF.Found,
		SpfRecord:    res.SPF.Value,
		DkimFound:    res.DKIM.Found,
		DkimSelector: res.DKIM.Selector,
		DmarcFound:   res.DMARC.Found,
		DmarcPolicy:  res.DMARC.Policy,
	})
	if err != nil {
		return time.Time{}, err
	}
	return ts.Time, nil
}

// checkedAt maps a nullable timestamp to a pointer, so "never checked" stays
// distinguishable from the zero time all the way out to the JSON null.
func checkedAt(ts pgtype.Timestamptz) *time.Time {
	if !ts.Valid {
		return nil
	}
	t := ts.Time
	return &t
}
