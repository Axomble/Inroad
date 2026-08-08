package contact

import (
	"context"
	"errors"
	"io"
	"slices"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/cursor"
)

// ErrListNotFound is what the handler layer maps to 404 when the target
// list doesn't belong to the caller's workspace. Kept in this package so
// no other domain has to be imported (mirrors campaign.Checker).
var ErrListNotFound = errors.New("list not found")

// ListChecker validates that a list belongs to the caller's workspace,
// without this domain having to know about the list domain (that would
// break the "app packages don't import each other" invariant in
// docs/architecture.md). The composition root in cmd/inroad wires a small
// adapter over *list.Service (mirrors campaign.Checker).
type ListChecker interface {
	ListExists(ctx context.Context, ws, listID uuid.UUID) (bool, error)
}

// Service depends on the Store interface (never the concrete sqlc-backed
// struct — dependency inversion) plus a ListChecker for cross-domain
// ownership checks.
type Service struct {
	store   Store
	checker ListChecker
}

// NewService constructs a Service backed by store and using checker to
// verify list ownership before mutating imports.
func NewService(store Store, checker ListChecker) *Service {
	return &Service{store: store, checker: checker}
}

// ImportCSV verifies the list belongs to the workspace, then imports rows.
func (s *Service) ImportCSV(ctx context.Context, ws, listID uuid.UUID, r io.Reader) (ImportResult, error) {
	ok, err := s.checker.ListExists(ctx, ws, listID)
	if err != nil {
		return ImportResult{}, err
	}
	if !ok {
		return ImportResult{}, ErrListNotFound
	}
	return s.importRows(ctx, ws, listID, r)
}

// Search runs one keyset page. It validates the request, resolves the cursor,
// runs the seek plus the bounded count, and turns the fetched rows into a page
// with its next/prev cursors.
func (s *Service) Search(ctx context.Context, ws uuid.UUID, req SearchRequest) (Page, error) {
	q, sort, limit, err := req.normalize()
	if err != nil {
		return Page{}, err
	}
	// The list is validated before anything is read, so an unknown or
	// cross-tenant list id is a 404 rather than a silently empty page.
	filter := SearchFilter{Query: q}
	if req.ListID != nil {
		ok, err := s.checker.ListExists(ctx, ws, *req.ListID)
		if err != nil {
			return Page{}, err
		}
		if !ok {
			return Page{}, ErrListNotFound
		}
		filter.ListID = req.ListID
	}

	var cur *cursor.Cursor
	if req.Cursor != "" {
		c, err := cursor.Decode(req.Cursor, sort)
		if err != nil {
			return Page{}, err
		}
		cur = &c
	}

	// One row beyond the page: its presence is what proves another page exists,
	// without a second query or an unbounded count.
	rows, err := s.store.Search(ctx, ws, SearchParams{Filter: filter, Sort: sort, Cur: cur, Limit: limit + 1})
	if err != nil {
		return Page{}, err
	}
	total, err := s.store.CountMatches(ctx, ws, filter, TotalCap)
	if err != nil {
		return Page{}, err
	}

	page := buildPage(rows, sort, cur, limit)
	page.TotalIsCapped = total > int64(TotalCap)
	page.Total = min(total, int64(TotalCap))
	return page, nil
}

// Record loads one contact's page: its own fields, its company link, and the
// deals it is primary on (capped — see DealCap). Engagement is a separate call
// on purpose: it aggregates over sends and tracking_events, so bundling it here
// would make the cheap half of the page pay for the expensive half on every
// load, and would tie two things that change on different schedules to one
// cache entry.
func (s *Service) Record(ctx context.Context, ws, contactID uuid.UUID) (Record, error) {
	record, err := s.store.Get(ctx, ws, contactID)
	if err != nil {
		return Record{}, err
	}
	// This error is propagated, never swallowed, and must stay that way.
	//
	// Suppression is the load-bearing fact on this page: invariants 20 and 42
	// make it the load-bearing WRITE on the ingest side, ordered ahead of
	// everything else precisely so a downstream failure can never skip honouring
	// an opt-out. The read side owes the same fail-safe direction. Degrading a
	// failed lookup to a nil Suppression would render the contact as "clear to
	// email" on the strength of a query that never answered — turning an
	// infrastructure blip into a compliance decision, and the exact mistake
	// invariant 20's ordering exists to prevent. Fail the request instead: a
	// record page that will not load is recoverable, an opt-out silently
	// overridden is not.
	suppression, err := s.store.Suppression(ctx, ws, contactID)
	if err != nil {
		return Record{}, err
	}
	record.Suppression = suppression
	// One row past the cap: its presence is what proves there are more, without
	// a count query.
	deals, err := s.store.ListDeals(ctx, ws, contactID, DealCap+1)
	if err != nil {
		return Record{}, err
	}
	record.DealsTruncated = len(deals) > DealCap
	if record.DealsTruncated {
		deals = deals[:DealCap]
	}
	record.Deals = deals
	return record, nil
}

// SetCompany links a contact to a company, or unlinks it when companyID is nil,
// and returns the refreshed record so a caller needs no follow-up GET.
//
// This is the write path that `contacts.company_id` was missing: the column has
// been readable since migration 000042 (the contact record's company, the company
// roster, the deal joins) but nothing in the API ever set it, so both reads were
// structurally empty. Deals carry their own company_id and are not this — a deal's
// company is who the money is with, which is not necessarily who the contact
// works for.
//
// Ownership of the COMPANY is checked before the write so a foreign company id is
// a 404 rather than the tenant FK's 23503; ownership of the CONTACT falls out of
// the workspace-pinned UPDATE affecting zero rows.
func (s *Service) SetCompany(ctx context.Context, ws, contactID uuid.UUID, companyID *uuid.UUID) (Record, error) {
	if companyID != nil {
		exists, err := s.store.CompanyExists(ctx, ws, *companyID)
		if err != nil {
			return Record{}, err
		}
		if !exists {
			return Record{}, ErrCompanyNotFound
		}
	}
	if err := s.store.SetCompany(ctx, ws, contactID, companyID); err != nil {
		return Record{}, err
	}
	return s.Record(ctx, ws, contactID)
}

// Engagement rolls up the contact's outreach history. Ownership is resolved
// first, so an unknown or cross-workspace id is ErrNotFound before any
// aggregate runs — a foreign id can neither read counts nor cost a scan.
func (s *Service) Engagement(ctx context.Context, ws, contactID uuid.UUID) (Engagement, error) {
	if _, err := s.store.Get(ctx, ws, contactID); err != nil {
		return Engagement{}, err
	}
	sends, err := s.store.SendStats(ctx, ws, contactID)
	if err != nil {
		return Engagement{}, err
	}
	tracking, err := s.store.TrackingStats(ctx, ws, contactID)
	if err != nil {
		return Engagement{}, err
	}
	stopReasons, err := s.store.EnrollmentCounts(ctx, ws, contactID)
	if err != nil {
		return Engagement{}, err
	}
	campaigns, err := s.store.ListCampaigns(ctx, ws, contactID, CampaignCap+1)
	if err != nil {
		return Engagement{}, err
	}
	truncated := len(campaigns) > CampaignCap
	if truncated {
		campaigns = campaigns[:CampaignCap]
	}
	return computeEngagement(contactID, sends, tracking, stopReasons, campaigns, truncated), nil
}

// buildPage trims the lookahead row, restores display order for a backward
// page, and derives the two cursors.
func buildPage(rows []SearchRow, sort cursor.Sort, cur *cursor.Cursor, limit int) Page {
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	backward := cur != nil && cur.Direction == cursor.Before
	if backward {
		// A Before page was scanned away from the cursor, so it arrives in
		// reverse display order.
		slices.Reverse(rows)
	}

	page := Page{Items: rows}
	if len(rows) == 0 {
		return page
	}
	first, last := rows[0], rows[len(rows)-1]

	if backward {
		// Travelling backward: a following page necessarily exists (it is the
		// one we came from), and the lookahead answers the preceding one.
		page.NextCursor = ptr(rowCursor(last, sort, cursor.After).Encode())
		if more {
			page.PrevCursor = ptr(rowCursor(first, sort, cursor.Before).Encode())
		}
		return page
	}
	if more {
		page.NextCursor = ptr(rowCursor(last, sort, cursor.After).Encode())
	}
	// No cursor at all means this is the first page, which by definition has
	// nothing before it.
	if cur != nil {
		page.PrevCursor = ptr(rowCursor(first, sort, cursor.Before).Encode())
	}
	return page
}

// rowCursor names a row's position under sort, travelling dir.
func rowCursor(r SearchRow, sort cursor.Sort, dir cursor.Direction) cursor.Cursor {
	if sort == cursor.SortEmail {
		return cursor.NewEmail(dir, r.SortEmail, r.ID)
	}
	return cursor.NewTime(sort, dir, r.CreatedAt, r.ID)
}

func ptr[T any](v T) *T { return &v }
