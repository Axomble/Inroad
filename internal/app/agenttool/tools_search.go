package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// The object types inroad_search spans. Values are singular and match the
// `type` field of every hit, so a model can feed one straight back into the
// types filter.
const (
	objectCampaign = "campaign"
	objectContact  = "contact"
	objectMailbox  = "mailbox"
	objectList     = "list"
)

// searchHit is one match. Label is what a human calls the record, Detail is
// the one fact that disambiguates two similar labels, and ID is carried
// because every follow-up read needs it.
type searchHit struct {
	Type   string `json:"type"`
	ID     string `json:"id"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
}

func searchTools(deps Deps) []Tool {
	if deps.Campaigns == nil && deps.Contacts == nil && deps.Mailboxes == nil && deps.Lists == nil {
		return nil
	}
	return []Tool{searchTool(deps)}
}

type searchArgs struct {
	baseArgs
	Query string   `json:"query"`
	Types []string `json:"types"`
	Limit *int     `json:"limit"`
}

func searchTool(deps Deps) Tool {
	types := []string{objectCampaign, objectContact, objectMailbox, objectList}
	return Tool{
		Name: "inroad_search",
		Description: "Find records across the workspace by name or email in one call: campaigns, contacts, sending mailboxes and contact lists. " +
			"Start here whenever the user names something without giving an id — it returns the id each follow-up read tool needs. " +
			"Narrow with types when you already know what you are looking for. " +
			fmt.Sprintf("The query must be at least %d characters, and each object type returns at most %d matches (maximum %d) — ", minContactQuery, defaultLimit, maxLimit) +
			"for a full listing use the matching *_read tool with method=list instead.",
		InputSchema: mustSchema(
			strField("query", fmt.Sprintf("Text to match against record names and email addresses; at least %d characters.", minContactQuery), true),
			field{name: "types", schema: jsonObject{
				{"type", "array"},
				{"description", "Restrict the search to these object types. Omit to search all of them."},
				{"items", jsonObject{{"type", "string"}, {"enum", types}}},
			}},
			limitField(),
		),
		Risk: RiskRead,
		Execute: func(ctx context.Context, p Principal, args json.RawMessage) (Result, error) {
			var a searchArgs
			if bad := decodeArgs(args, &a); bad != nil {
				return *bad, nil
			}
			limit, bad := resolveLimit(a.Limit)
			if bad != nil {
				return *bad, nil
			}
			q := strings.ToLower(strings.TrimSpace(a.Query))
			if len(q) < minContactQuery {
				return Fail(fmt.Sprintf("query must be at least %d characters; call again with a longer search term", minContactQuery)), nil
			}
			want, bad := wantedTypes(a.Types, types)
			if bad != nil {
				return *bad, nil
			}

			hits := make([]searchHit, 0, limit)
			for _, kind := range types {
				if !want[kind] {
					continue
				}
				found, err := searchOneType(ctx, deps, p.WorkspaceID, kind, q, limit)
				if err != nil {
					return Result{}, err
				}
				hits = append(hits, found...)
			}
			if len(hits) == 0 {
				return Ok(map[string]any{
					"results": hits, "returned": 0,
					"note": "nothing matched; try a shorter or differently spelled term, or list the object type with its *_read tool",
				}), nil
			}
			return Ok(map[string]any{"results": hits, "returned": len(hits)}), nil
		},
	}
}

// wantedTypes resolves the optional types filter, rejecting an unknown value
// rather than silently searching everything.
func wantedTypes(requested, known []string) (map[string]bool, *Result) {
	want := make(map[string]bool, len(known))
	if len(requested) == 0 {
		for _, k := range known {
			want[k] = true
		}
		return want, nil
	}
	valid := make(map[string]bool, len(known))
	for _, k := range known {
		valid[k] = true
	}
	for _, t := range requested {
		if !valid[t] {
			r := Fail(fmt.Sprintf("unknown type %q; types may contain %s", t, strings.Join(known, ", ")))
			return nil, &r
		}
		want[t] = true
	}
	return want, nil
}

func searchOneType(ctx context.Context, deps Deps, ws uuid.UUID, kind, q string, limit int) ([]searchHit, error) {
	switch kind {
	case objectCampaign:
		if deps.Campaigns == nil {
			return nil, nil
		}
		all, err := deps.Campaigns.List(ctx, ws)
		if err != nil {
			return nil, fmt.Errorf("search campaigns: %w", err)
		}
		hits := make([]searchHit, 0, limit)
		for _, c := range all {
			if len(hits) == limit {
				break
			}
			if matches(c.Name, q) || matches(c.Subject, q) {
				hits = append(hits, searchHit{
					Type: objectCampaign, ID: c.ID.String(), Label: c.Name, Detail: "status " + c.Status,
				})
			}
		}
		return hits, nil

	case objectContact:
		if deps.Contacts == nil {
			return nil, nil
		}
		page, err := deps.Contacts.Search(ctx, ws, ContactQuery{Query: q, Limit: limit})
		if err != nil {
			return nil, fmt.Errorf("search contacts: %w", err)
		}
		hits := make([]searchHit, 0, len(page.Matches))
		for _, m := range page.Matches {
			hits = append(hits, searchHit{
				Type: objectContact, ID: m.ID.String(), Label: m.Email, Detail: m.FirstName,
			})
		}
		return hits, nil

	case objectMailbox:
		if deps.Mailboxes == nil {
			return nil, nil
		}
		all, err := deps.Mailboxes.List(ctx, ws)
		if err != nil {
			return nil, fmt.Errorf("search mailboxes: %w", err)
		}
		hits := make([]searchHit, 0, limit)
		for _, m := range all {
			if len(hits) == limit {
				break
			}
			if matches(m.Email, q) || matches(m.DisplayName, q) {
				hits = append(hits, searchHit{
					Type: objectMailbox, ID: m.ID.String(), Label: m.Email, Detail: "status " + m.Status,
				})
			}
		}
		return hits, nil

	case objectList:
		if deps.Lists == nil {
			return nil, nil
		}
		all, err := deps.Lists.List(ctx, ws)
		if err != nil {
			return nil, fmt.Errorf("search lists: %w", err)
		}
		hits := make([]searchHit, 0, limit)
		for _, l := range all {
			if len(hits) == limit {
				break
			}
			if matches(l.Name, q) {
				hits = append(hits, searchHit{Type: objectList, ID: l.ID.String(), Label: l.Name})
			}
		}
		return hits, nil
	}
	return nil, nil
}
