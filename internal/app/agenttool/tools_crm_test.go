package agenttool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

type fakeCRM struct{ created CRMDealInput }

func (f *fakeCRM) ListCompanies(context.Context, uuid.UUID) ([]CRMCompany, error) {
	return []CRMCompany{{ID: uuid.New(), Name: "Acme"}}, nil
}
func (f *fakeCRM) ListPipelines(context.Context, uuid.UUID) ([]CRMPipeline, error) { return nil, nil }
func (f *fakeCRM) ListDeals(context.Context, uuid.UUID) ([]CRMDeal, error)         { return nil, nil }
func (f *fakeCRM) CreateCompany(_ context.Context, _ uuid.UUID, in CRMCompanyInput) (CRMCompany, error) {
	return CRMCompany{Name: in.Name}, nil
}
func (f *fakeCRM) CreateDeal(_ context.Context, _ uuid.UUID, in CRMDealInput) (CRMDeal, error) {
	f.created = in
	return CRMDeal{Name: in.Name}, nil
}

func TestCRMToolsRegisterByRisk(t *testing.T) {
	reg := New(Deps{CRM: &fakeCRM{}})
	if risk, ok := reg.Risk("inroad_crm_read"); !ok || risk != RiskRead {
		t.Fatalf("read risk = %v, %v", risk, ok)
	}
	if risk, ok := reg.Risk("inroad_crm_write"); !ok || risk != RiskWrite {
		t.Fatalf("write risk = %v, %v", risk, ok)
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
	p := Principal{WorkspaceID: uuid.New(), UserID: uuid.New(), Role: "member", AgentClientID: "thread-42"}
	args, err := json.Marshal(map[string]any{"loading_message": "Creating expansion deal", "method": "create_deal", "name": "Expansion", "pipeline_id": pipelineID, "stage_id": stageID, "currency": "USD"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := reg.Execute(context.Background(), p, "inroad_crm_write", args)
	if err != nil || !result.Success {
		t.Fatalf("execute: result=%+v err=%v", result, err)
	}
	if fake.created.Actor.UserID != p.UserID || fake.created.Actor.AgentClientID != p.AgentClientID {
		t.Fatalf("actor attribution lost: %+v", fake.created.Actor)
	}
}

func TestCRMWriteRejectsUnknownArguments(t *testing.T) {
	reg := New(Deps{CRM: &fakeCRM{}})
	result, err := reg.Execute(context.Background(), Principal{WorkspaceID: uuid.New(), UserID: uuid.New(), Role: "member"}, "inroad_crm_write", json.RawMessage(`{"loading_message":"Creating company","method":"create_company","name":"Acme","invented":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Success {
		t.Fatalf("unexpected success: %+v", result)
	}
}
