package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
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

type fakeResolver struct {
	streamer ai.ChatStreamer
	window   int
}

func (f fakeResolver) Resolve(context.Context, uuid.UUID, string) (agentchat.ResolvedModel, error) {
	window := f.window
	if window == 0 {
		window = 10000
	}
	return agentchat.ResolvedModel{ID: "provider/model", Name: "model", ContextWindowTokens: window, MaxOutputTokens: 1000, Streamer: f.streamer}, nil
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

type fakePublisher struct {
	events []agentchat.Event
	err    error
}

func (f *fakePublisher) Publish(_ context.Context, _ uuid.UUID, event agentchat.Event) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.events = append(f.events, event)
	return int64(len(f.events)), nil
}
func (*fakePublisher) Clear(context.Context, uuid.UUID) error { return nil }

type fakeTools struct {
	calls int
	// trace records every dispatch and every settled action, in order, so a
	// test can assert not just WHAT happened but that the bookkeeping for one
	// call committed before the next call ran.
	trace *[]string
	// output overrides the canned success payload.
	output json.RawMessage
}

func (fakeTools) Definitions(agentchat.Actor) []Tool {
	return []Tool{
		{Name: "inroad_contact_read", InputSchema: json.RawMessage(`{"type":"object"}`), Risk: "read"},
		{Name: "inroad_campaign_control", InputSchema: json.RawMessage(`{"type":"object"}`), Risk: "consequential"},
	}
}
func (f *fakeTools) Execute(_ context.Context, _ agentchat.Actor, name string, _ json.RawMessage) (ToolResult, error) {
	f.calls++
	if f.trace != nil {
		*f.trace = append(*f.trace, "execute "+name)
	}
	if len(f.output) > 0 {
		return ToolResult{Output: f.output}, nil
	}
	return ToolResult{Output: json.RawMessage(`{"success":true,"data":{"name":"Ada"}}`)}, nil
}
func (f *fakeTools) ExecuteApproved(ctx context.Context, actor agentchat.Actor, name string, input json.RawMessage) (ToolResult, error) {
	return f.Execute(ctx, actor, name, input)
}
func (*fakeTools) Validate(agentchat.Actor, string, json.RawMessage) error { return nil }

type fakeApprovals struct {
	agentchat.ApprovalStore
	requests  []agentchat.ApprovalRequest
	message   agentchat.MessageInput
	batch     agentchat.ApprovalBatch
	outcomes  []agentchat.ApprovalResult
	completed []agentchat.ApprovalResult
	trace     *[]string
}

func (f *fakeApprovals) LoadApprovalBatch(context.Context, agentchat.RunStart) (agentchat.ApprovalBatch, error) {
	return f.batch, nil
}

func (f *fakeApprovals) RecordApprovalOutcome(_ context.Context, result agentchat.ApprovalResult) error {
	f.outcomes = append(f.outcomes, result)
	if f.trace != nil {
		*f.trace = append(*f.trace, "settle "+result.ToolCallID+" "+result.Status)
	}
	return nil
}

func (f *fakeApprovals) CompleteApprovalBatch(_ context.Context, _ agentchat.RunStart, results []agentchat.ApprovalResult) error {
	f.completed = append(f.completed, results...)
	return nil
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
	// The two argument fragments reach Redis as ONE delta: consecutive deltas
	// of the same kind are coalesced, so a long answer costs tens of round
	// trips instead of one per token. Every non-delta event still arrives
	// separately and in order.
	var types []string
	for _, event := range publisher.events {
		types = append(types, event.Type)
	}
	want := []string{
		agentchat.EventToolInputStart, agentchat.EventToolInputDelta,
		agentchat.EventToolOutput, agentchat.EventTextDelta,
	}
	if !slices.Equal(types, want) {
		t.Fatalf("event types=%v, want %v", types, want)
	}
	if publisher.events[1].Text != `{"loading_message":"Finding Ada"}` {
		t.Fatalf("coalesced tool input delta=%q", publisher.events[1].Text)
	}
}

// Redis carries the PREVIEW of a run; Postgres is the record. A stream that
// cannot be written must therefore not fail a run whose tools have already
// committed their side effects — otherwise the campaign is paused, the
// transcript is missing, and the user is told it failed.
func TestRuntimeSurvivesAnUnwritableStream(t *testing.T) {
	workspaceID, threadID, runID, turnID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	store := &fakeStore{transcript: []agentchat.Message{{
		Row:   gen.AgentMessage{ID: uuid.New(), WorkspaceID: workspaceID, ThreadID: threadID, TurnID: turnID, Role: ai.RoleUser},
		Parts: []gen.AgentMessagePart{{ID: uuid.New(), Type: agentchat.PartText, TextContent: "Pause Q3"}},
	}}}
	streamer := &fakeStreamer{turns: [][]ai.StreamEvent{
		{
			{Type: ai.EventToolCallStart, ToolCallID: "call-1", ToolName: "inroad_contact_read"},
			{Type: ai.EventToolCallEnd, ToolCallID: "call-1", ToolName: "inroad_contact_read", ToolInput: json.RawMessage(`{"loading_message":"Reading"}`)},
			{Type: ai.EventUsage, Usage: &ai.Usage{}, StopReason: ai.StopToolUse},
		},
		{
			{Type: ai.EventTextDelta, Text: "Paused."},
			{Type: ai.EventUsage, Usage: &ai.Usage{}, StopReason: ai.StopEndTurn},
		},
	}}
	tools := &fakeTools{}
	runtime := &Runtime{
		Store: store, Models: fakeResolver{streamer: streamer}, Tools: tools,
		Publisher: &fakePublisher{err: errors.New("redis is down")},
	}
	if _, err := runtime.Execute(context.Background(), agentchat.RunStart{
		Actor:    agentchat.Actor{WorkspaceID: workspaceID, UserID: uuid.New(), Role: "member"},
		ThreadID: threadID, RunID: runID, TurnID: turnID, Selector: "default-smart-model",
	}); err != nil {
		t.Fatalf("run failed because the stream was unwritable: %v", err)
	}
	if tools.calls != 1 || len(store.persisted) != 3 {
		t.Fatalf("tool calls=%d persisted messages=%d, want 1 and 3", tools.calls, len(store.persisted))
	}
}

// Context management has to run on every step, not once per run: the loop is
// what grows the transcript, appending an assistant message and a tool result
// per step. Pruning only the pre-loop history lets a tool-heavy run walk past
// the model's window and take a provider error instead of compacting.
func TestRuntimePrunesInsideTheStepLoop(t *testing.T) {
	workspaceID, threadID, runID, turnID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	store := &fakeStore{transcript: []agentchat.Message{{
		Row:   gen.AgentMessage{ID: uuid.New(), WorkspaceID: workspaceID, ThreadID: threadID, TurnID: turnID, Role: ai.RoleUser},
		Parts: []gen.AgentMessagePart{{ID: uuid.New(), Type: agentchat.PartText, TextContent: "Audit every campaign"}},
	}}}
	// Two tool steps whose results are individually large enough that keeping
	// both would overrun the window, then a text answer.
	toolCall := func(id string) []ai.StreamEvent {
		return []ai.StreamEvent{
			{Type: ai.EventToolCallStart, ToolCallID: id, ToolName: "inroad_contact_read"},
			{Type: ai.EventToolCallEnd, ToolCallID: id, ToolName: "inroad_contact_read", ToolInput: json.RawMessage(`{"loading_message":"Reading"}`)},
			{Type: ai.EventUsage, Usage: &ai.Usage{}, StopReason: ai.StopToolUse},
		}
	}
	streamer := &fakeStreamer{turns: [][]ai.StreamEvent{
		toolCall("call-1"), toolCall("call-2"),
		{
			{Type: ai.EventTextDelta, Text: "All healthy."},
			{Type: ai.EventUsage, Usage: &ai.Usage{}, StopReason: ai.StopEndTurn},
		},
	}}
	bulky := json.RawMessage(`{"success":true,"rows":"` + strings.Repeat("x", 4000) + `"}`)
	runtime := &Runtime{
		Store: store, Models: fakeResolver{streamer: streamer, window: 2000},
		Tools: &fakeTools{output: bulky}, Publisher: &fakePublisher{},
	}
	if _, err := runtime.Execute(context.Background(), agentchat.RunStart{
		Actor:    agentchat.Actor{WorkspaceID: workspaceID, UserID: uuid.New(), Role: "member"},
		ThreadID: threadID, RunID: runID, TurnID: turnID, Selector: "default-smart-model",
	}); err != nil {
		t.Fatalf("run failed instead of compacting: %v", err)
	}
	notices := 0
	for _, message := range store.persisted {
		for _, part := range message.Parts {
			if part.Type == agentchat.PartCompactionNotice {
				notices++
			}
		}
	}
	// Compaction happened (so the request stayed inside the window) and the
	// transcript records it exactly once however many steps trimmed.
	if notices != 1 {
		t.Fatalf("compaction notices=%d, want exactly 1", notices)
	}
	if streamer.calls != 3 {
		t.Fatalf("provider calls=%d, want 3", streamer.calls)
	}
}

// Resuming a pause must settle each approved action the MOMENT its tool
// returns, must not re-run an action an earlier resume already executed, and
// must refuse to run an approval whose deadline passed while a sibling waited
// for a decision.
func TestRuntimeResumeSettlesEachApprovalAndSkipsStaleOnes(t *testing.T) {
	workspaceID, threadID, runID, turnID, messageID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	store := &fakeStore{transcript: []agentchat.Message{{
		Row:   gen.AgentMessage{ID: uuid.New(), WorkspaceID: workspaceID, ThreadID: threadID, TurnID: turnID, Role: ai.RoleUser},
		Parts: []gen.AgentMessagePart{{ID: uuid.New(), Type: agentchat.PartText, TextContent: "Pause both campaigns"}},
	}}}
	// The continuation run after the batch: a plain text answer.
	streamer := &fakeStreamer{turns: [][]ai.StreamEvent{{
		{Type: ai.EventTextDelta, Text: "Done."},
		{Type: ai.EventUsage, Usage: &ai.Usage{}, StopReason: ai.StopEndTurn},
	}}}

	var trace []string
	done := agentchat.PendingAction{
		ID: uuid.New(), ToolCallID: "call-done", ToolName: "inroad_campaign_control",
		Status: agentchat.ActionStatusExecuted, ExpiresAt: time.Now().Add(time.Hour),
	}
	stale := agentchat.PendingAction{
		ID: uuid.New(), ToolCallID: "call-stale", ToolName: "inroad_campaign_control",
		Status: agentchat.ActionStatusApproved, ExpiresAt: time.Now().Add(-time.Minute),
	}
	live := agentchat.PendingAction{
		ID: uuid.New(), ToolCallID: "call-live", ToolName: "inroad_campaign_control",
		Status: agentchat.ActionStatusApproved, ExpiresAt: time.Now().Add(time.Hour),
	}
	approvals := &fakeApprovals{trace: &trace, batch: agentchat.ApprovalBatch{Calls: []agentchat.ApprovalCall{
		{PartID: uuid.New(), ToolName: done.ToolName, ToolCallID: done.ToolCallID, Action: &done},
		{PartID: uuid.New(), ToolName: stale.ToolName, ToolCallID: stale.ToolCallID, Action: &stale},
		{PartID: uuid.New(), ToolName: live.ToolName, ToolCallID: live.ToolCallID, Action: &live},
	}}}
	tools := &fakeTools{trace: &trace}
	runtime := &Runtime{
		Store: store, Models: fakeResolver{streamer: streamer}, Tools: tools,
		Publisher: &fakePublisher{}, Approvals: approvals,
	}
	if _, err := runtime.Resume(context.Background(), agentchat.RunStart{
		Actor:    agentchat.Actor{WorkspaceID: workspaceID, UserID: uuid.New(), Role: "admin"},
		ThreadID: threadID, RunID: runID, TurnID: turnID, MessageID: messageID,
		Selector: "default-smart-model",
	}); err != nil {
		t.Fatal(err)
	}

	// Exactly one tool ran, and its action was settled before the loop moved
	// on — the already-executed action was skipped rather than repeated, and
	// the expired one was never dispatched.
	want := []string{"settle call-stale expired", "execute inroad_campaign_control", "settle call-live executed"}
	if !slices.Equal(trace, want) {
		t.Fatalf("trace=%v, want %v", trace, want)
	}
	if tools.calls != 1 {
		t.Fatalf("tool dispatches=%d, want 1", tools.calls)
	}
	// The skipped action contributes no tool_result part; the other two do.
	if len(approvals.completed) != 2 {
		t.Fatalf("completed results=%d, want 2", len(approvals.completed))
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
