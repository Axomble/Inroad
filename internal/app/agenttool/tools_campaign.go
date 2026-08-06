package agenttool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// CampaignReader is what the campaign tools need from the campaign domain.
// The signatures are *campaign.Service's, so the composition root can pass the
// service straight in; the interface exists so this package neither imports
// that one nor needs a database to be tested.
type CampaignReader interface {
	List(ctx context.Context, ws uuid.UUID) ([]gen.Campaign, error)
	Get(ctx context.Context, ws, id uuid.UUID) (gen.Campaign, error)
	Stats(ctx context.Context, ws, id uuid.UUID) (map[string]int64, error)
	ListEnrollments(ctx context.Context, ws, id uuid.UUID, limit, offset int32) ([]gen.ListCampaignEnrollmentsRow, error)
}

// CampaignController is the consequential half: the two state transitions the
// agent may request on a live campaign. Kept separate from CampaignReader so a
// deployment that wires only reads cannot accidentally expose control (interface
// segregation), and so the read tools stay usable while control is unwired.
type CampaignController interface {
	Pause(ctx context.Context, ws, id uuid.UUID) error
	Resume(ctx context.Context, ws, id uuid.UUID) error
}

// campaignSummary is the list-shaped view: what a human would read off a row.
type campaignSummary struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	Timezone        string `json:"timezone"`
	TrackingEnabled bool   `json:"tracking_enabled"`
	CreatedAt       string `json:"created_at,omitempty"`
	LaunchedAt      string `json:"launched_at,omitempty"`
}

func summarizeCampaign(c gen.Campaign) campaignSummary {
	return campaignSummary{
		ID:              c.ID.String(),
		Name:            c.Name,
		Status:          c.Status,
		Timezone:        c.Timezone,
		TrackingEnabled: c.TrackingEnabled,
		CreatedAt:       rfc3339(c.CreatedAt),
		LaunchedAt:      rfc3339(c.LaunchedAt),
	}
}

type campaignDetailView struct {
	campaignSummary
	Subject      string           `json:"subject"`
	ListID       string           `json:"list_id"`
	ListName     string           `json:"list_name,omitempty"`
	MailboxID    string           `json:"mailbox_id"`
	MailboxEmail string           `json:"mailbox_email,omitempty"`
	SendCounts   map[string]int64 `json:"send_counts"`
}

type campaignEnrollmentView struct {
	ContactEmail string `json:"contact_email"`
	FirstName    string `json:"first_name,omitempty"`
	Status       string `json:"status"`
	ReplyClass   string `json:"reply_class,omitempty"`
	ReplySource  string `json:"reply_source,omitempty"`
	RepliedAt    string `json:"replied_at,omitempty"`
}

const (
	methodGet         = "get"
	methodList        = "list"
	methodStats       = "stats"
	methodEnrollments = "enrollments"
	methodSearch      = "search"
	methodCreate      = "create"
	methodAddToList   = "add_to_list"
	methodPause       = "pause"
	methodResume      = "resume"
	methodOverview    = "overview"
	methodWorkspace   = "workspace"
	methodCampaign    = "campaign"
	methodPulse       = "pulse"
)

func campaignTools(deps Deps) []Tool {
	var out []Tool
	if deps.Campaigns != nil {
		out = append(out, campaignReadTool(deps))
	}
	if deps.CampaignAdmin != nil {
		out = append(out, campaignControlTool(deps.CampaignAdmin, deps.Campaigns))
	}
	return out
}

type campaignReadArgs struct {
	baseArgs
	Method     string `json:"method"`
	CampaignID string `json:"campaign_id"`
	Limit      *int   `json:"limit"`
	Offset     *int   `json:"offset"`
}

func campaignReadTool(deps Deps) Tool {
	methods := []string{methodList, methodGet, methodStats, methodEnrollments}
	return Tool{
		Name: "inroad_campaign_read",
		Description: "Read outbound campaigns in this workspace. Use method=list to find a campaign by name or see what is running, " +
			"method=get for one campaign's configuration (target list, sending mailbox, subject, send counts), " +
			"method=stats for its send counts by status, and method=enrollments to see which contacts are enrolled and how they replied. " +
			fmt.Sprintf("List results are capped at %d by default (maximum %d); page with offset. ", defaultLimit, maxLimit) +
			"Use this before any campaign question — never guess a campaign's status or audience.",
		InputSchema: mustSchema(
			methodField("Which read to perform.", methods),
			strField("campaign_id", "The campaign's id, from a previous list result or inroad_search. Required for get, stats and enrollments.", false),
			limitField(),
			offsetField(),
		),
		Risk: RiskRead,
		Execute: func(ctx context.Context, p Principal, args json.RawMessage) (Result, error) {
			var a campaignReadArgs
			if bad := decodeArgs(args, &a); bad != nil {
				return *bad, nil
			}
			limit, bad := resolveLimit(a.Limit)
			if bad != nil {
				return *bad, nil
			}
			offset, bad := resolveOffset(a.Offset)
			if bad != nil {
				return *bad, nil
			}

			switch a.Method {
			case methodList:
				return campaignList(ctx, deps.Campaigns, p.WorkspaceID, limit, offset)
			case methodGet:
				return campaignGet(ctx, deps, p.WorkspaceID, a.CampaignID)
			case methodStats:
				return campaignStats(ctx, deps.Campaigns, p.WorkspaceID, a.CampaignID)
			case methodEnrollments:
				return campaignEnrollments(ctx, deps.Campaigns, p.WorkspaceID, a.CampaignID, limit, offset)
			default:
				return unknownMethod(a.Method, methods), nil
			}
		},
	}
}

func campaignList(ctx context.Context, r CampaignReader, ws uuid.UUID, limit, offset int) (Result, error) {
	all, err := r.List(ctx, ws)
	if err != nil {
		return Result{}, fmt.Errorf("list campaigns: %w", err)
	}
	page := all
	if offset >= len(all) {
		page = nil
	} else {
		page = all[offset:]
	}
	if len(page) > limit {
		page = page[:limit]
	}
	items := make([]campaignSummary, 0, len(page))
	for _, c := range page {
		items = append(items, summarizeCampaign(c))
	}
	return Ok(map[string]any{"campaigns": items, "total": len(all), "returned": len(items), "offset": offset}), nil
}

func campaignGet(ctx context.Context, deps Deps, ws uuid.UUID, id string) (Result, error) {
	campaignID, bad := parseID("campaign_id", id)
	if bad != nil {
		return *bad, nil
	}
	c, err := deps.Campaigns.Get(ctx, ws, campaignID)
	if err != nil {
		if isNoRecord(err) {
			return campaignMissing(id), nil
		}
		return Result{}, fmt.Errorf("get campaign: %w", err)
	}
	counts, err := deps.Campaigns.Stats(ctx, ws, campaignID)
	if err != nil {
		return Result{}, fmt.Errorf("campaign stats: %w", err)
	}

	view := campaignDetailView{
		campaignSummary: summarizeCampaign(c),
		Subject:         c.Subject,
		ListID:          c.ListID.String(),
		MailboxID:       c.MailboxID.String(),
		SendCounts:      counts,
	}
	// Names make the payload readable to a model; the ids stay because a
	// follow-up read needs them. A lookup that fails is not worth failing the
	// whole read for — the ids are still there.
	if deps.Lists != nil {
		if l, err := deps.Lists.Get(ctx, ws, c.ListID); err == nil {
			view.ListName = l.Name
		}
	}
	if deps.Mailboxes != nil {
		if m, err := deps.Mailboxes.Get(ctx, ws, c.MailboxID); err == nil {
			view.MailboxEmail = m.Email
		}
	}
	return Ok(view), nil
}

func campaignStats(ctx context.Context, r CampaignReader, ws uuid.UUID, id string) (Result, error) {
	campaignID, bad := parseID("campaign_id", id)
	if bad != nil {
		return *bad, nil
	}
	// Ownership is confirmed before any child read, so a cross-tenant id is a
	// miss rather than an empty-but-plausible stats map.
	c, err := r.Get(ctx, ws, campaignID)
	if err != nil {
		if isNoRecord(err) {
			return campaignMissing(id), nil
		}
		return Result{}, fmt.Errorf("get campaign: %w", err)
	}
	counts, err := r.Stats(ctx, ws, campaignID)
	if err != nil {
		return Result{}, fmt.Errorf("campaign stats: %w", err)
	}
	return Ok(map[string]any{"campaign": c.Name, "status": c.Status, "send_counts": counts}), nil
}

func campaignEnrollments(ctx context.Context, r CampaignReader, ws uuid.UUID, id string, limit, offset int) (Result, error) {
	campaignID, bad := parseID("campaign_id", id)
	if bad != nil {
		return *bad, nil
	}
	c, err := r.Get(ctx, ws, campaignID)
	if err != nil {
		if isNoRecord(err) {
			return campaignMissing(id), nil
		}
		return Result{}, fmt.Errorf("get campaign: %w", err)
	}
	rows, err := r.ListEnrollments(ctx, ws, campaignID, int32(limit), int32(offset))
	if err != nil {
		return Result{}, fmt.Errorf("list enrollments: %w", err)
	}
	items := make([]campaignEnrollmentView, 0, len(rows))
	for _, row := range rows {
		items = append(items, campaignEnrollmentView{
			ContactEmail: row.Email,
			FirstName:    row.FirstName,
			Status:       row.Status,
			ReplyClass:   derefString(row.ReplyClass),
			ReplySource:  derefString(row.ReplySource),
			RepliedAt:    rfc3339(row.RepliedAt),
		})
	}
	return Ok(map[string]any{
		"campaign": c.Name, "enrollments": items, "returned": len(items), "offset": offset,
	}), nil
}

func campaignMissing(id string) Result {
	return Fail(fmt.Sprintf(
		"no campaign %s in this workspace; call inroad_campaign_read with method=list to see the campaigns that exist", id))
}

type campaignControlArgs struct {
	baseArgs
	Method     string `json:"method"`
	CampaignID string `json:"campaign_id"`
}

// campaignControlTool is the one consequential tool in this set: pausing or
// resuming a live campaign changes what mail goes out. The registry only
// reports the tier; the approval gate that acts on it lands in A4, in front of
// Execute.
func campaignControlTool(ctrl CampaignController, reader CampaignReader) Tool {
	methods := []string{methodPause, methodResume}
	return Tool{
		Name: "inroad_campaign_control",
		Description: "Pause or resume a running campaign. Pausing stops new sends immediately; resuming lets the schedule continue where it left off. " +
			"Use this when the user asks to stop or restart sending, or when deliverability has degraded and sending should stop. " +
			"This changes live sending behaviour and is submitted for human approval before it takes effect.",
		InputSchema: mustSchema(
			methodField("pause stops new sends; resume restarts them.", methods),
			strField("campaign_id", "The campaign's id, from inroad_campaign_read or inroad_search.", true),
		),
		Risk:    RiskConsequential,
		MinRole: "admin",
		Execute: func(ctx context.Context, p Principal, args json.RawMessage) (Result, error) {
			var a campaignControlArgs
			if bad := decodeArgs(args, &a); bad != nil {
				return *bad, nil
			}
			campaignID, bad := parseID("campaign_id", a.CampaignID)
			if bad != nil {
				return *bad, nil
			}
			var name string
			if reader != nil {
				c, err := reader.Get(ctx, p.WorkspaceID, campaignID)
				if err != nil {
					if isNoRecord(err) {
						return campaignMissing(a.CampaignID), nil
					}
					return Result{}, fmt.Errorf("get campaign: %w", err)
				}
				name = c.Name
			}

			var err error
			switch a.Method {
			case methodPause:
				err = ctrl.Pause(ctx, p.WorkspaceID, campaignID)
			case methodResume:
				err = ctrl.Resume(ctx, p.WorkspaceID, campaignID)
			default:
				return unknownMethod(a.Method, methods), nil
			}
			if err != nil {
				if isNoRecord(err) {
					return campaignMissing(a.CampaignID), nil
				}
				return Result{}, fmt.Errorf("%s campaign: %w", a.Method, err)
			}
			return Ok(map[string]any{"campaign": name, "id": campaignID.String(), "action": a.Method}), nil
		},
	}
}
