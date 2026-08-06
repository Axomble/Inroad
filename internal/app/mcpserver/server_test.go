package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inroad/inroad/internal/app/agenttool"
)

type fakeRegistry struct {
	tools       []agenttool.Tool
	failExecute bool
}

func (f fakeRegistry) Definitions(agenttool.Principal) []agenttool.Tool { return f.tools }
func (f fakeRegistry) Execute(context.Context, agenttool.Principal, string, json.RawMessage) (agenttool.Result, error) {
	if f.failExecute {
		return agenttool.Fail("record not found"), nil
	}
	return agenttool.Ok(map[string]bool{"called": true}), nil
}
func (f fakeRegistry) Risk(name string) (agenttool.Risk, bool) {
	for _, tool := range f.tools {
		if tool.Name == name {
			return tool.Risk, true
		}
	}
	return agenttool.RiskRead, false
}

func TestRequiredScopeMapsRegistryTools(t *testing.T) {
	tests := []struct {
		name string
		risk agenttool.Risk
		want string
	}{
		{"inroad_contact_read", agenttool.RiskRead, "contacts:read"},
		{"inroad_contact_write", agenttool.RiskWrite, "contacts:write"},
		{"inroad_list_read", agenttool.RiskRead, "lists:read"},
		{"inroad_mailbox_read", agenttool.RiskRead, "mailboxes:read"},
		{"inroad_campaign_control", agenttool.RiskConsequential, "campaigns:write"},
		{"inroad_company_read", agenttool.RiskRead, "crm:read"},
		{"inroad_company_write", agenttool.RiskWrite, "crm:write"},
		{"inroad_deal_read", agenttool.RiskRead, "crm:read"},
		{"inroad_deliverability_read", agenttool.RiskRead, "campaigns:read"},
		{"inroad_warmup_read", agenttool.RiskRead, "mailboxes:read"},
	}
	for _, tt := range tests {
		if got := requiredScope(tt.name, tt.risk); got != tt.want {
			t.Errorf("requiredScope(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestMCPDoesNotExposeToolsWithoutGrant(t *testing.T) {
	tool := agenttool.Tool{Name: "inroad_contact_read", Risk: agenttool.RiskRead}
	if !allowedTool(tool, []string{"contacts:read"}) {
		t.Fatal("granted read scope was rejected")
	}
	if allowedTool(tool, []string{"contacts:write"}) {
		t.Fatal("write scope exposed a read tool unexpectedly")
	}
	crm := agenttool.Tool{Name: "inroad_company_write", Risk: agenttool.RiskWrite}
	if !allowedTool(crm, []string{"crm:write"}) {
		t.Fatal("CRM scope mapping should remain explicit")
	}
	deliverability := agenttool.Tool{Name: "inroad_deliverability_read", Risk: agenttool.RiskRead}
	if !allowedTool(deliverability, []string{"campaigns:read"}) {
		t.Fatal("deliverability read tool should be exposed under campaigns:read")
	}
	warmup := agenttool.Tool{Name: "inroad_warmup_read", Risk: agenttool.RiskRead}
	if !allowedTool(warmup, []string{"mailboxes:read"}) {
		t.Fatal("warmup read tool should be exposed under mailboxes:read")
	}
}

func TestMCPRequiresBearerToken(t *testing.T) {
	workspaceID, userID := uuid.New(), uuid.New()
	h := New(fakeRegistry{}, func(context.Context, *http.Request) (agenttool.Principal, []string, time.Time, string, bool, error) {
		return agenttool.Principal{WorkspaceID: workspaceID, UserID: userID}, []string{"contacts:read"}, time.Now().Add(time.Hour), "client", true, nil
	}, "https://inroad.test/v1/mcp", "https://inroad.test/oauth2")
	req := httptest.NewRequest(http.MethodPost, "/v1/mcp", http.NoBody)
	res := httptest.NewRecorder()
	h.StreamableHTTP().ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("MCP without bearer status = %d, want 401", res.Code)
	}
}

func TestMCPMetadataIsPublic(t *testing.T) {
	h := New(fakeRegistry{}, func(context.Context, *http.Request) (agenttool.Principal, []string, time.Time, string, bool, error) {
		return agenttool.Principal{}, nil, time.Time{}, "", false, nil
	}, "https://inroad.test/v1/mcp", "https://inroad.test/oauth2")
	res := httptest.NewRecorder()
	h.Metadata().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", http.NoBody))
	if res.Code != http.StatusOK {
		t.Fatalf("metadata status = %d, want 200", res.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["resource"] != "https://inroad.test/v1/mcp" {
		t.Fatalf("resource metadata = %#v", body["resource"])
	}
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (b *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+b.token)
	base := b.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

func TestMCPEndToEndStreamableHTTP(t *testing.T) {
	workspaceID, userID := uuid.New(), uuid.New()
	reg := fakeRegistry{
		tools: []agenttool.Tool{
			{Name: "inroad_contact_read", Description: "Read contacts", InputSchema: json.RawMessage(`{"type":"object"}`), Risk: agenttool.RiskRead},
			{Name: "inroad_company_read", Description: "Read CRM companies", InputSchema: json.RawMessage(`{"type":"object"}`), Risk: agenttool.RiskRead},
			{Name: "inroad_company_write", Description: "Write CRM companies", InputSchema: json.RawMessage(`{"type":"object"}`), Risk: agenttool.RiskWrite},
		},
	}
	h := New(reg, func(ctx context.Context, r *http.Request) (agenttool.Principal, []string, time.Time, string, bool, error) {
		authHeader := r.Header.Get("Authorization")
		if authHeader != "Bearer valid_token" {
			return agenttool.Principal{}, nil, time.Time{}, "", false, nil
		}
		return agenttool.Principal{WorkspaceID: workspaceID, UserID: userID}, []string{"crm:read", "contacts:read"}, time.Now().Add(time.Hour), "client_1", true, nil
	}, "http://localhost/v1/mcp", "http://localhost/oauth2")

	ts := httptest.NewServer(h.StreamableHTTP())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, &mcp.ClientOptions{})
	transport := &mcp.StreamableClientTransport{
		Endpoint: ts.URL,
		HTTPClient: &http.Client{
			Transport: &bearerTransport{token: "valid_token"},
		},
		DisableStandaloneSSE: true,
	}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("failed to connect MCP client over Streamable HTTP: %v", err)
	}
	defer session.Close()

	toolsResult, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	if len(toolsResult.Tools) != 2 {
		t.Fatalf("ListTools returned %d tools, want 2 (contacts:read and crm:read tools)", len(toolsResult.Tools))
	}
	toolNames := map[string]bool{}
	for _, tool := range toolsResult.Tools {
		toolNames[tool.Name] = true
	}
	if !toolNames["inroad_contact_read"] || !toolNames["inroad_company_read"] {
		t.Fatalf("unexpected tools returned: %#v", toolNames)
	}
	if toolNames["inroad_company_write"] {
		t.Fatal("inroad_company_write tool exposed without crm:write scope")
	}

	callRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "inroad_company_read",
		Arguments: map[string]any{"id": "comp_123"},
	})
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}
	if callRes.IsError {
		t.Fatalf("CallTool returned IsError: true unexpectedly")
	}
}

func TestMCPCallToolFailureReturnsIsError(t *testing.T) {
	workspaceID, userID := uuid.New(), uuid.New()
	reg := fakeRegistry{
		tools:       []agenttool.Tool{{Name: "inroad_company_read", Description: "Read CRM companies", InputSchema: json.RawMessage(`{"type":"object"}`), Risk: agenttool.RiskRead}},
		failExecute: true,
	}
	h := New(reg, func(ctx context.Context, r *http.Request) (agenttool.Principal, []string, time.Time, string, bool, error) {
		return agenttool.Principal{WorkspaceID: workspaceID, UserID: userID}, []string{"crm:read"}, time.Now().Add(time.Hour), "client_1", true, nil
	}, "http://localhost/v1/mcp", "http://localhost/oauth2")

	ts := httptest.NewServer(h.StreamableHTTP())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, &mcp.ClientOptions{})
	transport := &mcp.StreamableClientTransport{
		Endpoint: ts.URL,
		HTTPClient: &http.Client{
			Transport: &bearerTransport{token: "valid_token"},
		},
		DisableStandaloneSSE: true,
	}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("failed to connect MCP client over Streamable HTTP: %v", err)
	}
	defer session.Close()

	callRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "inroad_company_read",
		Arguments: map[string]any{"id": "nonexistent"},
	})
	if err != nil {
		t.Fatalf("CallTool transport failed: %v", err)
	}
	if !callRes.IsError {
		t.Fatalf("CallTool should return IsError: true when tool execution fails")
	}
}
