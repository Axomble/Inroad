// Package agentchat is the in-app AI agent: thread/message persistence, the
// agentic run loop, and the Redis-backed stream that carries a run's output to
// however many browser tabs are watching (agent-platform spec §1/§2/§5, PR A2).
//
// The load-bearing decision is that THE RUN IS DECOUPLED FROM THE HTTP
// CONNECTION. Posting a message returns 202 immediately; the run executes in
// the API binary as a managed goroutine and writes every chunk to Redis.
// GET /agent/threads/{id}/stream is a pure reader over that Redis log, so
// closing the panel, refreshing, or opening a second tab neither stops the run
// nor loses a token.
//
// Runs execute in the API binary rather than through asynq on purpose: the
// agent needs sealed LLM credentials and relational data, and workers must
// never hold either.
//
// Following the reference domain in internal/app/mailbox, this package defines
// its own interfaces at every seam it depends on (Store, ModelResolver,
// StreamPublisher, ApprovalGate) and depends on the interface, never on the
// concrete implementation — so the whole run loop is unit-testable against a
// fake provider and a fake tool registry, with no network and no database.
package agentchat

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// SSE event types (spec §1). These are the wire contract the chat panel
// consumes; the ai.StreamEvent types this package converts FROM are an
// internal provider detail and deliberately do not match one-to-one.
const (
	// EventTextDelta is a fragment of the assistant's prose answer.
	EventTextDelta = "text_delta"
	// EventReasoningDelta is a fragment of extended thinking.
	EventReasoningDelta = "reasoning_delta"
	// EventToolInputStart announces a tool call (id + name) before any of its
	// arguments have streamed.
	EventToolInputStart = "tool_input_start"
	// EventToolInputDelta is a fragment of the tool's JSON arguments. Because
	// loading_message is injected as the FIRST property of every tool schema,
	// a client that incrementally parses these deltas has the human-readable
	// progress line long before the call is dispatched.
	EventToolInputDelta = "tool_input_delta"
	// EventToolOutput carries a completed tool call: final input, result, and
	// the loading_message the model wrote for it.
	EventToolOutput = "tool_output"
	// EventApprovalRequired parks a consequential/irreversible tool call for a
	// human decision. A2 emits it and exits the run; A4 implements the resume.
	EventApprovalRequired = "approval_required"
	// EventThreadTitle carries the generated thread title.
	EventThreadTitle = "thread_title"
	// EventQueueUpdated reports the thread's pending message queue after a
	// message was enqueued or promoted.
	EventQueueUpdated = "queue_updated"
	// EventMessagePersisted tells the client the canonical transcript changed
	// and should be refetched — the streamed chunks were the preview.
	EventMessagePersisted = "message_persisted"
	// EventRunError terminates a run that failed. Text is user-facing.
	EventRunError = "run_error"
	// EventDone terminates a successful (or cancelled) run and carries the
	// object types its tool calls touched, so the client can invalidate
	// exactly those RTK Query tags.
	EventDone = "done"
)

// terminalEvents end a run's stream. The SSE bridge resets its sequence
// high-water mark when it sees one, because the run's Redis log is deleted on
// completion and the next run's sequence restarts at 1.
var terminalEvents = map[string]bool{EventDone: true, EventRunError: true}

// Event is one chunk on the stream. Only the fields relevant to Type are
// populated. It is JSON-encoded into the SSE `data:` line; the sequence number
// travels in the SSE `id:` line, not in the body, so replaying a stored chunk
// after a reconnect produces byte-identical output.
type Event struct {
	Type string `json:"type"`

	// RunID is set on every event of a run so a client that reconnects
	// mid-run can tell whose output it is reading.
	RunID string `json:"run_id,omitempty"`

	// Text carries EventTextDelta / EventReasoningDelta fragments, the
	// EventToolInputDelta argument fragments, and the EventRunError message.
	Text string `json:"text,omitempty"`

	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	ToolInput  json.RawMessage `json:"tool_input,omitempty"`
	ToolOutput json.RawMessage `json:"tool_output,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
	// LoadingMessage is the model-written progress line for a tool call, read
	// out of the call's loading_message argument.
	LoadingMessage string `json:"loading_message,omitempty"`
	// Risk is the tool's tier on EventApprovalRequired ("consequential" |
	// "irreversible").
	Risk string `json:"risk,omitempty"`

	// Title is set on EventThreadTitle.
	Title string `json:"title,omitempty"`

	// Queued is the thread's pending queue on EventQueueUpdated.
	Queued []QueuedMessageDTO `json:"queued,omitempty"`

	// ObjectTypes is set on EventDone: the object types this run's MUTATING
	// tool calls touched. Read-only calls contribute nothing, because nothing
	// they did needs invalidating.
	ObjectTypes []string `json:"object_types,omitempty"`
}

// Thread lifecycle statuses on the wire.
const (
	RunStatusRunning        = "running"
	RunStatusPausedApproval = "paused_approval"
	RunStatusDone           = "done"
	RunStatusFailed         = "failed"
	RunStatusCancelled      = "cancelled"
)

// Message queue statuses.
const (
	MessageStatusSent       = "sent"
	MessageStatusQueued     = "queued"
	MessageStatusProcessing = "processing"
)

// Message part types persisted in agent_message_parts.
const (
	PartText             = "text"
	PartReasoning        = "reasoning"
	PartToolCall         = "tool_call"
	PartToolResult       = "tool_result"
	PartCompactionNotice = "compaction_notice"
)

// Tool-call part states.
const (
	PartStateRunning          = "running"
	PartStateDone             = "done"
	PartStateError            = "error"
	PartStateAwaitingApproval = "awaiting_approval"
)

// ThreadDTO is the wire shape of a thread. Messages is populated only by the
// single-thread read; the list omits it.
type ThreadDTO struct {
	ID                  string       `json:"id"`
	Title               string       `json:"title"`
	TotalInputTokens    int64        `json:"total_input_tokens"`
	TotalOutputTokens   int64        `json:"total_output_tokens"`
	ContextWindowTokens int          `json:"context_window_tokens"`
	ActiveRunID         *string      `json:"active_run_id"`
	CreatedAt           string       `json:"created_at"`
	UpdatedAt           string       `json:"updated_at"`
	Messages            []MessageDTO `json:"messages,omitempty"`
}

// MessageDTO is one persisted turn with its normalized parts.
type MessageDTO struct {
	ID        string    `json:"id"`
	TurnID    string    `json:"turn_id"`
	Role      string    `json:"role"`
	Status    string    `json:"status"`
	CreatedAt string    `json:"created_at"`
	Parts     []PartDTO `json:"parts"`
}

// PartDTO is one normalized message part. Fields irrelevant to Type are
// omitted rather than sent empty, so the client can switch on Type alone.
type PartDTO struct {
	ID         string          `json:"id"`
	OrderIndex int             `json:"order_index"`
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	Reasoning  string          `json:"reasoning,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolInput  json.RawMessage `json:"tool_input,omitempty"`
	ToolOutput json.RawMessage `json:"tool_output,omitempty"`
	State      string          `json:"state,omitempty"`
	Error      string          `json:"error,omitempty"`
}

// QueuedMessageDTO is one message waiting for the active run to finish.
type QueuedMessageDTO struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Model     string `json:"model"`
	CreatedAt string `json:"created_at"`
}

// SendResultDTO is the 202 body of POST /agent/threads/{id}/messages. RunID is
// null when the message was queued behind an already-active run; the client
// keeps its existing stream subscription and waits for queue_updated.
type SendResultDTO struct {
	MessageID string  `json:"message_id"`
	RunID     *string `json:"run_id"`
	Queued    bool    `json:"queued"`
}

// BrowsingContext is the client's page context for one message
// ({type:'record_page', object, record_id, url} | {type:'list_view', view,
// filters}). It is stored per-message and appended as TEXT to the last user
// message — never to the system prompt, which must stay byte-stable for the
// provider's prompt cache (spec §5).
type BrowsingContext struct {
	Type     string            `json:"type"`
	Object   string            `json:"object,omitempty"`
	RecordID string            `json:"record_id,omitempty"`
	URL      string            `json:"url,omitempty"`
	View     string            `json:"view,omitempty"`
	Filters  map[string]string `json:"filters,omitempty"`
}

// rfc3339 renders a timestamp for the wire; the zero value renders as "".
func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// uuidPtr renders an optional id as a nullable wire field.
func uuidPtr(id uuid.UUID) *string {
	if id == uuid.Nil {
		return nil
	}
	s := id.String()
	return &s
}
