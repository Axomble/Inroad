package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
}

type Result struct {
	Usage         ai.Usage
	Touched       []string
	FirstUserText string
}

type Runtime struct {
	Store     agentchat.Store
	Models    agentchat.ModelResolver
	Tools     ToolRegistry
	Publisher agentchat.StreamPublisher
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
	compacted, err := agentchat.Prune(system, definitions, messages, model.ContextWindowTokens)
	if err != nil {
		return Result{}, err
	}
	if compacted {
		_, err = r.Store.PersistMessage(ctx, agentchat.MessageInput{
			WorkspaceID: start.Actor.WorkspaceID, ThreadID: start.ThreadID, TurnID: start.TurnID,
			Role: ai.RoleAssistant, Status: agentchat.MessageStatusSent,
			Parts: []agentchat.PartInput{{Type: agentchat.PartCompactionNotice, Text: agentchat.CompactionNotice}},
		})
		if err != nil {
			return Result{}, err
		}
	}
	result := Result{FirstUserText: firstUserText}
	touched := map[string]bool{}
	for step := 0; step < MaxSteps; step++ {
		req := ai.ChatRequest{Model: model.Name, System: system, Messages: messages, Tools: definitions, MaxTokens: model.MaxOutputTokens}
		stream, err := model.Streamer.StreamChat(ctx, req)
		if err != nil {
			return result, err
		}
		parts, calls, usage, _, err := r.consume(ctx, start, stream)
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
		toolParts, toolMessage, err := r.executeTools(ctx, start, calls, touched)
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
	return result, errors.New("agent reached the 50-step safety limit")
}

func (r *Runtime) consume(ctx context.Context, start agentchat.RunStart, stream ai.ChatStream) ([]agentchat.PartInput, []toolCall, ai.Usage, string, error) {
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
		if _, err := r.Publisher.Publish(ctx, start.ThreadID, wire); err != nil {
			return parts, orderedCalls(calls, order), usage, stop, err
		}
	}
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
		if tool.Risk == "consequential" || tool.Risk == "irreversible" {
			continue
		}
		out = append(out, ai.ToolDef{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema})
	}
	return out
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

func (r *Runtime) executeTools(ctx context.Context, start agentchat.RunStart, calls []toolCall, touched map[string]bool) ([]agentchat.PartInput, ai.ChatMessage, error) {
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
		event := agentchat.Event{
			Type: agentchat.EventToolOutput, RunID: start.RunID.String(),
			ToolCallID: call.id, ToolName: call.name, ToolInput: call.input,
			ToolOutput: result.Output, IsError: result.IsError, LoadingMessage: loading,
		}
		if _, err := r.Publisher.Publish(ctx, start.ThreadID, event); err != nil {
			return nil, ai.ChatMessage{}, err
		}
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

func isWriteTool(name string) bool { return strings.HasSuffix(name, "_write") }

func objectType(name string) string {
	name = strings.TrimPrefix(name, "inroad_")
	name = strings.TrimSuffix(name, "_write")
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
