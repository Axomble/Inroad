package coreapi

import "context"

// DeadLetterClient is an optional execution-plane capability: recording a task
// that exhausted its asynq retries. Kept separate from Client — like
// CRMCaptureClient — so every worker fake and a future remote client are not
// forced to implement it to satisfy the main interface.
//
// A Client that does not implement it simply has no dead-letter capture, which
// degrades to today's behaviour (the task is lost silently) rather than to a
// failed task.
type DeadLetterClient interface {
	// RecordDeadLetter persists one retry-exhausted task. WorkspaceID is a
	// string here for the same reason every other coreapi id is: the seam is
	// transport-neutral and a future HTTP implementation carries ids as text.
	RecordDeadLetter(ctx context.Context, in DeadLetterInput) error
}

// DeadLetterInput is one retry-exhausted task as the execution plane observed
// it. Payload is the ORIGINAL task body byte-for-byte — the control plane
// stores it verbatim, because replaying anything other than what was captured
// would re-run different work than the task that failed.
type DeadLetterInput struct {
	WorkspaceID  string
	TaskType     string
	Payload      []byte
	LastError    string
	AttemptCount int
	// Queue is the asynq queue the task was claimed from, "" when the execution
	// plane could not tell. The control plane stores it so a replay can return
	// the task to the queue it ran on — which, for a warmup tick, is the worker
	// and therefore the IP the mailbox was warming from.
	Queue string
}
