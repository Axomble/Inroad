package contact

import (
	"encoding/csv"
	"errors"
	"net/http"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
)

// exportContactsCSV serves GET /api/v1/contacts.csv (mounted via ExportRoutes,
// not Routes() — see routes.go for why chi's segment-based mount matching
// rules that path out of the /api/v1/contacts subrouter).
//
// It honours the same q/sort/list filter arguments as GET /contacts but never
// cursor/limit: an export is the whole filtered set, not one page of it (see
// PrepareExport). The streaming shape mirrors campaign.ResultsCSV —
// csv.NewWriter straight onto the ResponseWriter, no buffered [][]string,
// headers set before the first write, a write error mid-stream stops the
// response rather than retrying or panicking.
func (h *Handler) exportContactsCSV(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	req, err := parseSearchRequest(r)
	if err != nil {
		httpx.Error(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	// list/q/sort are honoured; cursor/limit are accepted on GET /contacts
	// (paging) but not here — an export is the whole filtered set. Cleared
	// rather than left for PrepareExport to ignore, so nothing downstream can
	// ever depend on this endpoint reading a page position out of the query
	// string.
	req.Cursor = ""
	req.Limit = nil

	plan, err := h.svc.PrepareExport(r.Context(), ws, req)
	switch {
	case errors.Is(err, ErrListNotFound):
		httpx.Error(w, http.StatusNotFound, "list not found")
		return
	case IsValidationError(err):
		httpx.Error(w, http.StatusUnprocessableEntity, err.Error())
		return
	case err != nil:
		httpx.Error(w, http.StatusInternalServerError, "could not prepare contact export")
		return
	}

	// Headers before the first write: once a row reaches the client, the
	// status is already sent and an error can no longer be reported as one —
	// the exact reasoning campaign.ResultsCSV documents at its own headers.
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="contacts.csv"`)

	cw := csv.NewWriter(w)
	// Written unconditionally, even for zero matching contacts: a header-only
	// CSV reads to an operator as "nothing matched", where a zero-byte file
	// reads as a failed download.
	if err := cw.Write(plan.Header()); err != nil {
		return // the connection is gone; nothing useful to report
	}
	if err := h.svc.StreamExport(r.Context(), ws, plan, func(record []string) error {
		return cw.Write(record)
	}); err != nil {
		// See StreamExport's doc: headers (and possibly some rows) are already
		// on the wire by the time this can fire, so all that is left to do is
		// stop — not retry, not report a status that has already been sent.
		return
	}
	cw.Flush()
}
