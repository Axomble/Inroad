package agentchat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

const pendingActionColumns = `
id, workspace_id, thread_id, run_id, message_id, message_part_id, turn_id,
created_by_user_id, actor_role, tool_name, tool_call_id, arguments,
edited_arguments, risk_tier, status, decision_reason, decided_by_user_id,
decided_at, expires_at, result, error_message, executed_at, created_at, updated_at`

type rowScanner interface{ Scan(...any) error }

func scanPendingAction(row rowScanner) (PendingAction, error) {
	var action PendingAction
	var edited, result []byte
	var decidedBy pgtype.UUID
	var decidedAt, executedAt pgtype.Timestamptz
	err := row.Scan(
		&action.ID, &action.WorkspaceID, &action.ThreadID, &action.RunID,
		&action.MessageID, &action.MessagePartID, &action.TurnID,
		&action.CreatedByUserID, &action.ActorRole, &action.ToolName,
		&action.ToolCallID, &action.Arguments, &edited, &action.RiskTier,
		&action.Status, &action.DecisionReason, &decidedBy, &decidedAt,
		&action.ExpiresAt, &result, &action.ErrorMessage, &executedAt,
		&action.CreatedAt, &action.UpdatedAt,
	)
	if err != nil {
		return PendingAction{}, err
	}
	action.EditedArguments = edited
	action.Result = result
	if decidedBy.Valid {
		id := uuid.UUID(decidedBy.Bytes)
		action.DecidedByUserID = &id
	}
	if decidedAt.Valid {
		value := decidedAt.Time
		action.DecidedAt = &value
	}
	if executedAt.Valid {
		value := executedAt.Time
		action.ExecutedAt = &value
	}
	return action, nil
}

func (s *PgStore) PauseForApproval(
	ctx context.Context,
	messageInput MessageInput,
	start RunStart,
	requests []ApprovalRequest,
) ([]PendingAction, error) {
	if len(requests) == 0 {
		return nil, errors.New("agentchat: approval pause requires at least one action")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	message, err := s.persistMessageTx(ctx, s.q.WithTx(tx), messageInput)
	if err != nil {
		return nil, err
	}
	parts := make(map[string]uuid.UUID, len(message.Parts))
	for _, part := range message.Parts {
		if part.Type == PartToolCall {
			parts[part.ToolCallID] = part.ID
		}
	}
	result, err := tx.Exec(ctx, `
UPDATE agent_runs SET status = 'paused_approval'
WHERE workspace_id = $1 AND id = $2 AND status = 'running'`, start.Actor.WorkspaceID, start.RunID)
	if err != nil {
		return nil, err
	}
	if result.RowsAffected() != 1 {
		return nil, ErrRunActive
	}

	actions := make([]PendingAction, 0, len(requests))
	for _, request := range requests {
		partID, ok := parts[request.ToolCallID]
		if !ok {
			return nil, fmt.Errorf("agentchat: approval tool part %q not found", request.ToolCallID)
		}
		row := tx.QueryRow(ctx, `
INSERT INTO pending_actions (
    workspace_id, thread_id, run_id, message_id, message_part_id, turn_id,
    created_by_user_id, actor_role, tool_name, tool_call_id, arguments,
    risk_tier, expires_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
RETURNING `+pendingActionColumns,
			start.Actor.WorkspaceID, start.ThreadID, start.RunID, message.Row.ID,
			partID, start.TurnID, start.Actor.UserID, start.Actor.Role,
			request.ToolName, request.ToolCallID, request.Arguments,
			request.RiskTier, request.ExpiresAt)
		action, err := scanPendingAction(row)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO pending_action_audit (workspace_id, pending_action_id, actor_user_id, event)
VALUES ($1,$2,$3,'created')`, action.WorkspaceID, action.ID, action.CreatedByUserID); err != nil {
			return nil, err
		}
		actions = append(actions, action)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return actions, nil
}

func (s *PgStore) ListPendingActions(ctx context.Context, actor Actor, status string, limit int32) ([]PendingAction, error) {
	rows, err := s.pool.Query(ctx, `
SELECT `+pendingActionColumns+`
FROM pending_actions
WHERE workspace_id = $1 AND created_by_user_id = $2
  AND ($3 = '' OR status = $3)
ORDER BY CASE WHEN status = 'pending' THEN 0 ELSE 1 END, expires_at, created_at DESC
LIMIT $4`, actor.WorkspaceID, actor.UserID, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	actions := make([]PendingAction, 0)
	for rows.Next() {
		action, err := scanPendingAction(rows)
		if err != nil {
			return nil, err
		}
		actions = append(actions, action)
	}
	return actions, rows.Err()
}

func (s *PgStore) GetPendingAction(ctx context.Context, actor Actor, id uuid.UUID) (PendingAction, error) {
	action, err := scanPendingAction(s.pool.QueryRow(ctx, `
SELECT `+pendingActionColumns+` FROM pending_actions
WHERE workspace_id = $1 AND created_by_user_id = $2 AND id = $3`,
		actor.WorkspaceID, actor.UserID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return PendingAction{}, ErrActionNotFound
	}
	return action, err
}

func (s *PgStore) DecidePendingAction(
	ctx context.Context,
	actor Actor,
	id uuid.UUID,
	decision ApprovalDecision,
) (PendingAction, *RunStart, error) {
	status := ActionStatusRejected
	if decision.Decision == ApprovalDecisionApprove {
		status = ActionStatusApproved
	}
	return s.transitionPendingAction(ctx, actor, id, status, decision.EditedArguments, decision.Reason, true)
}

func (s *PgStore) transitionPendingAction(
	ctx context.Context,
	actor Actor,
	id uuid.UUID,
	status string,
	edited json.RawMessage,
	reason string,
	human bool,
) (PendingAction, *RunStart, error) {
	known, err := s.GetPendingAction(ctx, actor, id)
	if err != nil {
		return PendingAction{}, nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return PendingAction{}, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the run before the individual action. Decisions for different
	// actions in the same run are thereby serialized, so exactly one caller
	// observes "no pending actions" and claims the resume.
	if err := tx.QueryRow(ctx, `
SELECT id FROM agent_runs WHERE workspace_id = $1 AND id = $2 FOR UPDATE`,
		known.WorkspaceID, known.RunID).Scan(new(uuid.UUID)); err != nil {
		return PendingAction{}, nil, err
	}
	current, err := scanPendingAction(tx.QueryRow(ctx, `
SELECT `+pendingActionColumns+` FROM pending_actions
WHERE workspace_id = $1 AND created_by_user_id = $2 AND id = $3 FOR UPDATE`,
		actor.WorkspaceID, actor.UserID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return PendingAction{}, nil, ErrActionNotFound
	}
	if err != nil {
		return PendingAction{}, nil, err
	}
	if current.Status != ActionStatusPending {
		return PendingAction{}, nil, ErrActionDecided
	}
	if !current.ExpiresAt.After(time.Now()) {
		status = ActionStatusExpired
		edited = nil
		reason = "Approval expired before a decision was made."
		human = false
	}

	var decidedBy any
	if human {
		decidedBy = actor.UserID
	}
	updated, err := scanPendingAction(tx.QueryRow(ctx, `
UPDATE pending_actions
SET status = $4, edited_arguments = $5, decision_reason = $6,
    decided_by_user_id = $7, decided_at = now(), updated_at = now(),
    actor_role = CASE WHEN $8 THEN $9 ELSE actor_role END
WHERE workspace_id = $1 AND created_by_user_id = $2 AND id = $3 AND status = 'pending'
RETURNING `+pendingActionColumns,
		actor.WorkspaceID, actor.UserID, id, status, nullableJSON(edited), reason, decidedBy, human, actor.Role))
	if err != nil {
		return PendingAction{}, nil, err
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO pending_action_audit (workspace_id, pending_action_id, actor_user_id, event, details)
VALUES ($1,$2,$3,$4,jsonb_build_object('reason',$5::text))`,
		updated.WorkspaceID, updated.ID, decidedBy, status, reason); err != nil {
		return PendingAction{}, nil, err
	}

	var pending int
	if err := tx.QueryRow(ctx, `
SELECT count(*) FROM pending_actions
WHERE workspace_id = $1 AND run_id = $2 AND status = 'pending'`,
		updated.WorkspaceID, updated.RunID).Scan(&pending); err != nil {
		return PendingAction{}, nil, err
	}
	var start *RunStart
	if pending == 0 {
		result, err := tx.Exec(ctx, `
UPDATE agent_runs SET status = 'running'
WHERE workspace_id = $1 AND id = $2 AND status = 'paused_approval'`,
			updated.WorkspaceID, updated.RunID)
		if err != nil {
			return PendingAction{}, nil, err
		}
		if result.RowsAffected() == 1 {
			var selector string
			if err := tx.QueryRow(ctx, `SELECT model_id FROM agent_runs WHERE workspace_id = $1 AND id = $2`,
				updated.WorkspaceID, updated.RunID).Scan(&selector); err != nil {
				return PendingAction{}, nil, err
			}
			start = &RunStart{
				Actor:    Actor{WorkspaceID: updated.WorkspaceID, UserID: updated.CreatedByUserID, Role: updated.ActorRole},
				ThreadID: updated.ThreadID, RunID: updated.RunID, TurnID: updated.TurnID, Selector: selector,
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return PendingAction{}, nil, err
	}
	return updated, start, nil
}

func nullableJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func (s *PgStore) ExpirePendingActions(ctx context.Context, limit int32) ([]RunStart, error) {
	rows, err := s.pool.Query(ctx, `
SELECT workspace_id, created_by_user_id, id FROM pending_actions
WHERE status = 'pending' AND expires_at <= now()
ORDER BY expires_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	type candidate struct{ workspaceID, userID, id uuid.UUID }
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.workspaceID, &item.userID, &item.id); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	starts := make([]RunStart, 0)
	for _, item := range candidates {
		actor := Actor{WorkspaceID: item.workspaceID, UserID: item.userID}
		_, start, err := s.transitionPendingAction(ctx, actor, item.id, ActionStatusExpired, nil,
			"Approval expired before a decision was made.", false)
		if errors.Is(err, ErrActionDecided) || errors.Is(err, ErrActionNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if start != nil {
			starts = append(starts, *start)
		}
	}
	return starts, nil
}

func (s *PgStore) LoadApprovalBatch(ctx context.Context, start RunStart) (ApprovalBatch, error) {
	actions, err := s.listRunActions(ctx, start.Actor.WorkspaceID, start.RunID)
	if err != nil {
		return ApprovalBatch{}, err
	}
	if len(actions) == 0 {
		return ApprovalBatch{}, ErrActionNotFound
	}
	byCall := make(map[string]*PendingAction, len(actions))
	for i := range actions {
		byCall[actions[i].ToolCallID] = &actions[i]
	}
	rows, err := s.pool.Query(ctx, `
SELECT id, tool_name, tool_call_id, tool_input
FROM agent_message_parts
WHERE workspace_id = $1 AND message_id = $2 AND type = 'tool_call'
ORDER BY order_index`, start.Actor.WorkspaceID, actions[0].MessageID)
	if err != nil {
		return ApprovalBatch{}, err
	}
	defer rows.Close()
	batch := ApprovalBatch{Calls: make([]ApprovalCall, 0)}
	for rows.Next() {
		var call ApprovalCall
		if err := rows.Scan(&call.PartID, &call.ToolName, &call.ToolCallID, &call.Arguments); err != nil {
			return ApprovalBatch{}, err
		}
		call.Action = byCall[call.ToolCallID]
		batch.Calls = append(batch.Calls, call)
	}
	return batch, rows.Err()
}

func (s *PgStore) listRunActions(ctx context.Context, workspaceID, runID uuid.UUID) ([]PendingAction, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+pendingActionColumns+`
FROM pending_actions WHERE workspace_id = $1 AND run_id = $2 ORDER BY created_at, id`, workspaceID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	actions := make([]PendingAction, 0)
	for rows.Next() {
		action, err := scanPendingAction(rows)
		if err != nil {
			return nil, err
		}
		actions = append(actions, action)
	}
	return actions, rows.Err()
}

func (s *PgStore) CompleteApprovalBatch(ctx context.Context, start RunStart, results []ApprovalResult) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	parts := make([]PartInput, 0, len(results))
	for _, result := range results {
		state := PartStateDone
		if result.IsError {
			state = PartStateError
		}
		parts = append(parts, PartInput{
			Type: PartToolResult, ToolName: result.ToolName, ToolCallID: result.ToolCallID,
			ToolOutput: result.ToolOutput, State: state, Error: result.ErrorMessage,
		})
		if _, err := tx.Exec(ctx, `
UPDATE agent_message_parts SET state = $4, error_message = $5, tool_input = $6
WHERE workspace_id = $1 AND id = $2 AND tool_call_id = $3`,
			start.Actor.WorkspaceID, result.PartID, result.ToolCallID, state,
			result.ErrorMessage, result.ToolInput); err != nil {
			return err
		}
		if result.Action == nil || result.Action.Status != ActionStatusApproved {
			continue
		}
		actionStatus, auditEvent := ActionStatusExecuted, ActionStatusExecuted
		if result.IsError {
			actionStatus, auditEvent = ActionStatusFailed, ActionStatusFailed
		}
		if _, err := tx.Exec(ctx, `
UPDATE pending_actions
SET status = $3, result = $4, error_message = $5, executed_at = now(), updated_at = now()
WHERE workspace_id = $1 AND id = $2 AND status = 'approved'`,
			result.Action.WorkspaceID, result.Action.ID, actionStatus,
			result.ToolOutput, result.ErrorMessage); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO pending_action_audit (workspace_id, pending_action_id, event, details)
VALUES ($1,$2,$3,jsonb_build_object('tool_call_id',$4::text))`,
			result.Action.WorkspaceID, result.Action.ID, auditEvent, result.ToolCallID); err != nil {
			return err
		}
	}
	if _, err := s.persistMessageTx(ctx, s.q.WithTx(tx), MessageInput{
		WorkspaceID: start.Actor.WorkspaceID, ThreadID: start.ThreadID, TurnID: start.TurnID,
		Role: "user", Status: MessageStatusSent, Parts: parts,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PgStore) CancelApprovalRun(ctx context.Context, actor Actor, runID uuid.UUID, reason string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var threadID uuid.UUID
	if err := tx.QueryRow(ctx, `
SELECT r.thread_id FROM agent_runs r
JOIN agent_threads t ON t.id = r.thread_id AND t.workspace_id = r.workspace_id
WHERE r.workspace_id = $1 AND r.id = $2 AND r.status = 'paused_approval'
  AND t.created_by_user_id = $3
FOR UPDATE OF r`, actor.WorkspaceID, runID, actor.UserID).Scan(&threadID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrActionNotFound
		}
		return err
	}
	rows, err := tx.Query(ctx, `
UPDATE pending_actions
SET status = 'rejected', decision_reason = $4, decided_by_user_id = $3,
    decided_at = now(), updated_at = now()
WHERE workspace_id = $1 AND run_id = $2 AND created_by_user_id = $3
  AND status IN ('pending','approved')
RETURNING id`, actor.WorkspaceID, runID, actor.UserID, reason)
	if err != nil {
		return err
	}
	var actionIDs []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		actionIDs = append(actionIDs, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range actionIDs {
		if _, err := tx.Exec(ctx, `
INSERT INTO pending_action_audit (workspace_id, pending_action_id, actor_user_id, event, details)
VALUES ($1,$2,$3,'rejected',jsonb_build_object('reason',$4))`,
			actor.WorkspaceID, id, actor.UserID, reason); err != nil {
			return err
		}
	}
	qtx := s.q.WithTx(tx)
	if _, err := qtx.FinishAgentRun(ctx, gen.FinishAgentRunParams{
		WorkspaceID: actor.WorkspaceID, ID: runID, Status: RunStatusCancelled, Error: reason,
	}); err != nil {
		return err
	}
	if err := qtx.ClearAgentThreadActiveRun(ctx, gen.ClearAgentThreadActiveRunParams{
		WorkspaceID: actor.WorkspaceID, ActiveRunID: pgtype.UUID{Bytes: runID, Valid: true},
	}); err != nil {
		return err
	}
	if _, err := qtx.FinishProcessingAgentMessages(ctx, gen.FinishProcessingAgentMessagesParams{
		WorkspaceID: actor.WorkspaceID, ThreadID: threadID,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

var _ ApprovalStore = (*PgStore)(nil)
