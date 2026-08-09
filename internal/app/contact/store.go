// Package contact manages contacts and CSV import into lists.
package contact

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/cursor"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// UpsertInput carries the fields required to create or update a contact.
//
// CustomFields is an encoded JSON object of workspace-defined field values,
// MERGED into whatever the contact already holds (see UpsertContact). It is
// []byte rather than a map because it crosses straight into a JSONB parameter;
// callers with nothing to write leave it nil, which the store turns into an
// empty object so the merge is a no-op.
type UpsertInput struct {
	Email, FirstName, LastName, Company string
	CustomFields                        []byte
}

// SearchFilter is what decides which contacts match, independent of ordering
// and position. A nil ListID means the whole workspace; an empty Query means no
// text filter. Both are optional so one filter type serves the page query and
// the capped count.
type SearchFilter struct {
	ListID *uuid.UUID
	Query  string
}

// SearchParams is one page request: a filter, an ordering, and a position
// inside it. Cur is nil for the first page.
type SearchParams struct {
	Filter SearchFilter
	Sort   cursor.Sort
	Cur    *cursor.Cursor
	// Limit is how many rows to fetch. The service asks for one more than the
	// page size and uses the surplus to decide whether another page exists.
	Limit int
}

// SearchRow is one matched contact plus the two values a cursor is built from.
// SortEmail is lower(email) straight from Postgres so the cursor key is
// byte-identical to what idx_contacts_ws_email_id ordered by.
type SearchRow struct {
	ID          uuid.UUID
	Email       string
	FirstName   string
	LastName    string
	CompanyID   *uuid.UUID
	CompanyName string
	JobTitle    string
	LinkedInURL string
	DealCount   int64
	CreatedAt   time.Time
	SortEmail   string
}

// Store is the repository interface this domain depends on. It is defined
// here (by the consumer), not by the persistence layer, so the service can
// be unit-tested against a fake without a database.
type Store interface {
	// Upsert returns the contact id and whether it was newly inserted.
	Upsert(ctx context.Context, workspaceID uuid.UUID, in UpsertInput) (uuid.UUID, bool, error)
	AddToList(ctx context.Context, listID, contactID uuid.UUID) error
	// Search returns up to p.Limit rows in the scan order p implies. For a
	// Before cursor that order is the reverse of display order; flipping is the
	// service's job, so the store stays a pure "run this keyset seek".
	Search(ctx context.Context, workspaceID uuid.UUID, p SearchParams) ([]SearchRow, error)
	// CountMatches counts matching contacts, stopping at capAt. A result equal
	// to capAt means "at least capAt", never "exactly capAt".
	CountMatches(ctx context.Context, workspaceID uuid.UUID, f SearchFilter, capAt int) (int64, error)

	// Get returns the contact and its company link, without any child list.
	// A contact outside workspaceID yields ErrNotFound.
	Get(ctx context.Context, workspaceID, contactID uuid.UUID) (Record, error)
	// CompanyExists reports whether companyID is a company in workspaceID. The
	// contact domain reads the companies table directly through sqlc rather than
	// calling the crm domain: app packages do not import each other, and this is
	// a one-column ownership check, not a use of another domain's behaviour.
	CompanyExists(ctx context.Context, workspaceID, companyID uuid.UUID) (bool, error)
	// SetCompany links the contact to companyID, or unlinks it when companyID is
	// nil. ErrNotFound when the contact is not in workspaceID.
	SetCompany(ctx context.Context, workspaceID, contactID uuid.UUID, companyID *uuid.UUID) error
	// Suppression reports why the contact may not be emailed, or nil when no
	// address of theirs is on the workspace suppression list.
	Suppression(ctx context.Context, workspaceID, contactID uuid.UUID) (*RecordSuppression, error)
	// ListDeals returns up to limit deals the contact is primary on, in board
	// order. The service asks for one more than the cap to detect truncation.
	ListDeals(ctx context.Context, workspaceID, contactID uuid.UUID, limit int32) ([]RecordDeal, error)
	SendStats(ctx context.Context, workspaceID, contactID uuid.UUID) (SendStats, error)
	TrackingStats(ctx context.Context, workspaceID, contactID uuid.UUID) (TrackingStats, error)
	// EnrollmentCounts returns enrollment counts keyed by stop_reason, with ""
	// for an enrollment that has not stopped.
	EnrollmentCounts(ctx context.Context, workspaceID, contactID uuid.UUID) (map[string]int64, error)
	ListCampaigns(ctx context.Context, workspaceID, contactID uuid.UUID, limit int32) ([]CampaignEnrollment, error)
}

// PgStore implements Store by wrapping sqlc-generated queries plus, for the
// keyset search, statements assembled in search.go (see the note there on why
// that path is not sqlc).
type PgStore struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

// NewPgStore builds the store over a connection pool. The pool (rather than
// just *gen.Queries) is required because the search path executes SQL composed
// per access path.
func NewPgStore(pool *pgxpool.Pool) *PgStore {
	return &PgStore{pool: pool, q: gen.New(pool)}
}

func (s *PgStore) Upsert(ctx context.Context, ws uuid.UUID, in UpsertInput) (uuid.UUID, bool, error) {
	// A nil CustomFields is normalised to '{}' by the query itself (COALESCE in
	// UpsertContact), not here. It was guarded here first, which fixed exactly
	// this one caller and left every other one — the agent's contact tool and
	// the integration-test helpers — writing an explicit NULL into a NOT NULL
	// column. The fix belongs where all of them meet.
	row, err := s.q.UpsertContact(ctx, gen.UpsertContactParams{
		WorkspaceID: ws, Email: in.Email, FirstName: in.FirstName, LastName: in.LastName,
		Company: in.Company, CustomFields: in.CustomFields,
	})
	if err != nil {
		return uuid.Nil, false, err
	}
	return row.ID, row.Inserted, nil
}

func (s *PgStore) AddToList(ctx context.Context, listID, contactID uuid.UUID) error {
	return s.q.AddListMember(ctx, gen.AddListMemberParams{ListID: listID, ContactID: contactID})
}

func (s *PgStore) Search(ctx context.Context, ws uuid.UUID, p SearchParams) ([]SearchRow, error) {
	sql, args := searchSQL(ws, p.Filter, p.Sort, p.Cur, p.Limit)
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("search contacts: %w", err)
	}
	defer rows.Close()

	out := make([]SearchRow, 0, p.Limit)
	for rows.Next() {
		var r SearchRow
		if err := rows.Scan(&r.ID, &r.Email, &r.FirstName, &r.LastName, &r.CompanyID, &r.CompanyName,
			&r.JobTitle, &r.LinkedInURL, &r.DealCount, &r.CreatedAt, &r.SortEmail); err != nil {
			return nil, fmt.Errorf("scan contact: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search contacts: %w", err)
	}
	return out, nil
}

func (s *PgStore) CountMatches(ctx context.Context, ws uuid.UUID, f SearchFilter, capAt int) (int64, error) {
	sql, args := countSQL(ws, f, capAt)
	var n int64
	if err := s.pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count contacts: %w", err)
	}
	return n, nil
}
