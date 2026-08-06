package main

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/agenttool"
	"github.com/inroad/inroad/internal/app/crm"
)

// crmAgentPageLimit bounds what one agent read pulls into a model's context.
// It is deliberately smaller than the HTTP ceiling: the tool reports
// truncation rather than pretending a page is the whole workspace.
const crmAgentPageLimit int32 = 50

type crmTools struct{ service *crm.Service }

func crmCompanyView(row crm.Company) agenttool.CRMCompany {
	return agenttool.CRMCompany{ID: row.ID, Name: row.Name, Domain: row.Domain, Currency: row.Currency, DealCount: row.DealCount}
}

func crmDealView(row crm.Deal) agenttool.CRMDeal {
	return agenttool.CRMDeal{ID: row.ID, PipelineID: row.PipelineID, StageID: row.StageID, CompanyID: row.CompanyID,
		Name: row.Name, PipelineName: row.PipelineName, StageLabel: row.StageLabel, CompanyName: row.CompanyName,
		Currency: row.Currency, AmountMicros: row.AmountMicros}
}

func (a crmTools) ListCompanies(ctx context.Context, workspaceID uuid.UUID) (agenttool.CRMList[agenttool.CRMCompany], error) {
	page, err := a.service.ListCompanies(ctx, workspaceID, crm.PageRequest{Limit: crmAgentPageLimit})
	if err != nil {
		return agenttool.CRMList[agenttool.CRMCompany]{}, err
	}
	out := make([]agenttool.CRMCompany, len(page.Items))
	for i, row := range page.Items {
		out[i] = crmCompanyView(row)
	}
	return agenttool.NewCRMList(out, page.NextCursor != ""), nil
}

func (a crmTools) GetCompany(ctx context.Context, workspaceID, id uuid.UUID) (agenttool.CRMCompany, error) {
	row, err := a.service.GetCompany(ctx, workspaceID, id)
	if err != nil {
		return agenttool.CRMCompany{}, err
	}
	return crmCompanyView(row), nil
}

func (a crmTools) ListPipelines(ctx context.Context, workspaceID uuid.UUID) ([]agenttool.CRMPipeline, error) {
	rows, err := a.service.ListPipelines(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]agenttool.CRMPipeline, len(rows))
	for i, row := range rows {
		stages := make([]agenttool.CRMStage, len(row.Stages))
		for j, stage := range row.Stages {
			stages[j] = agenttool.CRMStage{ID: stage.ID, Label: stage.Label}
		}
		out[i] = agenttool.CRMPipeline{ID: row.ID, Name: row.Name, Stages: stages}
	}
	return out, nil
}

func (a crmTools) ListDeals(ctx context.Context, workspaceID uuid.UUID) (agenttool.CRMList[agenttool.CRMDeal], error) {
	page, err := a.service.ListDeals(ctx, workspaceID, crm.PageRequest{Limit: crmAgentPageLimit})
	if err != nil {
		return agenttool.CRMList[agenttool.CRMDeal]{}, err
	}
	out := make([]agenttool.CRMDeal, len(page.Items))
	for i, row := range page.Items {
		out[i] = crmDealView(row)
	}
	return agenttool.NewCRMList(out, page.NextCursor != ""), nil
}

func (a crmTools) GetDeal(ctx context.Context, workspaceID, id uuid.UUID) (agenttool.CRMDeal, error) {
	row, err := a.service.GetDeal(ctx, workspaceID, id)
	if err != nil {
		return agenttool.CRMDeal{}, err
	}
	return crmDealView(row), nil
}

func (a crmTools) GetBoard(ctx context.Context, workspaceID uuid.UUID, pipelineID *uuid.UUID) (agenttool.CRMBoard, error) {
	row, err := a.service.GetBoard(ctx, workspaceID, pipelineID)
	if err != nil {
		return agenttool.CRMBoard{}, err
	}
	pipeline := agenttool.CRMPipeline{ID: row.Pipeline.ID, Name: row.Pipeline.Name, Stages: make([]agenttool.CRMStage, len(row.Pipeline.Stages))}
	for i, stage := range row.Pipeline.Stages {
		pipeline.Stages[i] = agenttool.CRMStage{ID: stage.ID, Label: stage.Label}
	}
	stages := make([]agenttool.CRMBoardStage, len(row.Stages))
	for i, stage := range row.Stages {
		deals := make([]agenttool.CRMDeal, len(stage.Deals))
		for j, deal := range stage.Deals {
			deals[j] = crmDealView(deal)
		}
		stages[i] = agenttool.CRMBoardStage{Stage: agenttool.CRMStage{ID: stage.Stage.ID, Label: stage.Stage.Label}, Deals: deals, DealCount: stage.DealCount, AmountMicros: stage.AmountMicros}
	}
	return agenttool.CRMBoard{Pipeline: pipeline, Stages: stages}, nil
}

func (a crmTools) CreateCompany(ctx context.Context, workspaceID uuid.UUID, in agenttool.CRMCompanyInput) (agenttool.CRMCompany, error) {
	row, err := a.service.CreateCompanyWithActor(ctx, workspaceID,
		crm.CompanyInput{Name: in.Name, Domain: in.Domain, Currency: in.Currency}, crmAgentActor(in.Actor))
	if err != nil {
		return agenttool.CRMCompany{}, err
	}
	return crmCompanyView(row), nil
}

func (a crmTools) UpdateCompany(ctx context.Context, workspaceID, id uuid.UUID, in agenttool.CRMCompanyInput) (agenttool.CRMCompany, error) {
	row, err := a.service.UpdateCompanyWithActor(ctx, workspaceID, id, crm.CompanyInput{Name: in.Name, Domain: in.Domain, Currency: in.Currency}, crmAgentActor(in.Actor))
	if err != nil {
		return agenttool.CRMCompany{}, err
	}
	return crmCompanyView(row), nil
}

func (a crmTools) CreateDeal(ctx context.Context, workspaceID uuid.UUID, in agenttool.CRMDealInput) (agenttool.CRMDeal, error) {
	actor := crmAgentActor(in.Actor)
	row, err := a.service.CreateDeal(ctx, workspaceID, crm.DealInput{Name: in.Name, PipelineID: in.PipelineID, StageID: in.StageID, CompanyID: in.CompanyID, AmountMicros: in.AmountMicros, Currency: in.Currency, Source: "agent", Actor: actor})
	if err != nil {
		return agenttool.CRMDeal{}, err
	}
	return crmDealView(row), nil
}

func (a crmTools) UpdateDeal(ctx context.Context, workspaceID, id uuid.UUID, in agenttool.CRMDealInput) (agenttool.CRMDeal, error) {
	row, err := a.service.UpdateDeal(ctx, workspaceID, id, crm.DealInput{Name: in.Name, PipelineID: in.PipelineID, StageID: in.StageID, CompanyID: in.CompanyID, AmountMicros: in.AmountMicros, Currency: in.Currency, Source: "agent", Actor: crmAgentActor(in.Actor)})
	if err != nil {
		return agenttool.CRMDeal{}, err
	}
	return crmDealView(row), nil
}

func (a crmTools) MoveDeal(ctx context.Context, workspaceID uuid.UUID, in agenttool.CRMDealMoveInput) (agenttool.CRMDeal, error) {
	row, err := a.service.MoveDeal(ctx, workspaceID, in.DealID, crm.MoveDealInput{StageID: in.StageID, BeforeDealID: in.BeforeDealID, AfterDealID: in.AfterDealID, Actor: crmAgentActor(in.Actor)})
	if err != nil {
		return agenttool.CRMDeal{}, err
	}
	return crmDealView(row), nil
}

func (a crmTools) CreateNote(ctx context.Context, workspaceID uuid.UUID, in agenttool.CRMNoteInput) error {
	_, err := a.service.CreateNote(ctx, workspaceID, crm.NoteInput{
		Title: in.Title, Body: in.Body, Target: crm.Target{Type: in.Target.Type, ID: in.Target.ID},
		Actor: crmAgentActor(in.Actor),
	})
	return err
}

func (a crmTools) CreateTask(ctx context.Context, workspaceID uuid.UUID, in agenttool.CRMTaskInput) error {
	_, err := a.service.CreateTask(ctx, workspaceID, crm.TaskInput{
		Title: in.Title, Body: in.Body, Target: crm.Target{Type: in.Target.Type, ID: in.Target.ID},
		Actor: crmAgentActor(in.Actor),
	})
	return err
}

func (a crmTools) ListEvents(ctx context.Context, workspaceID uuid.UUID, target agenttool.CRMTarget) ([]agenttool.CRMEvent, error) {
	rows, err := a.service.ListEvents(ctx, workspaceID, crm.Target{Type: target.Type, ID: target.ID})
	if err != nil {
		return nil, err
	}
	out := make([]agenttool.CRMEvent, len(rows))
	for i, row := range rows {
		out[i] = agenttool.CRMEvent{ID: row.ID.String(), Name: row.Name, Kind: row.Kind, LinkedRecordCachedName: row.LinkedRecordCachedName,
			OccurredAt: row.OccurredAt.Format("2006-01-02T15:04:05Z07:00"), MergedCount: row.MergedCount, Actor: row.Actor, Data: row.Data,
			SourceMessageID: row.SourceMessageID, SourceThreadRef: row.SourceThreadRef}
	}
	return out, nil
}

func (a crmTools) ListDealThreads(ctx context.Context, workspaceID, dealID uuid.UUID) ([]agenttool.CRMThread, error) {
	rows, err := a.service.ListDealThreads(ctx, workspaceID, dealID)
	if err != nil {
		return nil, err
	}
	out := make([]agenttool.CRMThread, len(rows))
	for i, row := range rows {
		participants := make([]agenttool.CRMThreadParticipant, len(row.Participants))
		for j, participant := range row.Participants {
			participants[j] = agenttool.CRMThreadParticipant{Email: participant.Email, DisplayName: participant.DisplayName, ContactID: participant.ContactID}
		}
		messages := make([]agenttool.CRMThreadMessage, len(row.Messages))
		for j, message := range row.Messages {
			messages[j] = agenttool.CRMThreadMessage{ID: message.ID, Direction: message.Direction, Kind: message.Kind, MessageID: message.MessageID, SenderEmail: message.SenderEmail, RecipientEmail: message.RecipientEmail, Subject: message.Subject, ReplyClass: message.ReplyClass, OccurredAt: message.OccurredAt.Format("2006-01-02T15:04:05Z07:00")}
		}
		out[i] = agenttool.CRMThread{ID: row.ID, ThreadRef: row.ThreadRef, Subject: row.Subject, ReplyClass: row.ReplyClass, LastMessageAt: row.LastMessageAt.Format("2006-01-02T15:04:05Z07:00"), Participants: participants, Messages: messages}
	}
	return out, nil
}

func crmAgentActor(in agenttool.CRMActor) crm.Actor {
	userID := in.UserID.String()
	return crm.Actor{Type: "agent", ID: in.AgentClientID, ClientID: in.AgentClientID, OnBehalfOf: &userID, ThreadID: in.ThreadID, RunID: in.RunID}
}

// crmErrors is the ONE place a CRM error is classified for a model. It lives
// in the composition root because errors.Is against crm's sentinels is legal
// here and not inside agenttool (app packages do not import each other). Only
// text this package composed — never driver output — reaches the model.
type crmErrors struct{}

func (crmErrors) Recoverable(err error) (string, bool) {
	switch {
	case errors.Is(err, crm.ErrValidation), errors.Is(err, crm.ErrNotFound), errors.Is(err, crm.ErrConflict):
		return err.Error(), true
	default:
		return "", false
	}
}

var (
	_ agenttool.CRMIntegrationService = crmTools{}
	_ agenttool.ErrorClassifier       = crmErrors{}
)
