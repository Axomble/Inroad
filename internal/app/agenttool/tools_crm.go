package agenttool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
	ID           uuid.UUID `json:"id"`
	Name         string    `json:"name"`
	PipelineName string    `json:"pipeline_name"`
	StageLabel   string    `json:"stage_label"`
	CompanyName  string    `json:"company_name,omitempty"`
	Currency     string    `json:"currency"`
	AmountMicros *int64    `json:"amount_micros,omitempty"`
}
type CRMActor struct {
	UserID        uuid.UUID
	AgentClientID string
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
	Name, Kind, LinkedRecordCachedName string
	OccurredAt                         string
	MergedCount                        int
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
	ListPipelines(context.Context, uuid.UUID) ([]CRMPipeline, error)
	ListDeals(context.Context, uuid.UUID) (CRMList[CRMDeal], error)
	CreateCompany(context.Context, uuid.UUID, CRMCompanyInput) (CRMCompany, error)
	CreateDeal(context.Context, uuid.UUID, CRMDealInput) (CRMDeal, error)
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
}

func crmTools(deps Deps) []Tool {
	if deps.CRM == nil {
		return nil
	}
	errs := deps.CRMErrors
	tools := []Tool{
		crmReadTool(deps.CRM), crmWriteTool(deps.CRM, errs),
		crmCollectionReadTool("inroad_company_read", "Read CRM companies in this workspace.", "companies", deps.CRM),
		crmCompanyWriteTool(deps.CRM, errs),
		crmCollectionReadTool("inroad_deal_read", "Read CRM deals in this workspace.", "deals", deps.CRM),
		crmDealWriteTool(deps.CRM, errs),
		crmCollectionReadTool("inroad_pipeline_read", "Read CRM pipelines and their ordered stages.", "pipelines", deps.CRM),
	}
	if integrated, ok := deps.CRM.(CRMIntegrationService); ok {
		tools = append(tools, crmNoteWriteTool(integrated, errs), crmTaskWriteTool(integrated, errs), crmEventsReadTool(integrated))
	}
	return tools
}

func crmCollectionReadTool(name, description, collection string, svc CRMService) Tool {
	return Tool{Name: name, Description: description, InputSchema: mustSchema(), Risk: RiskRead,
		Execute: func(ctx context.Context, p Principal, raw json.RawMessage) (Result, error) {
			var args baseArgs
			if bad := decodeArgs(raw, &args); bad != nil {
				return *bad, nil
			}
			switch collection {
			case "companies":
				items, err := svc.ListCompanies(ctx, p.WorkspaceID)
				if err != nil {
					return Result{}, fmt.Errorf("list CRM companies: %w", err)
				}
				return Ok(items), nil
			case "deals":
				items, err := svc.ListDeals(ctx, p.WorkspaceID)
				if err != nil {
					return Result{}, fmt.Errorf("list CRM deals: %w", err)
				}
				return Ok(items), nil
			default:
				items, err := svc.ListPipelines(ctx, p.WorkspaceID)
				if err != nil {
					return Result{}, fmt.Errorf("list CRM pipelines: %w", err)
				}
				return Ok(items), nil
			}
		}}
}

func crmCompanyWriteTool(svc CRMService, errs ErrorClassifier) Tool {
	return Tool{Name: "inroad_company_write", Description: "Create a company in this workspace.",
		InputSchema: mustSchema(strField("name", "Company name.", true), strField("domain", "Company hostname such as example.com.", false),
			strField("currency", "Three-letter currency code; defaults to USD.", false)), Risk: RiskWrite,
		Execute: func(ctx context.Context, p Principal, raw json.RawMessage) (Result, error) {
			var args struct {
				baseArgs
				Name, Domain, Currency string
			}
			if bad := decodeArgs(raw, &args); bad != nil {
				return *bad, nil
			}
			if strings.TrimSpace(args.Name) == "" {
				return Fail("name is required; call again with the company name"), nil
			}
			item, err := svc.CreateCompany(ctx, p.WorkspaceID, CRMCompanyInput{Name: args.Name, Domain: args.Domain,
				Currency: args.Currency, Actor: CRMActor{UserID: p.UserID, AgentClientID: p.AgentClientID}})
			if err != nil {
				return recoverableCRMError(errs, err)
			}
			return Ok(item), nil
		}}
}

func crmDealWriteTool(svc CRMService, errs ErrorClassifier) Tool {
	amount := field{name: "amount_micros", schema: jsonObject{{"type", "integer"}, {"minimum", 0}}}
	return Tool{Name: "inroad_deal_write", Description: "Create a deal. Read pipelines first to obtain tenant-safe pipeline and stage ids.",
		InputSchema: mustSchema(strField("name", "Deal name.", true), strField("pipeline_id", "Pipeline id.", true),
			strField("stage_id", "Stage id belonging to the pipeline.", true), strField("company_id", "Optional company id.", false),
			amount, strField("currency", "Three-letter currency code; defaults to USD.", false)), Risk: RiskWrite,
		Execute: func(ctx context.Context, p Principal, raw json.RawMessage) (Result, error) {
			var args crmWriteArgs
			if bad := decodeArgs(raw, &args); bad != nil {
				return *bad, nil
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
			item, err := svc.CreateDeal(ctx, p.WorkspaceID, CRMDealInput{Name: args.Name, PipelineID: pipelineID,
				StageID: stageID, CompanyID: companyID, AmountMicros: args.AmountMicros, Currency: args.Currency,
				Actor: CRMActor{UserID: p.UserID, AgentClientID: p.AgentClientID}})
			if err != nil {
				return recoverableCRMError(errs, err)
			}
			return Ok(item), nil
		}}
}

func crmNoteWriteTool(svc CRMIntegrationService, errs ErrorClassifier) Tool {
	return crmTargetWriteTool("inroad_note_write", "Create a note attached to a contact, company, or deal.", "body", errs, func(ctx context.Context, p Principal, title, body string, target CRMTarget) error {
		return svc.CreateNote(ctx, p.WorkspaceID, CRMNoteInput{Title: title, Body: body, Target: target,
			Actor: CRMActor{UserID: p.UserID, AgentClientID: p.AgentClientID}})
	})
}

func crmTaskWriteTool(svc CRMIntegrationService, errs ErrorClassifier) Tool {
	return crmTargetWriteTool("inroad_task_write", "Create a follow-up task attached to a contact, company, or deal.", "title", errs, func(ctx context.Context, p Principal, title, body string, target CRMTarget) error {
		return svc.CreateTask(ctx, p.WorkspaceID, CRMTaskInput{Title: title, Body: body, Target: target,
			Actor: CRMActor{UserID: p.UserID, AgentClientID: p.AgentClientID}})
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

func crmReadTool(svc CRMService) Tool {
	methods := []string{"companies", "pipelines", "deals"}
	return Tool{Name: "inroad_crm_read", Description: "Read CRM companies, pipelines with stages, or deals in this workspace. Read pipelines before creating a deal so you can supply valid pipeline_id and stage_id values.", InputSchema: mustSchema(methodField("The CRM collection to read.", methods)), Risk: RiskRead, Execute: func(ctx context.Context, p Principal, raw json.RawMessage) (Result, error) {
		var args struct {
			baseArgs
			Method string `json:"method"`
		}
		if bad := decodeArgs(raw, &args); bad != nil {
			return *bad, nil
		}
		switch args.Method {
		case "companies":
			items, err := svc.ListCompanies(ctx, p.WorkspaceID)
			if err != nil {
				return Result{}, fmt.Errorf("list CRM companies: %w", err)
			}
			return Ok(items), nil
		case "pipelines":
			items, err := svc.ListPipelines(ctx, p.WorkspaceID)
			if err != nil {
				return Result{}, fmt.Errorf("list CRM pipelines: %w", err)
			}
			return Ok(items), nil
		case "deals":
			items, err := svc.ListDeals(ctx, p.WorkspaceID)
			if err != nil {
				return Result{}, fmt.Errorf("list CRM deals: %w", err)
			}
			return Ok(items), nil
		default:
			return unknownMethod(args.Method, methods), nil
		}
	}}
}

func crmWriteTool(svc CRMService, errs ErrorClassifier) Tool {
	methods := []string{"create_company", "create_deal"}
	amount := field{name: "amount_micros", schema: jsonObject{{"type", "integer"}, {"description", "Optional monetary amount in millionths of the currency unit; zero or greater."}, {"minimum", 0}}}
	return Tool{Name: "inroad_crm_write", Description: "Create a CRM company or deal. For a deal, first call inroad_crm_read with method=pipelines and method=companies to obtain tenant-safe IDs.", InputSchema: mustSchema(methodField("The CRM mutation to perform.", methods), strField("name", "Company or deal name.", true), strField("domain", "Company hostname such as example.com. Only for create_company.", false), strField("pipeline_id", "Pipeline id from inroad_crm_read. Required for create_deal.", false), strField("stage_id", "Stage id belonging to pipeline_id. Required for create_deal.", false), strField("company_id", "Optional company id from inroad_crm_read.", false), amount, strField("currency", "Three-letter currency code; defaults to USD.", false)), Risk: RiskWrite, Execute: func(ctx context.Context, p Principal, raw json.RawMessage) (Result, error) {
		var args crmWriteArgs
		if bad := decodeArgs(raw, &args); bad != nil {
			return *bad, nil
		}
		args.Name = strings.TrimSpace(args.Name)
		if args.Name == "" {
			return Fail("name is required; call again with the company or deal name"), nil
		}
		switch args.Method {
		case "create_company":
			item, err := svc.CreateCompany(ctx, p.WorkspaceID, CRMCompanyInput{Name: args.Name, Domain: args.Domain,
				Currency: args.Currency, Actor: CRMActor{UserID: p.UserID, AgentClientID: p.AgentClientID}})
			if err != nil {
				return recoverableCRMError(errs, err)
			}
			return Ok(item), nil
		case "create_deal":
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
				id, bad := parseID("company_id", args.CompanyID)
				if bad != nil {
					return *bad, nil
				}
				companyID = &id
			}
			item, err := svc.CreateDeal(ctx, p.WorkspaceID, CRMDealInput{Name: args.Name, PipelineID: pipelineID, StageID: stageID, CompanyID: companyID, AmountMicros: args.AmountMicros, Currency: args.Currency, Actor: CRMActor{UserID: p.UserID, AgentClientID: p.AgentClientID}})
			if err != nil {
				return recoverableCRMError(errs, err)
			}
			return Ok(item), nil
		default:
			return unknownMethod(args.Method, methods), nil
		}
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
