package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
)

var companyA = uuid.MustParse("eeeeeeee-0000-0000-0000-000000000001")

func linkArgs(contactID, companyID string) string {
	return fmt.Sprintf(`{"loading_message":"Linking company","method":"link_company","contact_id":%q,"company_id":%q}`, contactID, companyID)
}

func TestContactWriteLinksACompanyInThePrincipalsWorkspace(t *testing.T) {
	writes := &fakeContactWrites{linkName: "Acme Robotics"}
	reg := New(Deps{ContactWrites: writes})

	out := ok(t, reg, member(), "inroad_contact_write", linkArgs(contactA.String(), companyA.String()))

	if writes.gotWS != wsID {
		t.Fatalf("writer saw workspace %s, want the principal's %s", writes.gotWS, wsID)
	}
	if writes.gotContact != contactA || writes.gotCompany == nil || *writes.gotCompany != companyA {
		t.Fatalf("writer saw contact=%s company=%v", writes.gotContact, writes.gotCompany)
	}
	if out["linked"] != true || out["company_id"] != companyA.String() || out["company_name"] != "Acme Robotics" {
		t.Fatalf("result = %v", out)
	}
}

func TestContactWriteUnlinksWithANilCompany(t *testing.T) {
	writes := &fakeContactWrites{}
	reg := New(Deps{ContactWrites: writes})

	out := ok(t, reg, member(), "inroad_contact_write",
		fmt.Sprintf(`{"loading_message":"Unlinking company","method":"unlink_company","contact_id":%q}`, contactA))

	if !writes.setCalled || writes.gotCompany != nil {
		t.Fatalf("unlink must reach the writer with a nil company: called=%v company=%v", writes.setCalled, writes.gotCompany)
	}
	if out["linked"] != false {
		t.Fatalf("result = %v", out)
	}
	if _, present := out["company_id"]; present {
		t.Fatalf("an unlinked result must not name a company: %v", out)
	}
}

func TestContactWriteLinkRejectsBadIDsBeforeWriting(t *testing.T) {
	cases := map[string]string{
		"missing company_id": fmt.Sprintf(`{"loading_message":"Linking company","method":"link_company","contact_id":%q}`, contactA),
		"malformed company":  linkArgs(contactA.String(), "acme"),
		"malformed contact":  linkArgs("dana", companyA.String()),
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			writes := &fakeContactWrites{}
			fails(t, New(Deps{ContactWrites: writes}), member(), "inroad_contact_write", args)
			if writes.setCalled {
				t.Fatal("an invalid id reached the writer")
			}
		})
	}
}

// The two not-founds need opposite recoveries, so each must name the tool that
// fixes it. A cross-workspace id comes back from the adapter as the same
// sentinel as an unknown one (proved against Postgres in cmd/inroad).
func TestContactWriteLinkNotFoundsAreRecoverableAndDistinct(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		mustName string
	}{
		{name: "unknown contact", err: fmt.Errorf("wrapped: %w", ErrContactNotInWorkspace), mustName: "inroad_contact_read"},
		{name: "unknown company", err: fmt.Errorf("wrapped: %w", ErrCompanyNotInWorkspace), mustName: "inroad_company_read"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := New(Deps{ContactWrites: &fakeContactWrites{linkErr: tc.err}})
			msg := fails(t, reg, member(), "inroad_contact_write", linkArgs(contactA.String(), companyA.String()))
			if !strings.Contains(msg, tc.mustName) {
				t.Fatalf("recovery text %q does not name %s", msg, tc.mustName)
			}
		})
	}
}

func TestContactWriteLinkInfrastructureFaultAbortsTheRun(t *testing.T) {
	reg := New(Deps{ContactWrites: &fakeContactWrites{linkErr: errBoom}})
	_, err := reg.Execute(context.Background(), member(), "inroad_contact_write",
		json.RawMessage(linkArgs(contactA.String(), companyA.String())))
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v, want the infrastructure fault propagated", err)
	}
}

func TestCompanyReadSearchPassesTheTrimmedQuery(t *testing.T) {
	fake := &fakeCRM{truncated: true}
	reg := New(Deps{CRM: fake})

	res, err := reg.Execute(context.Background(), member(), "inroad_company_read",
		json.RawMessage(`{"loading_message":"Finding Acme","method":"search","query":"  Acme "}`))
	if err != nil || !res.Success {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	if fake.gotWS != wsID || fake.gotQuery != "Acme" {
		t.Fatalf("search saw ws=%s query=%q", fake.gotWS, fake.gotQuery)
	}
	list, isList := res.Data.(CRMList[CRMCompany])
	if !isList || len(list.Items) != 1 || !list.Truncated || list.Note == "" {
		t.Fatalf("data = %#v, want a truncation-reporting company list", res.Data)
	}
}

func TestCompanyReadSearchRefusesAShortQueryWithoutSearching(t *testing.T) {
	for _, query := range []string{"", " ", "a", " É "} {
		fake := &fakeCRM{}
		msg := fails(t, New(Deps{CRM: fake}), member(), "inroad_company_read",
			fmt.Sprintf(`{"loading_message":"Finding a company","method":"search","query":%q}`, query))
		if fake.searched {
			t.Fatalf("query %q reached the service", query)
		}
		if !strings.Contains(msg, "method=list") {
			t.Fatalf("recovery text %q does not offer browsing", msg)
		}
	}
}

func TestCompanyReadSearchErrorsUseTheClassifier(t *testing.T) {
	domainErr := errors.New("crm: invalid request")
	args := json.RawMessage(`{"loading_message":"Finding Acme","method":"search","query":"acme"}`)

	reg := New(Deps{CRM: &fakeCRM{searchErr: fmt.Errorf("wrapped: %w", domainErr)}, CRMErrors: fakeClassifier{recoverable: domainErr}})
	res, err := reg.Execute(context.Background(), member(), "inroad_company_read", args)
	if err != nil || res.Success || res.Error == "" {
		t.Fatalf("classified error: result=%+v err=%v", res, err)
	}

	reg = New(Deps{CRM: &fakeCRM{searchErr: errBoom}, CRMErrors: fakeClassifier{recoverable: domainErr}})
	if _, err := reg.Execute(context.Background(), member(), "inroad_company_read", args); !errors.Is(err, errBoom) {
		t.Fatalf("unclassified error must abort the run, got %v", err)
	}
}
