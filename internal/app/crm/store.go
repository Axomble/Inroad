package crm

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

type Store interface {
	ListCompanies(context.Context, uuid.UUID, PageRequest) (Page[Company], error)
	GetCompany(context.Context, uuid.UUID, uuid.UUID) (Company, error)
	CreateCompany(context.Context, uuid.UUID, CompanyInput) (Company, error)
	UpdateCompany(context.Context, uuid.UUID, uuid.UUID, CompanyInput) (Company, error)
	DeleteCompany(context.Context, uuid.UUID, uuid.UUID) error

	ListPipelines(context.Context, uuid.UUID, int32) ([]Pipeline, error)
	GetPipeline(context.Context, uuid.UUID, uuid.UUID) (Pipeline, error)
	CreatePipeline(context.Context, uuid.UUID, PipelineInput) (Pipeline, error)
	UpdatePipeline(context.Context, uuid.UUID, uuid.UUID, PipelineInput) (Pipeline, error)
	// PipelineIsDefault reports the guard state DeletePipeline enforces,
	// separately from existence, so the service can tell "no such pipeline"
	// (404) apart from "the default pipeline may not be deleted" (409).
	PipelineIsDefault(context.Context, uuid.UUID, uuid.UUID) (bool, error)
	DeletePipeline(context.Context, uuid.UUID, uuid.UUID) error
	CreateStage(context.Context, uuid.UUID, uuid.UUID, string, StageInput) (Stage, error)
	UpdateStage(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, StageInput) (Stage, error)
	StageExists(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (bool, error)
	CountStageDeals(context.Context, uuid.UUID, uuid.UUID) (int64, error)
	DeleteStage(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error

	ListDeals(context.Context, uuid.UUID, PageRequest) (Page[Deal], error)
	GetDeal(context.Context, uuid.UUID, uuid.UUID) (Deal, error)
	CreateDeal(context.Context, uuid.UUID, DealInput) (Deal, error)
	UpdateDeal(context.Context, uuid.UUID, uuid.UUID, DealInput) (Deal, error)
	DeleteDeal(context.Context, uuid.UUID, uuid.UUID) error

	ListNotes(context.Context, uuid.UUID, Target, PageRequest) (Page[Note], error)
	CreateNote(context.Context, uuid.UUID, NoteInput) (Note, error)
	UpdateNote(context.Context, uuid.UUID, uuid.UUID, string, string) (Note, error)
	DeleteNote(context.Context, uuid.UUID, uuid.UUID) error
	ListTasks(context.Context, uuid.UUID, Target, PageRequest) (Page[Task], error)
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

func (s *PgStore) ListCompanies(ctx context.Context, workspaceID uuid.UUID, page PageRequest) (Page[Company], error) {
	params := gen.ListCompaniesParams{WorkspaceID: workspaceID, PageLimit: page.Limit}
	if page.Cursor != "" {
		keys, err := decodeCursor(cursorCompanies, page.Cursor, 2)
		if err != nil {
			return Page[Company]{}, err
		}
		id, err := uuid.Parse(keys[0])
		if err != nil {
			return Page[Company]{}, validation("cursor is malformed")
		}
		params.Seek, params.CursorID, params.CursorName = true, id, keys[1]
	}
	rows, err := s.q.ListCompanies(ctx, params)
	if err != nil {
		return Page[Company]{}, err
	}
	out := Page[Company]{Items: make([]Company, len(rows))}
	for i, row := range rows {
		out.Items[i] = companyFromList(row)
	}
	if last, ok := lastOfFullPage(rows, page.Limit); ok {
		out.NextCursor = encodeCursor(cursorCompanies, last.ID.String(), last.NameKey)
	}
	return out, nil
}

// lastOfFullPage returns the row a next-page cursor should be built from: only
// a page filled to the limit can have a successor, so a short page ends the
// listing without an extra count query.
func lastOfFullPage[T any](rows []T, limit int32) (T, bool) {
	var zero T
	if limit <= 0 || int32(len(rows)) < limit {
		return zero, false
	}
	return rows[len(rows)-1], true
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

func (s *PgStore) ListPipelines(ctx context.Context, workspaceID uuid.UUID, limit int32) ([]Pipeline, error) {
	rows, err := s.q.ListPipelines(ctx, gen.ListPipelinesParams{WorkspaceID: workspaceID, Limit: limit})
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	stages, err := s.q.ListStagesForPipelines(ctx, gen.ListStagesForPipelinesParams{WorkspaceID: workspaceID, PipelineIds: ids})
	if err != nil {
		return nil, err
	}
	byPipeline := make(map[uuid.UUID][]gen.PipelineStage, len(rows))
	for _, stage := range stages {
		byPipeline[stage.PipelineID] = append(byPipeline[stage.PipelineID], stage)
	}
	out := make([]Pipeline, len(rows))
	for i, row := range rows {
		out[i] = pipelineFromRows(row, byPipeline[row.ID])
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

// CreatePipeline seeds the stage set through the same SQL function the
// workspace seed uses, so the default stages have exactly one definition
// (migration 000042) and the two paths cannot drift.
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
	if err := qtx.SeedPipelineStages(ctx, gen.SeedPipelineStagesParams{PipelineID: row.ID, WorkspaceID: workspaceID}); err != nil {
		return Pipeline{}, err
	}
	stages, err := qtx.ListPipelineStages(ctx, gen.ListPipelineStagesParams{WorkspaceID: workspaceID, PipelineID: row.ID})
	if err != nil {
		return Pipeline{}, err
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

func (s *PgStore) PipelineIsDefault(ctx context.Context, workspaceID, id uuid.UUID) (bool, error) {
	isDefault, err := s.q.PipelineIsDefault(ctx, gen.PipelineIsDefaultParams{WorkspaceID: workspaceID, ID: id})
	if err != nil {
		return false, notFound(err)
	}
	return isDefault, nil
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

func (s *PgStore) StageExists(ctx context.Context, workspaceID, pipelineID, id uuid.UUID) (bool, error) {
	_, err := s.q.GetPipelineStage(ctx, gen.GetPipelineStageParams{WorkspaceID: workspaceID, PipelineID: pipelineID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *PgStore) CountStageDeals(ctx context.Context, workspaceID, stageID uuid.UUID) (int64, error) {
	return s.q.CountStageDeals(ctx, gen.CountStageDealsParams{WorkspaceID: workspaceID, StageID: stageID})
}

func (s *PgStore) DeleteStage(ctx context.Context, workspaceID, pipelineID, id uuid.UUID) error {
	n, err := s.q.DeletePipelineStage(ctx, gen.DeletePipelineStageParams{WorkspaceID: workspaceID, PipelineID: pipelineID, ID: id})
	return affected(n, err)
}

func (s *PgStore) ListDeals(ctx context.Context, workspaceID uuid.UUID, page PageRequest) (Page[Deal], error) {
	params := gen.ListDealsParams{WorkspaceID: workspaceID, PageLimit: page.Limit}
	if page.Cursor != "" {
		keys, err := decodeCursor(cursorDeals, page.Cursor, 3)
		if err != nil {
			return Page[Deal]{}, err
		}
		stagePosition, err := strconv.ParseInt(keys[0], 10, 32)
		if err != nil {
			return Page[Deal]{}, validation("cursor is malformed")
		}
		id, err := uuid.Parse(keys[2])
		if err != nil {
			return Page[Deal]{}, validation("cursor is malformed")
		}
		params.Seek = true
		params.CursorStagePosition, params.CursorPosition, params.CursorID = int32(stagePosition), keys[1], id
	}
	rows, err := s.q.ListDeals(ctx, params)
	if err != nil {
		return Page[Deal]{}, err
	}
	out := Page[Deal]{Items: make([]Deal, len(rows))}
	for i, row := range rows {
		out.Items[i] = dealFromList(row)
	}
	if last, ok := lastOfFullPage(rows, page.Limit); ok {
		out.NextCursor = encodeCursor(cursorDeals,
			strconv.FormatInt(int64(last.StagePosition), 10), last.PositionKey, last.ID.String())
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
		Currency: in.Currency, CloseDate: pgDate(in.CloseDate), Position: position,
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
		position = next
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

func (s *PgStore) ListNotes(ctx context.Context, workspaceID uuid.UUID, target Target, page PageRequest) (Page[Note], error) {
	seek, cursorTime, cursorID, err := decodeTimeCursor(cursorNotes, page.Cursor)
	if err != nil {
		return Page[Note]{}, err
	}
	rows, err := listTargeted(target,
		func(id pgtype.UUID) ([]gen.Note, error) {
			return s.q.ListNotesForContact(ctx, gen.ListNotesForContactParams{WorkspaceID: workspaceID, ContactID: id,
				Seek: seek, CursorTime: cursorTime, CursorID: cursorID, PageLimit: page.Limit})
		},
		func(id pgtype.UUID) ([]gen.Note, error) {
			return s.q.ListNotesForCompany(ctx, gen.ListNotesForCompanyParams{WorkspaceID: workspaceID, CompanyID: id,
				Seek: seek, CursorTime: cursorTime, CursorID: cursorID, PageLimit: page.Limit})
		},
		func(id pgtype.UUID) ([]gen.Note, error) {
			return s.q.ListNotesForDeal(ctx, gen.ListNotesForDealParams{WorkspaceID: workspaceID, DealID: id,
				Seek: seek, CursorTime: cursorTime, CursorID: cursorID, PageLimit: page.Limit})
		},
		noteFromRow,
	)
	if err != nil {
		return Page[Note]{}, err
	}
	out := Page[Note]{Items: rows}
	if last, ok := lastOfFullPage(rows, page.Limit); ok {
		out.NextCursor = encodeCursor(cursorNotes, last.CreatedAt.UTC().Format(time.RFC3339Nano), last.ID.String())
	}
	return out, nil
}

// decodeTimeCursor reads the (created_at, id) cursor shared by the note lists.
func decodeTimeCursor(kind cursorKind, raw string) (bool, pgtype.Timestamptz, uuid.UUID, error) {
	if raw == "" {
		return false, pgtype.Timestamptz{Valid: true}, uuid.Nil, nil
	}
	keys, err := decodeCursor(kind, raw, 2)
	if err != nil {
		return false, pgtype.Timestamptz{}, uuid.Nil, err
	}
	at, err := time.Parse(time.RFC3339Nano, keys[0])
	if err != nil {
		return false, pgtype.Timestamptz{}, uuid.Nil, validation("cursor is malformed")
	}
	id, err := uuid.Parse(keys[1])
	if err != nil {
		return false, pgtype.Timestamptz{}, uuid.Nil, validation("cursor is malformed")
	}
	return true, pgtype.Timestamptz{Time: at, Valid: true}, id, nil
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

func (s *PgStore) ListTasks(ctx context.Context, workspaceID uuid.UUID, target Target, page PageRequest) (Page[Task], error) {
	seek, done, due, cursorID, err := decodeTaskCursor(page.Cursor)
	if err != nil {
		return Page[Task]{}, err
	}
	rows, err := listTargeted(target,
		func(id pgtype.UUID) ([]gen.Task, error) {
			return s.q.ListTasksForContact(ctx, gen.ListTasksForContactParams{WorkspaceID: workspaceID, ContactID: id,
				Seek: seek, CursorDone: done, CursorDue: due, CursorID: cursorID, PageLimit: page.Limit})
		},
		func(id pgtype.UUID) ([]gen.Task, error) {
			return s.q.ListTasksForCompany(ctx, gen.ListTasksForCompanyParams{WorkspaceID: workspaceID, CompanyID: id,
				Seek: seek, CursorDone: done, CursorDue: due, CursorID: cursorID, PageLimit: page.Limit})
		},
		func(id pgtype.UUID) ([]gen.Task, error) {
			return s.q.ListTasksForDeal(ctx, gen.ListTasksForDealParams{WorkspaceID: workspaceID, DealID: id,
				Seek: seek, CursorDone: done, CursorDue: due, CursorID: cursorID, PageLimit: page.Limit})
		},
		taskFromRow,
	)
	if err != nil {
		return Page[Task]{}, err
	}
	out := Page[Task]{Items: rows}
	if last, ok := lastOfFullPage(rows, page.Limit); ok {
		out.NextCursor = encodeCursor(cursorTasks, strconv.FormatBool(taskIsClosed(last.Status)),
			taskDueKey(last.DueAt), last.ID.String())
	}
	return out, nil
}

// taskIsClosed and taskDueKey mirror the task ordering expression in SQL
// (`status IN ('done','cancelled')` and `COALESCE(due_at, 'infinity')`). They
// are the Go half of one ordering, so they must change together with it.
func taskIsClosed(status string) bool { return status == TaskDone || status == TaskCancelled }

const taskDueInfinity = "infinity"

func taskDueKey(due *time.Time) string {
	if due == nil {
		return taskDueInfinity
	}
	return due.UTC().Format(time.RFC3339Nano)
}

func decodeTaskCursor(raw string) (bool, bool, pgtype.Timestamptz, uuid.UUID, error) {
	if raw == "" {
		return false, false, pgtype.Timestamptz{Valid: true}, uuid.Nil, nil
	}
	keys, err := decodeCursor(cursorTasks, raw, 3)
	if err != nil {
		return false, false, pgtype.Timestamptz{}, uuid.Nil, err
	}
	done, doneErr := strconv.ParseBool(keys[0])
	id, idErr := uuid.Parse(keys[2])
	if doneErr != nil || idErr != nil {
		return false, false, pgtype.Timestamptz{}, uuid.Nil, validation("cursor is malformed")
	}
	due := pgtype.Timestamptz{InfinityModifier: pgtype.Infinity, Valid: true}
	if keys[1] != taskDueInfinity {
		at, parseErr := time.Parse(time.RFC3339Nano, keys[1])
		if parseErr != nil {
			return false, false, pgtype.Timestamptz{}, uuid.Nil, validation("cursor is malformed")
		}
		due = pgtype.Timestamptz{Time: at, Valid: true}
	}
	return true, done, due, id, nil
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

func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

var _ Store = (*PgStore)(nil)
