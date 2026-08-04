package ai

import (
	"context"
	"encoding/json"
)

// This file is the CONTRACT between the agent runtime and the model providers.
// It is deliberately provider-neutral: the runtime (internal/app/agentchat)
// speaks only these types, and every provider kind — Anthropic direct, Bedrock,
// Vertex, OpenAI direct, Azure, any OpenAI-compatible gateway, Google AI Studio,
// Vertex Gemini — is reachable behind the same ChatStreamer. Nothing above this
// seam knows which door a model came through.

// Role values for ChatMessage. Tool results ride as parts of a user message
// (the shape every provider family accepts), never as a role of their own.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// ChatPart kinds. A conversation turn is a sequence of parts so a single
// assistant message can interleave prose with tool calls, which is what the
// runtime persists (one row per part) and replays on the next iteration.
const (
	PartText       = "text"
	PartReasoning  = "reasoning"
	PartToolCall   = "tool_call"
	PartToolResult = "tool_result"
)

// StreamEvent types emitted by ChatStream.Next.
const (
	EventTextDelta      = "text_delta"
	EventReasoningDelta = "reasoning_delta"
	EventToolCallStart  = "tool_call_start"
	EventToolInputDelta = "tool_input_delta"
	EventToolCallEnd    = "tool_call_end"
	EventUsage          = "usage"
)

// StopReason values reported on the terminal event of a provider turn.
const (
	StopEndTurn   = "end_turn"
	StopToolUse   = "tool_use"
	StopMaxTokens = "max_tokens"
)

// ChatPart is one element of a message. Only the fields relevant to Type are
// populated; the zero value of the rest carries no meaning.
type ChatPart struct {
	Type string `json:"type"`

	// Text carries PartText content and PartReasoning content.
	Text string `json:"text,omitempty"`

	// Tool-call fields (PartToolCall).
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	ToolInput  json.RawMessage `json:"tool_input,omitempty"`

	// Tool-result fields (PartToolResult). ToolCallID correlates back to the
	// call. IsError marks a tool that returned failure: the runtime feeds
	// failures back to the model as results rather than aborting the run, so
	// the model can self-correct.
	ToolOutput json.RawMessage `json:"tool_output,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
}

// ChatMessage is one turn of the conversation as the provider sees it.
type ChatMessage struct {
	Role  string     `json:"role"`
	Parts []ChatPart `json:"parts"`
}

// ToolDef is a tool as advertised to the model. InputSchema is a JSON Schema
// object; the registry injects the shared loading_message property so every
// tool narrates its own progress without per-tool UI work.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ChatRequest is one provider call. The runtime rebuilds it per iteration of
// the agentic loop, appending the previous iteration's tool results.
//
// Tools MUST stay constant for the whole conversation: providers cache the
// system prompt and tool definitions, and a changing tool list invalidates the
// cache on every turn. Discovery belongs inside a tool, not in rebinding.
type ChatRequest struct {
	// Model is the provider-facing model name (the bare name, not the
	// "<provider_row>/<name>" composite the API and settings use).
	Model     string        `json:"model"`
	System    string        `json:"system,omitempty"`
	Messages  []ChatMessage `json:"messages"`
	Tools     []ToolDef     `json:"tools,omitempty"`
	MaxTokens int           `json:"max_tokens"`
}

// Usage is token accounting for one provider turn.
//
// Normalization contract: OutputTokens EXCLUDES reasoning tokens for every
// provider. Anthropic SDK v1.61 and OpenAI-family responses both report an
// inclusive output total with reasoning as a subset, so those adapters
// subtract ReasoningTokens. Gemini reports candidate and thinking counts
// separately. Callers can therefore sum without knowing the provider.
type Usage struct {
	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
	CacheReadTokens int `json:"cache_read_tokens,omitempty"`
}

// StreamEvent is one incremental update from a provider turn. Deltas are
// fragments, not snapshots: the runtime accumulates them.
type StreamEvent struct {
	Type string `json:"type"`

	// Text carries EventTextDelta and EventReasoningDelta fragments.
	Text string `json:"text,omitempty"`

	// Tool-call streaming: EventToolCallStart announces ToolCallID+ToolName,
	// EventToolInputDelta streams the JSON argument fragments, and
	// EventToolCallEnd closes it with the accumulated ToolInput.
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	ToolInput  json.RawMessage `json:"tool_input,omitempty"`

	// Usage is set on EventUsage, emitted once before the stream ends.
	Usage *Usage `json:"usage,omitempty"`

	// StopReason is set on EventUsage: why the provider ended its turn.
	StopReason string `json:"stop_reason,omitempty"`
}

// ChatStream is a single provider turn in progress. Next blocks until the next
// event and returns io.EOF when the turn is complete. Close is safe to call
// once, and MUST be called (defer it) so the underlying HTTP body is released
// even when the caller abandons the stream early — a cancelled run must not
// leak a connection.
type ChatStream interface {
	Next() (StreamEvent, error)
	Close() error
}

// ChatStreamer is one configured door to a model provider. Implementations are
// constructed per provider row (kind + credentials + config) and are safe for
// concurrent use: a workspace may have several runs in flight at once.
//
// StreamChat MUST honor ctx cancellation — stopping a run cancels the context,
// and the provider request has to unwind with it.
type ChatStreamer interface {
	StreamChat(ctx context.Context, req ChatRequest) (ChatStream, error)
}

// StreamerFactory builds a ChatStreamer for a provider row. Kept as an
// interface so the runtime can be tested against a fake provider without any
// SDK, and so credential unsealing stays in the caller rather than here.
type StreamerFactory interface {
	Streamer(kind string, creds Credentials, config map[string]string) (ChatStreamer, error)
}
