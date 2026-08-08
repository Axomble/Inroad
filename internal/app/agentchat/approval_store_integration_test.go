//go:build integration

package agentchat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

func setupApprovalStore(t *testing.T) (context.Context, *PgStore, *gen.Queries, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	dsn := dbtest.DSN(t)
	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	q := gen.New(pool)
	workspace, err := q.CreateWorkspace(ctx, "Approval "+uuid.NewString())
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	user, err := q.CreateUser(ctx, gen.CreateUserParams{
		Email: "approval-" + uuid.NewString() + "@example.com", PasswordHash: ptrTo("test"),
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM workspaces WHERE id=$1", workspace.ID)
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id=$1", user.ID)
	})
	return ctx, NewPgStore(pool), q, workspace.ID, user.ID
}

func pauseTestAction(t *testing.T, ctx context.Context, store *PgStore, actor Actor, expiresAt time.Time) (PendingAction, RunStart) {
	t.Helper()
	thread, err := store.CreateThread(ctx, actor.WorkspaceID, actor.UserID, "Approval test")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	run, err := store.InsertRun(ctx, actor.WorkspaceID, thread.ID, "provider/model")
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}
	start := RunStart{Actor: actor, ThreadID: thread.ID, RunID: run.ID, TurnID: uuid.New(), Selector: "provider/model"}
	actions, err := store.PauseForApproval(ctx, MessageInput{
		WorkspaceID: actor.WorkspaceID, ThreadID: thread.ID, TurnID: start.TurnID,
		Role: "assistant", Status: MessageStatusSent,
		Parts: []PartInput{{
			Type: PartToolCall, ToolName: "inroad_campaign_control", ToolCallID: "call-1",
			ToolInput: []byte(`{"method":"pause","campaign_id":"11111111-1111-1111-1111-111111111111"}`),
			State:     PartStateAwaitingApproval,
		}},
	}, start, []ApprovalRequest{{
		ToolName: "inroad_campaign_control", ToolCallID: "call-1",
		Arguments: []byte(`{"method":"pause","campaign_id":"11111111-1111-1111-1111-111111111111"}`),
		RiskTier:  "consequential", ExpiresAt: expiresAt,
	}})
	if err != nil {
		t.Fatalf("pause for approval: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("actions=%d, want 1", len(actions))
	}
	return actions[0], start
}

func TestApprovalDecisionIsTenantScopedAuditedAndSingleUse(t *testing.T) {
	ctx, store, q, workspaceID, userID := setupApprovalStore(t)
	actor := Actor{WorkspaceID: workspaceID, UserID: userID, Role: "admin"}
	action, _ := pauseTestAction(t, ctx, store, actor, time.Now().Add(time.Hour))

	other, err := q.CreateUser(ctx, gen.CreateUserParams{
		Email: "approval-other-" + uuid.NewString() + "@example.com", PasswordHash: ptrTo("test"),
	})
	if err != nil {
		t.Fatalf("create other user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.pool.Exec(context.Background(), "DELETE FROM users WHERE id=$1", other.ID)
	})
	if _, err := store.GetPendingAction(ctx, Actor{WorkspaceID: workspaceID, UserID: other.ID}, action.ID); !errors.Is(err, ErrActionNotFound) {
		t.Fatalf("foreign owner read=%v, want ErrActionNotFound", err)
	}

	edited := []byte(`{"method":"resume","campaign_id":"11111111-1111-1111-1111-111111111111"}`)
	updated, resume, err := store.DecidePendingAction(ctx, actor, action.ID, ApprovalDecision{
		Decision: ApprovalDecisionApprove, EditedArguments: edited,
	})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	var editedObject map[string]string
	if err := json.Unmarshal(updated.EditedArguments, &editedObject); err != nil {
		t.Fatalf("decode edited arguments: %v", err)
	}
	if updated.Status != ActionStatusApproved || editedObject["method"] != "resume" || resume == nil {
		t.Fatalf("updated=%+v resume=%+v", updated, resume)
	}
	if _, _, err := store.DecidePendingAction(ctx, actor, action.ID, ApprovalDecision{Decision: ApprovalDecisionReject}); !errors.Is(err, ErrActionDecided) {
		t.Fatalf("second decision=%v, want ErrActionDecided", err)
	}

	var events []string
	rows, err := store.pool.Query(ctx, "SELECT event FROM pending_action_audit WHERE workspace_id=$1 AND pending_action_id=$2 ORDER BY created_at, id", workspaceID, action.ID)
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var event string
		if err := rows.Scan(&event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(events) != "[created approved]" {
		t.Fatalf("audit events=%v", events)
	}
}

// A run can pause more than once. The resume of the SECOND pause must load
// only that pause's calls: scoping by run id instead replays the first pause's
// message, re-running its non-gated tool calls and then tripping over an
// action that already executed — which strands the approval the human just
// granted.
func TestLoadApprovalBatchIsScopedToThePausedMessage(t *testing.T) {
	ctx, store, _, workspaceID, userID := setupApprovalStore(t)
	actor := Actor{WorkspaceID: workspaceID, UserID: userID, Role: "admin"}
	thread, err := store.CreateThread(ctx, workspaceID, userID, "Two pauses")
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	run, err := store.InsertRun(ctx, workspaceID, thread.ID, "provider/model")
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}
	start := RunStart{Actor: actor, ThreadID: thread.ID, RunID: run.ID, TurnID: uuid.New(), Selector: "provider/model"}

	// First pause: one gated call plus a read-only call that shares the message
	// and must NOT be replayed by a later resume.
	first, err := store.PauseForApproval(ctx, MessageInput{
		WorkspaceID: workspaceID, ThreadID: thread.ID, TurnID: start.TurnID,
		Role: "assistant", Status: MessageStatusSent,
		Parts: []PartInput{
			{Type: PartToolCall, ToolName: "inroad_contact_read", ToolCallID: "call-read",
				ToolInput: []byte(`{"query":"ada"}`), State: PartStateDone},
			{Type: PartToolCall, ToolName: "inroad_campaign_control", ToolCallID: "call-a",
				ToolInput: []byte(`{"method":"pause"}`), State: PartStateAwaitingApproval},
		},
	}, start, []ApprovalRequest{{
		ToolName: "inroad_campaign_control", ToolCallID: "call-a",
		Arguments: []byte(`{"method":"pause"}`), RiskTier: "consequential",
		ExpiresAt: time.Now().Add(time.Hour),
	}})
	if err != nil {
		t.Fatalf("first pause: %v", err)
	}

	firstResume, err := decideAndResume(ctx, store, actor, first[0].ID)
	if err != nil {
		t.Fatalf("approve first: %v", err)
	}
	if firstResume.MessageID != first[0].MessageID {
		t.Fatalf("resume message=%v, want the paused message %v", firstResume.MessageID, first[0].MessageID)
	}
	firstBatch, err := store.LoadApprovalBatch(ctx, firstResume)
	if err != nil {
		t.Fatalf("load first batch: %v", err)
	}
	if len(firstBatch.Calls) != 2 {
		t.Fatalf("first batch calls=%d, want the message's 2 tool calls", len(firstBatch.Calls))
	}

	// Settle the first pause the way a resume does, then pause again on a new
	// message — the same run, a second approval.
	for _, call := range firstBatch.Calls {
		if call.Action == nil {
			continue
		}
		if err := store.RecordApprovalOutcome(ctx, ApprovalResult{
			ToolName: call.ToolName, ToolCallID: call.ToolCallID, Action: call.Action,
			ToolOutput: []byte(`{"success":true}`), Status: ActionStatusExecuted,
		}); err != nil {
			t.Fatalf("record first outcome: %v", err)
		}
	}
	second, err := store.PauseForApproval(ctx, MessageInput{
		WorkspaceID: workspaceID, ThreadID: thread.ID, TurnID: start.TurnID,
		Role: "assistant", Status: MessageStatusSent,
		Parts: []PartInput{{
			Type: PartToolCall, ToolName: "inroad_campaign_control", ToolCallID: "call-b",
			ToolInput: []byte(`{"method":"resume"}`), State: PartStateAwaitingApproval,
		}},
	}, start, []ApprovalRequest{{
		ToolName: "inroad_campaign_control", ToolCallID: "call-b",
		Arguments: []byte(`{"method":"resume"}`), RiskTier: "consequential",
		ExpiresAt: time.Now().Add(time.Hour),
	}})
	if err != nil {
		t.Fatalf("second pause: %v", err)
	}
	if second[0].MessageID == first[0].MessageID {
		t.Fatal("second pause reused the first pause's message")
	}

	secondResume, err := decideAndResume(ctx, store, actor, second[0].ID)
	if err != nil {
		t.Fatalf("approve second: %v", err)
	}
	batch, err := store.LoadApprovalBatch(ctx, secondResume)
	if err != nil {
		t.Fatalf("load second batch: %v", err)
	}
	if len(batch.Calls) != 1 || batch.Calls[0].ToolCallID != "call-b" {
		ids := make([]string, len(batch.Calls))
		for i, call := range batch.Calls {
			ids[i] = call.ToolCallID
		}
		t.Fatalf("second batch calls=%v, want only [call-b]", ids)
	}
	if batch.Calls[0].Action == nil || batch.Calls[0].Action.Status != ActionStatusApproved {
		t.Fatalf("second batch action=%+v", batch.Calls[0].Action)
	}

	// The outcome recorded per call is committed on its own, and audited.
	if err := store.RecordApprovalOutcome(ctx, ApprovalResult{
		ToolName: "inroad_campaign_control", ToolCallID: "call-b", Action: batch.Calls[0].Action,
		ToolOutput: []byte(`{"success":true}`), Status: ActionStatusExecuted,
	}); err != nil {
		t.Fatalf("record second outcome: %v", err)
	}
	settled, err := store.GetPendingAction(ctx, actor, second[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if settled.Status != ActionStatusExecuted || settled.ExecutedAt == nil {
		t.Fatalf("settled action=%+v", settled)
	}
	var events []string
	rows, err := store.pool.Query(ctx, "SELECT event FROM pending_action_audit WHERE workspace_id=$1 AND pending_action_id=$2 ORDER BY created_at, id", workspaceID, second[0].ID)
	if err != nil {
		t.Fatalf("audit query: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var event string
		if err := rows.Scan(&event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(events) != "[created approved executed]" {
		t.Fatalf("audit events=%v", events)
	}
}

// decideAndResume approves an action and returns the resume its decision
// released, failing when the decision did not release one.
func decideAndResume(ctx context.Context, store *PgStore, actor Actor, id uuid.UUID) (RunStart, error) {
	_, start, err := store.DecidePendingAction(ctx, actor, id, ApprovalDecision{Decision: ApprovalDecisionApprove})
	if err != nil {
		return RunStart{}, err
	}
	if start == nil {
		return RunStart{}, errors.New("decision did not release a resume")
	}
	return *start, nil
}

func TestExpiredApprovalDeniesAndResumes(t *testing.T) {
	ctx, store, _, workspaceID, userID := setupApprovalStore(t)
	actor := Actor{WorkspaceID: workspaceID, UserID: userID, Role: "admin"}
	action, start := pauseTestAction(t, ctx, store, actor, time.Now().Add(-time.Minute))

	starts, err := store.ExpirePendingActions(ctx, 10)
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if len(starts) != 1 || starts[0].RunID != start.RunID || starts[0].Actor.Role != actor.Role {
		t.Fatalf("resume starts=%+v", starts)
	}
	updated, err := store.GetPendingAction(ctx, actor, action.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != ActionStatusExpired || updated.DecisionReason == "" {
		t.Fatalf("expired action=%+v", updated)
	}
}

// ptrTo takes the address of a literal, for the nullable columns sqlc models as
// *T (users.password_hash became nullable with federated sign-in, migration 000051).
func ptrTo[T any](v T) *T { return &v }
