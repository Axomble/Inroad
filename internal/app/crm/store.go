package crm

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

type Store interface {
	ListCompanies(context.Context, uuid.UUID, int32) ([]Company, error)
	GetCompany(context.Context, uuid.UUID, uuid.UUID) (Company, error)
	CreateCompany(context.Context, uuid.UUID, CompanyInput) (Company, error)
	UpdateCompany(context.Context, uuid.UUID, uuid.UUID, CompanyInput) (Company, error)
	DeleteCompany(context.Context, uuid.UUID, uuid.UUID) error

	ListPipelines(context.Context, uuid.UUID) ([]Pipeline, error)
	GetPipeline(context.Context, uuid.UUID, uuid.UUID) (Pipeline, error)
	CreatePipeline(context.Context, uuid.UUID, PipelineInput) (Pipeline, error)
	UpdatePipeline(context.Context, uuid.UUID, uuid.UUID, PipelineInput) (Pipeline, error)
	DeletePipeline(context.Context, uuid.UUID, uuid.UUID) error
	CreateStage(context.Context, uuid.UUID, uuid.UUID, string, StageInput) (Stage, error)
	UpdateStage(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, StageInput) (Stage, error)
	DeleteStage(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error

	ListDeals(context.Context, uuid.UUID, int32) ([]Deal, error)
	GetDeal(context.Context, uuid.UUID, uuid.UUID) (Deal, error)
	CreateDeal(context.Context, uuid.UUID, DealInput) (Deal, error)
	UpdateDeal(context.Context, uuid.UUID, uuid.UUID, DealInput) (Deal, error)
	DeleteDeal(context.Context, uuid.UUID, uuid.UUID) error

	ListNotes(context.Context, uuid.UUID, Target, int32) ([]Note, error)
	CreateNote(context.Context, uuid.UUID, NoteInput) (Note, error)
	UpdateNote(context.Context, uuid.UUID, uuid.UUID, string, string) (Note, error)
	DeleteNote(context.Context, uuid.UUID, uuid.UUID) error
	ListTasks(context.Context, uuid.UUID, Target, int32) ([]Task, error)
	CreateTask(context.Context, uuid.UUID, TaskInput) (Task, error)
	UpdateTask(context.Context, uuid.UUID, uuid.UUID, TaskInput) (Task, error)
	DeleteTask(context.Context, uuid.UUID, uuid.UUID) error

	ListContactEmails(context.Context, uuid.UUID, uuid.UUID) ([]ContactEmail, error)
	AddContactEmail(context.Context, uuid.UUID, uuid.UUID, string) (ContactEmail, error)
	SetPrimaryContactEmail(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error
}

type PgStore struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

func NewPgStore(pool *pgxpool.Pool) *PgStore { return &PgStore{pool: pool, q: gen.New(pool)} }

func (s *PgStore) ListCompanies(ctx context.Context, workspaceID uuid.UUID, limit int32) ([]Company, error) {
	rows, err := s.q.ListCompanies(ctx, gen.ListCompaniesParams{WorkspaceID: workspaceID, Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]Company, len(rows))
	for i, row := range rows {
		out[i] = companyFromList(row)
	}
	return out, nil
}

func (s *PgStore) GetCompany(ctx context.Context, workspaceID, id uuid.UUID) (Company, error) {
	row, err := s.q.GetCompany(ctx, gen.GetCompanyParams{WorkspaceID: workspaceID, ID: id})
	if err != nil {
		return Company{}, notFound(err)
	}
	return companyFromGet(row), nil
}

func (s *PgStore) CreateCompany(ctx context.Context, workspaceID uuid.UUID, in CompanyInput) (Company, error) {
	row, err := s.q.InsertCompany(ctx, gen.InsertCompanyParams{
		WorkspaceID: workspaceID, Name: in.Name, Domain: stringPtr(in.Domain),
		OwnerUserID: pgUUID(in.OwnerUserID), AnnualRevenueMicros: in.AnnualRevenueMicros,
		Currency: in.Currency,
	})
	if err != nil {
		return Company{}, err
	}
	return companyFromRow(row), nil
}

func (s *PgStore) UpdateCompany(ctx context.Context, workspaceID, id uuid.UUID, in CompanyInput) (Company, error) {
	row, err := s.q.UpdateCompany(ctx, gen.UpdateCompanyParams{
		WorkspaceID: workspaceID, ID: id, Name: in.Name, Domain: in.Domain,
		OwnerUserID: pgUUID(in.OwnerUserID), AnnualRevenueMicros: in.AnnualRevenueMicros,
		Currency: in.Currency,
	})
	if err != nil {
		return Company{}, notFound(err)
	}
	return companyFromRow(row), nil
}

func (s *PgStore) DeleteCompany(ctx context.Context, workspaceID, id uuid.UUID) error {
	n, err := s.q.DeleteCompany(ctx, gen.DeleteCompanyParams{WorkspaceID: workspaceID, ID: id})
	return affected(n, err)
}

func (s *PgStore) ListPipelines(ctx context.Context, workspaceID uuid.UUID) ([]Pipeline, error) {
	rows, err := s.q.ListPipelines(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]Pipeline, 0, len(rows))
	for _, row := range rows {
		stages, err := s.q.ListPipelineStages(ctx, gen.ListPipelineStagesParams{WorkspaceID: workspaceID, PipelineID: row.ID})
		if err != nil {
			return nil, err
		}
		out = append(out, pipelineFromRows(row, stages))
	}
	return out, nil
}

func (s *PgStore) GetPipeline(ctx context.Context, workspaceID, id uuid.UUID) (Pipeline, error) {
	row, err := s.q.GetPipeline(ctx, gen.GetPipelineParams{WorkspaceID: workspaceID, ID: id})
	if err != nil {
		return Pipeline{}, notFound(err)
	}
	stages, err := s.q.ListPipelineStages(ctx, gen.ListPipelineStagesParams{WorkspaceID: workspaceID, PipelineID: id})
	if err != nil {
		return Pipeline{}, err
	}
	return pipelineFromRows(row, stages), nil
}

var defaultStages = [...]struct {
	key, label, color string
	won, lost         bool
}{
	{"lead", "Lead", "#64748B", false, false},
	{"qualified", "Qualified", "#3B82F6", false, false},
	{"proposal", "Proposal", "#8B5CF6", false, false},
	{"won", "Won", "#22C55E", true, false},
	{"lost", "Lost", "#EF4444", false, true},
}

func (s *PgStore) CreatePipeline(ctx context.Context, workspaceID uuid.UUID, in PipelineInput) (Pipeline, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Pipeline{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	row, err := qtx.InsertPipeline(ctx, gen.InsertPipelineParams{WorkspaceID: workspaceID, Name: in.Name, IsDefault: false})
	if err != nil {
		return Pipeline{}, err
	}
	stages := make([]gen.PipelineStage, 0, len(defaultStages))
	for i, stage := range defaultStages {
		created, err := qtx.InsertPipelineStage(ctx, gen.InsertPipelineStageParams{
			WorkspaceID: workspaceID, PipelineID: row.ID, Key: stage.key,
			Label: stage.label, Color: stage.color, Position: int32(i), IsWon: stage.won, IsLost: stage.lost,
		})
		if err != nil {
			return Pipeline{}, err
		}
		stages = append(stages, created)
	}
	if err := tx.Commit(ctx); err != nil {
		return Pipeline{}, err
	}
	return pipelineFromRows(row, stages), nil
}

func (s *PgStore) UpdatePipeline(ctx context.Context, workspaceID, id uuid.UUID, in PipelineInput) (Pipeline, error) {
	_, err := s.q.UpdatePipeline(ctx, gen.UpdatePipelineParams{WorkspaceID: workspaceID, ID: id, Name: in.Name})
	if err != nil {
		return Pipeline{}, notFound(err)
	}
	return s.GetPipeline(ctx, workspaceID, id)
}

func (s *PgStore) DeletePipeline(ctx context.Context, workspaceID, id uuid.UUID) error {
	n, err := s.q.DeletePipeline(ctx, gen.DeletePipelineParams{WorkspaceID: workspaceID, ID: id})
	return affected(n, err)
}

func (s *PgStore) CreateStage(ctx context.Context, workspaceID, pipelineID uuid.UUID, key string, in StageInput) (Stage, error) {
	row, err := s.q.InsertPipelineStage(ctx, gen.InsertPipelineStageParams{
		WorkspaceID: workspaceID, PipelineID: pipelineID, Key: key, Label: in.Label,
		Color: in.Color, Position: in.Position, IsWon: in.IsWon, IsLost: in.IsLost,
	})
	if err != nil {
		return Stage{}, err
	}
	return stageFromRow(row), nil
}

func (s *PgStore) UpdateStage(ctx context.Context, workspaceID, pipelineID, id uuid.UUID, in StageInput) (Stage, error) {
	row, err := s.q.UpdatePipelineStage(ctx, gen.UpdatePipelineStageParams{
		WorkspaceID: workspaceID, PipelineID: pipelineID, ID: id, Label: in.Label,
		Color: in.Color, Position: in.Position, IsWon: in.IsWon, IsLost: in.IsLost,
	})
	if err != nil {
		return Stage{}, notFound(err)
	}
	return stageFromRow(row), nil
}

func (s *PgStore) DeleteStage(ctx context.Context, workspaceID, pipelineID, id uuid.UUID) error {
	n, err := s.q.DeletePipelineStage(ctx, gen.DeletePipelineStageParams{WorkspaceID: workspaceID, PipelineID: pipelineID, ID: id})
	return affected(n, err)
}

func (s *PgStore) ListDeals(ctx context.Context, workspaceID uuid.UUID, limit int32) ([]Deal, error) {
	rows, err := s.q.ListDeals(ctx, gen.ListDealsParams{WorkspaceID: workspaceID, Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]Deal, len(rows))
	for i, row := range rows {
		out[i] = dealFromList(row)
	}
	return out, nil
}

func (s *PgStore) GetDeal(ctx context.Context, workspaceID, id uuid.UUID) (Deal, error) {
	row, err := s.q.GetDeal(ctx, gen.GetDealParams{WorkspaceID: workspaceID, ID: id})
	if err != nil {
		return Deal{}, notFound(err)
	}
	return dealFromGet(row), nil
}

func (s *PgStore) CreateDeal(ctx context.Context, workspaceID uuid.UUID, in DealInput) (Deal, error) {
	position, err := s.q.NextDealPosition(ctx, gen.NextDealPositionParams{
		WorkspaceID: workspaceID, PipelineID: in.PipelineID, StageID: in.StageID,
	})
	if err != nil {
		return Deal{}, err
	}
	actor, err := in.Actor.JSON()
	if err != nil {
		return Deal{}, fmt.Errorf("encode actor: %w", err)
	}
	row, err := s.q.InsertDeal(ctx, gen.InsertDealParams{
		WorkspaceID: workspaceID, PipelineID: in.PipelineID, StageID: in.StageID,
		CompanyID: pgUUID(in.CompanyID), PrimaryContactID: pgUUID(in.PrimaryContactID),
		OwnerUserID: pgUUID(in.OwnerUserID), Name: in.Name, AmountMicros: in.AmountMicros,
		Currency: in.Currency, CloseDate: pgDate(in.CloseDate), Position: pgNumeric(position),
		Source: in.Source, SourceCampaignID: pgUUID(in.SourceCampaignID),
		SourceThreadRef: in.SourceThreadRef, CreatedByActor: actor,
	})
	if err != nil {
		return Deal{}, err
	}
	return s.GetDeal(ctx, workspaceID, row.ID)
}

func (s *PgStore) UpdateDeal(ctx context.Context, workspaceID, id uuid.UUID, in DealInput) (Deal, error) {
	current, err := s.q.GetDeal(ctx, gen.GetDealParams{WorkspaceID: workspaceID, ID: id})
	if err != nil {
		return Deal{}, notFound(err)
	}
	position := current.Position
	if current.PipelineID != in.PipelineID || current.StageID != in.StageID {
		next, nextErr := s.q.NextDealPosition(ctx, gen.NextDealPositionParams{
			WorkspaceID: workspaceID, PipelineID: in.PipelineID, StageID: in.StageID,
		})
		if nextErr != nil {
			return Deal{}, nextErr
		}
		position = pgNumeric(next)
	}
	_, err = s.q.UpdateDeal(ctx, gen.UpdateDealParams{
		WorkspaceID: workspaceID, ID: id, PipelineID: in.PipelineID, StageID: in.StageID,
		CompanyID: pgUUID(in.CompanyID), PrimaryContactID: pgUUID(in.PrimaryContactID),
		OwnerUserID: pgUUID(in.OwnerUserID), Name: in.Name, AmountMicros: in.AmountMicros,
		Currency: in.Currency, CloseDate: pgDate(in.CloseDate), Position: position,
	})
	if err != nil {
		return Deal{}, notFound(err)
	}
	return s.GetDeal(ctx, workspaceID, id)
}

func (s *PgStore) DeleteDeal(ctx context.Context, workspaceID, id uuid.UUID) error {
	n, err := s.q.DeleteDeal(ctx, gen.DeleteDealParams{WorkspaceID: workspaceID, ID: id})
	return affected(n, err)
}

//nolint:dupl // Notes and tasks use distinct sqlc row types; keeping both typed avoids reflection and unsafe conversions.
func (s *PgStore) ListNotes(ctx context.Context, workspaceID uuid.UUID, target Target, limit int32) ([]Note, error) {
	return listTargeted(target,
		func(id pgtype.UUID) ([]gen.Note, error) {
			return s.q.ListNotesForContact(ctx, gen.ListNotesForContactParams{WorkspaceID: workspaceID, ContactID: id, Limit: limit})
		},
		func(id pgtype.UUID) ([]gen.Note, error) {
			return s.q.ListNotesForCompany(ctx, gen.ListNotesForCompanyParams{WorkspaceID: workspaceID, CompanyID: id, Limit: limit})
		},
		func(id pgtype.UUID) ([]gen.Note, error) {
			return s.q.ListNotesForDeal(ctx, gen.ListNotesForDealParams{WorkspaceID: workspaceID, DealID: id, Limit: limit})
		},
		noteFromRow,
	)
}

func (s *PgStore) CreateNote(ctx context.Context, workspaceID uuid.UUID, in NoteInput) (Note, error) {
	actor, err := in.Actor.JSON()
	if err != nil {
		return Note{}, fmt.Errorf("encode actor: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Note{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	row, err := qtx.InsertNote(ctx, gen.InsertNoteParams{WorkspaceID: workspaceID, Title: in.Title, Body: in.Body, CreatedByActor: actor})
	if err != nil {
		return Note{}, err
	}
	contactID, companyID, dealID := targetUUIDs(in.Target)
	if err := qtx.InsertNoteTarget(ctx, gen.InsertNoteTargetParams{
		WorkspaceID: workspaceID, NoteID: row.ID, ContactID: contactID, CompanyID: companyID, DealID: dealID,
	}); err != nil {
		return Note{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Note{}, err
	}
	return noteFromRow(row), nil
}

func (s *PgStore) UpdateNote(ctx context.Context, workspaceID, id uuid.UUID, title, body string) (Note, error) {
	row, err := s.q.UpdateNote(ctx, gen.UpdateNoteParams{WorkspaceID: workspaceID, ID: id, Title: title, Body: body})
	if err != nil {
		return Note{}, notFound(err)
	}
	return noteFromRow(row), nil
}

func (s *PgStore) DeleteNote(ctx context.Context, workspaceID, id uuid.UUID) error {
	n, err := s.q.DeleteNote(ctx, gen.DeleteNoteParams{WorkspaceID: workspaceID, ID: id})
	return affected(n, err)
}

//nolint:dupl // See ListNotes: the parallel shape preserves compile-time mappings for separate aggregates.
func (s *PgStore) ListTasks(ctx context.Context, workspaceID uuid.UUID, target Target, limit int32) ([]Task, error) {
	return listTargeted(target,
		func(id pgtype.UUID) ([]gen.Task, error) {
			return s.q.ListTasksForContact(ctx, gen.ListTasksForContactParams{WorkspaceID: workspaceID, ContactID: id, Limit: limit})
		},
		func(id pgtype.UUID) ([]gen.Task, error) {
			return s.q.ListTasksForCompany(ctx, gen.ListTasksForCompanyParams{WorkspaceID: workspaceID, CompanyID: id, Limit: limit})
		},
		func(id pgtype.UUID) ([]gen.Task, error) {
			return s.q.ListTasksForDeal(ctx, gen.ListTasksForDealParams{WorkspaceID: workspaceID, DealID: id, Limit: limit})
		},
		taskFromRow,
	)
}

func listTargeted[Row, Value any](target Target, contact, company, deal func(pgtype.UUID) ([]Row, error), convert func(Row) Value) ([]Value, error) {
	id := pgtype.UUID{Bytes: target.ID, Valid: true}
	var rows []Row
	var err error
	switch target.Type {
	case TargetContact:
		rows, err = contact(id)
	case TargetCompany:
		rows, err = company(id)
	case TargetDeal:
		rows, err = deal(id)
	default:
		return nil, ErrValidation
	}
	if err != nil {
		return nil, err
	}
	out := make([]Value, len(rows))
	for i, row := range rows {
		out[i] = convert(row)
	}
	return out, nil
}

func (s *PgStore) CreateTask(ctx context.Context, workspaceID uuid.UUID, in TaskInput) (Task, error) {
	actor, err := in.Actor.JSON()
	if err != nil {
		return Task{}, fmt.Errorf("encode actor: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Task{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	row, err := qtx.InsertTask(ctx, gen.InsertTaskParams{
		WorkspaceID: workspaceID, Title: in.Title, Body: in.Body, DueAt: pgTime(in.DueAt),
		Status: in.Status, AssigneeUserID: pgUUID(in.AssigneeUserID), CreatedByActor: actor,
	})
	if err != nil {
		return Task{}, err
	}
	contactID, companyID, dealID := targetUUIDs(in.Target)
	if err := qtx.InsertTaskTarget(ctx, gen.InsertTaskTargetParams{
		WorkspaceID: workspaceID, TaskID: row.ID, ContactID: contactID, CompanyID: companyID, DealID: dealID,
	}); err != nil {
		return Task{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Task{}, err
	}
	return taskFromRow(row), nil
}

func (s *PgStore) UpdateTask(ctx context.Context, workspaceID, id uuid.UUID, in TaskInput) (Task, error) {
	row, err := s.q.UpdateTask(ctx, gen.UpdateTaskParams{
		WorkspaceID: workspaceID, ID: id, Title: in.Title, Body: in.Body,
		DueAt: pgTime(in.DueAt), Status: in.Status, AssigneeUserID: pgUUID(in.AssigneeUserID),
	})
	if err != nil {
		return Task{}, notFound(err)
	}
	return taskFromRow(row), nil
}

func (s *PgStore) DeleteTask(ctx context.Context, workspaceID, id uuid.UUID) error {
	n, err := s.q.DeleteTask(ctx, gen.DeleteTaskParams{WorkspaceID: workspaceID, ID: id})
	return affected(n, err)
}

func (s *PgStore) ListContactEmails(ctx context.Context, workspaceID, contactID uuid.UUID) ([]ContactEmail, error) {
	rows, err := s.q.ListContactEmails(ctx, gen.ListContactEmailsParams{WorkspaceID: workspaceID, ContactID: contactID})
	if err != nil {
		return nil, err
	}
	out := make([]ContactEmail, len(rows))
	for i, row := range rows {
		out[i] = contactEmailFromRow(row)
	}
	return out, nil
}

func (s *PgStore) AddContactEmail(ctx context.Context, workspaceID, contactID uuid.UUID, email string) (ContactEmail, error) {
	row, err := s.q.InsertContactEmail(ctx, gen.InsertContactEmailParams{WorkspaceID: workspaceID, ContactID: contactID, Email: email})
	if err != nil {
		return ContactEmail{}, err
	}
	return contactEmailFromRow(row), nil
}

func (s *PgStore) SetPrimaryContactEmail(ctx context.Context, workspaceID, contactID, emailID uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	if err := qtx.ClearPrimaryContactEmails(ctx, gen.ClearPrimaryContactEmailsParams{WorkspaceID: workspaceID, ContactID: contactID}); err != nil {
		return err
	}
	row, err := qtx.SetPrimaryContactEmail(ctx, gen.SetPrimaryContactEmailParams{
		WorkspaceID: workspaceID, ContactID: contactID, ID: emailID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	n, err := qtx.UpdateContactPrimaryEmail(ctx, gen.UpdateContactPrimaryEmailParams{
		WorkspaceID: workspaceID, ID: contactID, Email: row,
	})
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

func targetUUIDs(target Target) (pgtype.UUID, pgtype.UUID, pgtype.UUID) {
	value := pgtype.UUID{Bytes: target.ID, Valid: true}
	switch target.Type {
	case TargetContact:
		return value, pgtype.UUID{}, pgtype.UUID{}
	case TargetCompany:
		return pgtype.UUID{}, value, pgtype.UUID{}
	case TargetDeal:
		return pgtype.UUID{}, pgtype.UUID{}, value
	default:
		return pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}
	}
}

func affected(count int64, err error) error {
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func pgUUID(id *uuid.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}

func pgDate(value *time.Time) pgtype.Date {
	if value == nil {
		return pgtype.Date{}
	}
	return pgtype.Date{Time: *value, Valid: true}
}

func pgTime(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *value, Valid: true}
}

func pgNumeric(value int32) pgtype.Numeric {
	return pgtype.Numeric{Int: big.NewInt(int64(value)), Valid: true}
}

func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

var _ Store = (*PgStore)(nil)
