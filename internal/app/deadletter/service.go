// Package deadletter owns the record of tasks that exhausted their asynq
// retries, and the operator's path to re-run or file one.
//
// WHY: asynq archives a retry-exhausted task in Redis, where this product's
// operator cannot see it, it carries no tenant, and it does not survive Redis
// being flushed or replaced. For a platform whose job is "send this email at
// this time", a permanently dropped send was previously invisible. Capture
// (internal/platform/queue.DeadLetterErrorHandler) writes the terminal failure
// here; this domain reads and replays it.
//
// The single correctness property of the whole domain is that a replay cannot
// double-send. It is enforced by claim-before-enqueue against a status-guarded
// UPDATE — see Service.Replay and queries/deadletter.sql.
package deadletter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

var (
	// ErrNotFound is returned when the workspace has no such dead letter. A row
	// owned by ANOTHER workspace also produces this, deliberately: a tenant must
	// not be able to tell a foreign id from a nonexistent one.
	ErrNotFound = errors.New("deadletter: not found")
	// ErrNotPending is returned when the row exists but is no longer actionable
	// — already replayed, or already discarded. It is the operator-visible face
	// of the claim losing, and is what makes replaying an already-replayed row a
	// clean 409 instead of a second delivery.
	ErrNotPending = errors.New("deadletter: not pending")
	// ErrMalformedPayload is returned when a captured payload cannot be replayed
	// because it is not the shape its task type requires — most importantly, it
	// does not carry the workspace that owns the row. Replaying such a payload
	// would hand the queue a task nobody can safely execute, so it is refused.
	ErrMalformedPayload = errors.New("deadletter: malformed payload")
	// ErrValidation is returned for a caller-fixable input problem (an unknown
	// status filter).
	ErrValidation = errors.New("deadletter: invalid")
)

// Status values a dead letter can hold. Mirrors the CHECK constraint in
// migration 000069; the two must move together.
const (
	StatusPending   = "pending"
	StatusReplayed  = "replayed"
	StatusDiscarded = "discarded"
)

// Paging bounds for the triage list. The list is small by nature (it counts
// tasks a workspace has permanently lost), so the cap is about refusing an
// absurd request rather than about throughput.
const (
	defaultLimit = 50
	maxLimit     = 200
)

// releaseTimeout bounds the compensating write that undoes a claim whose
// enqueue failed. It runs on a context detached from the request's, so it needs
// a deadline of its own or a wedged database would hold the handler open.
const releaseTimeout = 5 * time.Second

// Capture is one retry-exhausted task, as recorded by the capture path. It is
// the write-side input type; nothing on the HTTP surface can construct one.
type Capture struct {
	WorkspaceID  uuid.UUID
	TaskType     string
	Payload      []byte
	LastError    string
	AttemptCount int32
}

// Enqueuer is the queue seam this domain replays through — one method, defined
// here by the consumer. internal/platform/queue.Client satisfies it; a test
// injects a fake and needs no Redis.
//
// key is a deterministic dedup identifier derived from the dead-letter row (see
// replayKey). It is defense in depth BEHIND the row claim, not the primary
// guard: the claim is what makes replay exactly-once even across a queue that
// has forgotten the key.
type Enqueuer interface {
	EnqueueReplay(ctx context.Context, taskType string, payload []byte, key string) error
}

// Service holds this domain's business rules. It depends on the Store
// interface and the Enqueuer seam, never on a concrete store or on asynq.
type Service struct {
	store Store
	enq   Enqueuer
}

// NewService builds the service over its two seams.
func NewService(store Store, enq Enqueuer) *Service {
	return &Service{store: store, enq: enq}
}

// Capture records one retry-exhausted task. Called by the execution plane
// through coreapi, never by an HTTP handler.
func (s *Service) Capture(ctx context.Context, in Capture) (gen.TaskDeadLetter, error) {
	if in.WorkspaceID == uuid.Nil {
		return gen.TaskDeadLetter{}, fmt.Errorf("%w: capture without a workspace", ErrValidation)
	}
	if in.TaskType == "" {
		return gen.TaskDeadLetter{}, fmt.Errorf("%w: capture without a task type", ErrValidation)
	}
	// A dead letter whose payload is not valid JSON cannot be stored in a JSONB
	// column at all, so normalise the empty/absent case to a JSON null rather
	// than letting the driver reject the insert and lose the record entirely.
	// Recording that a task of this type died is worth more than the payload.
	if len(in.Payload) == 0 {
		in.Payload = []byte("null")
	}
	row, err := s.store.Insert(ctx, in)
	if err != nil {
		return gen.TaskDeadLetter{}, fmt.Errorf("deadletter: insert: %w", err)
	}
	return row, nil
}

// ListParams is the read-side filter. Kept as a struct rather than four
// positional arguments so a caller cannot transpose limit and offset.
type ListParams struct {
	Status string
	Limit  int32
	Offset int32
}

// List returns the workspace's dead letters, newest first. An empty Status
// means any; any other value must be one of the three known statuses, so a
// typo'd filter is a 422 rather than a silently empty list.
func (s *Service) List(ctx context.Context, ws uuid.UUID, p ListParams) ([]gen.TaskDeadLetter, error) {
	if p.Status != "" && !isKnownStatus(p.Status) {
		return nil, fmt.Errorf("%w: unknown status %q", ErrValidation, p.Status)
	}
	if p.Limit <= 0 {
		p.Limit = defaultLimit
	}
	if p.Limit > maxLimit {
		p.Limit = maxLimit
	}
	if p.Offset < 0 {
		p.Offset = 0
	}
	rows, err := s.store.List(ctx, ws, p.Status, p.Limit, p.Offset)
	if err != nil {
		return nil, fmt.Errorf("deadletter: list: %w", err)
	}
	return rows, nil
}

// Get loads one dead letter in this workspace.
func (s *Service) Get(ctx context.Context, ws, id uuid.UUID) (gen.TaskDeadLetter, error) {
	row, ok, err := s.store.Get(ctx, ws, id)
	if err != nil {
		return gen.TaskDeadLetter{}, fmt.Errorf("deadletter: get: %w", err)
	}
	if !ok {
		return gen.TaskDeadLetter{}, ErrNotFound
	}
	return row, nil
}

// Replay re-enqueues a pending dead letter's original payload, exactly once.
//
// The ordering here IS the correctness property, so it is worth stating plainly:
//
//  1. CLAIM the row with a status='pending'-guarded UPDATE. Postgres serialises
//     concurrent UPDATEs on a row, so of two racing replays exactly one sees a
//     row returned; the loser gets claimed=false and ErrNotPending. A replay of
//     an ALREADY-replayed row loses the same way, on the same predicate.
//  2. VALIDATE the claimed payload against the row's own workspace. A payload
//     that does not name this workspace is refused rather than enqueued.
//  3. ENQUEUE, under a deterministic key derived from the row id.
//
// Claim-before-enqueue is deliberate and not interchangeable with its opposite.
// Enqueue-then-mark would hand the task to the queue and could then fail to
// record it, leaving the row pending and the mail sendable a second time — the
// exact failure this ticket exists to prevent. This order fails the other way:
// a claim that succeeds but whose enqueue fails LOSES the replay, and is
// compensated back to pending only because nothing reached the queue.
func (s *Service) Replay(ctx context.Context, ws, id uuid.UUID) (gen.TaskDeadLetter, error) {
	row, claimed, err := s.store.ClaimReplay(ctx, ws, id)
	if err != nil {
		return gen.TaskDeadLetter{}, fmt.Errorf("deadletter: claim replay: %w", err)
	}
	if !claimed {
		// The claim matched nothing. Distinguish "no such row in this
		// workspace" (404) from "exists but is not pending" (409) with a
		// follow-up read; the distinction is only ever reported to the
		// operator, never acted on, so reading after the claim is safe.
		return gen.TaskDeadLetter{}, s.explainFailedClaim(ctx, ws, id)
	}

	// Belt-and-braces on the tenant pin: the payload we are about to hand the
	// execution plane must name the SAME workspace as the row we claimed. The
	// SQL WHERE clause already guarantees the row is this tenant's; this
	// guarantees the PAYLOAD is too, so a corrupted or hand-edited row can
	// never cause the worker to act on another tenant's data.
	if err := verifyPayloadWorkspace(row.Payload, row.WorkspaceID); err != nil {
		s.releaseClaim(ctx, row)
		return gen.TaskDeadLetter{}, err
	}

	if err := s.enq.EnqueueReplay(ctx, row.TaskType, row.Payload, replayKey(row.ID)); err != nil {
		s.releaseClaim(ctx, row)
		return gen.TaskDeadLetter{}, fmt.Errorf("deadletter: enqueue replay: %w", err)
	}
	return row, nil
}

// Discard files a pending dead letter as triaged without re-running it.
func (s *Service) Discard(ctx context.Context, ws, id uuid.UUID) error {
	discarded, err := s.store.Discard(ctx, ws, id)
	if err != nil {
		return fmt.Errorf("deadletter: discard: %w", err)
	}
	if !discarded {
		return s.explainFailedClaim(ctx, ws, id)
	}
	return nil
}

// explainFailedClaim turns "the status-guarded UPDATE matched no row" into the
// right error for the operator: ErrNotFound when this workspace has no such
// row at all, ErrNotPending when it has one that is already terminal.
//
// A read error here is reported as ErrNotFound rather than surfaced: the
// caller's operation has already definitively not happened, and the only thing
// at stake is which of two client errors is reported.
func (s *Service) explainFailedClaim(ctx context.Context, ws, id uuid.UUID) error {
	_, ok, err := s.store.Get(ctx, ws, id)
	if err != nil || !ok {
		return ErrNotFound
	}
	return ErrNotPending
}

// releaseClaim compensates a claim whose replay did not go ahead. Its own error
// is logged into the returned error chain by the caller's context rather than
// replacing it: the caller is already returning a failure, and the ORIGINAL
// reason (bad payload, dead queue) is the one the operator needs. The row is
// left 'replayed' if this fails, which is the safe direction — a stuck-replayed
// row is visible and re-runnable by hand, an un-stuck one could be replayed
// twice.
func (s *Service) releaseClaim(ctx context.Context, row gen.TaskDeadLetter) {
	// A fresh context is deliberate: the request context may already be
	// cancelled (the client hung up mid-replay), and the compensating write
	// must still run — otherwise a cancelled request permanently strands the
	// row in 'replayed' with nothing enqueued.
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	if err := s.store.ReleaseReplay(releaseCtx, row.WorkspaceID, row.ID, row.ReplayedAt); err != nil {
		// Logged, not returned: the caller is already failing for a reason the
		// operator needs to see, and this row is now stranded in 'replayed'
		// with nothing enqueued — which is precisely the state this log line
		// exists to make findable. The payload is NOT logged (task payloads
		// carry reply bodies and recipient addresses).
		slog.ErrorContext(releaseCtx, "dead-letter replay claim could not be released",
			"dead_letter_id", row.ID, "workspace_id", row.WorkspaceID,
			"task_type", row.TaskType, "err", err)
	}
}

// verifyPayloadWorkspace rejects a payload that does not carry the workspace the
// row belongs to. Every capturable task payload in internal/platform/queue
// carries a workspace_id for exactly this reason (defense in depth on
// unguessable UUIDs), so a payload missing it is malformed by definition and a
// payload naming a DIFFERENT workspace is a corruption that must never reach a
// worker.
func verifyPayloadWorkspace(payload []byte, ws uuid.UUID) error {
	var envelope struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return fmt.Errorf("%w: not a JSON object: %w", ErrMalformedPayload, err)
	}
	if envelope.WorkspaceID == "" {
		return fmt.Errorf("%w: payload carries no workspace_id", ErrMalformedPayload)
	}
	payloadWS, err := uuid.Parse(envelope.WorkspaceID)
	if err != nil {
		return fmt.Errorf("%w: workspace_id is not a UUID: %w", ErrMalformedPayload, err)
	}
	if payloadWS != ws {
		return fmt.Errorf("%w: payload workspace does not match the row's", ErrMalformedPayload)
	}
	return nil
}

// replayKey is the queue-level dedup identifier for replaying one row. Derived
// from the row id alone, so every enqueue attempt for the same dead letter
// produces the same key and the queue collapses duplicates on its own.
//
// This is NOT the exactly-once guarantee — the row claim is, and it holds even
// against a queue that has forgotten the key (asynq only reserves a task id for
// its retention window). Two independent guards, and the durable one is the row.
func replayKey(id uuid.UUID) string { return "deadletter-replay:" + id.String() }

func isKnownStatus(s string) bool {
	switch s {
	case StatusPending, StatusReplayed, StatusDiscarded:
		return true
	default:
		return false
	}
}
