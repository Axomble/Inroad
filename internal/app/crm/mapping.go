package crm

import (
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

func companyFromList(row gen.ListCompaniesRow) Company {
	return company(row.ID, row.WorkspaceID, row.Name, row.Domain, row.OwnerUserID, row.AnnualRevenueMicros, row.Currency, row.DealCount, row.CreatedAt, row.UpdatedAt)
}

func companyFromGet(row gen.GetCompanyRow) Company {
	return company(row.ID, row.WorkspaceID, row.Name, row.Domain, row.OwnerUserID, row.AnnualRevenueMicros, row.Currency, row.DealCount, row.CreatedAt, row.UpdatedAt)
}

func companyFromRow(row gen.Company) Company {
	return company(row.ID, row.WorkspaceID, row.Name, row.Domain, row.OwnerUserID, row.AnnualRevenueMicros, row.Currency, 0, row.CreatedAt, row.UpdatedAt)
}

func company(id, workspaceID uuid.UUID, name string, domain *string, owner pgtype.UUID, revenue *int64, currency string, dealCount int64, createdAt, updatedAt pgtype.Timestamptz) Company {
	return Company{ID: id, WorkspaceID: workspaceID, Name: name, Domain: valueOrEmpty(domain), OwnerUserID: uuidValue(owner), AnnualRevenueMicros: revenue, Currency: currency, DealCount: dealCount, CreatedAt: createdAt.Time, UpdatedAt: updatedAt.Time}
}

func pipelineFromRows(row gen.Pipeline, stages []gen.PipelineStage) Pipeline {
	out := Pipeline{ID: row.ID, WorkspaceID: row.WorkspaceID, Name: row.Name, IsDefault: row.IsDefault, Stages: make([]Stage, len(stages)), CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time}
	for i, stage := range stages {
		out.Stages[i] = stageFromRow(stage)
	}
	return out
}

func stageFromRow(row gen.PipelineStage) Stage {
	return Stage{ID: row.ID, PipelineID: row.PipelineID, WorkspaceID: row.WorkspaceID, Key: row.Key, Label: row.Label, Color: row.Color, Position: row.Position, IsWon: row.IsWon, IsLost: row.IsLost, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time}
}

func dealFromList(row gen.ListDealsRow) Deal {
	return deal(row.ID, row.WorkspaceID, row.PipelineID, row.StageID, row.CompanyID, row.PrimaryContactID, row.OwnerUserID, row.Name, row.AmountMicros, row.Currency, row.CloseDate, row.Source, row.SourceCampaignID, row.SourceThreadRef, row.CreatedByActor, row.PipelineName, row.StageLabel, row.StageColor, row.StageIsWon, row.StageIsLost, row.CompanyName, row.ContactEmail, row.CreatedAt, row.UpdatedAt)
}

func dealFromGet(row gen.GetDealRow) Deal {
	return deal(row.ID, row.WorkspaceID, row.PipelineID, row.StageID, row.CompanyID, row.PrimaryContactID, row.OwnerUserID, row.Name, row.AmountMicros, row.Currency, row.CloseDate, row.Source, row.SourceCampaignID, row.SourceThreadRef, row.CreatedByActor, row.PipelineName, row.StageLabel, row.StageColor, row.StageIsWon, row.StageIsLost, row.CompanyName, row.ContactEmail, row.CreatedAt, row.UpdatedAt)
}

func deal(id, workspaceID, pipelineID, stageID uuid.UUID, companyID, contactID, ownerID pgtype.UUID, name string, amount *int64, currency string, closeDate pgtype.Date, source string, campaignID pgtype.UUID, threadRef string, actor []byte, pipelineName, stageLabel, stageColor string, won, lost bool, companyName, contactEmail string, createdAt, updatedAt pgtype.Timestamptz) Deal {
	return Deal{ID: id, WorkspaceID: workspaceID, PipelineID: pipelineID, StageID: stageID, CompanyID: uuidValue(companyID), PrimaryContactID: uuidValue(contactID), OwnerUserID: uuidValue(ownerID), Name: name, AmountMicros: amount, Currency: currency, CloseDate: dateValue(closeDate), Source: source, SourceCampaignID: uuidValue(campaignID), SourceThreadRef: threadRef, CreatedByActor: actor, PipelineName: pipelineName, StageLabel: stageLabel, StageColor: stageColor, StageIsWon: won, StageIsLost: lost, CompanyName: companyName, ContactEmail: contactEmail, CreatedAt: createdAt.Time, UpdatedAt: updatedAt.Time}
}

func noteFromRow(row gen.Note) Note {
	return Note{ID: row.ID, WorkspaceID: row.WorkspaceID, Title: row.Title, Body: row.Body, CreatedByActor: row.CreatedByActor, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time}
}

func taskFromRow(row gen.Task) Task {
	return Task{ID: row.ID, WorkspaceID: row.WorkspaceID, Title: row.Title, Body: row.Body, DueAt: timeValue(row.DueAt), Status: row.Status, AssigneeUserID: uuidValue(row.AssigneeUserID), CreatedByActor: row.CreatedByActor, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time}
}

func contactEmailFromRow(row gen.ContactEmail) ContactEmail {
	return ContactEmail{ID: row.ID, ContactID: row.ContactID, WorkspaceID: row.WorkspaceID, Email: row.Email, IsPrimary: row.IsPrimary, CreatedAt: row.CreatedAt.Time}
}

func uuidValue(value pgtype.UUID) *uuid.UUID {
	if !value.Valid {
		return nil
	}
	id := uuid.UUID(value.Bytes)
	return &id
}

func dateValue(value pgtype.Date) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time
	return &t
}

func timeValue(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time
	return &t
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
