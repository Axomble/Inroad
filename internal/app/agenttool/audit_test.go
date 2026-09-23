package agenttool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/audit"
)

func TestToolCallsAreAttributedToTheAgentNotTheUser(t *testing.T) {
	var seen audit.Actor
	var ok bool
	probe := Tool{
		Name: "probe", MinRole: "member",
		Execute: func(ctx context.Context, _ Principal, _ json.RawMessage) (Result, error) {
			seen, ok = audit.ActorFrom(ctx)
			return Ok(nil), nil
		},
	}
	reg := &Reg{tools: []Tool{probe}, byName: map[string]Tool{"probe": probe}}
	run := uuid.NewString()
	p := admin()
	p.AgentClientID, p.RunID = "inroad-chat", run

	// The ambient actor is the human, as RequireAuth would have set it.
	ctx := audit.WithActor(context.Background(), audit.UserActor(p.UserID))
	if _, err := reg.Execute(ctx, p, "probe", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !ok || seen.Type != audit.ActorAgent || seen.ID != run || seen.UserID == nil || *seen.UserID != p.UserID {
		t.Fatalf("actor inside the tool = %+v, want agent run %s on behalf of %s", seen, run, p.UserID)
	}

	// Without a run (an MCP client) the agent is identified by its client.
	p.RunID = ""
	p.AgentClientID = "mcp-client-7"
	if _, err := reg.Execute(ctx, p, "probe", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if seen.ID != "mcp-client-7" {
		t.Fatalf("actor id = %q, want the MCP client", seen.ID)
	}
}
