package contact

import (
	"encoding/csv"
	"errors"
	"log/slog"
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
		// Nothing has been queried yet, so this can only be a dead client
		// connection. Debug rather than error for that reason — it is not a
		// server fault and a disconnect mid-download is ordinary — but not
		// discarded, because "the export never produced anything" and "the
		// client hung up" must be distinguishable.
		slog.DebugContext(r.Context(), "contact_export_header_write_failed",
			"workspace_id", ws, "err", err)
		return
	}

	// rowsEmitted counts records the writer ACCEPTED, so a truncation log says
	// how much of the file the client actually got. Incremented after the write,
	// not before, so a failed row is not counted as delivered.
	var rowsEmitted int
	if err := h.svc.StreamExport(r.Context(), ws, plan, func(record []string) error {
		if werr := cw.Write(record); werr != nil {
			return werr
		}
		rowsEmitted++
		return nil
	}); err != nil {
		// The status cannot change — the header row is already on the wire, so
		// this response is committed to 200 whatever happens next (see
		// StreamExport's doc). "Cannot report to the client" is not "must not
		// record", though, and this is NOT the dead-connection case
		// campaign.ResultsCSV documents: that one materialises its whole result
		// set before writing, whereas StreamExport queries Postgres DURING the
		// response. A pool exhaustion or statement timeout here yields a file
		// short by an arbitrary number of contacts that is byte-for-byte
		// indistinguishable from a complete one, and the operator's next move is
		// to import it somewhere.
		//
		// Flushed first, deliberately: the rows already handed to the writer are
		// rows the client has been told about, and dropping up to a buffer's
		// worth of them on the way out makes the truncation worse, not more
		// visible.
		cw.Flush()
		slog.ErrorContext(r.Context(), "contact_export_truncated",
			"workspace_id", ws, "rows_emitted", rowsEmitted, "err", err)
		return
	}

	cw.Flush()
	// Flush is where a buffered write error finally surfaces, so a clean
	// StreamExport does not by itself mean a complete file reached the client.
	if err := cw.Error(); err != nil {
		slog.ErrorContext(r.Context(), "contact_export_flush_failed",
			"workspace_id", ws, "rows_emitted", rowsEmitted, "err", err)
	}
}
