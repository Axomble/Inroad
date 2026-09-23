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
	// Limit bounds the rows of the table itself this batch may delete, which
	// bounds how long its one transaction holds locks.
	Limit int32
	// After resumes strictly after the last row a previous batch in the same run
	// deleted. The zero value starts from the oldest row.
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
	// Deleted is how many rows of the table itself were removed. It is bounded
	// by RetentionRequest.Limit, and Deleted < Limit means nothing eligible is
	// left past the cursor.
	Deleted int64
	// Dependents counts rows removed from OTHER tables along with them, so the
	// sweep's metrics are honest about what a delete cost: the messages of a
	// deleted inbox thread, the tracking events and rollups of a deleted send.
	// Zero where a table has no dependents.
	Dependents int64
	// RolledUp is how many tracking_event_rollups rows the batch inserted or
	// incremented. Tracking events only; zero everywhere else.
	RolledUp int64
	// Next is the cursor to resume from. Meaningful only when Deleted > 0.
	Next RetentionCursor
}
