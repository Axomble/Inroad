// Package enrollment tracks a contact's position in a campaign's step
// sequence and owns the single advance/stop entry points.
package enrollment

// EnrollmentStatus is the typed enum mirrored by the DB CHECK on
// sequence_enrollments.status.
type EnrollmentStatus string

const (
	StatusActive    EnrollmentStatus = "active"
	StatusCompleted EnrollmentStatus = "completed"
	StatusStopped   EnrollmentStatus = "stopped"
)

// StopReason is why an active enrollment was halted. NULL in the DB while the
// enrollment is active/completed. Reply and bounce consumers are deferred, but
// the reasons are defined now so MarkStepStopped is the single stop entry
// point for every future halt trigger.
type StopReason string

const (
	StopReplied    StopReason = "replied"
	StopBounced    StopReason = "bounced"
	StopSuppressed StopReason = "suppressed"
	StopManual     StopReason = "manual"
	// StopFailed halts an enrollment the sequence engine can no longer make
	// progress on: a degenerate mailbox cap of 0, or a cap-defer loop that has
	// exceeded its ceiling without ever clearing.
	StopFailed StopReason = "failed"
)
