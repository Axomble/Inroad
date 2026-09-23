//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/app/agenttool"
	"github.com/inroad/inroad/internal/app/contact"
	"github.com/inroad/inroad/internal/app/crm"
	"github.com/inroad/inroad/internal/app/list"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// linkFixture is two workspaces, each with one contact and one company, and
// the agent registry wired exactly as main.go wires it — the real adapters over
// the real contact and CRM services. What the fakes in agenttool cannot prove
// is that a FOREIGN id is indistinguishable from an unknown one all the way
// down, and that a refused link writes nothing.
type linkFixture struct {
	pool                           *pgxpool.Pool
	reg                            agenttool.Registry
	ws, other                      uuid.UUID
	contact, company               uuid.UUID
	foreignContact, foreignCompany uuid.UUID
}

func newLinkFixture(t *testing.T) linkFixture {
	t.Helper()
	ctx := context.Background()
	if err := db.Migrate(dbtest.DSN(t)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dbtest.DSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	queries := gen.New(pool)

	f := linkFixture{pool: pool}
	f.ws = f.workspace(t, "Agent link")
	f.other = f.workspace(t, "Agent link other")
	f.contact = f.scalar(t, `INSERT INTO contacts (workspace_id, email) VALUES ($1, 'dana@acme.test') RETURNING id`, f.ws)
	f.foreignContact = f.scalar(t, `INSERT INTO contacts (workspace_id, email) VALUES ($1, 'eve@other.test') RETURNING id`, f.other)
	f.company = f.scalar(t, `INSERT INTO companies (workspace_id, name) VALUES ($1, 'Acme Robotics') RETURNING id`, f.ws)
	f.foreignCompany = f.scalar(t, `INSERT INTO companies (workspace_id, name) VALUES ($1, 'Acme Foreign') RETURNING id`, f.other)

	lists := list.NewService(list.NewPgStore(queries))
	contacts := contact.NewService(contact.NewPgStore(pool), listCheckerAdapter{lists: lists}, contact.NewPgFieldStore(queries))
	tools := contactTools{service: contacts, store: contact.NewPgStore(pool), lists: lists, pool: pool}
	f.reg = agenttool.New(agenttool.Deps{
		Contacts: tools, ContactWrites: tools,
		CRM: crmTools{service: crm.NewService(crm.NewPgStore(pool))}, CRMErrors: crmErrors{},
	})
	return f
}

func (f linkFixture) workspace(t *testing.T, label string) uuid.UUID {
	t.Helper()
	ws, err := gen.New(f.pool).CreateWorkspace(context.Background(), label+" "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	return ws.ID
}

func (f linkFixture) scalar(t *testing.T, sql string, args ...any) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		t.Fatalf("seed (%s): %v", sql, err)
	}
	return id
}

// linkedCompany reads contacts.company_id straight from the table, so the
// assertion does not trust the code under test to report its own write.
func (f linkFixture) linkedCompany(t *testing.T, contactID uuid.UUID) *uuid.UUID {
	t.Helper()
	var id *uuid.UUID
	if err := f.pool.QueryRow(context.Background(), `SELECT company_id FROM contacts WHERE id = $1`, contactID).Scan(&id); err != nil {
		t.Fatalf("read company_id: %v", err)
	}
	return id
}

func (f linkFixture) run(t *testing.T, ws uuid.UUID, tool, args string) agenttool.Result {
	t.Helper()
	p := agenttool.Principal{WorkspaceID: ws, UserID: uuid.New(), Role: "member"}
	res, err := f.reg.Execute(context.Background(), p, tool, json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s: run aborted: %v", tool, err)
	}
	return res
}

func linkCall(contactID, companyID uuid.UUID) string {
	return fmt.Sprintf(`{"loading_message":"Linking","method":"link_company","contact_id":%q,"company_id":%q}`, contactID, companyID)
}

func TestAgentLinksAContactToACompanyItFoundBySearch(t *testing.T) {
	f := newLinkFixture(t)

	// The agent's path: resolve the company by name, then link with its id.
	found := f.run(t, f.ws, "inroad_company_read", `{"loading_message":"Finding Acme","method":"search","query":"acme"}`)
	companies, ok := found.Data.(agenttool.CRMList[agenttool.CRMCompany])
	if !found.Success || !ok || len(companies.Items) != 1 || companies.Items[0].ID != f.company {
		t.Fatalf("search = %+v, want only this workspace's Acme Robotics", found)
	}

	res := f.run(t, f.ws, "inroad_contact_write", linkCall(f.contact, companies.Items[0].ID))
	if !res.Success {
		t.Fatalf("link failed: %s", res.Error)
	}
	if got := f.linkedCompany(t, f.contact); got == nil || *got != f.company {
		t.Fatalf("company_id = %v, want %s", got, f.company)
	}

	unlink := fmt.Sprintf(`{"loading_message":"Unlinking","method":"unlink_company","contact_id":%q}`, f.contact)
	if res := f.run(t, f.ws, "inroad_contact_write", unlink); !res.Success {
		t.Fatalf("unlink failed: %s", res.Error)
	}
	if got := f.linkedCompany(t, f.contact); got != nil {
		t.Fatalf("company_id = %v after unlink, want NULL", got)
	}
}

func TestAgentLinkRefusesForeignAndUnknownIDsWithoutWriting(t *testing.T) {
	f := newLinkFixture(t)
	cases := []struct {
		name               string
		contactID, company uuid.UUID
		mustName           string
	}{
		{name: "another workspace's company", contactID: f.contact, company: f.foreignCompany, mustName: "no company"},
		{name: "an unknown company", contactID: f.contact, company: uuid.New(), mustName: "no company"},
		{name: "another workspace's contact", contactID: f.foreignContact, company: f.company, mustName: "no contact"},
		{name: "an unknown contact", contactID: uuid.New(), company: f.company, mustName: "no contact"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := f.run(t, f.ws, "inroad_contact_write", linkCall(tc.contactID, tc.company))
			if res.Success || !strings.Contains(res.Error, tc.mustName) {
				t.Fatalf("result = %+v, want a recoverable %q failure", res, tc.mustName)
			}
			if got := f.linkedCompany(t, f.contact); got != nil {
				t.Fatalf("own contact company_id = %v, want still NULL", got)
			}
			if got := f.linkedCompany(t, f.foreignContact); got != nil {
				t.Fatalf("foreign contact company_id = %v, want still NULL", got)
			}
		})
	}
}

func TestAgentCompanySearchNeverReturnsAnotherWorkspacesCompany(t *testing.T) {
	f := newLinkFixture(t)
	res := f.run(t, f.other, "inroad_company_read", `{"loading_message":"Finding Acme","method":"search","query":"robotics"}`)
	companies, ok := res.Data.(agenttool.CRMList[agenttool.CRMCompany])
	if !res.Success || !ok || len(companies.Items) != 0 {
		t.Fatalf("other workspace searching for this one's company got %+v", res)
	}
}
