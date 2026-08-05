package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/agentchat"
	"github.com/inroad/inroad/internal/platform/ai"
)

const MaxSteps = 50

type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	Risk        string
}

type ToolResult struct {
	Output  json.RawMessage
	IsError bool
}

type ToolRegistry interface {
	Definitions(agentchat.Actor) []Tool
	Execute(context.Context, agentchat.Actor, string, json.RawMessage) (ToolResult, error)
	ExecuteApproved(context.Context, agentchat.Actor, string, json.RawMessage) (ToolResult, error)
	Validate(agentchat.Actor, string, json.RawMessage) error
}

type Result struct {
	Usage         ai.Usage
	Touched       []string
	FirstUserText string
	Paused        bool
}

type Runtime struct {
	Store     agentchat.Store
	Models    agentchat.ModelResolver
	Tools     ToolRegistry
	Publisher agentchat.StreamPublisher
	Approvals agentchat.ApprovalStore
	// Logger records what the run could not surface on the stream. Optional:
	// an unset logger falls back to the default, so a test constructing a
	// Runtime by literal needs nothing extra.
	Logger *slog.Logger
}

func (r *Runtime) logger() *slog.Logger {
	if r.Logger == nil {
		return slog.Default()
	}
	return r.Logger
}

// publisher builds the run's coalescing, never-failing view of the stream.
func (r *Runtime) publisher(start agentchat.RunStart) *runPublisher {
	return newRunPublisher(r.Publisher, r.logger(), start.ThreadID, start.RunID.String())
}

type toolCall struct {
	id    string
	name  string
	input json.RawMessage
	part  int
}

func (r *Runtime) Execute(ctx context.Context, start agentchat.RunStart) (Result, error) {
	model, err := r.Models.Resolve(ctx, start.Actor.WorkspaceID, start.Selector)
	if err != nil {
		return Result{}, err
	}
	if err := r.Store.SetRunModel(ctx, start.Actor.WorkspaceID, start.RunID, model.ID); err != nil {
		return Result{}, err
	}
	instructions, err := r.Models.Instructions(ctx, start.Actor.WorkspaceID)
	if err != nil {
		return Result{}, err
	}
	system := agentchat.SystemPrompt(instructions)
	definitions := r.toolDefinitions(start.Actor)
	messages, firstUserText, err := r.transcript(ctx, start)
	if err != nil {
		return Result{}, err
	}
	pub := r.publisher(start)
	defer pub.Close(ctx)
	result := Result{FirstUserText: firstUserText}
	touched := map[string]bool{}
	compactedOnce := false
	for step := 0; step < MaxSteps; step++ {
		// Pruning belongs INSIDE the loop: one step's tool results can be
		// larger than the whole conversation was, and the loop may append
		// fifty of them. Pruning only the pre-loop transcript means a long
		// tool-heavy run walks past the window and gets a provider 400 instead
		// of the compaction the spec promises. The notice is persisted at most
		// once — the transcript records that trimming happened, not how often.
		compacted, err := agentchat.Prune(system, definitions, messages, model.ContextWindowTokens)
		if err != nil {
			return result, err
		}
		if compacted && !compactedOnce {
			compactedOnce = true
			if _, err := r.Store.PersistMessage(ctx, agentchat.MessageInput{
				WorkspaceID: start.Actor.WorkspaceID, ThreadID: start.ThreadID, TurnID: start.TurnID,
				Role: ai.RoleAssistant, Status: agentchat.MessageStatusSent,
				Parts: []agentchat.PartInput{{Type: agentchat.PartCompactionNotice, Text: agentchat.CompactionNotice}},
			}); err != nil {
				return result, err
			}
		}
		req := ai.ChatRequest{Model: model.Name, System: system, Messages: messages, Tools: definitions, MaxTokens: model.MaxOutputTokens}
		stream, err := model.Streamer.StreamChat(ctx, req)
		if err != nil {
			return result, err
		}
		parts, calls, usage, stop, err := r.consume(ctx, start, stream, pub)
		closeErr := stream.Close()
		if err != nil {
			for i := range parts {
				if parts[i].Type == agentchat.PartToolCall && parts[i].State == agentchat.PartStateRunning {
					parts[i].State = agentchat.PartStateError
					parts[i].Error = "The model stream ended before this tool could run."
				}
			}
			if len(parts) > 0 {
				persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				_, persistErr := r.Store.PersistMessage(persistCtx, agentchat.MessageInput{
					WorkspaceID: start.Actor.WorkspaceID, ThreadID: start.ThreadID, TurnID: start.TurnID,
					Role: ai.RoleAssistant, Status: agentchat.MessageStatusSent, Parts: parts,
				})
				cancelPersist()
				if persistErr != nil {
					return result, errors.Join(err, persistErr)
				}
			}
			return result, err
		}
		if closeErr != nil {
			return result, closeErr
		}
		addUsage(&result.Usage, usage)
		if err := r.Store.AddThreadUsage(ctx, start.Actor.WorkspaceID, start.ThreadID, int64(usage.InputTokens), int64(usage.OutputTokens), int32(model.ContextWindowTokens)); err != nil {
			return result, err
		}
		if len(calls) == 0 {
			if _, err := r.Store.PersistMessage(ctx, agentchat.MessageInput{
				WorkspaceID: start.Actor.WorkspaceID, ThreadID: start.ThreadID, TurnID: start.TurnID,
				Role: ai.RoleAssistant, Status: agentchat.MessageStatusSent, Parts: parts,
			}); err != nil {
				return result, err
			}
			result.Touched = sortedKeys(touched)
			return result, nil
		}
		// A step cut off at max_tokens leaves its tool arguments truncated:
		// running them would dispatch a call the model never finished writing.
		// Stop with a reason instead of treating the fragment as intent.
		if stop == ai.StopMaxTokens {
			for i := range parts {
				if parts[i].Type == agentchat.PartToolCall && parts[i].State == agentchat.PartStateRunning {
					parts[i].State = agentchat.PartStateError
					parts[i].Error = "The model hit its output limit while writing this tool call."
				}
			}
			if _, err := r.Store.PersistMessage(ctx, agentchat.MessageInput{
				WorkspaceID: start.Actor.WorkspaceID, ThreadID: start.ThreadID, TurnID: start.TurnID,
				Role: ai.RoleAssistant, Status: agentchat.MessageStatusSent, Parts: parts,
			}); err != nil {
				return result, err
			}
			return result, errors.New("the model reached its output limit before it finished requesting a tool")
		}
		if requests := r.approvalRequests(start.Actor, calls, parts); len(requests) > 0 {
			actions, err := r.Approvals.PauseForApproval(ctx, agentchat.MessageInput{
				WorkspaceID: start.Actor.WorkspaceID, ThreadID: start.ThreadID, TurnID: start.TurnID,
				Role: ai.RoleAssistant, Status: agentchat.MessageStatusSent, Parts: parts,
			}, start, requests)
			if err != nil {
				return result, err
			}
			pub.Publish(ctx, agentchat.Event{
				Type: agentchat.EventMessagePersisted, RunID: start.RunID.String(),
			})
			for _, action := range actions {
				pub.Publish(ctx, agentchat.Event{
					Type: agentchat.EventApprovalRequired, RunID: start.RunID.String(),
					ActionID: action.ID.String(), ToolCallID: action.ToolCallID,
					ToolName: action.ToolName, ToolInput: action.Arguments,
					Risk: action.RiskTier, Status: action.Status,
					ExpiresAt: action.ExpiresAt.UTC().Format(time.RFC3339),
				})
			}
			result.Paused = true
			return result, nil
		}
		toolParts, toolMessage, err := r.executeTools(ctx, start, calls, touched, pub)
		if err != nil {
			return result, err
		}
		for i, call := range calls {
			parts[call.part].State = toolParts[i].State
			parts[call.part].Error = toolParts[i].Error
		}
		if _, err := r.Store.PersistMessage(ctx, agentchat.MessageInput{
			WorkspaceID: start.Actor.WorkspaceID, ThreadID: start.ThreadID, TurnID: start.TurnID,
			Role: ai.RoleAssistant, Status: agentchat.MessageStatusSent, Parts: parts,
		}); err != nil {
			return result, err
		}
		messages = append(messages, toChatMessage(ai.RoleAssistant, parts))
		if _, err := r.Store.PersistMessage(ctx, agentchat.MessageInput{
			WorkspaceID: start.Actor.WorkspaceID, ThreadID: start.ThreadID, TurnID: start.TurnID,
			Role: ai.RoleUser, Status: agentchat.MessageStatusSent, Parts: toolParts,
		}); err != nil {
			return result, err
		}
		messages = append(messages, toolMessage)
	}
	return result, fmt.Errorf("agent reached the %d-step safety limit", MaxSteps)
}

func (r *Runtime) approvalRequests(actor agentchat.Actor, calls []toolCall, parts []agentchat.PartInput) []agentchat.ApprovalRequest {
	if r.Approvals == nil || r.Tools == nil {
		return nil
	}
	risks := make(map[string]string)
	for _, tool := range r.Tools.Definitions(actor) {
		risks[tool.Name] = tool.Risk
	}
	requests := make([]agentchat.ApprovalRequest, 0)
	for _, call := range calls {
		risk := risks[call.name]
		if risk != "consequential" && risk != "irreversible" {
			continue
		}
		parts[call.part].State = agentchat.PartStateAwaitingApproval
		requests = append(requests, agentchat.ApprovalRequest{
			ToolName: call.name, ToolCallID: call.id, Arguments: call.input,
			RiskTier: risk, ExpiresAt: time.Now().Add(24 * time.Hour),
		})
	}
	return requests
}

func (r *Runtime) consume(ctx context.Context, start agentchat.RunStart, stream ai.ChatStream, pub *runPublisher) ([]agentchat.PartInput, []toolCall, ai.Usage, string, error) {
	var parts []agentchat.PartInput
	calls := map[string]*toolCall{}
	var order []string
	var usage ai.Usage
	stop := ai.StopEndTurn
	for {
		event, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return parts, orderedCalls(calls, order), usage, stop, err
		}
		wire := agentchat.Event{RunID: start.RunID.String()}
		switch event.Type {
		case ai.EventTextDelta:
			appendTextPart(&parts, agentchat.PartText, event.Text)
			wire.Type, wire.Text = agentchat.EventTextDelta, event.Text
		case ai.EventReasoningDelta:
			appendTextPart(&parts, agentchat.PartReasoning, event.Text)
			wire.Type, wire.Text = agentchat.EventReasoningDelta, event.Text
		case ai.EventToolCallStart:
			call := &toolCall{id: event.ToolCallID, name: event.ToolName, part: len(parts)}
			calls[call.id] = call
			order = append(order, call.id)
			parts = append(parts, agentchat.PartInput{Type: agentchat.PartToolCall, ToolCallID: call.id, ToolName: call.name, State: agentchat.PartStateRunning})
			wire.Type, wire.ToolCallID, wire.ToolName = agentchat.EventToolInputStart, call.id, call.name
		case ai.EventToolInputDelta:
			wire.Type, wire.ToolCallID, wire.Text = agentchat.EventToolInputDelta, event.ToolCallID, event.Text
		case ai.EventToolCallEnd:
			call := calls[event.ToolCallID]
			if call == nil {
				call = &toolCall{id: event.ToolCallID, name: event.ToolName, part: len(parts)}
				calls[call.id], order = call, append(order, call.id)
				parts = append(parts, agentchat.PartInput{Type: agentchat.PartToolCall, ToolCallID: call.id, ToolName: call.name, State: agentchat.PartStateRunning})
			}
			call.input = event.ToolInput
			parts[call.part].ToolInput = event.ToolInput
			continue
		case ai.EventUsage:
			if event.Usage != nil {
				usage = *event.Usage
			}
			stop = event.StopReason
			continue
		default:
			continue
		}
		pub.Publish(ctx, wire)
	}
	// The step is over: nothing may stay buffered behind a delta window while
	// the loop moves on to persist and to publish tool output.
	pub.Flush(ctx)
	return parts, orderedCalls(calls, order), usage, stop, nil
}

func orderedCalls(calls map[string]*toolCall, order []string) []toolCall {
	out := make([]toolCall, 0, len(order))
	for _, id := range order {
		out = append(out, *calls[id])
	}
	return out
}

func appendTextPart(parts *[]agentchat.PartInput, kind, delta string) {
	if delta == "" {
		return
	}
	p := *parts
	if len(p) > 0 && p[len(p)-1].Type == kind {
		if kind == agentchat.PartReasoning {
			p[len(p)-1].Reasoning += delta
		} else {
			p[len(p)-1].Text += delta
		}
		return
	}
	next := agentchat.PartInput{Type: kind}
	if kind == agentchat.PartReasoning {
		next.Reasoning = delta
	} else {
		next.Text = delta
	}
	*parts = append(*parts, next)
}

func (r *Runtime) toolDefinitions(actor agentchat.Actor) []ai.ToolDef {
	if r.Tools == nil {
		return nil
	}
	tools := r.Tools.Definitions(actor)
	out := make([]ai.ToolDef, 0, len(tools))
	for _, tool := range tools {
		if r.Approvals == nil && (tool.Risk == "consequential" || tool.Risk == "irreversible") {
			continue
		}
		out = append(out, ai.ToolDef{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema})
	}
	return out
}

func (r *Runtime) Resume(ctx context.Context, start agentchat.RunStart) (Result, error) {
	if r.Approvals == nil {
		return Result{}, errors.New("agent approval store is unavailable")
	}
	batch, err := r.Approvals.LoadApprovalBatch(ctx, start)
	if err != nil {
		return Result{}, err
	}
	pub := r.publisher(start)
	defer pub.Close(ctx)
	touched := make(map[string]bool)
	results := make([]agentchat.ApprovalResult, 0, len(batch.Calls))
	for _, call := range batch.Calls {
		input := call.Arguments
		toolResult := ToolResult{}
		outcome := ""
		var executeErr error
		switch {
		case call.Action == nil:
			toolResult, executeErr = r.Tools.Execute(ctx, start.Actor, call.ToolName, input)
		case call.Action.Status == agentchat.ActionStatusExecuted || call.Action.Status == agentchat.ActionStatusFailed:
			// Already run in an earlier resume of this same message. Nothing
			// to do, and re-running it would repeat the side effect.
			continue
		case call.Action.Status == agentchat.ActionStatusApproved && !call.Action.ExpiresAt.After(time.Now()):
			// Approved, but the deadline passed before the run got to it —
			// typically because a sibling action in the same pause sat
			// undecided until the expiry sweep. An approval is permission to
			// act NOW, not whenever the batch happens to resume.
			outcome = agentchat.ActionStatusExpired
			toolResult.Output, _ = json.Marshal(map[string]string{
				"status": "rejected", "reason": "This approval expired before the agent could run it.",
			})
		case call.Action.Status == agentchat.ActionStatusApproved:
			input = call.Action.EffectiveArguments()
			toolResult, executeErr = r.Tools.ExecuteApproved(ctx, start.Actor, call.ToolName, input)
			outcome = agentchat.ActionStatusExecuted
		case call.Action.Status == agentchat.ActionStatusRejected || call.Action.Status == agentchat.ActionStatusExpired:
			toolResult.Output, _ = json.Marshal(map[string]string{
				"status": "rejected", "reason": call.Action.DecisionReason,
			})
		default:
			return Result{}, fmt.Errorf("approval action %s cannot resume from status %s", call.Action.ID, call.Action.Status)
		}
		if executeErr != nil {
			return Result{}, executeErr
		}
		if len(toolResult.Output) == 0 || !json.Valid(toolResult.Output) {
			toolResult = ToolResult{Output: json.RawMessage(`{"success":false,"error":"tool returned invalid output"}`), IsError: true}
		}
		if toolResult.IsError && outcome == agentchat.ActionStatusExecuted {
			outcome = agentchat.ActionStatusFailed
		}
		result := agentchat.ApprovalResult{
			PartID: call.PartID, ToolName: call.ToolName, ToolCallID: call.ToolCallID,
			ToolInput: input, ToolOutput: toolResult.Output, IsError: toolResult.IsError,
			Action: call.Action, Status: outcome,
		}
		// Settle this action before touching the next one: its side effect has
		// already happened, so its row must not still read 'approved' if the
		// call after it fails the run.
		if outcome != "" {
			if err := r.Approvals.RecordApprovalOutcome(ctx, result); err != nil {
				return Result{}, err
			}
		}
		results = append(results, result)
		pub.Publish(ctx, agentchat.Event{
			Type: agentchat.EventToolOutput, RunID: start.RunID.String(),
			ToolCallID: call.ToolCallID, ToolName: call.ToolName, ToolInput: input,
			ToolOutput: toolResult.Output, IsError: toolResult.IsError,
		})
		if !toolResult.IsError && isWriteTool(call.ToolName) {
			touched[objectType(call.ToolName)] = true
		}
	}
	if err := r.Approvals.CompleteApprovalBatch(ctx, start, results); err != nil {
		return Result{}, err
	}
	result, err := r.Execute(ctx, start)
	for _, object := range result.Touched {
		touched[object] = true
	}
	result.Touched = sortedKeys(touched)
	return result, err
}

func (r *Runtime) transcript(ctx context.Context, start agentchat.RunStart) ([]ai.ChatMessage, string, error) {
	stored, err := r.Store.ListTranscript(ctx, start.Actor.WorkspaceID, start.ThreadID)
	if err != nil {
		return nil, "", err
	}
	messages := make([]ai.ChatMessage, 0, len(stored))
	firstUserText := ""
	for _, message := range stored {
		chat := ai.ChatMessage{Role: message.Row.Role}
		for _, part := range message.Parts {
			switch part.Type {
			case agentchat.PartText, agentchat.PartCompactionNotice:
				chat.Parts = append(chat.Parts, ai.ChatPart{Type: ai.PartText, Text: part.TextContent})
				if message.Row.Role == ai.RoleUser && firstUserText == "" {
					firstUserText = part.TextContent
				}
			case agentchat.PartReasoning:
				chat.Parts = append(chat.Parts, ai.ChatPart{Type: ai.PartReasoning, Text: part.ReasoningContent})
			case agentchat.PartToolCall:
				chat.Parts = append(chat.Parts, ai.ChatPart{Type: ai.PartToolCall, ToolCallID: part.ToolCallID, ToolName: part.ToolName, ToolInput: part.ToolInput})
			case agentchat.PartToolResult:
				chat.Parts = append(chat.Parts, ai.ChatPart{Type: ai.PartToolResult, ToolCallID: part.ToolCallID, ToolName: part.ToolName, ToolOutput: part.ToolOutput, IsError: part.State == agentchat.PartStateError})
			}
		}
		if contextText := agentchat.BrowsingContextText(message.Row.BrowsingContext); contextText != "" {
			chat.Parts = append(chat.Parts, ai.ChatPart{Type: ai.PartText, Text: contextText})
		}
		if len(chat.Parts) > 0 {
			messages = append(messages, chat)
		}
	}
	return messages, firstUserText, nil
}

func (r *Runtime) executeTools(ctx context.Context, start agentchat.RunStart, calls []toolCall, touched map[string]bool, pub *runPublisher) ([]agentchat.PartInput, ai.ChatMessage, error) {
	parts := make([]agentchat.PartInput, 0, len(calls))
	chat := ai.ChatMessage{Role: ai.RoleUser}
	for _, call := range calls {
		loading := loadingMessage(call.input)
		result := ToolResult{Output: json.RawMessage(`{"success":false,"error":"tool is not available"}`), IsError: true}
		if r.Tools != nil {
			var err error
			result, err = r.Tools.Execute(ctx, start.Actor, call.name, call.input)
			if err != nil {
				return nil, ai.ChatMessage{}, err
			}
		}
		if len(result.Output) == 0 || !json.Valid(result.Output) {
			result = ToolResult{Output: json.RawMessage(`{"success":false,"error":"tool returned invalid output"}`), IsError: true}
		}
		state := agentchat.PartStateDone
		if result.IsError {
			state = agentchat.PartStateError
		}
		parts = append(parts, agentchat.PartInput{
			Type: agentchat.PartToolResult, ToolName: call.name, ToolCallID: call.id,
			ToolOutput: result.Output, State: state,
		})
		chat.Parts = append(chat.Parts, ai.ChatPart{
			Type: ai.PartToolResult, ToolName: call.name, ToolCallID: call.id,
			ToolOutput: result.Output, IsError: result.IsError,
		})
		pub.Publish(ctx, agentchat.Event{
			Type: agentchat.EventToolOutput, RunID: start.RunID.String(),
			ToolCallID: call.id, ToolName: call.name, ToolInput: call.input,
			ToolOutput: result.Output, IsError: result.IsError, LoadingMessage: loading,
		})
		if !result.IsError && isWriteTool(call.name) {
			touched[objectType(call.name)] = true
		}
	}
	return parts, chat, nil
}

func loadingMessage(input json.RawMessage) string {
	var args map[string]json.RawMessage
	if json.Unmarshal(input, &args) != nil {
		return ""
	}
	var message string
	_ = json.Unmarshal(args["loading_message"], &message)
	return strings.TrimSpace(message)
}

func isWriteTool(name string) bool {
	return strings.HasSuffix(name, "_write") || strings.HasSuffix(name, "_control") || strings.HasSuffix(name, "_import")
}

func objectType(name string) string {
	name = strings.TrimPrefix(name, "inroad_")
	name = strings.TrimSuffix(name, "_write")
	name = strings.TrimSuffix(name, "_control")
	name = strings.TrimSuffix(name, "_import")
	if name == "contacts" {
		return "contact"
	}
	return name
}

func toChatMessage(role string, parts []agentchat.PartInput) ai.ChatMessage {
	out := ai.ChatMessage{Role: role}
	for _, part := range parts {
		switch part.Type {
		case agentchat.PartText, agentchat.PartCompactionNotice:
			out.Parts = append(out.Parts, ai.ChatPart{Type: ai.PartText, Text: part.Text})
		case agentchat.PartReasoning:
			out.Parts = append(out.Parts, ai.ChatPart{Type: ai.PartReasoning, Text: part.Reasoning})
		case agentchat.PartToolCall:
			out.Parts = append(out.Parts, ai.ChatPart{Type: ai.PartToolCall, ToolCallID: part.ToolCallID, ToolName: part.ToolName, ToolInput: part.ToolInput})
		}
	}
	return out
}

func addUsage(total *ai.Usage, next ai.Usage) {
	total.InputTokens += next.InputTokens
	total.OutputTokens += next.OutputTokens
	total.ReasoningTokens += next.ReasoningTokens
	total.CacheReadTokens += next.CacheReadTokens
}

func sortedKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		if key != "" {
			out = append(out, key)
		}
	}
	slices.Sort(out)
	return out
}

func (r *Runtime) GenerateTitle(ctx context.Context, workspaceID uuid.UUID, firstUserText string) string {
	fallback := titleFallback(firstUserText)
	if fallback == "" {
		return "New conversation"
	}
	model, err := r.Models.Resolve(ctx, workspaceID, ai.SentinelFastModel)
	if err != nil {
		return fallback
	}
	request := ai.ChatRequest{
		Model:     model.Name,
		System:    "Write a concise title for this conversation. Return only the title, with no quotes or punctuation wrapper. Maximum 60 characters.",
		Messages:  []ai.ChatMessage{{Role: ai.RoleUser, Parts: []ai.ChatPart{{Type: ai.PartText, Text: firstUserText}}}},
		MaxTokens: min(model.MaxOutputTokens, 64),
	}
	stream, err := model.Streamer.StreamChat(ctx, request)
	if err != nil {
		return fallback
	}
	defer func() { _ = stream.Close() }()
	var title strings.Builder
	for {
		event, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fallback
		}
		if event.Type == ai.EventTextDelta {
			title.WriteString(event.Text)
		}
	}
	clean := cleanTitle(title.String())
	if clean == "" {
		return fallback
	}
	return clean
}

func titleFallback(text string) string {
	words := strings.Fields(text)
	if len(words) > 8 {
		words = words[:8]
	}
	return cleanTitle(strings.Join(words, " "))
}

func cleanTitle(title string) string {
	title = strings.Trim(strings.TrimSpace(title), `"' `)
	title = strings.Join(strings.Fields(title), " ")
	runes := []rune(title)
	if len(runes) > 60 {
		title = strings.TrimSpace(string(runes[:60]))
	}
	return title
}
