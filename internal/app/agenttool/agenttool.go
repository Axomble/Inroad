// Package agenttool is the ONE registry of capabilities an AI agent can invoke.
//
// It exists so the in-app agent (PR A2/A3), the approval queue (A4), and the
// MCP server (Phase C) all reach the product through a single seam, executing
// as the invoking user with one authorization path and one audit path. A tool
// calls domain services, never stores directly — an agent must not be able to
// do something the HTTP surface would refuse.
package agenttool

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
)

// Risk tiers. The tier decides whether a call executes immediately or parks in
// the approval queue, and it is a property of the TOOL, never of the caller:
// one tool has exactly one risk level, so a client can always tell from the
// call alone whether it is consequential. Consolidate operations within a tier
// (a `method` argument), never across one.
type Risk int

const (
	// RiskRead never modifies anything. Runs without approval, always.
	RiskRead Risk = iota
	// RiskWrite is a reversible mutation. Runs without approval by default,
	// but is attributed in the activity feed and can be reverted.
	RiskWrite
	// RiskConsequential changes system behaviour in a way a user would want to
	// see first (pausing a live campaign, bulk imports). Approval required;
	// workspace policy may relax specific tools.
	RiskConsequential
	// RiskIrreversible sends mail to a real person or destroys data. ALWAYS
	// requires per-action human approval. There is no "always allow" for this
	// tier and no policy that can relax it — it is the deterministic backstop
	// that survives a prompt injection the model fell for.
	RiskIrreversible
)

func (r Risk) String() string {
	switch r {
	case RiskRead:
		return "read"
	case RiskWrite:
		return "write"
	case RiskConsequential:
		return "consequential"
	case RiskIrreversible:
		return "irreversible"
	default:
		return "unknown"
	}
}

// NeedsApproval reports the DEFAULT gate for a tier. Workspace policy may
// escalate anything and may relax RiskConsequential, but never RiskIrreversible
// (enforced by the approval layer in A4, which does not consult policy for it).
func (r Risk) NeedsApproval() bool { return r >= RiskConsequential }

// Errors the runtime distinguishes.
var (
	// ErrNotFound is returned for an unknown tool name.
	ErrNotFound = errors.New("agenttool: unknown tool")
	// ErrForbidden is returned when the principal may not use a tool that
	// exists — checked at catalog time AND again at execution, so a stale
	// descriptor from an earlier turn cannot escalate.
	ErrForbidden = errors.New("agenttool: not permitted")
)

// Principal is who a tool call runs as. The agent never has authority of its
// own: every call carries the delegating user's identity and role, and tools
// enforce exactly what that user could do through the UI.
type Principal struct {
	WorkspaceID uuid.UUID
	UserID      uuid.UUID
	// Role is the workspace role ("owner", "admin", "member"), used for the
	// same gating the HTTP handlers apply.
	Role string
	// AgentClientID identifies the agent surface making the call (a chat
	// thread, an MCP client). Recorded on audit rows so agent activity is
	// never indistinguishable from the human's own actions.
	AgentClientID string
}

// Result is a tool's outcome. Tools report failure as a Result rather than an
// error return whenever the model could plausibly recover: a bad argument or a
// missing record comes back as Success=false with actionable text, which the
// runtime feeds to the model as a tool result so it can retry. Reserve the
// error return for infrastructure faults that should abort the run.
type Result struct {
	Success bool `json:"success"`
	// Data is the payload on success — shaped for a model to reason about:
	// prefer names over UUIDs, and keep it small enough not to flood context.
	Data any `json:"data,omitempty"`
	// Error is actionable recovery guidance on failure, phrased as an
	// instruction ("no campaign named X; call inroad_campaign_read with
	// method=list to see available names"), not a stack trace.
	Error string `json:"error,omitempty"`
}

// Ok and Fail build the two Result shapes.
func Ok(data any) Result     { return Result{Success: true, Data: data} }
func Fail(msg string) Result { return Result{Success: false, Error: msg} }

// ExecuteFunc runs a tool. args is the model-supplied JSON, already checked to
// be syntactically valid; semantic validation belongs to the tool.
type ExecuteFunc func(ctx context.Context, p Principal, args json.RawMessage) (Result, error)

// Tool is one capability.
type Tool struct {
	// Name is the model-visible identifier, prefixed by product and resource
	// ("inroad_campaign_read"). Prefixes measurably improve tool selection and
	// keep names unique when several MCP servers are mounted together.
	Name string
	// Description tells the model when to reach for this tool — the single
	// highest-leverage field for correct selection.
	Description string
	// InputSchema is a JSON Schema object. The registry injects the shared
	// loading_message property (see LoadingMessageProperty) at registration,
	// so tools do not declare it themselves.
	InputSchema json.RawMessage
	Risk        Risk
	// MinRole gates the tool to a workspace role ("" means any member).
	MinRole string
	Execute ExecuteFunc
}

// LoadingMessageProperty is injected as the first property of every tool's
// input schema. The model fills it with a short present-tense sentence about
// what this specific call is doing ("Pausing campaign Q3 outbound"), and the
// UI renders it verbatim while the call runs. One convention buys human
// progress narration for every tool, forever, with no per-tool UI work.
const LoadingMessageProperty = "loading_message"

// Registry is the runtime's view of the tool surface. Implementations must
// filter by principal in Definitions and re-check permission inside Execute.
type Registry interface {
	// Definitions returns the tools this principal may use, in a stable order
	// (provider prompt caching depends on the list not reshuffling).
	Definitions(p Principal) []Tool
	// Execute resolves name, re-checks permission, and runs the tool.
	// Returns ErrNotFound / ErrForbidden for those cases.
	Execute(ctx context.Context, p Principal, name string, args json.RawMessage) (Result, error)
	// Risk reports a tool's tier without executing it, so the runtime can park
	// a call in the approval queue before doing any work.
	Risk(name string) (Risk, bool)
}
