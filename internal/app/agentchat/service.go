package agentchat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

var ErrValidation = errors.New("agentchat: invalid request")

const (
	defaultThreadLimit = int32(30)
	maxThreadLimit     = int32(100)
	maxMessageChars    = 20_000
	maxTitleChars      = 120
	// streamHeartbeat is how often an idle SSE connection gets a comment line,
	// short enough for the 30-60s idle timeouts proxies ship with.
	streamHeartbeat = 20 * time.Second
	// maxStreamsPerUser caps concurrent SSE attachments for one user. Each one
	// holds a goroutine and a fan-out slot for as long as its panel is open, so
	// a client stuck in a reconnect loop must not be able to accumulate them
	// without bound. A handful covers the real case (a few tabs, plus the
	// overlap while a reconnect replaces a dead connection).
	maxStreamsPerUser = 8
)

// ErrStreamLimit is returned when a user already holds the maximum number of
// concurrent streams. It is transient by nature — closing a tab frees a slot.
var ErrStreamLimit = errors.New("agentchat: too many concurrent streams for this user")

type Actor struct {
	WorkspaceID uuid.UUID
	UserID      uuid.UUID
	Role        string
}

type RunStart struct {
	Actor    Actor
	ThreadID uuid.UUID
	RunID    uuid.UUID
	TurnID   uuid.UUID
	Selector string
	// MessageID is set only when resuming from an approval pause: it is the
	// assistant message THAT pause parked. A run can pause more than once, so
	// the resume must replay the calls of the pause it is resuming and no
	// others — replaying by run id re-executes every earlier pause's
	// non-gated tool calls and strands the approval that was just granted.
	MessageID uuid.UUID
}

type RunManager interface {
	Start(RunStart)
	Stop(context.Context, uuid.UUID) error
}

type ApprovalRunManager interface {
	Resume(RunStart)
	ContinueQueue(RunStart)
	ValidateApproval(Actor, string, []byte) error
}

type StreamTransport interface {
	Attach(context.Context, uuid.UUID, int64) (<-chan Frame, error)
	Publish(context.Context, uuid.UUID, Event) (int64, error)
}

type Service struct {
	store     Store
	runs      RunManager
	streams   StreamTransport
	approvals ApprovalStore

	// mu guards openStreams, the per-user count of live SSE attachments on
	// this node.
	mu          sync.Mutex
	openStreams map[uuid.UUID]int
}

func NewService(store Store, runs RunManager, streams StreamTransport) *Service {
	approvals, _ := store.(ApprovalStore)
	return &Service{
		store: store, runs: runs, streams: streams, approvals: approvals,
		openStreams: map[uuid.UUID]int{},
	}
}

func (s *Service) ListPendingActions(ctx context.Context, actor Actor, status string, limit int32) ([]PendingActionDTO, error) {
	if s.approvals == nil {
		return nil, errors.New("agent approvals are unavailable")
	}
	if limit == 0 {
		limit = defaultThreadLimit
	}
	if limit < 1 || limit > maxThreadLimit {
		return nil, fmt.Errorf("%w: limit must be between 1 and %d", ErrValidation, maxThreadLimit)
	}
	allowed := map[string]bool{
		"": true, ActionStatusPending: true, ActionStatusApproved: true,
		ActionStatusRejected: true, ActionStatusExpired: true,
		ActionStatusExecuted: true, ActionStatusFailed: true,
	}
	if !allowed[status] {
		return nil, fmt.Errorf("%w: invalid action status", ErrValidation)
	}
	actions, err := s.approvals.ListPendingActions(ctx, actor, status, limit)
	if err != nil {
		return nil, err
	}
	out := make([]PendingActionDTO, len(actions))
	for i, action := range actions {
		out[i] = pendingActionDTO(action)
	}
	return out, nil
}

func (s *Service) GetPendingAction(ctx context.Context, actor Actor, id uuid.UUID) (PendingActionDTO, error) {
	if s.approvals == nil {
		return PendingActionDTO{}, errors.New("agent approvals are unavailable")
	}
	action, err := s.approvals.GetPendingAction(ctx, actor, id)
	if err != nil {
		return PendingActionDTO{}, err
	}
	return pendingActionDTO(action), nil
}

func (s *Service) DecidePendingAction(ctx context.Context, actor Actor, id uuid.UUID, in ApprovalDecision) (PendingActionDTO, error) {
	if s.approvals == nil {
		return PendingActionDTO{}, errors.New("agent approvals are unavailable")
	}
	if in.Decision != ApprovalDecisionApprove && in.Decision != ApprovalDecisionReject {
		return PendingActionDTO{}, fmt.Errorf("%w: decision must be approve or reject", ErrValidation)
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Decision == ApprovalDecisionReject && (in.Reason == "" || len(in.Reason) > 1000) {
		return PendingActionDTO{}, fmt.Errorf("%w: rejection reason must be 1-1000 characters", ErrValidation)
	}
	if len(in.EditedArguments) > 0 {
		var object map[string]json.RawMessage
		if json.Unmarshal(in.EditedArguments, &object) != nil || object == nil {
			return PendingActionDTO{}, fmt.Errorf("%w: edited_arguments must be a JSON object", ErrValidation)
		}
	}
	action, err := s.approvals.GetPendingAction(ctx, actor, id)
	if err != nil {
		return PendingActionDTO{}, err
	}
	if in.Decision == ApprovalDecisionApprove {
		arguments := action.Arguments
		if len(in.EditedArguments) > 0 {
			arguments = in.EditedArguments
		}
		runner, ok := s.runs.(ApprovalRunManager)
		if !ok {
			return PendingActionDTO{}, errors.New("agent approval runner is unavailable")
		}
		if err := runner.ValidateApproval(actor, action.ToolName, arguments); err != nil {
			return PendingActionDTO{}, fmt.Errorf("%w: %w", ErrValidation, err)
		}
	}
	updated, start, err := s.approvals.DecidePendingAction(ctx, actor, id, in)
	if err != nil {
		return PendingActionDTO{}, err
	}
	_, _ = s.streams.Publish(ctx, updated.ThreadID, Event{
		Type: EventMessagePersisted, RunID: updated.RunID.String(),
	})
	if start != nil {
		if runner, ok := s.runs.(ApprovalRunManager); ok {
			runner.Resume(*start)
		}
	}
	return pendingActionDTO(updated), nil
}

func (s *Service) CreateThread(ctx context.Context, actor Actor) (ThreadDTO, error) {
	row, err := s.store.CreateThread(ctx, actor.WorkspaceID, actor.UserID, "")
	if err != nil {
		return ThreadDTO{}, err
	}
	return threadDTO(row, nil), nil
}

func (s *Service) GetThread(ctx context.Context, actor Actor, id uuid.UUID) (ThreadDTO, error) {
	row, err := s.store.GetThread(ctx, actor.WorkspaceID, actor.UserID, id)
	if err != nil {
		return ThreadDTO{}, err
	}
	messages, err := s.store.ListMessages(ctx, actor.WorkspaceID, id)
	if err != nil {
		return ThreadDTO{}, err
	}
	return threadDTO(row, messages), nil
}

func (s *Service) ListThreads(ctx context.Context, actor Actor, offset, limit int32) ([]ThreadDTO, error) {
	if offset < 0 {
		return nil, fmt.Errorf("%w: offset must be non-negative", ErrValidation)
	}
	if limit == 0 {
		limit = defaultThreadLimit
	}
	if limit < 0 || limit > maxThreadLimit {
		return nil, fmt.Errorf("%w: limit must be between 1 and %d", ErrValidation, maxThreadLimit)
	}
	rows, err := s.store.ListThreads(ctx, actor.WorkspaceID, actor.UserID, offset, limit)
	if err != nil {
		return nil, err
	}
	out := make([]ThreadDTO, len(rows))
	for i, row := range rows {
		out[i] = threadDTO(row, nil)
	}
	return out, nil
}

func (s *Service) RenameThread(ctx context.Context, actor Actor, id uuid.UUID, title string) (ThreadDTO, error) {
	title = strings.TrimSpace(title)
	if title == "" || len(title) > maxTitleChars {
		return ThreadDTO{}, fmt.Errorf("%w: title must be 1-%d characters", ErrValidation, maxTitleChars)
	}
	row, err := s.store.RenameThread(ctx, actor.WorkspaceID, actor.UserID, id, title)
	if err != nil {
		return ThreadDTO{}, err
	}
	return threadDTO(row, nil), nil
}

func (s *Service) DeleteThread(ctx context.Context, actor Actor, id uuid.UUID) error {
	if _, err := s.store.GetThread(ctx, actor.WorkspaceID, actor.UserID, id); err != nil {
		return err
	}
	if run, err := s.store.GetActiveRun(ctx, actor.WorkspaceID, id); err == nil {
		if run.Status == RunStatusPausedApproval && s.approvals != nil {
			if err := s.approvals.CancelApprovalRun(ctx, actor, run.ID, "Thread deleted while awaiting approval."); err != nil {
				return err
			}
		} else {
			if err := s.runs.Stop(ctx, run.ID); err != nil {
				return err
			}
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return s.store.SoftDeleteThread(ctx, actor.WorkspaceID, actor.UserID, id)
}

type SendInput struct {
	Text            string
	Model           string
	BrowsingContext *BrowsingContext
}

func (s *Service) Send(ctx context.Context, actor Actor, threadID uuid.UUID, in SendInput) (SendResultDTO, error) {
	if _, err := s.store.GetThread(ctx, actor.WorkspaceID, actor.UserID, threadID); err != nil {
		return SendResultDTO{}, err
	}
	in.Text = strings.TrimSpace(in.Text)
	if in.Text == "" || len(in.Text) > maxMessageChars {
		return SendResultDTO{}, fmt.Errorf("%w: message must be 1-%d characters", ErrValidation, maxMessageChars)
	}
	selector := strings.TrimSpace(in.Model)
	if selector == "" {
		selector = "default-smart-model"
	}
	var browsing []byte
	var err error
	if in.BrowsingContext != nil {
		browsing, err = json.Marshal(in.BrowsingContext)
		if err != nil {
			return SendResultDTO{}, fmt.Errorf("%w: invalid browsing_context", ErrValidation)
		}
	}
	turnID := uuid.New()
	message, err := s.store.PersistMessage(ctx, MessageInput{
		WorkspaceID: actor.WorkspaceID, ThreadID: threadID, TurnID: turnID,
		Role: "user", Status: MessageStatusQueued, ModelSelector: selector, BrowsingContext: browsing,
		Parts: []PartInput{{Type: PartText, Text: in.Text}},
	})
	if err != nil {
		return SendResultDTO{}, err
	}
	run, err := s.store.InsertRun(ctx, actor.WorkspaceID, threadID, selector)
	if errors.Is(err, ErrRunActive) {
		s.publishQueue(ctx, actor.WorkspaceID, threadID)
		return SendResultDTO{MessageID: message.Row.ID.String(), Queued: true}, nil
	}
	if err != nil {
		_ = s.store.DeleteQueued(ctx, actor.WorkspaceID, threadID, message.Row.ID)
		return SendResultDTO{}, err
	}
	promoted, err := s.store.PromoteOldestQueued(ctx, actor.WorkspaceID, threadID)
	if err != nil {
		_ = s.store.FinishRun(ctx, actor.WorkspaceID, run.ID, RunStatusFailed, "message queue could not be promoted")
		_ = s.store.DeleteQueued(ctx, actor.WorkspaceID, threadID, message.Row.ID)
		return SendResultDTO{}, err
	}
	if err := s.store.SetThreadActiveRun(ctx, actor.WorkspaceID, threadID, run.ID); err != nil {
		_ = s.store.FinishRun(ctx, actor.WorkspaceID, run.ID, RunStatusFailed, "thread could not start")
		_ = s.store.FinishProcessing(ctx, actor.WorkspaceID, threadID)
		return SendResultDTO{}, err
	}
	s.runs.Start(RunStart{Actor: actor, ThreadID: threadID, RunID: run.ID, TurnID: promoted.TurnID, Selector: selector})
	if promoted.ID != message.Row.ID {
		s.publishQueue(ctx, actor.WorkspaceID, threadID)
		return SendResultDTO{MessageID: message.Row.ID.String(), Queued: true}, nil
	}
	runID := run.ID.String()
	return SendResultDTO{MessageID: message.Row.ID.String(), RunID: &runID}, nil
}

func (s *Service) ListQueue(ctx context.Context, actor Actor, threadID uuid.UUID) ([]QueuedMessageDTO, error) {
	if _, err := s.store.GetThread(ctx, actor.WorkspaceID, actor.UserID, threadID); err != nil {
		return nil, err
	}
	rows, err := s.store.ListQueued(ctx, actor.WorkspaceID, threadID)
	if err != nil {
		return nil, err
	}
	return queuedDTOs(rows), nil
}

func (s *Service) DeleteQueued(ctx context.Context, actor Actor, threadID, messageID uuid.UUID) error {
	if _, err := s.store.GetThread(ctx, actor.WorkspaceID, actor.UserID, threadID); err != nil {
		return err
	}
	if err := s.store.DeleteQueued(ctx, actor.WorkspaceID, threadID, messageID); err != nil {
		return err
	}
	s.publishQueue(ctx, actor.WorkspaceID, threadID)
	return nil
}

func (s *Service) publishQueue(ctx context.Context, workspaceID, threadID uuid.UUID) {
	queued, err := s.store.ListQueued(ctx, workspaceID, threadID)
	if err != nil {
		return
	}
	_, _ = s.streams.Publish(ctx, threadID, Event{Type: EventQueueUpdated, Queued: queuedDTOs(queued)})
}

func (s *Service) Stop(ctx context.Context, actor Actor, threadID uuid.UUID) error {
	if _, err := s.store.GetThread(ctx, actor.WorkspaceID, actor.UserID, threadID); err != nil {
		return err
	}
	run, err := s.store.GetActiveRun(ctx, actor.WorkspaceID, threadID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoActiveRun
	}
	if err != nil {
		return err
	}
	if run.Status == RunStatusPausedApproval && s.approvals != nil {
		if err := s.approvals.CancelApprovalRun(ctx, actor, run.ID, "Stopped by the user while awaiting approval."); err != nil {
			return err
		}
		_, _ = s.streams.Publish(ctx, threadID, Event{Type: EventMessagePersisted, RunID: run.ID.String()})
		_, _ = s.streams.Publish(ctx, threadID, Event{Type: EventDone, RunID: run.ID.String()})
		if runner, ok := s.runs.(ApprovalRunManager); ok {
			runner.ContinueQueue(RunStart{Actor: actor, ThreadID: threadID, RunID: run.ID})
		}
		return nil
	}
	return s.runs.Stop(ctx, run.ID)
}

func (s *Service) Attach(ctx context.Context, actor Actor, threadID uuid.UUID, after int64) (<-chan Frame, error) {
	if after < 0 {
		return nil, fmt.Errorf("%w: after sequence must be non-negative", ErrValidation)
	}
	if _, err := s.store.GetThread(ctx, actor.WorkspaceID, actor.UserID, threadID); err != nil {
		return nil, err
	}
	if !s.claimStream(actor.UserID) {
		return nil, ErrStreamLimit
	}
	frames, err := s.streams.Attach(ctx, threadID, after)
	if err != nil {
		s.releaseStream(actor.UserID)
		return nil, err
	}
	// The transport ties the stream's life to ctx, so releasing on ctx is the
	// same moment the goroutine behind it exits — no extra bookkeeping
	// goroutine, and no slot leaked by a client that vanishes mid-stream.
	context.AfterFunc(ctx, func() { s.releaseStream(actor.UserID) })
	return frames, nil
}

func (s *Service) claimStream(userID uuid.UUID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.openStreams[userID] >= maxStreamsPerUser {
		return false
	}
	s.openStreams[userID]++
	return true
}

func (s *Service) releaseStream(userID uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.openStreams[userID] <= 1 {
		delete(s.openStreams, userID)
		return
	}
	s.openStreams[userID]--
}

func threadDTO(row gen.AgentThread, messages []Message) ThreadDTO {
	out := ThreadDTO{
		ID: row.ID.String(), Title: row.Title,
		TotalInputTokens: row.TotalInputTokens, TotalOutputTokens: row.TotalOutputTokens,
		ContextWindowTokens: int(row.ContextWindowTokens), ActiveRunID: pgUUIDPtr(row.ActiveRunID),
		CreatedAt: pgTime(row.CreatedAt), UpdatedAt: pgTime(row.UpdatedAt),
	}
	if messages != nil {
		out.Messages = make([]MessageDTO, len(messages))
		for i, message := range messages {
			out.Messages[i] = messageDTO(message)
		}
	}
	return out
}

func messageDTO(message Message) MessageDTO {
	out := MessageDTO{
		ID: message.Row.ID.String(), TurnID: message.Row.TurnID.String(),
		Role: message.Row.Role, Status: message.Row.Status, CreatedAt: pgTime(message.Row.CreatedAt),
		Parts: make([]PartDTO, len(message.Parts)),
	}
	for i, part := range message.Parts {
		out.Parts[i] = PartDTO{
			ID: part.ID.String(), OrderIndex: int(part.OrderIndex), Type: part.Type,
			Text: part.TextContent, Reasoning: part.ReasoningContent,
			ToolName: part.ToolName, ToolCallID: part.ToolCallID,
			ToolInput: part.ToolInput, ToolOutput: part.ToolOutput,
			State: part.State, Error: part.ErrorMessage,
		}
	}
	return out
}

func queuedDTOs(messages []Message) []QueuedMessageDTO {
	out := make([]QueuedMessageDTO, 0, len(messages))
	for _, message := range messages {
		text := ""
		for _, part := range message.Parts {
			if part.Type == PartText {
				text += part.TextContent
			}
		}
		out = append(out, QueuedMessageDTO{
			ID: message.Row.ID.String(), Text: text, Model: message.Row.ModelSelector,
			CreatedAt: pgTime(message.Row.CreatedAt),
		})
	}
	return out
}

func pgTime(value pgtype.Timestamptz) string {
	if !value.Valid {
		return ""
	}
	return rfc3339(value.Time)
}

func pgUUIDPtr(value pgtype.UUID) *string {
	if !value.Valid {
		return nil
	}
	return uuidPtr(value.Bytes)
}
