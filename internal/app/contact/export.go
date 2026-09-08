package contact

import (
	"context"
	"slices"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/cursor"
)

// exportPageSize is how many rows StreamExport reads from the store per
// round trip. It is unrelated to SearchRequest.Limit (an export ignores that
// entirely — see PrepareExport): this is purely an internal batching size, big
// enough that a 200k-contact export costs a few hundred round trips rather
// than a few hundred thousand, small enough that one page is a trivial amount
// of memory.
const exportPageSize = 500

// ExportPlan is a validated, ready-to-stream contact export: the resolved
// filter/sort (identical vocabulary to Search — q/sort/list — but never
// cursor/limit, because an export is the whole filtered set) and the
// workspace's live custom-field columns, in header order.
//
// PrepareExport resolves everything that can fail with a proper HTTP status
// (an unknown sort, a cross-tenant list) BEFORE the caller commits any part of
// the response. StreamExport, which runs after headers are written, can then
// only ever stop the stream, never change the status — see StreamExport's doc.
type ExportPlan struct {
	filter SearchFilter
	sort   cursor.Sort
	fields []FieldDef
}

// Header returns the CSV header row: the built-in columns import accepts,
// followed by the workspace's live custom-field keys in the order
// ListFieldDefs returns them (archived first excluded, then label, then key —
// see queries/contactfield.sql), matching the order a settings page lists them.
//
// The header is never formula-escaped: builtinColumns is a fixed list and a
// custom key matches ^[a-z][a-z0-9_]{0,39}$ (migration 000052), so no header
// cell can begin with a trigger character.
func (p ExportPlan) Header() []string {
	header := make([]string, 0, len(builtinColumns)+len(p.fields))
	header = append(header, builtinColumns...)
	for _, f := range p.customFields() {
		header = append(header, f.Key)
	}
	return header
}

// customFields is the live definitions that get a column of their own: every
// one whose key is not already a built-in header.
//
// A key colliding with a built-in is skipped rather than renamed, because
// import ALREADY ignores such a column (mapCustomColumns skips
// builtinColumns), so a column for it could only ever be write-only. Emitting
// it would additionally break the round trip outright: the header would carry
// "email" twice and importRows' `col[name] = i` keeps the LAST occurrence, so
// the custom column would take over the address column and every contact would
// re-import under whatever that field held.
//
// normalizeKey now reserves these names, so only a workspace that predates that
// guard can reach this — which is exactly why the skip lives here and not only
// there.
func (p ExportPlan) customFields() []FieldDef {
	out := make([]FieldDef, 0, len(p.fields))
	for _, f := range p.fields {
		if slices.Contains(builtinColumns, f.Key) {
			continue
		}
		out = append(out, f)
	}
	return out
}

// record renders one matched row in Header order, with every cell defused
// against spreadsheet formula execution (escapeFormula).
func (p ExportPlan) record(row SearchRow) ([]string, error) {
	values, err := decodeStringMap(row.CustomFields)
	if err != nil {
		return nil, err
	}
	custom := p.customFields()
	record := make([]string, 0, len(builtinColumns)+len(custom))
	record = append(record,
		escapeFormula(row.Email), escapeFormula(row.FirstName),
		escapeFormula(row.LastName), escapeFormula(row.Company))
	for _, f := range custom {
		record = append(record, escapeFormula(values[f.Key]))
	}
	return record, nil
}

// PrepareExport validates the request and resolves ownership exactly as
// Search does (same q/sort/list vocabulary, same ErrListNotFound/validation
// errors), then loads the workspace's live custom-field definitions. Cursor
// and Limit on req are ignored: an export is the whole filtered set, not one
// page of it — the caller (exportContactsCSV) clears them before calling this,
// but PrepareExport does not depend on that, since it never reads them itself.
func (s *Service) PrepareExport(ctx context.Context, ws uuid.UUID, req SearchRequest) (ExportPlan, error) {
	q, sort, _, err := req.normalize()
	if err != nil {
		return ExportPlan{}, err
	}
	filter := SearchFilter{Query: q}
	if req.ListID != nil {
		ok, err := s.checker.ListExists(ctx, ws, *req.ListID)
		if err != nil {
			return ExportPlan{}, err
		}
		if !ok {
			return ExportPlan{}, ErrListNotFound
		}
		filter.ListID = req.ListID
	}

	defs, err := s.fields.ListFieldDefs(ctx, ws)
	if err != nil {
		return ExportPlan{}, err
	}
	live := make([]FieldDef, 0, len(defs))
	for _, d := range defs {
		if d.Live() {
			live = append(live, d)
		}
	}
	return ExportPlan{filter: filter, sort: sort, fields: live}, nil
}

// StreamExport walks plan's filtered set one keyset page at a time, over the
// same Store.Search/cursor machinery Search itself uses, handing each row to
// emit as a rendered CSV record. It is deliberately NOT a second pagination
// scheme: the only state it keeps between pages is the cursor built from the
// last row of the one before, exactly what Search's own NextCursor construction
// does (see rowCursor) — so a 200k-contact workspace never loads into memory,
// only exportPageSize rows at a time.
//
// An error here can only stop the stream, never report an HTTP status: by the
// time this runs, the caller has already written the response headers and the
// CSV header row (see exportContactsCSV), and once those — or enough rows to
// fill the transport's own write buffer — are on the wire, the status line is
// committed. Matching campaign.ResultsCSV's write-error handling, the caller's
// only correct response to a non-nil return here is to stop, not to retry or
// report.
func (s *Service) StreamExport(ctx context.Context, ws uuid.UUID, plan ExportPlan, emit func([]string) error) error {
	var cur *cursor.Cursor
	for {
		rows, err := s.store.Search(ctx, ws, SearchParams{Filter: plan.filter, Sort: plan.sort, Cur: cur, Limit: exportPageSize})
		if err != nil {
			return err
		}
		for _, row := range rows {
			record, err := plan.record(row)
			if err != nil {
				return err
			}
			if err := emit(record); err != nil {
				return err
			}
		}
		// Fewer rows than asked for is what proves this was the last page —
		// the same lookahead-free signal a LIMIT-bounded read always gives when
		// nothing more remains, no separate count needed.
		if len(rows) < exportPageSize {
			return nil
		}
		next := rowCursor(rows[len(rows)-1], plan.sort, cursor.After)
		cur = &next
	}
}
