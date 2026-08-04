package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// maxListNameLen bounds what a model may name a list. The column is unbounded
// text; a tool that accepts unbounded text writes unbounded text.
const maxListNameLen = 200

// ListReader is the read half of the contact-list domain. The signatures are
// *list.Service's. MemberCount is NOT workspace-scoped — the tool must confirm
// ownership with Get first, which is why both live on the same interface.
type ListReader interface {
	List(ctx context.Context, ws uuid.UUID) ([]gen.List, error)
	Get(ctx context.Context, ws, id uuid.UUID) (gen.List, error)
	MemberCount(ctx context.Context, id uuid.UUID) (int64, error)
}

// ListWriter creates a contact list.
type ListWriter interface {
	Create(ctx context.Context, ws uuid.UUID, name string) (gen.List, error)
}

type listView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at,omitempty"`
	// Members is only filled by method=get; counting is a query per list, so
	// the listing deliberately omits it rather than fanning out.
	Members *int64 `json:"members,omitempty"`
}

func renderList(l gen.List) listView {
	return listView{ID: l.ID.String(), Name: l.Name, CreatedAt: rfc3339(l.CreatedAt)}
}

func listTools(deps Deps) []Tool {
	var out []Tool
	if deps.Lists != nil {
		out = append(out, listReadTool(deps.Lists))
	}
	if deps.ListWrites != nil {
		out = append(out, listWriteTool(deps.ListWrites))
	}
	return out
}

type listReadArgs struct {
	baseArgs
	Method string `json:"method"`
	ListID string `json:"list_id"`
	Limit  *int   `json:"limit"`
}

func listReadTool(r ListReader) Tool {
	methods := []string{methodList, methodGet}
	return Tool{
		Name: "inroad_list_read",
		Description: "Read the contact lists in this workspace — the audiences campaigns send to. " +
			"Use method=list to find a list by name or see what audiences exist, and method=get for one list including its member count. " +
			fmt.Sprintf("Results are capped at %d by default (maximum %d). ", defaultLimit, maxLimit) +
			"To see who is on a list, call inroad_contact_read with method=list and that list_id.",
		InputSchema: mustSchema(
			methodField("Which read to perform.", methods),
			strField("list_id", "The list's id, from a previous list result or inroad_search. Required for get.", false),
			limitField(),
		),
		Risk: RiskRead,
		Execute: func(ctx context.Context, p Principal, args json.RawMessage) (Result, error) {
			var a listReadArgs
			if bad := decodeArgs(args, &a); bad != nil {
				return *bad, nil
			}
			limit, bad := resolveLimit(a.Limit)
			if bad != nil {
				return *bad, nil
			}

			switch a.Method {
			case methodList:
				all, err := r.List(ctx, p.WorkspaceID)
				if err != nil {
					return Result{}, fmt.Errorf("list contact lists: %w", err)
				}
				page := all
				if len(page) > limit {
					page = page[:limit]
				}
				items := make([]listView, 0, len(page))
				for _, l := range page {
					items = append(items, renderList(l))
				}
				return Ok(map[string]any{"lists": items, "total": len(all), "returned": len(items)}), nil
			case methodGet:
				id, bad := parseID("list_id", a.ListID)
				if bad != nil {
					return *bad, nil
				}
				// Workspace ownership is established here, before the count:
				// MemberCount takes only the list id, so an unverified id would
				// count another tenant's members.
				l, err := r.Get(ctx, p.WorkspaceID, id)
				if err != nil {
					if isNoRecord(err) {
						return listMissing(a.ListID), nil
					}
					return Result{}, fmt.Errorf("get list: %w", err)
				}
				n, err := r.MemberCount(ctx, l.ID)
				if err != nil {
					return Result{}, fmt.Errorf("count list members: %w", err)
				}
				v := renderList(l)
				v.Members = &n
				return Ok(v), nil
			default:
				return unknownMethod(a.Method, methods), nil
			}
		},
	}
}

type listWriteArgs struct {
	baseArgs
	Name string `json:"name"`
}

func listWriteTool(w ListWriter) Tool {
	return Tool{
		Name: "inroad_list_write",
		Description: "Create an empty contact list (an audience) in this workspace. " +
			"Use this before adding contacts to a new audience with inroad_contact_write method=add_to_list. " +
			"Check inroad_list_read first — a list with the same name is not rejected, so creating one blindly leaves a duplicate audience behind.",
		InputSchema: mustSchema(
			strField("name", fmt.Sprintf("A short descriptive name for the list, up to %d characters.", maxListNameLen), true),
		),
		Risk: RiskWrite,
		Execute: func(ctx context.Context, p Principal, args json.RawMessage) (Result, error) {
			var a listWriteArgs
			if bad := decodeArgs(args, &a); bad != nil {
				return *bad, nil
			}
			name := strings.TrimSpace(a.Name)
			if name == "" {
				return Fail("name is required and cannot be blank; call again with a short descriptive list name"), nil
			}
			if len(name) > maxListNameLen {
				return Fail(fmt.Sprintf("name must be at most %d characters; call again with a shorter name", maxListNameLen)), nil
			}
			l, err := w.Create(ctx, p.WorkspaceID, name)
			if err != nil {
				return Result{}, fmt.Errorf("create list: %w", err)
			}
			return Ok(renderList(l)), nil
		},
	}
}

func listMissing(id string) Result {
	return Fail(fmt.Sprintf(
		"no contact list %s in this workspace; call inroad_list_read with method=list to see the lists that exist", id))
}
