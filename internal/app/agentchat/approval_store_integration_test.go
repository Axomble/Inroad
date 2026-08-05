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
		Email: "approval-" + uuid.NewString() + "@example.com", PasswordHash: "test",
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
		Email: "approval-other-" + uuid.NewString() + "@example.com", PasswordHash: "test",
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
