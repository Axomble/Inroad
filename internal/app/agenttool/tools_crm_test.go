package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeCRM struct {
	created   CRMDealInput
	truncated bool
	writeErr  error
}

func (f *fakeCRM) ListCompanies(context.Context, uuid.UUID) (CRMList[CRMCompany], error) {
	return NewCRMList([]CRMCompany{{ID: uuid.New(), Name: "Acme"}}, f.truncated), nil
}
func (f *fakeCRM) GetCompany(context.Context, uuid.UUID, uuid.UUID) (CRMCompany, error) {
	return CRMCompany{Name: "Acme"}, nil
}
func (f *fakeCRM) ListPipelines(context.Context, uuid.UUID) ([]CRMPipeline, error) { return nil, nil }
func (f *fakeCRM) ListDeals(context.Context, uuid.UUID) (CRMList[CRMDeal], error) {
	return NewCRMList([]CRMDeal{}, f.truncated), nil
}
func (f *fakeCRM) GetDeal(context.Context, uuid.UUID, uuid.UUID) (CRMDeal, error) {
	return CRMDeal{Name: "Expansion"}, nil
}
func (f *fakeCRM) GetBoard(context.Context, uuid.UUID, *uuid.UUID) (CRMBoard, error) {
	return CRMBoard{}, nil
}
func (f *fakeCRM) CreateCompany(_ context.Context, _ uuid.UUID, in CRMCompanyInput) (CRMCompany, error) {
	if f.writeErr != nil {
		return CRMCompany{}, f.writeErr
	}
	return CRMCompany{Name: in.Name}, nil
}
func (f *fakeCRM) UpdateCompany(ctx context.Context, workspaceID, _ uuid.UUID, in CRMCompanyInput) (CRMCompany, error) {
	return f.CreateCompany(ctx, workspaceID, in)
}
func (f *fakeCRM) CreateDeal(_ context.Context, _ uuid.UUID, in CRMDealInput) (CRMDeal, error) {
	if f.writeErr != nil {
		return CRMDeal{}, f.writeErr
	}
	f.created = in
	return CRMDeal{Name: in.Name}, nil
}
func (f *fakeCRM) UpdateDeal(ctx context.Context, workspaceID, _ uuid.UUID, in CRMDealInput) (CRMDeal, error) {
	return f.CreateDeal(ctx, workspaceID, in)
}
func (f *fakeCRM) MoveDeal(_ context.Context, _ uuid.UUID, in CRMDealMoveInput) (CRMDeal, error) {
	if f.writeErr != nil {
		return CRMDeal{}, f.writeErr
	}
	return CRMDeal{ID: in.DealID, StageID: in.StageID}, nil
}

// fakeClassifier stands in for the composition root's sentinel matching.
type fakeClassifier struct{ recoverable error }

type fakeRateLimiter struct {
	allowed bool
	err     error
	key     string
	limit   int
	window  time.Duration
}

func (f *fakeRateLimiter) Allow(_ context.Context, key string, limit int, window time.Duration) (bool, error) {
	f.key, f.limit, f.window = key, limit, window
	return f.allowed, f.err
}

func (f fakeClassifier) Recoverable(err error) (string, bool) {
	if f.recoverable != nil && errors.Is(err, f.recoverable) {
		return "the CRM rejected these values", true
	}
	return "", false
}

func TestCRMToolsRegisterByRisk(t *testing.T) {
	reg := New(Deps{CRM: &fakeCRM{}})
	for _, legacy := range []string{"inroad_crm_read", "inroad_crm_write"} {
		if _, ok := reg.Risk(legacy); ok {
			t.Fatalf("legacy tool %s is still registered", legacy)
		}
	}
	for _, name := range []string{"inroad_company_read", "inroad_deal_read", "inroad_pipeline_read"} {
		if risk, ok := reg.Risk(name); !ok || risk != RiskRead {
			t.Fatalf("%s risk = %v, %v", name, risk, ok)
		}
	}
	for _, name := range []string{"inroad_company_write", "inroad_deal_write"} {
		if risk, ok := reg.Risk(name); !ok || risk != RiskWrite {
			t.Fatalf("%s risk = %v, %v", name, risk, ok)
		}
	}
}

func TestCRMWriteAttributesAgentDeal(t *testing.T) {
	fake := &fakeCRM{}
	reg := New(Deps{CRM: fake})
	pipelineID, stageID := uuid.New(), uuid.New()
	p := Principal{WorkspaceID: uuid.New(), UserID: uuid.New(), Role: "member", AgentClientID: "inroad-chat", ThreadID: "thread-42", RunID: "run-7"}
	args, err := json.Marshal(map[string]any{"loading_message": "Creating expansion deal", "method": "create", "name": "Expansion", "pipeline_id": pipelineID, "stage_id": stageID, "currency": "USD"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := reg.Execute(context.Background(), p, "inroad_deal_write", args)
	if err != nil || !result.Success {
		t.Fatalf("execute: result=%+v err=%v", result, err)
	}
	if fake.created.Actor.UserID != p.UserID || fake.created.Actor.AgentClientID != p.AgentClientID {
		t.Fatalf("actor attribution lost: %+v", fake.created.Actor)
	}
	if fake.created.Actor.ThreadID != p.ThreadID || fake.created.Actor.RunID != p.RunID {
		t.Fatalf("run attribution lost: %+v", fake.created.Actor)
	}
}

func TestCRMWriteRateLimitIsPerWorkspaceAndClient(t *testing.T) {
	limiter := &fakeRateLimiter{allowed: false}
	reg := New(Deps{CRM: &fakeCRM{}, CRMWriteLimiter: limiter})
	p := Principal{WorkspaceID: uuid.New(), UserID: uuid.New(), Role: "member", AgentClientID: "mcp-client-1"}
	result, err := reg.Execute(context.Background(), p, "inroad_company_write", json.RawMessage(`{"loading_message":"Creating company","method":"create","name":"Acme"}`))
	if err != nil || result.Success || !strings.Contains(result.Error, "rate limit") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if !strings.Contains(limiter.key, p.WorkspaceID.String()) || !strings.Contains(limiter.key, p.AgentClientID) {
		t.Fatalf("unsafe limiter key %q", limiter.key)
	}
	if limiter.limit != crmWritesPerMinute || limiter.window != time.Minute {
		t.Fatalf("limit=%d window=%s", limiter.limit, limiter.window)
	}
}

func TestCRMWriteRateLimiterFailsClosed(t *testing.T) {
	limiterErr := errors.New("redis unavailable")
	reg := New(Deps{CRM: &fakeCRM{}, CRMWriteLimiter: &fakeRateLimiter{allowed: true, err: limiterErr}})
	result, err := reg.Execute(context.Background(), Principal{WorkspaceID: uuid.New(), UserID: uuid.New(), Role: "member"}, "inroad_company_write", json.RawMessage(`{"loading_message":"Creating company","method":"create","name":"Acme"}`))
	if !errors.Is(err, limiterErr) || result.Success {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

// TestCRMReadReportsTruncation proves a partial listing says so. A model that
// cannot see the cut silently reasons over "all" the records it was given.
func TestCRMReadReportsTruncation(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		reg := New(Deps{CRM: &fakeCRM{truncated: truncated}})
		result, err := reg.Execute(context.Background(), Principal{WorkspaceID: uuid.New(), UserID: uuid.New(), Role: "member"},
			"inroad_company_read", json.RawMessage(`{"loading_message":"Reading companies","method":"list"}`))
		if err != nil || !result.Success {
			t.Fatalf("execute: result=%+v err=%v", result, err)
		}
		list, ok := result.Data.(CRMList[CRMCompany])
		if !ok {
			t.Fatalf("data = %T, want CRMList[CRMCompany]", result.Data)
		}
		if list.Truncated != truncated || (truncated && list.Note == "") {
			t.Fatalf("truncated=%v: got %+v", truncated, list)
		}
	}
}

// TestCRMWriteErrorsUseTheClassifier proves a domain failure is classified by
// the injected seam, not by matching substrings, and that an unclassified
// failure aborts the run instead of leaking its text to the model.
func TestCRMWriteErrorsUseTheClassifier(t *testing.T) {
	domainErr := errors.New("crm: invalid request")
	args := json.RawMessage(`{"loading_message":"Creating company","method":"create","name":"Acme"}`)
	p := Principal{WorkspaceID: uuid.New(), UserID: uuid.New(), Role: "member"}

	reg := New(Deps{CRM: &fakeCRM{writeErr: fmt.Errorf("wrapped: %w", domainErr)}, CRMErrors: fakeClassifier{recoverable: domainErr}})
	result, err := reg.Execute(context.Background(), p, "inroad_company_write", args)
	if err != nil || result.Success || !strings.Contains(result.Error, "the CRM rejected these values") {
		t.Fatalf("classified error: result=%+v err=%v", result, err)
	}

	leaky := errors.New(`ERROR: duplicate key value violates unique constraint "uq_companies_workspace_domain"`)
	reg = New(Deps{CRM: &fakeCRM{writeErr: leaky}, CRMErrors: fakeClassifier{recoverable: domainErr}})
	result, err = reg.Execute(context.Background(), p, "inroad_company_write", args)
	if err == nil {
		t.Fatalf("unclassified error must abort the run, got result=%+v", result)
	}
	if strings.Contains(result.Error, "uq_companies_workspace_domain") {
		t.Fatalf("driver text reached the model: %q", result.Error)
	}
}

func TestCRMWriteRejectsUnknownArguments(t *testing.T) {
	reg := New(Deps{CRM: &fakeCRM{}})
	result, err := reg.Execute(context.Background(), Principal{WorkspaceID: uuid.New(), UserID: uuid.New(), Role: "member"}, "inroad_company_write", json.RawMessage(`{"loading_message":"Creating company","method":"create","name":"Acme","invented":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Success {
		t.Fatalf("unexpected success: %+v", result)
	}
}
