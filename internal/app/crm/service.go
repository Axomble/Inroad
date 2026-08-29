package crm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	// Aliased: this domain already calls its ACTIVITY-FEED rows "events"
	// (see ListEvents/mergeEvents). realtimeevents keeps the two apart —
	// one is durable history, the other a best-effort notification.
	realtimeevents "github.com/inroad/inroad/internal/app/events"
)

const (
	// defaultPageLimit is what a caller that asks for no limit gets;
	// maxPageLimit is the ceiling, so no single request can be made to scan a
	// whole workspace.
	defaultPageLimit int32 = 50
	maxPageLimit     int32 = 200
	// maxPipelines bounds the pipeline listing. Pipelines are a small,
	// human-curated set; a workspace with more than this has a data problem,
	// not a pagination need.
	maxPipelines int32 = 100
	// boardDealLimit bounds the deals a single board render loads. The
	// per-stage counts and totals come from a separate aggregate query, so a
	// board over this size still reports correct totals.
	boardDealLimit int32 = 500
)

// normalizePage clamps the requested page size. A caller-supplied limit is
// trusted only within [1, maxPageLimit]; anything else is the default.
func normalizePage(page PageRequest) PageRequest {
	if page.Limit <= 0 {
		page.Limit = defaultPageLimit
	}
	if page.Limit > maxPageLimit {
		page.Limit = maxPageLimit
	}
	return page
}

var (
	currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)
	colorPattern    = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
)

// ReplyLabelReader answers whether a reply label opens a deal. The reply-label
// taxonomy is owned by the replylabel app domain and app/* packages never
// import each other (CLAUDE.md), so this narrow, consumer-defined interface is
// satisfied at wiring time by an adapter over that domain's service — the same
// shape as campaign.DomainAuthReader.
type ReplyLabelReader interface {
	// CapturesDeal reports whether the workspace's label for key has the
	// captures_deal role. ok=false means no label claims the key, which is not
	// an error: the caller degrades to the pre-taxonomy rule.
	CapturesDeal(ctx context.Context, workspaceID uuid.UUID, key string) (captures bool, ok bool, err error)
}

// ServiceOption configures optional, nil-safe collaborators on the service.
type ServiceOption func(*Service)

// WithReplyLabels wires the source of truth for which reply labels auto-capture
// a deal. Unwired, CapturePositiveReply degrades to the pre-taxonomy rule (only
// reply_class "positive" captures) rather than failing — see capturesDeal.
func WithReplyLabels(r ReplyLabelReader) ServiceOption {
	return func(s *Service) { s.replyLabels = r }
}

// WithEvents wires realtime announcements. Unwired, the service is silent and
// clients learn about a move on their next refetch — the pre-socket behaviour.
func WithEvents(p realtimeevents.Publisher) ServiceOption {
	return func(s *Service) { s.realtime = p }
}

type Service struct {
	store Store
	// replyLabels is optional; nil is a supported state (see capturesDeal).
	replyLabels ReplyLabelReader
	// realtime announces changes to a workspace's open tabs. Named `realtime`
	// rather than `events` because this domain already has an `emit` for the
	// ACTIVITY FEED (a durable record, and an error there fails the write). These
	// are different things: one is persisted history, the other a best-effort
	// notification. Nil is valid and means realtime is disabled.
	realtime realtimeevents.Publisher
}

func NewService(store Store, opts ...ServiceOption) *Service {
	s := &Service{store: store}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *Service) ListCompanies(ctx context.Context, workspaceID uuid.UUID, page PageRequest) (Page[Company], error) {
	return s.store.ListCompanies(ctx, workspaceID, normalizePage(page))
}

func (s *Service) GetCompany(ctx context.Context, workspaceID, id uuid.UUID) (Company, error) {
	return s.store.GetCompany(ctx, workspaceID, id)
}

func (s *Service) CreateCompany(ctx context.Context, workspaceID uuid.UUID, in CompanyInput) (Company, error) {
	return s.CreateCompanyWithActor(ctx, workspaceID, in, Actor{Type: "system", ID: "crm"})
}

func (s *Service) CreateCompanyWithActor(ctx context.Context, workspaceID uuid.UUID, in CompanyInput, actor Actor) (Company, error) {
	if err := validateCompany(&in); err != nil {
		return Company{}, err
	}
	company, err := s.store.CreateCompany(ctx, workspaceID, in)
	if writeErr := translateWriteError(err); writeErr != nil {
		return Company{}, writeErr
	}
	if err := s.emit(ctx, workspaceID, EventInput{Name: "company.created", Kind: "create", ObjectType: "company",
		ObjectID: &company.ID, CompanyID: &company.ID, Actor: actor, LinkedRecordCachedName: company.Name}); err != nil {
		return Company{}, err
	}
	return company, nil
}

func (s *Service) UpdateCompany(ctx context.Context, workspaceID, id uuid.UUID, in CompanyInput) (Company, error) {
	return s.UpdateCompanyWithActor(ctx, workspaceID, id, in, Actor{Type: "system", ID: "crm"})
}

func (s *Service) UpdateCompanyWithActor(ctx context.Context, workspaceID, id uuid.UUID, in CompanyInput, actor Actor) (Company, error) {
	if err := validateCompany(&in); err != nil {
		return Company{}, err
	}
	company, err := s.store.UpdateCompany(ctx, workspaceID, id, in)
	if writeErr := translateWriteError(err); writeErr != nil {
		return Company{}, writeErr
	}
	if err := s.emit(ctx, workspaceID, EventInput{Name: "company.updated", Kind: "update", ObjectType: "company",
		ObjectID: &company.ID, CompanyID: &company.ID, Actor: actor, LinkedRecordCachedName: company.Name}); err != nil {
		return Company{}, err
	}
	return company, nil
}

// ListCompanyContacts and ListCompanyDeals resolve the company first, so an
// unknown or cross-workspace id is 404 rather than an empty page that reads like
// a company with no relations.
func (s *Service) ListCompanyContacts(ctx context.Context, workspaceID, companyID uuid.UUID, page PageRequest) (Page[CompanyContact], error) {
	if _, err := s.store.GetCompany(ctx, workspaceID, companyID); err != nil {
		return Page[CompanyContact]{}, err
	}
	return s.store.ListCompanyContacts(ctx, workspaceID, companyID, normalizePage(page))
}

func (s *Service) ListCompanyDeals(ctx context.Context, workspaceID, companyID uuid.UUID, page PageRequest) (Page[Deal], error) {
	if _, err := s.store.GetCompany(ctx, workspaceID, companyID); err != nil {
		return Page[Deal]{}, err
	}
	return s.store.ListCompanyDeals(ctx, workspaceID, companyID, normalizePage(page))
}

func (s *Service) DeleteCompany(ctx context.Context, workspaceID, id uuid.UUID) error {
	return translateDeleteError(s.store.DeleteCompany(ctx, workspaceID, id))
}

func (s *Service) ListPipelines(ctx context.Context, workspaceID uuid.UUID) ([]Pipeline, error) {
	return s.store.ListPipelines(ctx, workspaceID, maxPipelines)
}

func (s *Service) GetPipeline(ctx context.Context, workspaceID, id uuid.UUID) (Pipeline, error) {
	return s.store.GetPipeline(ctx, workspaceID, id)
}

func (s *Service) CreatePipeline(ctx context.Context, workspaceID uuid.UUID, in PipelineInput) (Pipeline, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 120 {
		return Pipeline{}, validation("pipeline name must be between 1 and 120 characters")
	}
	pipeline, err := s.store.CreatePipeline(ctx, workspaceID, in)
	return pipeline, translateWriteError(err)
}

func (s *Service) UpdatePipeline(ctx context.Context, workspaceID, id uuid.UUID, in PipelineInput) (Pipeline, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 120 {
		return Pipeline{}, validation("pipeline name must be between 1 and 120 characters")
	}
	pipeline, err := s.store.UpdatePipeline(ctx, workspaceID, id, in)
	return pipeline, translateWriteError(err)
}

// DeletePipeline distinguishes the two ways a delete can be refused: an
// unknown pipeline is 404, the default pipeline is 409 with text a caller can
// act on. Collapsing both into "not found" (which is what a guarded DELETE
// returning zero rows does) tells the caller their record vanished.
func (s *Service) DeletePipeline(ctx context.Context, workspaceID, id uuid.UUID) error {
	isDefault, err := s.store.PipelineIsDefault(ctx, workspaceID, id)
	if err != nil {
		return err
	}
	if isDefault {
		return conflict("the default pipeline cannot be deleted; make another pipeline the default first")
	}
	return translateDeleteError(s.store.DeletePipeline(ctx, workspaceID, id))
}

func (s *Service) CreateStage(ctx context.Context, workspaceID, pipelineID uuid.UUID, in StageInput) (Stage, error) {
	if err := validateStage(&in); err != nil {
		return Stage{}, err
	}
	key := strings.Trim(strings.ToLower(regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(in.Label, "_")), "_")
	if key == "" {
		return Stage{}, validation("stage label must contain a letter or number")
	}
	stage, err := s.store.CreateStage(ctx, workspaceID, pipelineID, key, in)
	return stage, translateWriteError(err)
}

func (s *Service) UpdateStage(ctx context.Context, workspaceID, pipelineID, id uuid.UUID, in StageInput) (Stage, error) {
	if err := validateStage(&in); err != nil {
		return Stage{}, err
	}
	stage, err := s.store.UpdateStage(ctx, workspaceID, pipelineID, id, in)
	return stage, translateWriteError(err)
}

func (s *Service) DeleteStage(ctx context.Context, workspaceID, pipelineID, id uuid.UUID) error {
	exists, err := s.store.StageExists(ctx, workspaceID, pipelineID, id)
	if err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	deals, err := s.store.CountStageDeals(ctx, workspaceID, id)
	if err != nil {
		return err
	}
	if deals > 0 {
		return fmt.Errorf("%w: stage still has %d deal(s); move them to another stage first", ErrConflict, deals)
	}
	return translateDeleteError(s.store.DeleteStage(ctx, workspaceID, pipelineID, id))
}

func (s *Service) ListDeals(ctx context.Context, workspaceID uuid.UUID, page PageRequest) (Page[Deal], error) {
	return s.store.ListDeals(ctx, workspaceID, normalizePage(page))
}

func (s *Service) GetDeal(ctx context.Context, workspaceID, id uuid.UUID) (Deal, error) {
	return s.store.GetDeal(ctx, workspaceID, id)
}

func (s *Service) GetBoard(ctx context.Context, workspaceID uuid.UUID, pipelineID *uuid.UUID) (Board, error) {
	store, err := integration(s.store)
	if err != nil {
		return Board{}, err
	}
	return store.GetBoard(ctx, workspaceID, pipelineID)
}

func (s *Service) MoveDeal(ctx context.Context, workspaceID, dealID uuid.UUID, in MoveDealInput) (Deal, error) {
	if in.StageID == uuid.Nil || in.Actor.Type == "" {
		return Deal{}, validation("stage and actor attribution are required")
	}
	if in.BeforeDealID != nil && in.AfterDealID != nil && *in.BeforeDealID == *in.AfterDealID {
		return Deal{}, validation("before and after deals must differ")
	}
	if (in.BeforeDealID != nil && *in.BeforeDealID == dealID) || (in.AfterDealID != nil && *in.AfterDealID == dealID) {
		return Deal{}, validation("a deal cannot be positioned relative to itself")
	}
	store, err := integration(s.store)
	if err != nil {
		return Deal{}, err
	}
	deal, err := store.MoveDeal(ctx, workspaceID, dealID, in)
	if err != nil {
		return deal, translateWriteError(err)
	}

	// The move is committed, so announce it. This is the event the spec singles
	// out (§6): crm/api.ts already applies an OPTIMISTIC patch when a card is
	// dragged, so the mover's own tab would fight the echo of its own action and
	// the card would visibly snap back and forth.
	//
	// The actor comes from in.Actor, which MoveDeal already requires for the
	// activity feed — no context lookup needed here, unlike campaign.Launch. A
	// "system" actor (an automation moving a deal) carries no user id, and every
	// tab then correctly treats the move as somebody else's.
	actorID := ""
	if in.Actor.Type == "user" {
		actorID = in.Actor.ID
	}
	realtimeevents.Emit(ctx, s.realtime, workspaceID.String(), realtimeevents.Event{
		Type:        "deal.moved",
		SubjectKind: "deal",
		SubjectID:   dealID.String(),
		ActorID:     actorID,
		OccurredAt:  time.Now().UTC(),
		// Ids only: which deal, which stage it landed in, and where in the order.
		// A client patches its cached board from this; anything richer it refetches
		// through the authorized endpoint.
		Data: map[string]any{
			"deal_id":  dealID.String(),
			"stage_id": in.StageID.String(),
		},
	})
	return deal, nil
}

func (s *Service) GetSettings(ctx context.Context, workspaceID uuid.UUID) (CRMSettings, error) {
	store, err := integration(s.store)
	if err != nil {
		return CRMSettings{}, err
	}
	return store.GetSettings(ctx, workspaceID)
}

func (s *Service) UpdateSettings(ctx context.Context, workspaceID uuid.UUID, policy string) (CRMSettings, error) {
	policy = strings.TrimSpace(policy)
	switch policy {
	case "sent_and_received", "sent", "off":
	default:
		return CRMSettings{}, validation("auto capture policy must be sent_and_received, sent, or off")
	}
	store, err := integration(s.store)
	if err != nil {
		return CRMSettings{}, err
	}
	return store.UpdateSettings(ctx, workspaceID, policy)
}

func (s *Service) CapturePositiveReply(ctx context.Context, workspaceID uuid.UUID, in CaptureReplyInput) (Deal, error) {
	if in.EnrollmentID == uuid.Nil || in.SendID == uuid.Nil || strings.TrimSpace(in.SenderEmail) == "" {
		return Deal{}, validation("enrollment, send, and sender are required")
	}
	captures, err := s.capturesDeal(ctx, workspaceID, in.ReplyClass)
	if err != nil {
		return Deal{}, err
	}
	if !captures {
		return Deal{}, validation("only a reply label that captures deals can be auto-captured")
	}
	store, err := integration(s.store)
	if err != nil {
		return Deal{}, err
	}
	return store.CapturePositiveReply(ctx, workspaceID, in)
}

// capturesDeal decides whether a reply classified as key may open a deal.
//
// The rule used to be the literal `key == "positive"`. It is now the label's
// captures_deal role, so a workspace can point auto-capture at a label of its
// own ("demo requested") — but the old rule remains the fallback for the two
// cases where the taxonomy cannot answer: no reader wired, and a key no label
// claims (a deleted custom label whose key survives on historical rows).
// Degrading to "positive still captures" keeps a pre-taxonomy deployment
// working; degrading to "nothing captures" would silently switch auto-capture
// off, which no operator asked for.
func (s *Service) capturesDeal(ctx context.Context, workspaceID uuid.UUID, key string) (bool, error) {
	if s.replyLabels == nil {
		return key == "positive", nil
	}
	captures, ok, err := s.replyLabels.CapturesDeal(ctx, workspaceID, key)
	if err != nil {
		return false, err
	}
	if !ok {
		return key == "positive", nil
	}
	return captures, nil
}

func (s *Service) ListDealThreads(ctx context.Context, workspaceID, dealID uuid.UUID) ([]Thread, error) {
	store, err := integration(s.store)
	if err != nil {
		return nil, err
	}
	return store.ListDealThreads(ctx, workspaceID, dealID)
}

func (s *Service) ListEvents(ctx context.Context, workspaceID uuid.UUID, target Target) ([]Event, error) {
	if err := validateTarget(target); err != nil {
		return nil, err
	}
	store, err := integration(s.store)
	if err != nil {
		return nil, err
	}
	events, err := store.ListEvents(ctx, workspaceID, target, maxPageLimit)
	if err != nil {
		return nil, err
	}
	return mergeEvents(events), nil
}

func (s *Service) CreateDeal(ctx context.Context, workspaceID uuid.UUID, in DealInput) (Deal, error) {
	if err := validateDeal(&in); err != nil {
		return Deal{}, err
	}
	deal, err := s.store.CreateDeal(ctx, workspaceID, in)
	if writeErr := translateWriteError(err); writeErr != nil {
		return Deal{}, writeErr
	}
	if err := s.emit(ctx, workspaceID, EventInput{Name: "deal.created", Kind: "create", ObjectType: "deal",
		ObjectID: &deal.ID, ContactID: deal.PrimaryContactID, CompanyID: deal.CompanyID, DealID: &deal.ID,
		Actor: in.Actor, LinkedRecordCachedName: deal.Name}); err != nil {
		return Deal{}, err
	}
	return deal, nil
}

func (s *Service) UpdateDeal(ctx context.Context, workspaceID, id uuid.UUID, in DealInput) (Deal, error) {
	if err := validateDeal(&in); err != nil {
		return Deal{}, err
	}
	before, err := s.store.GetDeal(ctx, workspaceID, id)
	if err != nil {
		return Deal{}, err
	}
	deal, err := s.store.UpdateDeal(ctx, workspaceID, id, in)
	if writeErr := translateWriteError(err); writeErr != nil {
		return Deal{}, writeErr
	}
	name, kind := "deal.updated", "update"
	data := json.RawMessage(`{}`)
	if before.StageID != deal.StageID {
		name, kind = "deal.stage_changed", "stage_change"
		data, _ = json.Marshal(map[string]any{"from_stage_id": before.StageID, "to_stage_id": deal.StageID})
	}
	if err := s.emit(ctx, workspaceID, EventInput{Name: name, Kind: kind, ObjectType: "deal", ObjectID: &deal.ID,
		ContactID: deal.PrimaryContactID, CompanyID: deal.CompanyID, DealID: &deal.ID, Actor: in.Actor,
		Data: data, LinkedRecordCachedName: deal.Name}); err != nil {
		return Deal{}, err
	}
	return deal, nil
}

func (s *Service) DeleteDeal(ctx context.Context, workspaceID, id uuid.UUID) error {
	return translateDeleteError(s.store.DeleteDeal(ctx, workspaceID, id))
}

func (s *Service) ListNotes(ctx context.Context, workspaceID uuid.UUID, target Target, page PageRequest) (Page[Note], error) {
	if err := validateTarget(target); err != nil {
		return Page[Note]{}, err
	}
	return s.store.ListNotes(ctx, workspaceID, target, normalizePage(page))
}

func (s *Service) CreateNote(ctx context.Context, workspaceID uuid.UUID, in NoteInput) (Note, error) {
	in.Title, in.Body = strings.TrimSpace(in.Title), strings.TrimSpace(in.Body)
	if err := validateTarget(in.Target); err != nil {
		return Note{}, err
	}
	if in.Body == "" || len(in.Body) > 20_000 || len(in.Title) > 200 {
		return Note{}, validation("note body is required; title is limited to 200 and body to 20000 characters")
	}
	note, err := s.store.CreateNote(ctx, workspaceID, in)
	if writeErr := translateWriteError(err); writeErr != nil {
		return Note{}, writeErr
	}
	event := eventForTarget(in.Target, "note.created", "note", note.ID, in.Actor, note.Title)
	if err := s.emit(ctx, workspaceID, event); err != nil {
		return Note{}, err
	}
	return note, nil
}

func (s *Service) UpdateNote(ctx context.Context, workspaceID, id uuid.UUID, title, body string) (Note, error) {
	title, body = strings.TrimSpace(title), strings.TrimSpace(body)
	if body == "" || len(body) > 20_000 || len(title) > 200 {
		return Note{}, validation("note body is required; title is limited to 200 and body to 20000 characters")
	}
	note, err := s.store.UpdateNote(ctx, workspaceID, id, title, body)
	return note, translateWriteError(err)
}

func (s *Service) DeleteNote(ctx context.Context, workspaceID, id uuid.UUID) error {
	return translateDeleteError(s.store.DeleteNote(ctx, workspaceID, id))
}

func (s *Service) ListTasks(ctx context.Context, workspaceID uuid.UUID, target Target, page PageRequest) (Page[Task], error) {
	if err := validateTarget(target); err != nil {
		return Page[Task]{}, err
	}
	return s.store.ListTasks(ctx, workspaceID, target, normalizePage(page))
}

func (s *Service) CreateTask(ctx context.Context, workspaceID uuid.UUID, in TaskInput) (Task, error) {
	if err := validateTask(&in); err != nil {
		return Task{}, err
	}
	task, err := s.store.CreateTask(ctx, workspaceID, in)
	if writeErr := translateWriteError(err); writeErr != nil {
		return Task{}, writeErr
	}
	event := eventForTarget(in.Target, "task.created", "task", task.ID, in.Actor, task.Title)
	if err := s.emit(ctx, workspaceID, event); err != nil {
		return Task{}, err
	}
	return task, nil
}

func (s *Service) UpdateTask(ctx context.Context, workspaceID, id uuid.UUID, in TaskInput) (Task, error) {
	if err := validateTask(&in); err != nil {
		return Task{}, err
	}
	task, err := s.store.UpdateTask(ctx, workspaceID, id, in)
	return task, translateWriteError(err)
}

func (s *Service) DeleteTask(ctx context.Context, workspaceID, id uuid.UUID) error {
	return translateDeleteError(s.store.DeleteTask(ctx, workspaceID, id))
}

func (s *Service) ListContactEmails(ctx context.Context, workspaceID, contactID uuid.UUID) ([]ContactEmail, error) {
	return s.store.ListContactEmails(ctx, workspaceID, contactID)
}

func (s *Service) AddContactEmail(ctx context.Context, workspaceID, contactID uuid.UUID, email string) (ContactEmail, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email {
		return ContactEmail{}, validation("email must be a single valid address")
	}
	row, err := s.store.AddContactEmail(ctx, workspaceID, contactID, email)
	return row, translateWriteError(err)
}

func (s *Service) SetPrimaryContactEmail(ctx context.Context, workspaceID, contactID, emailID uuid.UUID) error {
	return translateWriteError(s.store.SetPrimaryContactEmail(ctx, workspaceID, contactID, emailID))
}

func validateCompany(in *CompanyInput) error {
	in.Name, in.Domain, in.Currency = strings.TrimSpace(in.Name), strings.ToLower(strings.TrimSpace(in.Domain)), strings.ToUpper(strings.TrimSpace(in.Currency))
	if in.Currency == "" {
		in.Currency = "USD"
	}
	if in.Name == "" || len(in.Name) > 200 {
		return validation("company name must be between 1 and 200 characters")
	}
	if !currencyPattern.MatchString(in.Currency) {
		return validation("currency must be a three-letter ISO code")
	}
	if in.AnnualRevenueMicros != nil && *in.AnnualRevenueMicros < 0 {
		return validation("annual revenue cannot be negative")
	}
	if in.Domain != "" {
		u, err := url.Parse("https://" + in.Domain)
		if err != nil || u.Hostname() == "" || u.Host != in.Domain {
			return validation("domain must be a hostname such as example.com")
		}
	}
	return nil
}

func validateStage(in *StageInput) error {
	in.Label, in.Color = strings.TrimSpace(in.Label), strings.TrimSpace(in.Color)
	if in.Label == "" || len(in.Label) > 80 {
		return validation("stage label must be between 1 and 80 characters")
	}
	if !colorPattern.MatchString(in.Color) {
		return validation("stage color must be a six-digit hex color")
	}
	if in.Position < 0 || (in.IsWon && in.IsLost) {
		return validation("stage position cannot be negative and a stage cannot be both won and lost")
	}
	return nil
}

func validateDeal(in *DealInput) error {
	in.Name, in.Currency, in.Source = strings.TrimSpace(in.Name), strings.ToUpper(strings.TrimSpace(in.Currency)), strings.TrimSpace(in.Source)
	if in.Currency == "" {
		in.Currency = "USD"
	}
	if in.Source == "" {
		in.Source = "manual"
	}
	if in.PipelineID == uuid.Nil || in.StageID == uuid.Nil || in.Name == "" || len(in.Name) > 200 {
		return validation("pipeline, stage, and a deal name of at most 200 characters are required")
	}
	if !currencyPattern.MatchString(in.Currency) {
		return validation("currency must be a three-letter ISO code")
	}
	if in.AmountMicros != nil && *in.AmountMicros < 0 {
		return validation("deal amount cannot be negative")
	}
	if in.Actor.Type == "" {
		return validation("deal actor attribution is required")
	}
	return nil
}

func validateTask(in *TaskInput) error {
	in.Title, in.Body, in.Status = strings.TrimSpace(in.Title), strings.TrimSpace(in.Body), strings.TrimSpace(in.Status)
	if err := validateTarget(in.Target); err != nil {
		return err
	}
	if in.Title == "" || len(in.Title) > 200 || len(in.Body) > 20_000 {
		return validation("task title is required and limited to 200 characters; body is limited to 20000")
	}
	switch in.Status {
	case "":
		in.Status = TaskOpen
	case TaskOpen, TaskInProgress, TaskDone, TaskCancelled:
	default:
		return validation("invalid task status")
	}
	return nil
}

func validateTarget(target Target) error {
	if target.ID == uuid.Nil {
		return validation("target id is required")
	}
	switch target.Type {
	case TargetContact, TargetCompany, TargetDeal:
		return nil
	default:
		return validation("target type must be contact, company, or deal")
	}
}

func validation(message string) error { return fmt.Errorf("%w: %s", ErrValidation, message) }
func conflict(message string) error   { return fmt.Errorf("%w: %s", ErrConflict, message) }

func (s *Service) emit(ctx context.Context, workspaceID uuid.UUID, in EventInput) error {
	store, ok := s.store.(integrationStore)
	if !ok {
		return nil
	}
	return store.AppendEvent(ctx, workspaceID, in)
}

func eventForTarget(target Target, name, kind string, objectID uuid.UUID, actor Actor, cachedName string) EventInput {
	event := EventInput{Name: name, Kind: kind, ObjectType: kind, ObjectID: &objectID, Actor: actor,
		LinkedRecordCachedName: cachedName}
	switch target.Type {
	case TargetContact:
		event.ContactID = &target.ID
	case TargetCompany:
		event.CompanyID = &target.ID
	case TargetDeal:
		event.DealID = &target.ID
	}
	return event
}

func translateWriteError(err error) error {
	if err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrValidation) {
		return err
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.Code {
	case "23505":
		return fmt.Errorf("%w: a record with these values already exists", ErrConflict)
	case "23503":
		return fmt.Errorf("%w: a referenced record does not exist in this workspace", ErrValidation)
	case "23514", "23502":
		return fmt.Errorf("%w: request violates a CRM data constraint", ErrValidation)
	default:
		return err
	}
}

func translateDeleteError(err error) error {
	if err == nil || errors.Is(err, ErrNotFound) {
		return err
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return fmt.Errorf("%w: record is still referenced", ErrConflict)
	}
	return translateWriteError(err)
}
