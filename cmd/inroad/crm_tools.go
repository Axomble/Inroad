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

func (a crmTools) ListCompanies(ctx context.Context, workspaceID uuid.UUID) (agenttool.CRMList[agenttool.CRMCompany], error) {
	page, err := a.service.ListCompanies(ctx, workspaceID, crm.PageRequest{Limit: crmAgentPageLimit})
	if err != nil {
		return agenttool.CRMList[agenttool.CRMCompany]{}, err
	}
	out := make([]agenttool.CRMCompany, len(page.Items))
	for i, row := range page.Items {
		out[i] = agenttool.CRMCompany{ID: row.ID, Name: row.Name, Domain: row.Domain, Currency: row.Currency, DealCount: row.DealCount}
	}
	return agenttool.NewCRMList(out, page.NextCursor != ""), nil
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
		out[i] = agenttool.CRMDeal{ID: row.ID, Name: row.Name, PipelineName: row.PipelineName, StageLabel: row.StageLabel, CompanyName: row.CompanyName, Currency: row.Currency, AmountMicros: row.AmountMicros}
	}
	return agenttool.NewCRMList(out, page.NextCursor != ""), nil
}

func (a crmTools) CreateCompany(ctx context.Context, workspaceID uuid.UUID, in agenttool.CRMCompanyInput) (agenttool.CRMCompany, error) {
	row, err := a.service.CreateCompanyWithActor(ctx, workspaceID,
		crm.CompanyInput{Name: in.Name, Domain: in.Domain, Currency: in.Currency}, crmAgentActor(in.Actor))
	if err != nil {
		return agenttool.CRMCompany{}, err
	}
	return agenttool.CRMCompany{ID: row.ID, Name: row.Name, Domain: row.Domain, Currency: row.Currency, DealCount: row.DealCount}, nil
}

func (a crmTools) CreateDeal(ctx context.Context, workspaceID uuid.UUID, in agenttool.CRMDealInput) (agenttool.CRMDeal, error) {
	actor := crmAgentActor(in.Actor)
	row, err := a.service.CreateDeal(ctx, workspaceID, crm.DealInput{Name: in.Name, PipelineID: in.PipelineID, StageID: in.StageID, CompanyID: in.CompanyID, AmountMicros: in.AmountMicros, Currency: in.Currency, Source: "agent", Actor: actor})
	if err != nil {
		return agenttool.CRMDeal{}, err
	}
	return agenttool.CRMDeal{ID: row.ID, Name: row.Name, PipelineName: row.PipelineName, StageLabel: row.StageLabel, CompanyName: row.CompanyName, Currency: row.Currency, AmountMicros: row.AmountMicros}, nil
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
		out[i] = agenttool.CRMEvent{Name: row.Name, Kind: row.Kind, LinkedRecordCachedName: row.LinkedRecordCachedName,
			OccurredAt: row.OccurredAt.Format("2006-01-02T15:04:05Z07:00"), MergedCount: row.MergedCount}
	}
	return out, nil
}

func crmAgentActor(in agenttool.CRMActor) crm.Actor {
	userID := in.UserID.String()
	return crm.Actor{Type: "agent", ID: in.AgentClientID, ClientID: in.AgentClientID, OnBehalfOf: &userID}
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
