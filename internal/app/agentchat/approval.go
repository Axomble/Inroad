package agentchat

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	ActionStatusPending  = "pending"
	ActionStatusApproved = "approved"
	ActionStatusRejected = "rejected"
	ActionStatusExpired  = "expired"
	ActionStatusExecuted = "executed"
	ActionStatusFailed   = "failed"

	ApprovalDecisionApprove = "approve"
	ApprovalDecisionReject  = "reject"
)

var (
	ErrActionNotFound = errors.New("agentchat: pending action not found")
	ErrActionDecided  = errors.New("agentchat: pending action already decided")
)

type PendingAction struct {
	ID              uuid.UUID
	WorkspaceID     uuid.UUID
	ThreadID        uuid.UUID
	RunID           uuid.UUID
	MessageID       uuid.UUID
	MessagePartID   uuid.UUID
	TurnID          uuid.UUID
	CreatedByUserID uuid.UUID
	ActorRole       string
	ToolName        string
	ToolCallID      string
	Arguments       json.RawMessage
	EditedArguments json.RawMessage
	RiskTier        string
	Status          string
	DecisionReason  string
	DecidedByUserID *uuid.UUID
	DecidedAt       *time.Time
	ExpiresAt       time.Time
	Result          json.RawMessage
	ErrorMessage    string
	ExecutedAt      *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (a PendingAction) EffectiveArguments() json.RawMessage {
	if len(a.EditedArguments) > 0 {
		return a.EditedArguments
	}
	return a.Arguments
}

type ApprovalRequest struct {
	ToolName   string
	ToolCallID string
	Arguments  json.RawMessage
	RiskTier   string
	ExpiresAt  time.Time
}

type ApprovalCall struct {
	PartID     uuid.UUID
	ToolName   string
	ToolCallID string
	Arguments  json.RawMessage
	Action     *PendingAction
}

type ApprovalBatch struct {
	Calls []ApprovalCall
}

type ApprovalResult struct {
	PartID       uuid.UUID
	ToolName     string
	ToolCallID   string
	ToolInput    json.RawMessage
	ToolOutput   json.RawMessage
	IsError      bool
	ErrorMessage string
	Action       *PendingAction
}

type ApprovalDecision struct {
	Decision        string
	EditedArguments json.RawMessage
	Reason          string
}

type ApprovalStore interface {
	PauseForApproval(context.Context, MessageInput, RunStart, []ApprovalRequest) ([]PendingAction, error)
	ListPendingActions(context.Context, Actor, string, int32) ([]PendingAction, error)
	GetPendingAction(context.Context, Actor, uuid.UUID) (PendingAction, error)
	DecidePendingAction(context.Context, Actor, uuid.UUID, ApprovalDecision) (PendingAction, *RunStart, error)
	ExpirePendingActions(context.Context, int32) ([]RunStart, error)
	LoadApprovalBatch(context.Context, RunStart) (ApprovalBatch, error)
	CompleteApprovalBatch(context.Context, RunStart, []ApprovalResult) error
	CancelApprovalRun(context.Context, Actor, uuid.UUID, string) error
}

type PendingActionDTO struct {
	ID              string          `json:"id"`
	WorkspaceID     string          `json:"workspace_id"`
	ThreadID        string          `json:"thread_id"`
	RunID           string          `json:"run_id"`
	ToolName        string          `json:"tool_name"`
	ToolCallID      string          `json:"tool_call_id"`
	Arguments       json.RawMessage `json:"arguments"`
	EditedArguments json.RawMessage `json:"edited_arguments,omitempty"`
	RiskTier        string          `json:"risk_tier"`
	Status          string          `json:"status"`
	DecisionReason  string          `json:"decision_reason,omitempty"`
	ExpiresAt       string          `json:"expires_at"`
	Result          json.RawMessage `json:"result,omitempty"`
	Error           string          `json:"error,omitempty"`
	CreatedAt       string          `json:"created_at"`
	UpdatedAt       string          `json:"updated_at"`
}

func pendingActionDTO(action PendingAction) PendingActionDTO {
	return PendingActionDTO{
		ID: action.ID.String(), WorkspaceID: action.WorkspaceID.String(),
		ThreadID: action.ThreadID.String(), RunID: action.RunID.String(),
		ToolName: action.ToolName, ToolCallID: action.ToolCallID,
		Arguments: action.Arguments, EditedArguments: action.EditedArguments,
		RiskTier: action.RiskTier, Status: action.Status,
		DecisionReason: action.DecisionReason, ExpiresAt: rfc3339(action.ExpiresAt),
		Result: action.Result, Error: action.ErrorMessage,
		CreatedAt: rfc3339(action.CreatedAt), UpdatedAt: rfc3339(action.UpdatedAt),
	}
}
