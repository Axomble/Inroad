package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// roleRank mirrors auth.roleRank. It is duplicated as literals rather than
// imported because `app/*` packages do not import each other (CLAUDE.md); the
// HTTP middleware remains the authority for request admission, this ranking
// only gates which tools an already-authenticated principal may reach.
var roleRank = map[string]int{"member": 1, "admin": 2, "owner": 3}

// Reg is the concrete Registry: an immutable, name-sorted tool table built
// once at startup. Nothing mutates after New returns, so it is safe to share
// across concurrent runs without locking.
type Reg struct {
	tools  []Tool
	byName map[string]Tool
}

var _ Registry = (*Reg)(nil)

// New builds the registry over deps. Tools whose dependency is nil are not
// registered.
//
// It panics if one of this package's own tool schemas cannot take the
// loading_message injection. That is a programming error in a package-literal
// schema, caught by the tests in this package at construction time — never on
// a request path.
func New(deps Deps) *Reg {
	var tools []Tool
	for _, group := range [][]Tool{
		searchTools(deps),
		campaignTools(deps),
		contactTools(deps),
		mailboxTools(deps),
		listTools(deps),
		deliverabilityTools(deps),
		warmupTools(deps),
	} {
		tools = append(tools, group...)
	}

	reg := &Reg{byName: make(map[string]Tool, len(tools))}
	for _, t := range tools {
		schema, err := withLoadingMessage(t.InputSchema)
		if err != nil {
			panic(fmt.Sprintf("agenttool: tool %s: %v", t.Name, err))
		}
		t.InputSchema = schema
		if _, dup := reg.byName[t.Name]; dup {
			panic(fmt.Sprintf("agenttool: duplicate tool name %s", t.Name))
		}
		reg.byName[t.Name] = t
		reg.tools = append(reg.tools, t)
	}
	// Sorted by name so the definition list handed to a provider is identical
	// across processes and restarts; prompt caching keys on that list.
	sort.Slice(reg.tools, func(i, j int) bool { return reg.tools[i].Name < reg.tools[j].Name })
	return reg
}

// Definitions returns the tools p may use, name-sorted.
func (r *Reg) Definitions(p Principal) []Tool {
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		if permits(t.MinRole, p.Role) {
			out = append(out, t)
		}
	}
	return out
}

// Risk reports a tool's tier without executing it.
func (r *Reg) Risk(name string) (Risk, bool) {
	t, ok := r.byName[name]
	if !ok {
		return RiskRead, false
	}
	return t.Risk, true
}

// Execute resolves name, re-checks the principal's role against the CURRENT
// descriptor (a tool list bound earlier in the conversation cannot escalate a
// demoted user), validates that args is syntactically an object, and runs the
// tool with panics converted to errors.
func (r *Reg) Execute(ctx context.Context, p Principal, name string, args json.RawMessage) (res Result, err error) {
	t, ok := r.byName[name]
	if !ok {
		return Result{}, fmt.Errorf("%w: %q%s", ErrNotFound, name, suggestion(name, r.tools))
	}
	if !permits(t.MinRole, p.Role) {
		return Result{}, fmt.Errorf("%w: %s requires the %s role", ErrForbidden, name, t.MinRole)
	}
	if bad := validateArgsObject(args); bad != nil {
		return *bad, nil
	}

	defer func() {
		if rec := recover(); rec != nil {
			res = Result{}
			err = fmt.Errorf("agenttool: tool %s panicked: %v", name, rec)
		}
	}()
	return t.Execute(ctx, p, args)
}

// validateArgsObject checks the syntactic shape the ExecuteFunc contract
// promises: absent/empty args is an empty object, anything else must parse as
// one. Semantic validation is the tool's own.
//
// JSON null needs its own check: decoding "null" into a map succeeds and
// yields a nil map, so without it a literal null would reach a tool that was
// promised an object — and the provider seam relies on this rejection.
func validateArgsObject(args json.RawMessage) *Result {
	if len(args) == 0 {
		return nil
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(args, &probe); err != nil {
		r := Fail(fmt.Sprintf("arguments must be a JSON object (%s); call the tool again with an object matching its input schema", err))
		return &r
	}
	if probe == nil {
		r := Fail("arguments must be a JSON object, not null; call the tool again with an object matching its input schema")
		return &r
	}
	return nil
}

// permits reports whether role satisfies minRole. An empty minRole means any
// member of the workspace.
func permits(minRole, role string) bool {
	if minRole == "" {
		return true
	}
	return roleRank[role] >= roleRank[minRole]
}

// suggestion appends a "did you mean" clause for the registered name closest
// to want, so a model that misremembers a tool name self-corrects on the next
// turn instead of retrying the same miss.
func suggestion(want string, tools []Tool) string {
	best, bestDist := "", 0
	for _, t := range tools {
		d := editDistance(want, t.Name)
		if best == "" || d < bestDist {
			best, bestDist = t.Name, d
		}
	}
	// Beyond roughly a third of the name the "closest" match is noise, and a
	// wrong suggestion sends the model further off than no suggestion at all.
	if best == "" || bestDist > 1+len(best)/3 {
		return ""
	}
	return fmt.Sprintf(" (did you mean %q?)", best)
}

// editDistance is Levenshtein distance over bytes; tool names are ASCII.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, min(cur[j-1]+1, prev[j-1]+cost))
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
