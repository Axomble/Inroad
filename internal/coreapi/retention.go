package coreapi

import (
	"time"

	"github.com/google/uuid"
)

// Seam types for the recipient-data retention sweep (maintenance:retention).
//
// The methods that carry them are deliberately NOT on Client, for the reason
// stated on BreakerResult: the sweep depends on a narrow interface it defines
// itself (maintenance.Retainer), satisfied by the in-process client via type
// assertion at the composition root — the same shape as maintenance.Cleaner.
//
// The remote transport does not carry them and never will. Every retention
// batch is a cross-tenant DELETE by age; a route that answered one would let a
// fleet host delete rows from every workspace, which is exactly the capability
// docs/security.md invariant 73 says this seam may never express. Not
// implementing the interface is how registerScheduled is told so (see
// internal/coreapi/remote/controlplane.go).

// RetentionRequest is one bounded batch of a retention sweep.
type RetentionRequest struct {
	// OlderThan is the window: rows whose age is at least this are eligible.
	// The cutoff is computed from the DATABASE clock, so replicas with skewed
	// clocks agree on it.
	OlderThan time.Duration
	// Limit bounds the rows the batch SCANS past the cursor — and so the rows it
	// can delete, and the length of its one transaction. Scanning, not deleting,
	// is what is bounded: a batch whose rows are all kept by a guard costs the
	// same as one that deletes them all.
	Limit int32
	// After resumes strictly after the last row a previous batch scanned. The
	// zero value starts from the oldest row.
	After RetentionCursor
}

// RetentionCursor is a position in a table's (age, id) order. The zero value is
// before every row.
type RetentionCursor struct {
	At time.Time
	ID uuid.UUID
}

// RetentionBatch is what one batch did.
type RetentionBatch struct {
	// Scanned is how many rows past the cursor the batch examined, kept or
	// deleted. Scanned < RetentionRequest.Limit means nothing is left past the
	// cursor: the table is drained.
	Scanned int64
	// Deleted is how many of those rows of the table itself were removed.
	Deleted int64
	// Dependents counts rows removed from OTHER tables along with them, so the
	// sweep's metrics are honest about what a delete cost: the messages of a
	// deleted inbox thread, the tracking events and rollups of a deleted send.
	// Zero where a table has no dependents.
	Dependents int64
	// RolledUp is how many tracking_event_rollups rows the batch inserted or
	// incremented. Tracking events only; zero everywhere else.
	RolledUp int64
	// Next is the cursor to resume from: the last row SCANNED. Meaningful only
	// when Scanned > 0.
	Next RetentionCursor
}

// RetentionProgress is where a table's sweep got to in a previous run.
type RetentionProgress struct {
	// Cursor is the position to resume from; the zero value means "from the
	// oldest row".
	Cursor RetentionCursor
	// CycleExpired reports that the table has been walked without starting over
	// for more than a day. The sweep then starts over from the oldest row, so a
	// row a guard stopped protecting behind the cursor is revisited.
	CycleExpired bool
}
