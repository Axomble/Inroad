package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"testing"
)

// fullDeps wires every capability, so the tests see the whole surface.
func fullDeps() Deps {
	return Deps{
		Campaigns:      &fakeCampaigns{},
		CampaignAdmin:  &fakeCampaignAdmin{},
		Contacts:       &fakeContacts{},
		ContactWrites:  &fakeContactWrites{},
		ContactImports: &fakeContactImports{},
		Mailboxes:      &fakeMailboxes{},
		Lists:          &fakeLists{},
		ListWrites:     &fakeListWrites{},
		Deliverability: &fakeHealth{},
		Warmup:         &fakeWarmup{},
	}
}

func names(tools []Tool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}

func TestLoadingMessageIsInjectedFirstAndRequired(t *testing.T) {
	for _, tool := range New(fullDeps()).Definitions(admin()) {
		var schema struct {
			Type       string          `json:"type"`
			Properties json.RawMessage `json:"properties"`
			Required   []string        `json:"required"`
		}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatalf("%s: schema does not parse: %v", tool.Name, err)
		}
		if schema.Type != "object" {
			t.Errorf("%s: schema type = %q, want object", tool.Name, schema.Type)
		}
		if !slices.Contains(schema.Required, LoadingMessageProperty) {
			t.Errorf("%s: %s missing from required %v", tool.Name, LoadingMessageProperty, schema.Required)
		}
		if got := string(schema.Properties); !strings.HasPrefix(got, `{"`+LoadingMessageProperty+`":`) {
			t.Errorf("%s: %s is not the first property: %s", tool.Name, LoadingMessageProperty, got)
		}
	}
}

// A tool that declares loading_message itself would end up with two competing
// descriptions, so registration rejects it rather than merging them.
func TestWithLoadingMessageRejectsSelfDeclaredProperty(t *testing.T) {
	schema := mustSchema(strField(LoadingMessageProperty, "mine", true))
	if _, err := withLoadingMessage(schema); err == nil {
		t.Fatal("want an error for a self-declared loading_message, got nil")
	}
}

func TestWithLoadingMessageRejectsNonObjectSchema(t *testing.T) {
	for _, schema := range []string{`{"type":"array"}`, `"nope"`, `{`} {
		if _, err := withLoadingMessage(json.RawMessage(schema)); err == nil {
			t.Errorf("schema %s: want an error, got nil", schema)
		}
	}
}

// A tool with no arguments of its own still gets the injected property, and
// the resulting properties object must stay valid JSON.
func TestWithLoadingMessagePreservesEmptyProperties(t *testing.T) {
	out, err := withLoadingMessage(mustSchema())
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(out, &schema); err != nil {
		t.Fatalf("result does not parse: %v", err)
	}
	if len(schema.Properties) != 1 || schema.Properties[LoadingMessageProperty] == nil {
		t.Fatalf("properties = %v, want only %s", schema.Properties, LoadingMessageProperty)
	}
	if len(schema.Required) != 1 {
		t.Fatalf("required = %v, want just the injected property", schema.Required)
	}
}

func TestDefinitionsAreNameSortedAndByteStable(t *testing.T) {
	first := New(fullDeps()).Definitions(admin())
	got := names(first)
	if !sort.StringsAreSorted(got) {
		t.Errorf("definitions are not name-sorted: %v", got)
	}

	// Prompt caching keys on the serialized tool list, so a second process
	// building the same registry must produce identical bytes.
	second := New(fullDeps()).Definitions(admin())
	if len(first) != len(second) {
		t.Fatalf("tool count changed between builds: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Name != second[i].Name {
			t.Fatalf("order changed at %d: %s vs %s", i, first[i].Name, second[i].Name)
		}
		if string(first[i].InputSchema) != string(second[i].InputSchema) {
			t.Errorf("%s: schema bytes changed between builds\n%s\n%s",
				first[i].Name, first[i].InputSchema, second[i].InputSchema)
		}
	}
}

func TestDefinitionsFilterByRole(t *testing.T) {
	reg := New(fullDeps())
	memberTools := names(reg.Definitions(member()))
	adminTools := names(reg.Definitions(admin()))

	if slices.Contains(memberTools, "inroad_campaign_control") {
		t.Errorf("member sees the consequential control tool: %v", memberTools)
	}
	if !slices.Contains(adminTools, "inroad_campaign_control") {
		t.Errorf("admin does not see the control tool: %v", adminTools)
	}
	if !slices.Contains(memberTools, "inroad_campaign_read") {
		t.Errorf("member cannot read campaigns: %v", memberTools)
	}
	// An unknown role outranks nothing, so it keeps the ungated read surface
	// and loses everything with a MinRole.
	unknown := names(reg.Definitions(Principal{WorkspaceID: wsID, Role: "gremlin"}))
	if slices.Contains(unknown, "inroad_campaign_control") {
		t.Errorf("unknown role sees the control tool: %v", unknown)
	}
}

// A tool list bound earlier in a conversation must not let a demoted user
// execute what they can no longer see.
func TestExecuteRechecksRole(t *testing.T) {
	reg := New(fullDeps())
	args := json.RawMessage(`{"loading_message":"Pausing","method":"pause","campaign_id":"11111111-1111-1111-1111-111111111111"}`)
	if _, err := reg.Execute(context.Background(), member(), "inroad_campaign_control", args); !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
}

func TestExecuteUnknownToolSuggestsClosestName(t *testing.T) {
	reg := New(fullDeps())
	_, err := reg.Execute(context.Background(), admin(), "inroad_campaign_reed", nil)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if !strings.Contains(err.Error(), "inroad_campaign_read") {
		t.Errorf("no usable suggestion in %q", err)
	}

	// Nothing close enough should not invent a suggestion — a wrong one sends
	// the model further off than none.
	_, err = reg.Execute(context.Background(), admin(), "delete_everything", nil)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if strings.Contains(err.Error(), "did you mean") {
		t.Errorf("suggested something for an unrelated name: %q", err)
	}
}

func TestExecuteRecoversPanic(t *testing.T) {
	deps := fullDeps()
	deps.Lists = &fakeLists{panicNow: true}
	reg := New(deps)

	args := json.RawMessage(`{"loading_message":"Listing audiences","method":"list"}`)
	_, err := reg.Execute(context.Background(), admin(), "inroad_list_read", args)
	if err == nil {
		t.Fatal("want an error from the panicking tool, got nil")
	}
	if !strings.Contains(err.Error(), "panicked") {
		t.Errorf("error does not name the panic: %v", err)
	}
}

func TestExecuteRejectsNonObjectArguments(t *testing.T) {
	reg := New(fullDeps())
	for _, args := range []string{`[1,2]`, `"list"`, `{`} {
		res, err := reg.Execute(context.Background(), admin(), "inroad_list_read", json.RawMessage(args))
		if err != nil {
			t.Fatalf("args %s: unexpected error %v", args, err)
		}
		if res.Success {
			t.Errorf("args %s: succeeded, want a recoverable failure", args)
		}
		if res.Error == "" {
			t.Errorf("args %s: no recovery instruction", args)
		}
	}
}

// JSON null decodes into a map without error and yields a nil map, so it needs
// its own rejection: a tool promised an object must never be handed null, and
// the provider seam relies on this check rather than repeating it.
func TestExecuteRejectsNullArguments(t *testing.T) {
	reg := New(fullDeps())
	res, err := reg.Execute(context.Background(), admin(), "inroad_list_read", json.RawMessage(`null`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Success {
		t.Fatal("null arguments were accepted")
	}
	if !strings.Contains(res.Error, "null") {
		t.Fatalf("failure does not tell the model null is the problem: %q", res.Error)
	}
}

// An unknown property is a model mistake it can correct, so it comes back as a
// Result naming the schema rather than being silently dropped.
func TestExecuteRejectsUnknownArgumentProperty(t *testing.T) {
	reg := New(fullDeps())
	args := json.RawMessage(`{"loading_message":"Listing","method":"list","workspace_id":"22222222-2222-2222-2222-222222222222"}`)
	res, err := reg.Execute(context.Background(), admin(), "inroad_list_read", args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Success {
		t.Fatal("a workspace_id argument was accepted; the workspace must come only from the principal")
	}
}

func TestExecuteAcceptsAbsentArguments(t *testing.T) {
	reg := New(fullDeps())
	res, err := reg.Execute(context.Background(), admin(), "inroad_list_read", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// method is required, so this fails — but as a Result, not an error.
	if res.Success {
		t.Fatal("a call with no method succeeded")
	}
}

func TestUnwiredCapabilitiesAreNotRegistered(t *testing.T) {
	reg := New(Deps{Campaigns: &fakeCampaigns{}})
	got := names(reg.Definitions(admin()))
	want := []string{"inroad_campaign_read", "inroad_search"}
	if !slices.Equal(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
	if _, err := reg.Execute(context.Background(), admin(), "inroad_campaign_control", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("unwired control tool err = %v, want ErrNotFound", err)
	}
}

func TestRiskReportsTierWithoutExecuting(t *testing.T) {
	reg := New(fullDeps())
	cases := map[string]Risk{
		"inroad_search":              RiskRead,
		"inroad_campaign_read":       RiskRead,
		"inroad_contact_read":        RiskRead,
		"inroad_mailbox_read":        RiskRead,
		"inroad_deliverability_read": RiskRead,
		"inroad_warmup_read":         RiskRead,
		"inroad_list_read":           RiskRead,
		"inroad_contact_write":       RiskWrite,
		"inroad_list_write":          RiskWrite,
		"inroad_campaign_control":    RiskConsequential,
	}
	for name, want := range cases {
		got, ok := reg.Risk(name)
		if !ok {
			t.Errorf("%s is not registered", name)
			continue
		}
		if got != want {
			t.Errorf("%s risk = %s, want %s", name, got, want)
		}
	}
	if _, ok := reg.Risk("inroad_send_reply"); ok {
		t.Error("an unregistered tool reported a risk tier")
	}
}

// No tool in this set may send mail, delete data, or expose a credential.
func TestNoToolIsIrreversible(t *testing.T) {
	for _, tool := range New(fullDeps()).Definitions(admin()) {
		if tool.Risk == RiskIrreversible {
			t.Errorf("%s is irreversible; A2 ships no such tool", tool.Name)
		}
		if strings.Contains(tool.Name, "send") || strings.Contains(tool.Name, "delete") {
			t.Errorf("%s looks like a send or delete tool", tool.Name)
		}
	}
}

// Every description has to say when to reach for the tool — it is the field
// tool selection actually turns on.
func TestEveryToolHasAUsefulDescription(t *testing.T) {
	for _, tool := range New(fullDeps()).Definitions(admin()) {
		if len(tool.Description) < 80 {
			t.Errorf("%s: description is too thin to guide selection: %q", tool.Name, tool.Description)
		}
		if !strings.HasPrefix(tool.Name, "inroad_") {
			t.Errorf("%s: tool names are product-prefixed", tool.Name)
		}
	}
}
