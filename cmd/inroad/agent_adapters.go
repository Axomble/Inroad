package main

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/app/agentchat"
	"github.com/inroad/inroad/internal/app/agentrun"
	"github.com/inroad/inroad/internal/app/agenttool"
	"github.com/inroad/inroad/internal/app/contact"
	"github.com/inroad/inroad/internal/app/deliverability"
	"github.com/inroad/inroad/internal/app/list"
	"github.com/inroad/inroad/internal/app/mailbox"
	"github.com/inroad/inroad/internal/app/pulse"
	"github.com/inroad/inroad/internal/app/warmup"
)

type runtimeTools struct{ registry agenttool.Registry }

func (a runtimeTools) Definitions(actor agentchat.Actor) []agentrun.Tool {
	principal := toolPrincipal(actor)
	definitions := a.registry.Definitions(principal)
	out := make([]agentrun.Tool, 0, len(definitions))
	for _, tool := range definitions {
		out = append(out, agentrun.Tool{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema, Risk: tool.Risk.String()})
	}
	return out
}

func (a runtimeTools) Execute(ctx context.Context, actor agentchat.Actor, name string, input json.RawMessage) (agentrun.ToolResult, error) {
	risk, ok := a.registry.Risk(name)
	if !ok {
		return agentrun.ToolResult{}, agenttool.ErrNotFound
	}
	if risk.NeedsApproval() {
		output, _ := json.Marshal(agenttool.Fail("this action requires human approval and is unavailable until the approval gate is enabled"))
		return agentrun.ToolResult{Output: output, IsError: true}, nil
	}
	result, err := a.registry.Execute(ctx, toolPrincipal(actor), name, input)
	if err != nil {
		return agentrun.ToolResult{}, err
	}
	output, err := json.Marshal(result)
	if err != nil {
		return agentrun.ToolResult{}, err
	}
	return agentrun.ToolResult{Output: output, IsError: !result.Success}, nil
}

func toolPrincipal(actor agentchat.Actor) agenttool.Principal {
	return agenttool.Principal{
		WorkspaceID: actor.WorkspaceID, UserID: actor.UserID, Role: actor.Role,
		AgentClientID: "inroad-chat",
	}
}

type contactTools struct {
	service *contact.Service
	store   contact.Store
	lists   *list.Service
	pool    *pgxpool.Pool
}

type mailboxTools struct{ service *mailbox.Service }

func (a mailboxTools) List(ctx context.Context, ws uuid.UUID) ([]agenttool.MailboxView, error) {
	rows, err := a.service.List(ctx, ws)
	if err != nil {
		return nil, err
	}
	out := make([]agenttool.MailboxView, len(rows))
	for i, row := range rows {
		out[i] = mailboxToolView(row)
	}
	return out, nil
}

func (a mailboxTools) Get(ctx context.Context, ws, id uuid.UUID) (agenttool.MailboxView, error) {
	row, err := a.service.Get(ctx, ws, id)
	if err != nil {
		return agenttool.MailboxView{}, err
	}
	return mailboxToolView(row), nil
}

func mailboxToolView(row mailbox.MailboxSafe) agenttool.MailboxView {
	return agenttool.MailboxView{
		ID: row.ID, Email: row.Email, DisplayName: row.DisplayName, Provider: row.Provider,
		Status: row.Status, LastError: row.LastError, DailyCap: row.DailyCap,
		MinIntervalSeconds: row.MinIntervalSeconds, RampEnabled: row.RampEnabled,
		RampStartCap: row.RampStartCap, RampDays: row.RampDays,
	}
}

type warmupTools struct{ service *warmup.Service }

type deliverabilityToolAdapter struct {
	deliverability *deliverability.Service
	pulse          *pulse.Service
}

func (a deliverabilityToolAdapter) WorkspaceHealth(ctx context.Context, ws uuid.UUID) (agenttool.WorkspaceHealth, error) {
	report, err := a.deliverability.Report(ctx, ws)
	if err != nil {
		return agenttool.WorkspaceHealth{}, err
	}
	return agenttool.WorkspaceHealth{
		Score: report.Score, AtRiskMailboxes: healthRisks(report.AtRiskMailboxes),
		AtRiskDomains: healthRisks(report.AtRiskDomains),
	}, nil
}

func (a deliverabilityToolAdapter) CampaignHealth(ctx context.Context, ws, campaignID uuid.UUID) (agenttool.CampaignHealth, error) {
	report, err := a.deliverability.CampaignReport(ctx, ws, campaignID)
	if err != nil {
		return agenttool.CampaignHealth{}, err
	}
	events := make([]agenttool.HealthPauseEvent, len(report.PauseEvents))
	for i, event := range report.PauseEvents {
		events[i] = agenttool.HealthPauseEvent{
			Reason: event.Reason, Metric: event.Metric, Value: event.Value,
			Threshold: event.Threshold, Delivered: event.Delivered, CreatedAt: event.CreatedAt,
		}
	}
	return agenttool.CampaignHealth{
		Score: report.Score, Verdict: report.Verdict,
		AutoPauseEnabled:  report.Guardrails.AutoPauseEnabled,
		BouncePausePct:    report.Guardrails.BouncePausePct,
		ComplaintPausePct: report.Guardrails.ComplaintPausePct,
		PauseEvents:       events,
	}, nil
}

func (a deliverabilityToolAdapter) Snapshot(ctx context.Context, ws uuid.UUID) (agenttool.Snapshot, error) {
	row, err := a.pulse.Get(ctx, ws)
	if err != nil {
		return agenttool.Snapshot{}, err
	}
	attention := make([]agenttool.SnapshotAttention, len(row.Attention))
	for i, item := range row.Attention {
		attention[i] = agenttool.SnapshotAttention{Kind: item.Kind, Severity: item.Severity, Count: item.Count, Reason: item.Reason}
	}
	return agenttool.Snapshot{
		MailboxesTotal: row.Mailboxes.Total, MailboxesActive: row.Mailboxes.Active,
		MailboxesPaused: row.Mailboxes.Paused, MailboxesError: row.Mailboxes.Error,
		WarmupPool: row.Warmup.Pool, WarmupHealthy: row.Warmup.Healthy,
		WarmupWatch: row.Warmup.Watch, WarmupAtRisk: row.Warmup.AtRisk,
		CampaignsTotal: row.Campaigns.Total, CampaignsRunning: row.Campaigns.Running,
		CampaignsDraft: row.Campaigns.Draft, CampaignsPaused: row.Campaigns.Paused,
		ContactsTotal: row.Contacts.Total, SentToday: row.Sending.SentToday,
		DailyCap: row.Sending.DailyCap, Attention: attention,
	}, nil
}

func healthRisks(rows []deliverability.Risk) []agenttool.HealthRisk {
	out := make([]agenttool.HealthRisk, len(rows))
	for i, row := range rows {
		out[i] = agenttool.HealthRisk{Label: row.Label, Reason: row.Reason}
	}
	return out
}

func (a warmupTools) Overview(ctx context.Context, ws uuid.UUID) (agenttool.WarmupOverview, error) {
	row, err := a.service.GetOverview(ctx, ws)
	if err != nil {
		return agenttool.WarmupOverview{}, err
	}
	out := agenttool.WarmupOverview{PoolSize: row.PoolSize, Active: row.Active, Mailboxes: make([]agenttool.WarmupMailbox, len(row.Mailboxes))}
	for i, mailbox := range row.Mailboxes {
		id, err := uuid.Parse(mailbox.MailboxID)
		if err != nil {
			return agenttool.WarmupOverview{}, err
		}
		out.Mailboxes[i] = agenttool.WarmupMailbox{
			MailboxID: id, Email: mailbox.Email, Enabled: mailbox.Enabled,
			HealthState: mailbox.HealthState, HealthReason: mailbox.HealthReason,
			TodaySent: mailbox.TodaySent, TodayTarget: mailbox.TodayTarget,
			InboxRate7d: mailbox.InboxRate7d, SpamRate7d: mailbox.SpamRate7d,
		}
	}
	return out, nil
}

func (a warmupTools) Detail(ctx context.Context, ws, mailboxID uuid.UUID) (agenttool.WarmupDetail, error) {
	row, err := a.service.GetWarmupDetail(ctx, ws, mailboxID)
	if err != nil {
		return agenttool.WarmupDetail{}, err
	}
	participantID, err := uuid.Parse(row.Participant.MailboxID)
	if err != nil {
		return agenttool.WarmupDetail{}, err
	}
	out := agenttool.WarmupDetail{
		Participant: agenttool.WarmupParticipant{
			MailboxID: participantID, Enabled: row.Participant.Enabled,
			StartVolume: row.Participant.StartVolume, MaxVolume: row.Participant.MaxVolume,
			RampIncrement: row.Participant.RampIncrement, ReplyRate: row.Participant.ReplyRate,
			HealthState: row.Participant.HealthState, HealthReason: row.Participant.HealthReason,
			StartedAt: row.Participant.StartedAt, TodaySent: row.Participant.TodaySent,
			TodayTarget: row.Participant.TodayTarget,
		},
		Series: make([]agenttool.WarmupDay, len(row.Series)),
	}
	for i, day := range row.Series {
		out.Series[i] = agenttool.WarmupDay{Day: day.Day, Sent: day.Sent, Received: day.Received, Inbox: day.Inbox, Spam: day.Spam, Replies: day.Replies}
	}
	return out, nil
}

func (a contactTools) Search(ctx context.Context, ws uuid.UUID, query agenttool.ContactQuery) (agenttool.ContactPage, error) {
	limit := query.Limit
	page, err := a.service.Search(ctx, ws, contact.SearchRequest{ListID: query.ListID, Q: query.Query, Limit: &limit})
	if errors.Is(err, contact.ErrListNotFound) {
		return agenttool.ContactPage{}, pgx.ErrNoRows
	}
	if err != nil {
		return agenttool.ContactPage{}, err
	}
	out := agenttool.ContactPage{Total: page.Total, TotalIsCapped: page.TotalIsCapped, Matches: make([]agenttool.ContactMatch, len(page.Items))}
	for i, row := range page.Items {
		out.Matches[i] = agenttool.ContactMatch{ID: row.ID, Email: row.Email, FirstName: row.FirstName, CreatedAt: row.CreatedAt}
	}
	return out, nil
}

func (a contactTools) Create(ctx context.Context, ws uuid.UUID, input agenttool.ContactInput) (uuid.UUID, bool, error) {
	return a.store.Upsert(ctx, ws, contact.UpsertInput{Email: input.Email, FirstName: input.FirstName, LastName: input.LastName, Company: input.Company})
}

func (a contactTools) AddToList(ctx context.Context, ws, listID, contactID uuid.UUID) error {
	if _, err := a.lists.Get(ctx, ws, listID); err != nil {
		return err
	}
	var exists bool
	if err := a.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM contacts WHERE workspace_id=$1 AND id=$2)", ws, contactID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return pgx.ErrNoRows
	}
	return a.store.AddToList(ctx, listID, contactID)
}
