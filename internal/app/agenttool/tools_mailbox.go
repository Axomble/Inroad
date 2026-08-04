package agenttool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// MailboxView is the mailbox as an agent may see it: identity, sending posture
// and health. There is no credential field to omit — the type simply has none,
// so no future edit to a tool can leak one (docs/security.md). Host and
// username columns are left out too: they tell a model nothing it can act on.
type MailboxView struct {
	ID                 uuid.UUID
	Email              string
	DisplayName        string
	Provider           string
	Status             string
	LastError          string
	DailyCap           int32
	MinIntervalSeconds int32
	RampEnabled        bool
	RampStartCap       int32
	RampDays           int32
}

// MailboxReader is what the mailbox tool needs. Implementations must never
// widen this to anything carrying a sealed secret.
type MailboxReader interface {
	List(ctx context.Context, ws uuid.UUID) ([]MailboxView, error)
	Get(ctx context.Context, ws, id uuid.UUID) (MailboxView, error)
}

type mailboxViewJSON struct {
	ID                 string `json:"id"`
	Email              string `json:"email"`
	DisplayName        string `json:"display_name,omitempty"`
	Provider           string `json:"provider"`
	Status             string `json:"status"`
	LastError          string `json:"last_error,omitempty"`
	DailyCap           int32  `json:"daily_cap"`
	MinIntervalSeconds int32  `json:"min_interval_seconds"`
	RampEnabled        bool   `json:"ramp_enabled"`
	RampStartCap       int32  `json:"ramp_start_cap"`
	RampDays           int32  `json:"ramp_days"`
}

func renderMailbox(m MailboxView) mailboxViewJSON {
	return mailboxViewJSON{
		ID: m.ID.String(), Email: m.Email, DisplayName: m.DisplayName, Provider: m.Provider,
		Status: m.Status, LastError: m.LastError, DailyCap: m.DailyCap,
		MinIntervalSeconds: m.MinIntervalSeconds, RampEnabled: m.RampEnabled,
		RampStartCap: m.RampStartCap, RampDays: m.RampDays,
	}
}

func mailboxTools(deps Deps) []Tool {
	if deps.Mailboxes == nil {
		return nil
	}
	return []Tool{mailboxReadTool(deps.Mailboxes)}
}

type mailboxReadArgs struct {
	baseArgs
	Method    string `json:"method"`
	MailboxID string `json:"mailbox_id"`
	Limit     *int   `json:"limit"`
}

func mailboxReadTool(r MailboxReader) Tool {
	methods := []string{methodList, methodGet}
	return Tool{
		Name: "inroad_mailbox_read",
		Description: "Read the sending mailboxes connected to this workspace: which are active, paused or erroring, their daily send cap and minimum interval, and their ramp settings. " +
			"Use method=list to see the whole pool (start here when the user asks why sending is slow or stopped) and method=get for one mailbox. " +
			fmt.Sprintf("Results are capped at %d by default (maximum %d). ", defaultLimit, maxLimit) +
			"For warmup progress and inbox placement use inroad_warmup_read instead. This tool never exposes mailbox credentials.",
		InputSchema: mustSchema(
			methodField("Which read to perform.", methods),
			strField("mailbox_id", "The mailbox's id, from a previous list result. Required for get.", false),
			limitField(),
		),
		Risk: RiskRead,
		Execute: func(ctx context.Context, p Principal, args json.RawMessage) (Result, error) {
			var a mailboxReadArgs
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
					return Result{}, fmt.Errorf("list mailboxes: %w", err)
				}
				page := all
				if len(page) > limit {
					page = page[:limit]
				}
				items := make([]mailboxViewJSON, 0, len(page))
				for _, m := range page {
					items = append(items, renderMailbox(m))
				}
				return Ok(map[string]any{"mailboxes": items, "total": len(all), "returned": len(items)}), nil
			case methodGet:
				id, bad := parseID("mailbox_id", a.MailboxID)
				if bad != nil {
					return *bad, nil
				}
				m, err := r.Get(ctx, p.WorkspaceID, id)
				if err != nil {
					if isNoRecord(err) {
						return mailboxMissing(a.MailboxID), nil
					}
					return Result{}, fmt.Errorf("get mailbox: %w", err)
				}
				return Ok(renderMailbox(m)), nil
			default:
				return unknownMethod(a.Method, methods), nil
			}
		},
	}
}

func mailboxMissing(id string) Result {
	return Fail(fmt.Sprintf(
		"no mailbox %s in this workspace; call inroad_mailbox_read with method=list to see the connected mailboxes", id))
}
