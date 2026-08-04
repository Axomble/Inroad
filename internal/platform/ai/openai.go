package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/azure"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/openai/openai-go/v3/shared"
)

// openAIStreamer speaks the Chat Completions protocol — deliberately NOT the
// Responses API, because the whole point of the openai_compatible kind is that
// OpenRouter, LiteLLM, Groq, Together, vLLM and Ollama all implement Chat
// Completions and almost none implement Responses.
type openAIStreamer struct {
	kind   string
	client openai.Client
	// legacyMaxTokens sends the older max_tokens field instead of
	// max_completion_tokens. Gateways lag the OpenAI schema, and max_tokens is
	// the field every one of them still accepts.
	legacyMaxTokens bool
}

func newOpenAIStreamer(kind string, creds Credentials, config map[string]string, hc *http.Client) (*openAIStreamer, error) {
	// The API key is set explicitly on every path, even when empty, so a stray
	// OPENAI_API_KEY in the host environment can never be picked up and sent to
	// a workspace's gateway.
	opts := []option.RequestOption{option.WithHTTPClient(hc), option.WithAPIKey(creds.APIKey)}
	legacy := false

	switch kind {
	case KindOpenAI:
		if creds.APIKey == "" {
			return nil, fmt.Errorf("%w: kind %q requires credentials.api_key", ErrBadProviderConfig, kind)
		}

	case KindAzureOpenAI:
		endpoint, err := requireHTTPBaseURL(config, kind, "endpoint")
		if err != nil {
			return nil, err
		}
		apiVersion, err := requireConfig(config, kind, "api_version")
		if err != nil {
			return nil, err
		}
		if creds.APIKey == "" {
			return nil, fmt.Errorf("%w: kind %q requires credentials.api_key", ErrBadProviderConfig, kind)
		}
		opts = append(opts, azure.WithEndpoint(endpoint, apiVersion), azure.WithAPIKey(creds.APIKey))

	case KindOpenAICompatible:
		baseURL, err := requireHTTPBaseURL(config, kind, "base_url")
		if err != nil {
			return nil, err
		}
		// A keyless door (a local Ollama) is legitimate here, so no key check.
		opts = append(opts, option.WithBaseURL(normalizeBaseURL(baseURL)))
		legacy = true

	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedKind, kind)
	}

	return &openAIStreamer{kind: kind, client: openai.NewClient(opts...), legacyMaxTokens: legacy}, nil
}

// normalizeBaseURL makes a stored gateway root usable as an SDK base: the SDK
// resolves relative paths against it, which silently drops the last segment
// ("…/v1" would become "…/chat/completions") unless it ends in a slash.
func normalizeBaseURL(raw string) string {
	return strings.TrimSuffix(strings.TrimSpace(raw), "/") + "/"
}

func (s *openAIStreamer) StreamChat(ctx context.Context, req ChatRequest) (ChatStream, error) {
	if err := validateRequest(req); err != nil {
		return nil, err
	}
	params, err := openAIParams(req, s.legacyMaxTokens)
	if err != nil {
		return nil, err
	}
	return &openAIStream{kind: s.kind, src: s.client.Chat.Completions.NewStreaming(ctx, params)}, nil
}

// openAIParams translates the neutral ChatRequest into Chat Completions params.
//
// Two shape changes matter. The system prompt becomes a leading system message
// (not a top-level field), and tool results become their own tool-role messages
// that must IMMEDIATELY follow the assistant message that requested them — so
// when a user turn carries tool results, those are emitted before the user's
// own prose. Reasoning parts are dropped on replay: no OpenAI-family endpoint
// accepts reasoning text back as input.
func openAIParams(req ChatRequest, legacyMaxTokens bool) (openai.ChatCompletionNewParams, error) {
	params := openai.ChatCompletionNewParams{
		Model:         req.Model,
		StreamOptions: openai.ChatCompletionStreamOptionsParam{IncludeUsage: param.NewOpt(true)},
	}
	if legacyMaxTokens {
		params.MaxTokens = param.NewOpt(int64(req.MaxTokens))
	} else {
		params.MaxCompletionTokens = param.NewOpt(int64(req.MaxTokens))
	}
	if req.System != "" {
		params.Messages = append(params.Messages, openai.SystemMessage(req.System))
	}

	for _, m := range req.Messages {
		msgs, err := openAIMessages(m)
		if err != nil {
			return params, err
		}
		params.Messages = append(params.Messages, msgs...)
	}
	if len(params.Messages) == 0 {
		return params, fmt.Errorf("%w: request carried no sendable content", ErrBadProviderConfig)
	}

	for _, t := range req.Tools {
		schema, err := toolSchemaMap(t)
		if err != nil {
			return params, err
		}
		fn := shared.FunctionDefinitionParam{Name: t.Name, Parameters: schema}
		if t.Description != "" {
			fn.Description = param.NewOpt(t.Description)
		}
		params.Tools = append(params.Tools, openai.ChatCompletionFunctionTool(fn))
	}
	return params, nil
}

func openAIMessages(m ChatMessage) ([]openai.ChatCompletionMessageParamUnion, error) {
	var out []openai.ChatCompletionMessageParamUnion
	var text strings.Builder
	var toolCalls []openai.ChatCompletionMessageToolCallUnionParam

	for _, p := range m.Parts {
		switch p.Type {
		case PartText:
			text.WriteString(p.Text)
		case PartReasoning:
			continue
		case PartToolCall:
			input := "{}"
			if len(p.ToolInput) > 0 {
				input = string(p.ToolInput)
			}
			toolCalls = append(toolCalls, openai.ChatCompletionMessageToolCallUnionParam{
				OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
					ID: p.ToolCallID,
					Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
						Name:      p.ToolName,
						Arguments: input,
					},
				},
			})
		case PartToolResult:
			out = append(out, openai.ToolMessage(toolResultText(p), p.ToolCallID))
		default:
			return nil, fmt.Errorf("%w: unknown part type %q", ErrBadProviderConfig, p.Type)
		}
	}

	if m.Role == RoleAssistant {
		if text.Len() == 0 && len(toolCalls) == 0 {
			return out, nil
		}
		assistant := openai.ChatCompletionAssistantMessageParam{ToolCalls: toolCalls}
		if text.Len() > 0 {
			assistant.Content.OfString = param.NewOpt(text.String())
		}
		return append(out, openai.ChatCompletionMessageParamUnion{OfAssistant: &assistant}), nil
	}
	if text.Len() > 0 {
		out = append(out, openai.UserMessage(text.String()))
	}
	return out, nil
}

// openAIStream turns Chat Completions chunks into the neutral event sequence.
// Tool calls arrive as INDEX-keyed fragments spread over many chunks, so open
// calls are tracked by index and closed when the choice reports a finish
// reason — the protocol has no per-tool terminator of its own.
type openAIStream struct {
	kind string
	src  *ssestream.Stream[openai.ChatCompletionChunk]
	eventQueue

	toolCalls    map[int64]*openAIToolCall
	order        []int64
	usage        Usage
	rawOutput    int
	reasoning    int
	stopReason   string
	emittedUsage bool
	closeOnce    sync.Once
}

type openAIToolCall struct {
	id      string
	name    string
	started bool
	input   strings.Builder
}

func (s *openAIStream) Next() (StreamEvent, error) {
	for {
		if ev, ok := s.pop(); ok {
			return ev, nil
		}
		if !s.src.Next() {
			if err := s.src.Err(); err != nil {
				var apiErr *openai.Error
				if errors.As(err, &apiErr) {
					return StreamEvent{}, providerStatusError(s.kind, apiErr.StatusCode)
				}
				return StreamEvent{}, providerError(s.kind, err)
			}
			if s.emittedUsage {
				return StreamEvent{}, io.EOF
			}
			// A stream that ended without a finish_reason still owes the
			// runtime its terminal usage event and any unterminated tool call.
			s.emittedUsage = true
			s.closeOpenToolCalls()
			s.push(s.finalUsage())
			continue
		}
		s.consume(s.src.Current())
	}
}

// finalUsage applies the package's accounting contract. OpenAI-family responses
// fold reasoning tokens INTO completion_tokens, so the reasoning slice is
// subtracted back out here.
func (s *openAIStream) finalUsage() StreamEvent {
	u := normalizeUsage(s.usage.InputTokens, s.rawOutput, s.reasoning, s.usage.CacheReadTokens, true)
	return usageEvent(u, s.stopReason)
}

func (s *openAIStream) consume(chunk openai.ChatCompletionChunk) {
	if chunk.Usage.TotalTokens > 0 || chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0 {
		s.usage.InputTokens = int(chunk.Usage.PromptTokens)
		s.rawOutput = int(chunk.Usage.CompletionTokens)
		s.reasoning = int(chunk.Usage.CompletionTokensDetails.ReasoningTokens)
		s.usage.CacheReadTokens = int(chunk.Usage.PromptTokensDetails.CachedTokens)
	}
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != "" {
			s.push(StreamEvent{Type: EventTextDelta, Text: choice.Delta.Content})
		}
		if reasoning := openAIReasoningDelta(choice.Delta); reasoning != "" {
			s.push(StreamEvent{Type: EventReasoningDelta, Text: reasoning})
		}
		for _, tc := range choice.Delta.ToolCalls {
			s.consumeToolCall(tc)
		}
		if choice.FinishReason != "" {
			s.stopReason = openAIStopReason(choice.FinishReason)
			s.closeOpenToolCalls()
		}
	}
}

func (s *openAIStream) consumeToolCall(tc openai.ChatCompletionChunkChoiceDeltaToolCall) {
	if s.toolCalls == nil {
		s.toolCalls = map[int64]*openAIToolCall{}
	}
	call, ok := s.toolCalls[tc.Index]
	if !ok {
		call = &openAIToolCall{}
		s.toolCalls[tc.Index] = call
		s.order = append(s.order, tc.Index)
	}
	if tc.ID != "" {
		call.id = tc.ID
	}
	if tc.Function.Name != "" {
		call.name += tc.Function.Name
	}
	// The announcement waits for a name: the first fragment of a call may carry
	// only an index, and a start event without a tool name tells the UI nothing.
	if !call.started && call.name != "" {
		call.started = true
		s.push(StreamEvent{Type: EventToolCallStart, ToolCallID: call.id, ToolName: call.name})
	}
	if tc.Function.Arguments != "" {
		call.input.WriteString(tc.Function.Arguments)
		s.push(StreamEvent{Type: EventToolInputDelta, ToolCallID: call.id, Text: tc.Function.Arguments})
	}
}

func (s *openAIStream) closeOpenToolCalls() {
	for _, index := range s.order {
		call, ok := s.toolCalls[index]
		if !ok {
			continue
		}
		delete(s.toolCalls, index)
		if !call.started {
			s.push(StreamEvent{Type: EventToolCallStart, ToolCallID: call.id, ToolName: call.name})
		}
		s.push(StreamEvent{
			Type:       EventToolCallEnd,
			ToolCallID: call.id,
			ToolName:   call.name,
			ToolInput:  accumulatedToolInput(call.input.String()),
		})
	}
	s.order = nil
}

// openAIReasoningDelta reads the reasoning fragment gateways add outside the
// documented schema. There is no standard field: DeepSeek and vLLM emit
// reasoning_content, OpenRouter emits reasoning. Both are read as extras rather
// than guessed at, and a model with no reasoning simply has neither.
func openAIReasoningDelta(delta openai.ChatCompletionChunkChoiceDelta) string {
	for _, key := range []string{"reasoning_content", "reasoning"} {
		field, ok := delta.JSON.ExtraFields[key]
		if !ok || !field.Valid() {
			continue
		}
		var text string
		if err := json.Unmarshal([]byte(field.Raw()), &text); err == nil && text != "" {
			return text
		}
	}
	return ""
}

func openAIStopReason(reason string) string {
	switch reason {
	case "tool_calls", "function_call":
		return StopToolUse
	case "length":
		return StopMaxTokens
	default:
		return StopEndTurn
	}
}

func (s *openAIStream) Close() error {
	var err error
	s.closeOnce.Do(func() { err = s.src.Close() })
	return err
}
