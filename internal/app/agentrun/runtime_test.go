package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/app/agentchat"
	"github.com/inroad/inroad/internal/platform/ai"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

type fakeStore struct {
	agentchat.Store
	transcript []agentchat.Message
	persisted  []agentchat.MessageInput
	usage      ai.Usage
	model      string
}

func (f *fakeStore) ListTranscript(context.Context, uuid.UUID, uuid.UUID) ([]agentchat.Message, error) {
	return f.transcript, nil
}
func (f *fakeStore) SetRunModel(_ context.Context, _, _ uuid.UUID, model string) error {
	f.model = model
	return nil
}
func (f *fakeStore) PersistMessage(_ context.Context, in agentchat.MessageInput) (agentchat.Message, error) {
	f.persisted = append(f.persisted, in)
	return agentchat.Message{}, nil
}
func (f *fakeStore) AddThreadUsage(_ context.Context, _, _ uuid.UUID, input, output int64, _ int32) error {
	f.usage.InputTokens += int(input)
	f.usage.OutputTokens += int(output)
	return nil
}

type fakeResolver struct{ streamer ai.ChatStreamer }

func (f fakeResolver) Resolve(context.Context, uuid.UUID, string) (agentchat.ResolvedModel, error) {
	return agentchat.ResolvedModel{ID: "provider/model", Name: "model", ContextWindowTokens: 10000, MaxOutputTokens: 1000, Streamer: f.streamer}, nil
}
func (fakeResolver) Instructions(context.Context, uuid.UUID) (string, error) {
	return "Be direct.", nil
}

type fakeStreamer struct {
	turns          [][]ai.StreamEvent
	terminalErrors []error
	calls          int
	requests       []ai.ChatRequest
}

func (f *fakeStreamer) StreamChat(_ context.Context, request ai.ChatRequest) (ai.ChatStream, error) {
	f.requests = append(f.requests, request)
	index := f.calls
	events := f.turns[index]
	f.calls++
	var terminalErr error
	if index < len(f.terminalErrors) {
		terminalErr = f.terminalErrors[index]
	}
	return &fakeStream{events: events, terminalErr: terminalErr}, nil
}

type fakeStream struct {
	events      []ai.StreamEvent
	index       int
	terminalErr error
}

func (f *fakeStream) Next() (ai.StreamEvent, error) {
	if f.index == len(f.events) {
		if f.terminalErr != nil {
			err := f.terminalErr
			f.terminalErr = nil
			return ai.StreamEvent{}, err
		}
		return ai.StreamEvent{}, io.EOF
	}
	event := f.events[f.index]
	f.index++
	return event, nil
}
func (*fakeStream) Close() error { return nil }

type fakePublisher struct{ events []agentchat.Event }

func (f *fakePublisher) Publish(_ context.Context, _ uuid.UUID, event agentchat.Event) (int64, error) {
	f.events = append(f.events, event)
	return int64(len(f.events)), nil
}
func (*fakePublisher) Clear(context.Context, uuid.UUID) error { return nil }

type fakeTools struct{ calls int }

func (fakeTools) Definitions(agentchat.Actor) []Tool {
	return []Tool{
		{Name: "inroad_contact_read", InputSchema: json.RawMessage(`{"type":"object"}`), Risk: "read"},
		{Name: "inroad_campaign_control", InputSchema: json.RawMessage(`{"type":"object"}`), Risk: "consequential"},
	}
}
func (f *fakeTools) Execute(context.Context, agentchat.Actor, string, json.RawMessage) (ToolResult, error) {
	f.calls++
	return ToolResult{Output: json.RawMessage(`{"success":true,"data":{"name":"Ada"}}`)}, nil
}
func (f *fakeTools) ExecuteApproved(ctx context.Context, actor agentchat.Actor, name string, input json.RawMessage) (ToolResult, error) {
	return f.Execute(ctx, actor, name, input)
}
func (*fakeTools) Validate(agentchat.Actor, string, json.RawMessage) error { return nil }

type fakeApprovals struct {
	agentchat.ApprovalStore
	requests []agentchat.ApprovalRequest
	message  agentchat.MessageInput
}

func (f *fakeApprovals) PauseForApproval(_ context.Context, message agentchat.MessageInput, _ agentchat.RunStart, requests []agentchat.ApprovalRequest) ([]agentchat.PendingAction, error) {
	f.message = message
	f.requests = append([]agentchat.ApprovalRequest(nil), requests...)
	actions := make([]agentchat.PendingAction, len(requests))
	for i, request := range requests {
		actions[i] = agentchat.PendingAction{
			ID: uuid.New(), ToolName: request.ToolName, ToolCallID: request.ToolCallID,
			Arguments: request.Arguments, RiskTier: request.RiskTier,
			Status: agentchat.ActionStatusPending, ExpiresAt: request.ExpiresAt,
		}
	}
	return actions, nil
}

func TestRuntimePausesConsequentialToolBeforeExecution(t *testing.T) {
	workspaceID, threadID, runID, turnID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	store := &fakeStore{transcript: []agentchat.Message{{
		Row:   gen.AgentMessage{WorkspaceID: workspaceID, ThreadID: threadID, TurnID: turnID, Role: ai.RoleUser, Status: agentchat.MessageStatusProcessing},
		Parts: []gen.AgentMessagePart{{Type: agentchat.PartText, TextContent: "Pause the campaign"}},
	}}}
	streamer := &fakeStreamer{turns: [][]ai.StreamEvent{{
		{Type: ai.EventToolCallStart, ToolCallID: "call-control", ToolName: "inroad_campaign_control"},
		{Type: ai.EventToolCallEnd, ToolCallID: "call-control", ToolName: "inroad_campaign_control", ToolInput: json.RawMessage(`{"loading_message":"Pausing Q3","method":"pause"}`)},
		{Type: ai.EventUsage, Usage: &ai.Usage{InputTokens: 7, OutputTokens: 3}, StopReason: ai.StopToolUse},
	}}}
	tools, approvals, publisher := &fakeTools{}, &fakeApprovals{}, &fakePublisher{}
	runtime := &Runtime{Store: store, Models: fakeResolver{streamer: streamer}, Tools: tools, Publisher: publisher, Approvals: approvals}
	result, err := runtime.Execute(context.Background(), agentchat.RunStart{
		Actor:    agentchat.Actor{WorkspaceID: workspaceID, UserID: uuid.New(), Role: "admin"},
		ThreadID: threadID, RunID: runID, TurnID: turnID, Selector: "default-smart-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Paused || tools.calls != 0 {
		t.Fatalf("paused=%v tool calls=%d", result.Paused, tools.calls)
	}
	if len(approvals.requests) != 1 || approvals.requests[0].ToolName != "inroad_campaign_control" {
		t.Fatalf("requests=%+v", approvals.requests)
	}
	if approvals.message.Parts[0].State != agentchat.PartStateAwaitingApproval {
		t.Fatalf("part state=%q", approvals.message.Parts[0].State)
	}
	if len(streamer.requests[0].Tools) != 2 {
		t.Fatalf("approval-enabled tool set=%+v", streamer.requests[0].Tools)
	}
	if len(publisher.events) < 2 || publisher.events[len(publisher.events)-1].Type != agentchat.EventApprovalRequired {
		t.Fatalf("events=%+v", publisher.events)
	}
}

func TestRuntimeStreamsToolLoopAndPersistsTranscript(t *testing.T) {
	workspaceID, threadID, runID, turnID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	now := pgtype.Timestamptz{Time: time.Now(), Valid: true}
	store := &fakeStore{transcript: []agentchat.Message{{
		Row:   gen.AgentMessage{ID: uuid.New(), WorkspaceID: workspaceID, ThreadID: threadID, TurnID: turnID, Role: ai.RoleUser, Status: agentchat.MessageStatusProcessing, CreatedAt: now},
		Parts: []gen.AgentMessagePart{{ID: uuid.New(), Type: agentchat.PartText, TextContent: "Find Ada"}},
	}}}
	streamer := &fakeStreamer{turns: [][]ai.StreamEvent{
		{
			{Type: ai.EventToolCallStart, ToolCallID: "call-1", ToolName: "inroad_contact_read"},
			{Type: ai.EventToolInputDelta, ToolCallID: "call-1", Text: `{"loading_message":"Finding Ada"`},
			{Type: ai.EventToolInputDelta, ToolCallID: "call-1", Text: `}`},
			{Type: ai.EventToolCallEnd, ToolCallID: "call-1", ToolName: "inroad_contact_read", ToolInput: json.RawMessage(`{"loading_message":"Finding Ada"}`)},
			{Type: ai.EventUsage, Usage: &ai.Usage{InputTokens: 10, OutputTokens: 4}, StopReason: ai.StopToolUse},
		},
		{
			{Type: ai.EventTextDelta, Text: "Ada is in your contacts."},
			{Type: ai.EventUsage, Usage: &ai.Usage{InputTokens: 14, OutputTokens: 6}, StopReason: ai.StopEndTurn},
		},
	}}
	tools, publisher := &fakeTools{}, &fakePublisher{}
	runtime := &Runtime{Store: store, Models: fakeResolver{streamer: streamer}, Tools: tools, Publisher: publisher}
	result, err := runtime.Execute(context.Background(), agentchat.RunStart{
		Actor:    agentchat.Actor{WorkspaceID: workspaceID, UserID: uuid.New(), Role: "member"},
		ThreadID: threadID, RunID: runID, TurnID: turnID, Selector: "default-smart-model",
	})
	if err != nil {
		t.Fatal(err)
	}
	if streamer.calls != 2 || tools.calls != 1 {
		t.Fatalf("provider calls=%d tool calls=%d", streamer.calls, tools.calls)
	}
	if len(streamer.requests[0].Tools) != 1 || streamer.requests[0].Tools[0].Name != "inroad_contact_read" {
		t.Fatalf("approval-tier tool leaked to model: %+v", streamer.requests[0].Tools)
	}
	if len(store.persisted) != 3 {
		t.Fatalf("persisted messages=%d, want assistant/tool-result/assistant", len(store.persisted))
	}
	if store.persisted[0].Parts[0].State != agentchat.PartStateDone {
		t.Fatalf("tool call state=%q", store.persisted[0].Parts[0].State)
	}
	if result.Usage.InputTokens != 24 || result.Usage.OutputTokens != 10 || store.usage.InputTokens != 24 {
		t.Fatalf("usage result=%+v stored=%+v", result.Usage, store.usage)
	}
	if len(publisher.events) != 5 || publisher.events[3].Type != agentchat.EventToolOutput {
		t.Fatalf("events=%+v", publisher.events)
	}
}

func TestRuntimePersistsPartialTextWhenProviderStreamFails(t *testing.T) {
	providerErr := errors.New("provider disconnected")
	workspaceID, threadID, runID, turnID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	store := &fakeStore{transcript: []agentchat.Message{{
		Row: gen.AgentMessage{
			ID: uuid.New(), WorkspaceID: workspaceID, ThreadID: threadID, TurnID: turnID,
			Role: ai.RoleUser, Status: agentchat.MessageStatusProcessing,
		},
		Parts: []gen.AgentMessagePart{{ID: uuid.New(), Type: agentchat.PartText, TextContent: "Hello"}},
	}}}
	streamer := &fakeStreamer{
		turns:          [][]ai.StreamEvent{{{Type: ai.EventTextDelta, Text: "Partial answer"}}},
		terminalErrors: []error{providerErr},
	}
	runtime := &Runtime{
		Store: store, Models: fakeResolver{streamer: streamer},
		Publisher: &fakePublisher{},
	}
	_, err := runtime.Execute(context.Background(), agentchat.RunStart{
		Actor:    agentchat.Actor{WorkspaceID: workspaceID, UserID: uuid.New()},
		ThreadID: threadID, RunID: runID, TurnID: turnID, Selector: "default-smart-model",
	})
	if !errors.Is(err, providerErr) {
		t.Fatalf("error=%v", err)
	}
	if len(store.persisted) != 1 || store.persisted[0].Parts[0].Text != "Partial answer" {
		t.Fatalf("persisted=%+v", store.persisted)
	}
}
