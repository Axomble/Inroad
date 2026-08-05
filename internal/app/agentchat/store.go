package agentchat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Store-level sentinels: constraint outcomes translated so the service and the
// run loop can act on them without importing pgx.
var (
	// ErrThreadNotFound is returned when a thread id does not resolve inside
	// the caller's workspace AND ownership. The two cases are deliberately
	// indistinguishable: another user's thread must 404, not 403, or its
	// existence leaks.
	ErrThreadNotFound = errors.New("agentchat: thread not found")
	// ErrRunActive is the uq_agent_runs_one_active_per_thread violation: the
	// thread already has a live run, so the caller's message stays queued.
	// This is an expected outcome of a concurrent send, not a failure.
	ErrRunActive = errors.New("agentchat: thread already has an active run")
	// ErrQueueEmpty is returned when a promotion found nothing to promote.
	ErrQueueEmpty = errors.New("agentchat: no queued message")
)

// PartInput is one part to persist alongside a message.
type PartInput struct {
	Type       string
	Text       string
	Reasoning  string
	ToolName   string
	ToolCallID string
	ToolInput  json.RawMessage
	ToolOutput json.RawMessage
	State      string
	Error      string
}

// MessageInput is a message and its parts, written atomically. The run loop
// persists one of these per provider turn rather than only at the end of the
// run, so a paused or crashed run leaves a transcript that can be replayed
// verbatim — which is what makes the A4 resume cheap.
type MessageInput struct {
	WorkspaceID     uuid.UUID
	ThreadID        uuid.UUID
	TurnID          uuid.UUID
	Role            string
	Status          string
	ModelSelector   string
	BrowsingContext []byte
	Parts           []PartInput
}

// Message is a persisted message with its parts, as the service and the run
// loop consume it.
type Message struct {
	Row   gen.AgentMessage
	Parts []gen.AgentMessagePart
}

// Store is the repository interface this domain depends on. It is defined here
// (by the consumer) so the service and the run loop can be unit-tested against
// a fake with no database.
//
// Every method takes workspaceID explicitly and every underlying statement
// pins it (security invariant 4). Thread reads additionally take userID:
// threads are owner-scoped within the workspace (spec §7.7), so the ownership
// filter lives in SQL rather than in a Go check a caller could forget.
type Store interface {
	CreateThread(ctx context.Context, workspaceID, userID uuid.UUID, title string) (gen.AgentThread, error)
	GetThread(ctx context.Context, workspaceID, userID, id uuid.UUID) (gen.AgentThread, error)
	ListThreads(ctx context.Context, workspaceID, userID uuid.UUID, offset, limit int32) ([]gen.AgentThread, error)
	RenameThread(ctx context.Context, workspaceID, userID, id uuid.UUID, title string) (gen.AgentThread, error)
	SoftDeleteThread(ctx context.Context, workspaceID, userID, id uuid.UUID) error
	// SetThreadTitle writes a GENERATED title. It is a no-op once the thread
	// has a title, so a user rename always wins over a late model answer.
	SetThreadTitle(ctx context.Context, workspaceID, id uuid.UUID, title string) (bool, error)
	AddThreadUsage(ctx context.Context, workspaceID, id uuid.UUID, inputTokens, outputTokens int64, contextWindow int32) error
	// SetThreadActiveRun points the thread at its live run; uuid.Nil clears it.
	SetThreadActiveRun(ctx context.Context, workspaceID, id, runID uuid.UUID) error

	// PersistMessage writes a message and all of its parts in ONE transaction,
	// so a transcript never contains a message with half its parts.
	PersistMessage(ctx context.Context, in MessageInput) (Message, error)
	// ListMessages returns the full thread transcript including queued
	// messages (the read model behind GET /agent/threads/{id}).
	ListMessages(ctx context.Context, workspaceID, threadID uuid.UUID) ([]Message, error)
	// ListTranscript returns only the messages the provider should see —
	// everything except messages still waiting their turn.
	ListTranscript(ctx context.Context, workspaceID, threadID uuid.UUID) ([]Message, error)
	ListQueued(ctx context.Context, workspaceID, threadID uuid.UUID) ([]Message, error)
	// PromoteOldestQueued moves the oldest queued message into the run that is
	// about to answer it, returning ErrQueueEmpty when there is none.
	PromoteOldestQueued(ctx context.Context, workspaceID, threadID uuid.UUID) (gen.AgentMessage, error)
	FinishProcessing(ctx context.Context, workspaceID, threadID uuid.UUID) error
	DeleteQueued(ctx context.Context, workspaceID, threadID, id uuid.UUID) error

	// InsertRun starts a run, returning ErrRunActive when the thread already
	// has one (the database enforces this, not a read-then-write).
	InsertRun(ctx context.Context, workspaceID, threadID uuid.UUID, modelID string) (gen.AgentRun, error)
	GetRun(ctx context.Context, workspaceID, id uuid.UUID) (gen.AgentRun, error)
	GetActiveRun(ctx context.Context, workspaceID, threadID uuid.UUID) (gen.AgentRun, error)
	SetRunModel(ctx context.Context, workspaceID, id uuid.UUID, modelID string) error
	FinishRun(ctx context.Context, workspaceID, id uuid.UUID, status, errMsg string) error
	// PauseRunForApproval parks a run awaiting a human decision. The run stays
	// LIVE, so no new run can start on the thread while it waits (A4 seam).
	PauseRunForApproval(ctx context.Context, workspaceID, id uuid.UUID) error
	// RecoverStuckRuns fails every run left 'running' by a dead process and
	// closes out the messages they were answering. Deployment-scoped
	// infrastructure repair run once at startup — hence no workspace filter.
	RecoverStuckRuns(ctx context.Context, reason string) (int64, error)
}

// PgStore implements Store over sqlc-generated queries. It is the only place
// in this domain that knows about gen.Queries or Postgres error codes.
type PgStore struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

func NewPgStore(pool *pgxpool.Pool) *PgStore {
	return &PgStore{pool: pool, q: gen.New(pool)}
}

// ---- threads ---------------------------------------------------------------

func (s *PgStore) CreateThread(ctx context.Context, workspaceID, userID uuid.UUID, title string) (gen.AgentThread, error) {
	return s.q.InsertAgentThread(ctx, gen.InsertAgentThreadParams{
		WorkspaceID: workspaceID, CreatedByUserID: userID, Title: title,
	})
}

func (s *PgStore) GetThread(ctx context.Context, workspaceID, userID, id uuid.UUID) (gen.AgentThread, error) {
	row, err := s.q.GetAgentThread(ctx, gen.GetAgentThreadParams{
		WorkspaceID: workspaceID, CreatedByUserID: userID, ID: id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return gen.AgentThread{}, ErrThreadNotFound
	}
	return row, err
}

func (s *PgStore) ListThreads(ctx context.Context, workspaceID, userID uuid.UUID, offset, limit int32) ([]gen.AgentThread, error) {
	return s.q.ListAgentThreads(ctx, gen.ListAgentThreadsParams{
		WorkspaceID: workspaceID, CreatedByUserID: userID, Offset: offset, Limit: limit,
	})
}

func (s *PgStore) RenameThread(ctx context.Context, workspaceID, userID, id uuid.UUID, title string) (gen.AgentThread, error) {
	row, err := s.q.RenameAgentThread(ctx, gen.RenameAgentThreadParams{
		WorkspaceID: workspaceID, CreatedByUserID: userID, ID: id, Title: title,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return gen.AgentThread{}, ErrThreadNotFound
	}
	return row, err
}

func (s *PgStore) SoftDeleteThread(ctx context.Context, workspaceID, userID, id uuid.UUID) error {
	rows, err := s.q.SoftDeleteAgentThread(ctx, gen.SoftDeleteAgentThreadParams{
		WorkspaceID: workspaceID, CreatedByUserID: userID, ID: id,
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrThreadNotFound
	}
	return nil
}

func (s *PgStore) SetThreadTitle(ctx context.Context, workspaceID, id uuid.UUID, title string) (bool, error) {
	// Zero rows means the thread already had a title (a user rename won the
	// race) — the intended no-op, not an error.
	rows, err := s.q.SetAgentThreadTitle(ctx, gen.SetAgentThreadTitleParams{
		WorkspaceID: workspaceID, ID: id, Title: title,
	})
	return rows > 0, err
}

func (s *PgStore) AddThreadUsage(ctx context.Context, workspaceID, id uuid.UUID, inputTokens, outputTokens int64, contextWindow int32) error {
	return s.q.AddAgentThreadUsage(ctx, gen.AddAgentThreadUsageParams{
		WorkspaceID: workspaceID, ID: id,
		TotalInputTokens: inputTokens, TotalOutputTokens: outputTokens,
		ContextWindowTokens: contextWindow,
	})
}

func (s *PgStore) SetThreadActiveRun(ctx context.Context, workspaceID, id, runID uuid.UUID) error {
	return s.q.SetAgentThreadActiveRun(ctx, gen.SetAgentThreadActiveRunParams{
		WorkspaceID: workspaceID, ID: id,
		ActiveRunID: pgtype.UUID{Bytes: runID, Valid: runID != uuid.Nil},
	})
}

// ---- messages & parts ------------------------------------------------------

func (s *PgStore) PersistMessage(ctx context.Context, in MessageInput) (Message, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Message{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	message, err := s.persistMessageTx(ctx, s.q.WithTx(tx), in)
	if err != nil {
		return Message{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Message{}, err
	}
	return message, nil
}

func (s *PgStore) persistMessageTx(ctx context.Context, qtx *gen.Queries, in MessageInput) (Message, error) {
	row, err := qtx.InsertAgentMessage(ctx, gen.InsertAgentMessageParams{
		WorkspaceID: in.WorkspaceID,
		// The generated field is named ID because the statement is an
		// INSERT ... SELECT over agent_threads; it binds the THREAD id, and
		// the statement emits zero rows for a thread outside this workspace.
		ID:              in.ThreadID,
		TurnID:          in.TurnID,
		Role:            in.Role,
		Status:          in.Status,
		ModelSelector:   in.ModelSelector,
		BrowsingContext: in.BrowsingContext,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Message{}, ErrThreadNotFound
	}
	if err != nil {
		return Message{}, err
	}

	parts := make([]gen.AgentMessagePart, 0, len(in.Parts))
	for i, p := range in.Parts {
		part, err := qtx.InsertAgentMessagePart(ctx, gen.InsertAgentMessagePartParams{
			WorkspaceID:      in.WorkspaceID,
			ID:               row.ID, // bound as the MESSAGE id (INSERT ... SELECT)
			OrderIndex:       int32(i),
			Type:             p.Type,
			TextContent:      p.Text,
			ReasoningContent: p.Reasoning,
			ToolName:         p.ToolName,
			ToolCallID:       p.ToolCallID,
			ToolInput:        p.ToolInput,
			ToolOutput:       p.ToolOutput,
			State:            p.State,
			ErrorMessage:     p.Error,
		})
		if err != nil {
			return Message{}, fmt.Errorf("agentchat: insert part %d: %w", i, err)
		}
		parts = append(parts, part)
	}
	return Message{Row: row, Parts: parts}, nil
}

func (s *PgStore) ListMessages(ctx context.Context, workspaceID, threadID uuid.UUID) ([]Message, error) {
	rows, err := s.q.ListAgentMessages(ctx, gen.ListAgentMessagesParams{WorkspaceID: workspaceID, ThreadID: threadID})
	if err != nil {
		return nil, err
	}
	return s.attachParts(ctx, workspaceID, threadID, rows)
}

func (s *PgStore) ListTranscript(ctx context.Context, workspaceID, threadID uuid.UUID) ([]Message, error) {
	rows, err := s.q.ListAgentTranscriptMessages(ctx, gen.ListAgentTranscriptMessagesParams{WorkspaceID: workspaceID, ThreadID: threadID})
	if err != nil {
		return nil, err
	}
	return s.attachParts(ctx, workspaceID, threadID, rows)
}

func (s *PgStore) ListQueued(ctx context.Context, workspaceID, threadID uuid.UUID) ([]Message, error) {
	rows, err := s.q.ListQueuedAgentMessages(ctx, gen.ListQueuedAgentMessagesParams{WorkspaceID: workspaceID, ThreadID: threadID})
	if err != nil {
		return nil, err
	}
	return s.attachParts(ctx, workspaceID, threadID, rows)
}

// attachParts fans one thread-wide part read out over the given messages,
// rather than issuing a query per message.
func (s *PgStore) attachParts(ctx context.Context, workspaceID, threadID uuid.UUID, rows []gen.AgentMessage) ([]Message, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	parts, err := s.q.ListAgentMessagePartsByThread(ctx, gen.ListAgentMessagePartsByThreadParams{
		WorkspaceID: workspaceID, ThreadID: threadID,
	})
	if err != nil {
		return nil, err
	}
	byMessage := make(map[uuid.UUID][]gen.AgentMessagePart, len(rows))
	for _, p := range parts {
		byMessage[p.MessageID] = append(byMessage[p.MessageID], p)
	}
	out := make([]Message, len(rows))
	for i, r := range rows {
		out[i] = Message{Row: r, Parts: byMessage[r.ID]}
	}
	return out, nil
}

func (s *PgStore) PromoteOldestQueued(ctx context.Context, workspaceID, threadID uuid.UUID) (gen.AgentMessage, error) {
	row, err := s.q.PromoteOldestQueuedAgentMessage(ctx, gen.PromoteOldestQueuedAgentMessageParams{
		WorkspaceID: workspaceID, ThreadID: threadID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return gen.AgentMessage{}, ErrQueueEmpty
	}
	return row, err
}

func (s *PgStore) FinishProcessing(ctx context.Context, workspaceID, threadID uuid.UUID) error {
	_, err := s.q.FinishProcessingAgentMessages(ctx, gen.FinishProcessingAgentMessagesParams{
		WorkspaceID: workspaceID, ThreadID: threadID,
	})
	return err
}

func (s *PgStore) DeleteQueued(ctx context.Context, workspaceID, threadID, id uuid.UUID) error {
	rows, err := s.q.DeleteQueuedAgentMessage(ctx, gen.DeleteQueuedAgentMessageParams{
		WorkspaceID: workspaceID, ThreadID: threadID, ID: id,
	})
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrQueueEmpty
	}
	return nil
}

// ---- runs ------------------------------------------------------------------

func (s *PgStore) InsertRun(ctx context.Context, workspaceID, threadID uuid.UUID, modelID string) (gen.AgentRun, error) {
	row, err := s.q.InsertAgentRun(ctx, gen.InsertAgentRunParams{
		WorkspaceID: workspaceID,
		ID:          threadID, // bound as the THREAD id (INSERT ... SELECT)
		ModelID:     modelID,
	})
	switch {
	case isUniqueViolation(err):
		return gen.AgentRun{}, ErrRunActive
	case errors.Is(err, pgx.ErrNoRows):
		return gen.AgentRun{}, ErrThreadNotFound
	}
	return row, err
}

func (s *PgStore) GetRun(ctx context.Context, workspaceID, id uuid.UUID) (gen.AgentRun, error) {
	return s.q.GetAgentRun(ctx, gen.GetAgentRunParams{WorkspaceID: workspaceID, ID: id})
}

func (s *PgStore) GetActiveRun(ctx context.Context, workspaceID, threadID uuid.UUID) (gen.AgentRun, error) {
	return s.q.GetActiveAgentRun(ctx, gen.GetActiveAgentRunParams{WorkspaceID: workspaceID, ThreadID: threadID})
}

func (s *PgStore) SetRunModel(ctx context.Context, workspaceID, id uuid.UUID, modelID string) error {
	return s.q.SetAgentRunModel(ctx, gen.SetAgentRunModelParams{WorkspaceID: workspaceID, ID: id, ModelID: modelID})
}

func (s *PgStore) FinishRun(ctx context.Context, workspaceID, id uuid.UUID, status, errMsg string) error {
	// Zero rows means the run already reached a terminal state (a stop racing
	// a natural completion) — first writer wins, and that is not an error.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	if _, err := qtx.FinishAgentRun(ctx, gen.FinishAgentRunParams{
		WorkspaceID: workspaceID, ID: id, Status: status, Error: errMsg,
	}); err != nil {
		return err
	}
	if err := qtx.ClearAgentThreadActiveRun(ctx, gen.ClearAgentThreadActiveRunParams{
		WorkspaceID: workspaceID, ActiveRunID: pgtype.UUID{Bytes: id, Valid: true},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PgStore) PauseRunForApproval(ctx context.Context, workspaceID, id uuid.UUID) error {
	_, err := s.q.PauseAgentRunForApproval(ctx, gen.PauseAgentRunForApprovalParams{WorkspaceID: workspaceID, ID: id})
	return err
}

func (s *PgStore) RecoverStuckRuns(ctx context.Context, reason string) (int64, error) {
	runs, err := s.q.FailStuckAgentRuns(ctx, reason)
	if err != nil {
		return 0, err
	}
	if _, err := s.q.ResetStuckAgentMessages(ctx); err != nil {
		return runs, err
	}
	if _, err := s.q.ClearStuckAgentThreadActiveRuns(ctx); err != nil {
		return runs, err
	}
	return runs, nil
}

// isUniqueViolation reports SQLSTATE 23505 (unique_violation).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// Compile-time proof the sqlc-backed store satisfies the domain's seam.
var _ Store = (*PgStore)(nil)
