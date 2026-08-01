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
