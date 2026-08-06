package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type CRMCompany struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Domain    string    `json:"domain,omitempty"`
	Currency  string    `json:"currency"`
	DealCount int64     `json:"deal_count"`
}
type CRMStage struct {
	ID    uuid.UUID `json:"id"`
	Label string    `json:"label"`
}
type CRMPipeline struct {
	ID     uuid.UUID  `json:"id"`
	Name   string     `json:"name"`
	Stages []CRMStage `json:"stages"`
}
type CRMDeal struct {
	ID           uuid.UUID  `json:"id"`
	PipelineID   uuid.UUID  `json:"pipeline_id"`
	StageID      uuid.UUID  `json:"stage_id"`
	CompanyID    *uuid.UUID `json:"company_id,omitempty"`
	Name         string     `json:"name"`
	PipelineName string     `json:"pipeline_name"`
	StageLabel   string     `json:"stage_label"`
	CompanyName  string     `json:"company_name,omitempty"`
	Currency     string     `json:"currency"`
	AmountMicros *int64     `json:"amount_micros,omitempty"`
}
type CRMActor struct {
	UserID        uuid.UUID
	AgentClientID string
	ThreadID      string
	RunID         string
}
type CRMCompanyInput struct {
	Name, Domain, Currency string
	Actor                  CRMActor
}
type CRMDealInput struct {
	Name                string
	PipelineID, StageID uuid.UUID
	CompanyID           *uuid.UUID
	AmountMicros        *int64
	Currency            string
	Actor               CRMActor
}
type CRMTarget struct {
	Type string
	ID   uuid.UUID
}
type CRMNoteInput struct {
	Title, Body string
	Target      CRMTarget
	Actor       CRMActor
}
type CRMTaskInput struct {
	Title, Body string
	Target      CRMTarget
	Actor       CRMActor
}
type CRMEvent struct {
	ID, Name, Kind, LinkedRecordCachedName string
	OccurredAt                             string
	MergedCount                            int
	Actor, Data                            json.RawMessage
	SourceMessageID, SourceThreadRef       string
}

type CRMBoardStage struct {
	Stage        CRMStage  `json:"stage"`
	Deals        []CRMDeal `json:"deals"`
	DealCount    int64     `json:"deal_count"`
	AmountMicros int64     `json:"amount_micros"`
}
type CRMBoard struct {
	Pipeline CRMPipeline     `json:"pipeline"`
	Stages   []CRMBoardStage `json:"stages"`
}
type CRMThread struct {
	ID            uuid.UUID              `json:"id"`
	ThreadRef     string                 `json:"thread_ref"`
	Subject       string                 `json:"subject,omitempty"`
	ReplyClass    string                 `json:"reply_class,omitempty"`
	LastMessageAt string                 `json:"last_message_at"`
	Participants  []CRMThreadParticipant `json:"participants"`
	Messages      []CRMThreadMessage     `json:"messages"`
}
type CRMThreadParticipant struct {
	Email       string     `json:"email"`
	DisplayName string     `json:"display_name,omitempty"`
	ContactID   *uuid.UUID `json:"contact_id,omitempty"`
}
type CRMThreadMessage struct {
	ID             uuid.UUID `json:"id"`
	Direction      string    `json:"direction"`
	Kind           string    `json:"kind"`
	MessageID      string    `json:"message_id,omitempty"`
	SenderEmail    string    `json:"sender_email,omitempty"`
	RecipientEmail string    `json:"recipient_email,omitempty"`
	Subject        string    `json:"subject,omitempty"`
	ReplyClass     string    `json:"reply_class,omitempty"`
	OccurredAt     string    `json:"occurred_at"`
}
type CRMDealMoveInput struct {
	DealID, StageID           uuid.UUID
	BeforeDealID, AfterDealID *uuid.UUID
	Actor                     CRMActor
}

type crmWriteArgs struct {
	baseArgs
	Method       string `json:"method"`
	Name         string `json:"name"`
	Domain       string `json:"domain"`
	PipelineID   string `json:"pipeline_id"`
	StageID      string `json:"stage_id"`
	CompanyID    string `json:"company_id"`
	Currency     string `json:"currency"`
	AmountMicros *int64 `json:"amount_micros"`
}

// CRMList is one page of a CRM listing as the model sees it. Truncated is not
// cosmetic: a model that silently reasons over the first page of a longer list
// draws confidently wrong conclusions ("this workspace has 50 deals"), so the
// limit is always stated in the payload.
type CRMList[T any] struct {
	Items     []T    `json:"items"`
	Truncated bool   `json:"truncated"`
	Note      string `json:"note,omitempty"`
}

// NewCRMList builds a list payload, attaching the truncation note in one place
// so every CRM listing reports it identically.
func NewCRMList[T any](items []T, truncated bool) CRMList[T] {
	out := CRMList[T]{Items: items, Truncated: truncated}
	if truncated {
		out.Note = "more records exist than are shown; this is one page, not the whole workspace"
	}
	return out
}

type CRMService interface {
	ListCompanies(context.Context, uuid.UUID) (CRMList[CRMCompany], error)
	GetCompany(context.Context, uuid.UUID, uuid.UUID) (CRMCompany, error)
	ListPipelines(context.Context, uuid.UUID) ([]CRMPipeline, error)
	ListDeals(context.Context, uuid.UUID) (CRMList[CRMDeal], error)
	GetDeal(context.Context, uuid.UUID, uuid.UUID) (CRMDeal, error)
	GetBoard(context.Context, uuid.UUID, *uuid.UUID) (CRMBoard, error)
	CreateCompany(context.Context, uuid.UUID, CRMCompanyInput) (CRMCompany, error)
	UpdateCompany(context.Context, uuid.UUID, uuid.UUID, CRMCompanyInput) (CRMCompany, error)
	CreateDeal(context.Context, uuid.UUID, CRMDealInput) (CRMDeal, error)
	UpdateDeal(context.Context, uuid.UUID, uuid.UUID, CRMDealInput) (CRMDeal, error)
	MoveDeal(context.Context, uuid.UUID, CRMDealMoveInput) (CRMDeal, error)
}

// ErrorClassifier decides what a domain write failure looks like to the model.
// It is declared here and implemented by the composition root so agenttool
// never imports a domain package to inspect its sentinels — and so raw driver
// text, which leaks table and column names, can never be forwarded to a model.
type ErrorClassifier interface {
	// Recoverable returns model-safe recovery guidance and true when the
	// caller can fix the call itself; false means an infrastructure fault that
	// must abort the run.
	Recoverable(err error) (string, bool)
}

type CRMIntegrationService interface {
	CRMService
	CreateNote(context.Context, uuid.UUID, CRMNoteInput) error
	CreateTask(context.Context, uuid.UUID, CRMTaskInput) error
	ListEvents(context.Context, uuid.UUID, CRMTarget) ([]CRMEvent, error)
	ListDealThreads(context.Context, uuid.UUID, uuid.UUID) ([]CRMThread, error)
}

func crmTools(deps Deps) []Tool {
	if deps.CRM == nil {
		return nil
	}
	errs := deps.CRMErrors
	tools := []Tool{
		crmCompanyReadTool(deps.CRM),
		crmCompanyWriteTool(deps.CRM, errs),
		crmDealReadTool(deps.CRM),
		crmDealWriteTool(deps.CRM, errs),
		crmPipelineReadTool(deps.CRM),
	}
	if integrated, ok := deps.CRM.(CRMIntegrationService); ok {
		tools = append(tools, crmNoteWriteTool(integrated, errs), crmTaskWriteTool(integrated, errs), crmEventsReadTool(integrated), crmThreadReadTool(integrated))
	}
	for i := range tools {
		if tools[i].Risk == RiskWrite {
			tools[i] = withCRMWriteLimit(tools[i], deps.CRMWriteLimiter)
		}
	}
	return tools
}

const crmWritesPerMinute = 30

func withCRMWriteLimit(tool Tool, limiter RateLimiter) Tool {
	if limiter == nil {
		return tool
	}
	execute := tool.Execute
	tool.Execute = func(ctx context.Context, p Principal, raw json.RawMessage) (Result, error) {
		clientID := p.AgentClientID
		if clientID == "" {
			clientID = p.UserID.String()
		}
		key := fmt.Sprintf("agent-crm-write:%s:%s", p.WorkspaceID, clientID)
		allowed, err := limiter.Allow(ctx, key, crmWritesPerMinute, time.Minute)
		if err != nil {
			return Result{}, fmt.Errorf("limit CRM agent writes: %w", err)
		}
		if !allowed {
			return Fail("CRM write rate limit reached; wait briefly before trying again"), nil
		}
		return execute(ctx, p, raw)
	}
	return tool
}

func crmActor(p Principal) CRMActor {
	return CRMActor{UserID: p.UserID, AgentClientID: p.AgentClientID, ThreadID: p.ThreadID, RunID: p.RunID}
}

func crmCompanyReadTool(svc CRMService) Tool {
	methods := []string{"list", "get"}
	return Tool{Name: "inroad_company_read", Description: "List CRM companies or get one company by id.",
		InputSchema: mustSchema(methodField("Company read operation.", methods), strField("company_id", "Company id required for get.", false)), Risk: RiskRead,
		Execute: func(ctx context.Context, p Principal, raw json.RawMessage) (Result, error) {
			var args struct {
				baseArgs
				Method    string `json:"method"`
				CompanyID string `json:"company_id"`
			}
			if bad := decodeArgs(raw, &args); bad != nil {
				return *bad, nil
			}
			switch args.Method {
			case "list":
				items, err := svc.ListCompanies(ctx, p.WorkspaceID)
				if err != nil {
					return Result{}, fmt.Errorf("list CRM companies: %w", err)
				}
				return Ok(items), nil
			case "get":
				id, bad := parseID("company_id", args.CompanyID)
				if bad != nil {
					return *bad, nil
				}
				item, err := svc.GetCompany(ctx, p.WorkspaceID, id)
				if err != nil {
					return Result{}, fmt.Errorf("get CRM company: %w", err)
				}
				return Ok(item), nil
			default:
				return unknownMethod(args.Method, methods), nil
			}
		}}
}

func crmPipelineReadTool(svc CRMService) Tool {
	return Tool{Name: "inroad_pipeline_read", Description: "Read CRM pipelines and their ordered stages.", InputSchema: mustSchema(), Risk: RiskRead,
		Execute: func(ctx context.Context, p Principal, raw json.RawMessage) (Result, error) {
			var args baseArgs
			if bad := decodeArgs(raw, &args); bad != nil {
				return *bad, nil
			}
			items, err := svc.ListPipelines(ctx, p.WorkspaceID)
			if err != nil {
				return Result{}, fmt.Errorf("list CRM pipelines: %w", err)
			}
			return Ok(items), nil
		}}
}

func crmDealReadTool(svc CRMService) Tool {
	methods := []string{"list", "get", "board"}
	return Tool{Name: "inroad_deal_read", Description: "List deals, get one deal, or read a pipeline board with stage totals.",
		InputSchema: mustSchema(methodField("Deal read operation.", methods), strField("deal_id", "Deal id required for get.", false), strField("pipeline_id", "Optional pipeline id for board.", false)), Risk: RiskRead,
		Execute: func(ctx context.Context, p Principal, raw json.RawMessage) (Result, error) {
			var args struct {
				baseArgs
				Method     string `json:"method"`
				DealID     string `json:"deal_id"`
				PipelineID string `json:"pipeline_id"`
			}
			if bad := decodeArgs(raw, &args); bad != nil {
				return *bad, nil
			}
			switch args.Method {
			case "list":
				items, err := svc.ListDeals(ctx, p.WorkspaceID)
				if err != nil {
					return Result{}, fmt.Errorf("list CRM deals: %w", err)
				}
				return Ok(items), nil
			case "get":
				id, bad := parseID("deal_id", args.DealID)
				if bad != nil {
					return *bad, nil
				}
				item, err := svc.GetDeal(ctx, p.WorkspaceID, id)
				if err != nil {
					return Result{}, fmt.Errorf("get CRM deal: %w", err)
				}
				return Ok(item), nil
			case "board":
				var pipelineID *uuid.UUID
				if args.PipelineID != "" {
					id, bad := parseID("pipeline_id", args.PipelineID)
					if bad != nil {
						return *bad, nil
					}
					pipelineID = &id
				}
				board, err := svc.GetBoard(ctx, p.WorkspaceID, pipelineID)
				if err != nil {
					return Result{}, fmt.Errorf("get CRM board: %w", err)
				}
				return Ok(board), nil
			default:
				return unknownMethod(args.Method, methods), nil
			}
		}}
}

func crmCompanyWriteTool(svc CRMService, errs ErrorClassifier) Tool {
	methods := []string{"create", "update"}
	return Tool{Name: "inroad_company_write", Description: "Create or update a company in this workspace.",
		InputSchema: mustSchema(methodField("Company write operation.", methods), strField("company_id", "Company id required for update.", false), strField("name", "Company name.", true), strField("domain", "Company hostname such as example.com.", false),
			strField("currency", "Three-letter currency code; defaults to USD.", false)), Risk: RiskWrite,
		Execute: func(ctx context.Context, p Principal, raw json.RawMessage) (Result, error) {
			var args struct {
				baseArgs
				Method    string `json:"method"`
				CompanyID string `json:"company_id"`
				Name      string `json:"name"`
				Domain    string `json:"domain"`
				Currency  string `json:"currency"`
			}
			if bad := decodeArgs(raw, &args); bad != nil {
				return *bad, nil
			}
			if strings.TrimSpace(args.Name) == "" {
				return Fail("name is required; call again with the company name"), nil
			}
			input := CRMCompanyInput{Name: args.Name, Domain: args.Domain, Currency: args.Currency, Actor: crmActor(p)}
			var item CRMCompany
			var err error
			switch args.Method {
			case "create":
				item, err = svc.CreateCompany(ctx, p.WorkspaceID, input)
			case "update":
				id, bad := parseID("company_id", args.CompanyID)
				if bad != nil {
					return *bad, nil
				}
				item, err = svc.UpdateCompany(ctx, p.WorkspaceID, id, input)
			default:
				return unknownMethod(args.Method, methods), nil
			}
			if err != nil {
				return recoverableCRMError(errs, err)
			}
			return Ok(item), nil
		}}
}

func crmDealWriteTool(svc CRMService, errs ErrorClassifier) Tool {
	methods := []string{"create", "update", "move_stage"}
	amount := field{name: "amount_micros", schema: jsonObject{{"type", "integer"}, {"minimum", 0}}}
	return Tool{Name: "inroad_deal_write", Description: "Create, update, or move a deal. Read the deal and pipelines first to obtain current tenant-safe ids.",
		InputSchema: mustSchema(methodField("Deal write operation.", methods), strField("deal_id", "Deal id required for update or move_stage.", false), strField("name", "Deal name required for create or update.", false), strField("pipeline_id", "Pipeline id required for create or update.", false),
			strField("stage_id", "Destination stage id.", true), strField("company_id", "Optional company id.", false), strField("before_deal_id", "Optional neighbor placed after this deal.", false), strField("after_deal_id", "Optional neighbor placed before this deal.", false),
			amount, strField("currency", "Three-letter currency code; defaults to USD.", false)), Risk: RiskWrite,
		Execute: func(ctx context.Context, p Principal, raw json.RawMessage) (Result, error) {
			var args struct {
				crmWriteArgs
				DealID       string `json:"deal_id"`
				BeforeDealID string `json:"before_deal_id"`
				AfterDealID  string `json:"after_deal_id"`
			}
			if bad := decodeArgs(raw, &args); bad != nil {
				return *bad, nil
			}
			if args.Method == "move_stage" {
				dealID, bad := parseID("deal_id", args.DealID)
				if bad != nil {
					return *bad, nil
				}
				stageID, bad := parseID("stage_id", args.StageID)
				if bad != nil {
					return *bad, nil
				}
				beforeID, bad := optionalID("before_deal_id", args.BeforeDealID)
				if bad != nil {
					return *bad, nil
				}
				afterID, bad := optionalID("after_deal_id", args.AfterDealID)
				if bad != nil {
					return *bad, nil
				}
				item, err := svc.MoveDeal(ctx, p.WorkspaceID, CRMDealMoveInput{DealID: dealID, StageID: stageID, BeforeDealID: beforeID, AfterDealID: afterID, Actor: crmActor(p)})
				if err != nil {
					return recoverableCRMError(errs, err)
				}
				return Ok(item), nil
			}
			if args.Method != "create" && args.Method != "update" {
				return unknownMethod(args.Method, methods), nil
			}
			if strings.TrimSpace(args.Name) == "" {
				return Fail("name is required for create or update; read the deal and call again with its full values"), nil
			}
			pipelineID, bad := parseID("pipeline_id", args.PipelineID)
			if bad != nil {
				return *bad, nil
			}
			stageID, bad := parseID("stage_id", args.StageID)
			if bad != nil {
				return *bad, nil
			}
			var companyID *uuid.UUID
			if args.CompanyID != "" {
				id, invalid := parseID("company_id", args.CompanyID)
				if invalid != nil {
					return *invalid, nil
				}
				companyID = &id
			}
			input := CRMDealInput{Name: args.Name, PipelineID: pipelineID,
				StageID: stageID, CompanyID: companyID, AmountMicros: args.AmountMicros, Currency: args.Currency,
				Actor: crmActor(p)}
			var item CRMDeal
			var err error
			if args.Method == "create" {
				item, err = svc.CreateDeal(ctx, p.WorkspaceID, input)
			} else {
				dealID, invalid := parseID("deal_id", args.DealID)
				if invalid != nil {
					return *invalid, nil
				}
				item, err = svc.UpdateDeal(ctx, p.WorkspaceID, dealID, input)
			}
			if err != nil {
				return recoverableCRMError(errs, err)
			}
			return Ok(item), nil
		}}
}

func optionalID(name, raw string) (*uuid.UUID, *Result) {
	if raw == "" {
		return nil, nil
	}
	id, bad := parseID(name, raw)
	if bad != nil {
		return nil, bad
	}
	return &id, nil
}

func crmNoteWriteTool(svc CRMIntegrationService, errs ErrorClassifier) Tool {
	return crmTargetWriteTool("inroad_note_write", "Create a note attached to a contact, company, or deal.", "body", errs, func(ctx context.Context, p Principal, title, body string, target CRMTarget) error {
		return svc.CreateNote(ctx, p.WorkspaceID, CRMNoteInput{Title: title, Body: body, Target: target,
			Actor: crmActor(p)})
	})
}

func crmTaskWriteTool(svc CRMIntegrationService, errs ErrorClassifier) Tool {
	return crmTargetWriteTool("inroad_task_write", "Create a follow-up task attached to a contact, company, or deal.", "title", errs, func(ctx context.Context, p Principal, title, body string, target CRMTarget) error {
		return svc.CreateTask(ctx, p.WorkspaceID, CRMTaskInput{Title: title, Body: body, Target: target,
			Actor: crmActor(p)})
	})
}

func crmTargetWriteTool(name, description, requiredText string, errs ErrorClassifier, execute func(context.Context, Principal, string, string, CRMTarget) error) Tool {
	fields := []field{
		strField("target_type", "Target kind: contact, company, or deal.", true),
		strField("target_id", "Target record id.", true),
		strField("title", "Short title.", requiredText == "title"),
		strField("body", "Detail text.", requiredText == "body"),
	}
	return Tool{Name: name, Description: description, InputSchema: mustSchema(fields...), Risk: RiskWrite,
		Execute: func(ctx context.Context, p Principal, raw json.RawMessage) (Result, error) {
			var args struct {
				baseArgs
				TargetType string `json:"target_type"`
				TargetID   string `json:"target_id"`
				Title      string `json:"title"`
				Body       string `json:"body"`
			}
			if bad := decodeArgs(raw, &args); bad != nil {
				return *bad, nil
			}
			id, bad := parseID("target_id", args.TargetID)
			if bad != nil {
				return *bad, nil
			}
			if args.TargetType != "contact" && args.TargetType != "company" && args.TargetType != "deal" {
				return Fail("target_type must be contact, company, or deal"), nil
			}
			if err := execute(ctx, p, args.Title, args.Body, CRMTarget{Type: args.TargetType, ID: id}); err != nil {
				return recoverableCRMError(errs, err)
			}
			return Ok(map[string]bool{"created": true}), nil
		}}
}

func crmEventsReadTool(svc CRMIntegrationService) Tool {
	return Tool{Name: "inroad_events_read", Description: "Read the attributed CRM activity feed for a contact, company, or deal.",
		InputSchema: mustSchema(strField("target_type", "Target kind: contact, company, or deal.", true), strField("target_id", "Target record id.", true)),
		Risk:        RiskRead, Execute: func(ctx context.Context, p Principal, raw json.RawMessage) (Result, error) {
			var args struct {
				baseArgs
				TargetType string `json:"target_type"`
				TargetID   string `json:"target_id"`
			}
			if bad := decodeArgs(raw, &args); bad != nil {
				return *bad, nil
			}
			id, bad := parseID("target_id", args.TargetID)
			if bad != nil {
				return *bad, nil
			}
			items, err := svc.ListEvents(ctx, p.WorkspaceID, CRMTarget{Type: args.TargetType, ID: id})
			if err != nil {
				return Result{}, fmt.Errorf("list CRM events: %w", err)
			}
			return Ok(items), nil
		}}
}

func crmThreadReadTool(svc CRMIntegrationService) Tool {
	return Tool{Name: "inroad_thread_read", Description: "Read structured CRM thread metadata for a deal. Returns participants and message metadata, never raw message bodies.",
		InputSchema: mustSchema(strField("deal_id", "Deal id whose linked threads should be read.", true)), Risk: RiskRead,
		Execute: func(ctx context.Context, p Principal, raw json.RawMessage) (Result, error) {
			var args struct {
				baseArgs
				DealID string `json:"deal_id"`
			}
			if bad := decodeArgs(raw, &args); bad != nil {
				return *bad, nil
			}
			id, bad := parseID("deal_id", args.DealID)
			if bad != nil {
				return *bad, nil
			}
			items, err := svc.ListDealThreads(ctx, p.WorkspaceID, id)
			if err != nil {
				return Result{}, fmt.Errorf("list structured CRM threads: %w", err)
			}
			return Ok(items), nil
		}}
}

// recoverableCRMError turns a domain write failure into either a model-visible
// retry prompt or an aborting error. The decision is delegated to the injected
// classifier (sentinel matching in the composition root) rather than made here
// by matching substrings of an error message, which both misclassifies and
// forwards raw driver text to the model.
func recoverableCRMError(errs ErrorClassifier, err error) (Result, error) {
	if errs != nil {
		if message, ok := errs.Recoverable(err); ok {
			return Fail(message + "; correct the values and call again after reading the current CRM records"), nil
		}
	}
	return Result{}, fmt.Errorf("write CRM: %w", err)
}
