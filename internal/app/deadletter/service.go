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
	// ErrBadCursor is returned for a page cursor this server did not mint, or
	// minted for a different status filter. Separate from ErrValidation because
	// it maps to 400 rather than 422: a cursor is opaque machine state, so a bad
	// one is a malformed request rather than a value the operator chose wrongly.
	// It is never silently ignored — see decodeCursor.
	ErrBadCursor = errors.New("deadletter: bad cursor")
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
//
// The cap silently shortening a page is only safe because NextCursor reports
// whether more rows follow. Under the previous LIMIT/OFFSET surface it was not:
// the client had to infer "more exist" from the page being as long as it asked
// for, so a clamped request looked identical to the end of the list.
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

// ListParams is the read-side request: which lifecycle state, how many rows, and
// where to resume. Kept as a struct rather than positional arguments so a caller
// cannot transpose them.
type ListParams struct {
	Status string
	Limit  int32
	// Cursor is an opaque token from a previous page's NextCursor, empty for the
	// first page. It is only valid under the SAME Status it was minted for.
	Cursor string
}

// Page is one keyset page. NextCursor is empty exactly when this is the last
// page — never "the page looked short", which the client cannot distinguish
// from a clamp (and could not, before this: the SPA inferred "more exist" from
// len(rows) == the limit it asked for, so the service silently capping a
// 250-row request at 200 made the Load-more button vanish and left every row
// past 200 permanently unreachable, with no error anywhere).
type Page struct {
	Items      []gen.TaskDeadLetter
	NextCursor string
}

// List returns one page of the workspace's dead letters, newest first. An empty
// Status means any; any other value must be one of the three known statuses, so
// a typo'd filter is a 422 rather than a silently empty list.
//
// The cursor is decoded AFTER the status is validated, so an unknown status is
// reported as such even when the request also carries a cursor — the status is
// what the operator typed, the cursor is machine state derived from it.
func (s *Service) List(ctx context.Context, ws uuid.UUID, p ListParams) (Page, error) {
	if p.Status != "" && !isKnownStatus(p.Status) {
		return Page{}, fmt.Errorf("%w: unknown status %q", ErrValidation, p.Status)
	}
	if p.Limit <= 0 {
		p.Limit = defaultLimit
	}
	if p.Limit > maxLimit {
		p.Limit = maxLimit
	}

	q := ListQuery{Status: p.Status, Limit: p.Limit}
	if p.Cursor != "" {
		cur, err := decodeCursor(p.Status, p.Cursor)
		if err != nil {
			return Page{}, err
		}
		q.Cursor = &cur
	}

	// One row beyond the page. Its PRESENCE is what proves another page exists —
	// not the page being full, which is only a guess (a page can be exactly full
	// with nothing after it) and not a count, which would cost a second query on
	// every request. Lookahead rather than crm's lastOfFullPage precisely because
	// lastOfFullPage hands out a cursor for any full page, so the last full page
	// leads the client to a phantom empty one.
	q.Limit = p.Limit + 1
	rows, err := s.store.List(ctx, ws, q)
	if err != nil {
		return Page{}, fmt.Errorf("deadletter: list: %w", err)
	}

	page := Page{Items: rows}
	if int32(len(rows)) > p.Limit {
		page.Items = rows[:p.Limit]
		// Minted from the last KEPT row, not the lookahead row: the next page
		// must START at the lookahead row, and the seek is strictly-after.
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeCursor(p.Status, Cursor{CreatedAt: last.CreatedAt.Time, ID: last.ID})
	}
	return page, nil
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
		// exists to make findable. The payload is NOT logged: it carries
		// recipient addresses, and a log sink is a different audience from the
		// tenant this API answers to.
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
