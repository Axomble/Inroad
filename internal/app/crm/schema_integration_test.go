//go:build integration

package crm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// newCRMWorkspace boots a migrated pool and a fresh workspace for one test.
func newCRMWorkspace(t *testing.T, label string) (context.Context, *pgxpool.Pool, uuid.UUID) {
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
	workspace, err := gen.New(pool).CreateWorkspace(ctx, label+" "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	return ctx, pool, workspace.ID
}

func labels(stages []Stage) []string {
	out := make([]string, len(stages))
	for i, stage := range stages {
		out[i] = stage.Label
	}
	return out
}

// TestStagesCanBeReordered is the regression for the non-deferrable
// UNIQUE(pipeline_id, position) that made any reorder a hard 409.
func TestStagesCanBeReordered(t *testing.T) {
	ctx, pool, ws := newCRMWorkspace(t, "CRM reorder")
	service := NewService(NewPgStore(pool))
	pipelines, err := service.ListPipelines(ctx, ws)
	if err != nil || len(pipelines) != 1 {
		t.Fatalf("pipelines = %+v, %v", pipelines, err)
	}
	stages := pipelines[0].Stages
	if len(stages) != 5 {
		t.Fatalf("want 5 seeded stages, got %d", len(stages))
	}
	// Move the last stage to the front and shift everything else down —
	// exactly the sequence of writes a drag-and-drop reorder issues.
	order := append([]Stage{stages[4]}, stages[:4]...)
	for i, stage := range order {
		if _, err := service.UpdateStage(ctx, ws, pipelines[0].ID, stage.ID, StageInput{
			Label: stage.Label, Color: stage.Color, Position: int32(i), IsWon: stage.IsWon, IsLost: stage.IsLost,
		}); err != nil {
			t.Fatalf("reorder %s to position %d: %v", stage.Label, i, err)
		}
	}
	after, err := service.GetPipeline(ctx, ws, pipelines[0].ID)
	if err != nil {
		t.Fatalf("reread pipeline: %v", err)
	}
	want := []string{"Lost", "Lead", "Qualified", "Proposal", "Won"}
	for i, label := range want {
		if after.Stages[i].Label != label {
			t.Fatalf("stage order = %v, want %v", labels(after.Stages), want)
		}
	}
}

// TestContactEmailSyncTrigger covers both trigger defects: a plain
// contacts.email UPDATE must succeed without the caller pre-clearing
// primaries, and an address already owned by ANOTHER contact must fail loud
// rather than silently leaving the contact with zero alias rows.
func TestContactEmailSyncTrigger(t *testing.T) {
	ctx, pool, ws := newCRMWorkspace(t, "CRM trigger")
	store := NewPgStore(pool)
	var first, second uuid.UUID
	for _, seed := range []struct {
		email string
		into  *uuid.UUID
	}{{"one@acme.test", &first}, {"two@acme.test", &second}} {
		if err := pool.QueryRow(ctx, `INSERT INTO contacts(workspace_id,email) VALUES($1,$2) RETURNING id`,
			ws, seed.email).Scan(seed.into); err != nil {
			t.Fatalf("seed %s: %v", seed.email, err)
		}
	}

	// A plain rename: no Go-side pre-clearing, no 23505.
	if _, err := pool.Exec(ctx, `UPDATE contacts SET email='one.renamed@acme.test' WHERE workspace_id=$1 AND id=$2`,
		ws, first); err != nil {
		t.Fatalf("plain contacts.email update: %v", err)
	}
	aliases, err := store.ListContactEmails(ctx, ws, first)
	if err != nil {
		t.Fatalf("aliases: %v", err)
	}
	var primaries int
	for _, alias := range aliases {
		if alias.IsPrimary {
			primaries++
			if alias.Email != "one.renamed@acme.test" {
				t.Fatalf("primary alias = %q, want the new address", alias.Email)
			}
		}
	}
	if len(aliases) != 2 || primaries != 1 {
		t.Fatalf("aliases after rename = %+v", aliases)
	}

	// Claiming another contact's address must fail loud, not silently drop
	// every alias row for this contact.
	_, err = pool.Exec(ctx, `UPDATE contacts SET email='two@acme.test' WHERE workspace_id=$1 AND id=$2`, ws, first)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("foreign-claim update = %v, want a 23505 unique violation", err)
	}
	aliases, err = store.ListContactEmails(ctx, ws, first)
	if err != nil || len(aliases) != 2 {
		t.Fatalf("aliases after refused claim = %+v, %v", aliases, err)
	}
}

// TestSetPrimaryContactEmailFlipsTheSendAddress proves the alias flip actually
// rewrites contacts.email, which is the address the send path reads.
func TestSetPrimaryContactEmailFlipsTheSendAddress(t *testing.T) {
	ctx, pool, ws := newCRMWorkspace(t, "CRM primary")
	store := NewPgStore(pool)
	service := NewService(store)
	var contactID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO contacts(workspace_id,email) VALUES($1,'primary@acme.test') RETURNING id`,
		ws).Scan(&contactID); err != nil {
		t.Fatalf("contact: %v", err)
	}
	alias, err := service.AddContactEmail(ctx, ws, contactID, "alias@acme.test")
	if err != nil {
		t.Fatalf("add alias: %v", err)
	}
	if err := service.SetPrimaryContactEmail(ctx, ws, contactID, alias.ID); err != nil {
		t.Fatalf("set primary: %v", err)
	}
	var email string
	if err := pool.QueryRow(ctx, `SELECT email::text FROM contacts WHERE workspace_id=$1 AND id=$2`, ws, contactID).
		Scan(&email); err != nil {
		t.Fatalf("reread contact: %v", err)
	}
	if email != "alias@acme.test" {
		t.Fatalf("contacts.email = %q, want the promoted alias", email)
	}
	rows, err := store.ListContactEmails(ctx, ws, contactID)
	if err != nil {
		t.Fatalf("aliases: %v", err)
	}
	var primaries int
	for _, row := range rows {
		if row.IsPrimary {
			primaries++
		}
	}
	if len(rows) != 2 || primaries != 1 {
		t.Fatalf("aliases = %+v", rows)
	}
}

// TestCompositeForeignKeysRejectCrossTenantReferences proves tenant ownership
// is enforced by PostgreSQL, not by application convention.
func TestCompositeForeignKeysRejectCrossTenantReferences(t *testing.T) {
	ctx, pool, first := newCRMWorkspace(t, "CRM tenant A")
	second, err := gen.New(pool).CreateWorkspace(ctx, "CRM tenant B "+uuid.NewString())
	if err != nil {
		t.Fatalf("second workspace: %v", err)
	}
	service := NewService(NewPgStore(pool))
	foreign, err := service.CreateCompany(ctx, second.ID, CompanyInput{Name: "Foreign", Currency: "USD"})
	if err != nil {
		t.Fatalf("foreign company: %v", err)
	}
	pipelines, err := service.ListPipelines(ctx, first)
	if err != nil {
		t.Fatalf("pipelines: %v", err)
	}
	_, err = service.CreateDeal(ctx, first, DealInput{
		Name: "Cross tenant", PipelineID: pipelines[0].ID, StageID: pipelines[0].Stages[0].ID,
		CompanyID: &foreign.ID, Currency: "USD", Source: "manual", Actor: Actor{Type: "user", ID: uuid.NewString()},
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("cross-tenant company_id = %v, want ErrValidation (composite FK rejection)", err)
	}
}

// TestDeleteRestrictKeepsReferencedRecords proves ON DELETE RESTRICT surfaces
// as an actionable conflict rather than cascading a company's deals away, and
// that each business refusal explains itself instead of reading as a 404.
func TestDeleteRestrictKeepsReferencedRecords(t *testing.T) {
	ctx, pool, ws := newCRMWorkspace(t, "CRM restrict")
	service := NewService(NewPgStore(pool))
	company, err := service.CreateCompany(ctx, ws, CompanyInput{Name: "Acme", Currency: "USD"})
	if err != nil {
		t.Fatalf("company: %v", err)
	}
	pipelines, err := service.ListPipelines(ctx, ws)
	if err != nil {
		t.Fatalf("pipelines: %v", err)
	}
	deal, err := service.CreateDeal(ctx, ws, DealInput{
		Name: "Expansion", PipelineID: pipelines[0].ID, StageID: pipelines[0].Stages[0].ID,
		CompanyID: &company.ID, Currency: "USD", Source: "manual", Actor: Actor{Type: "user", ID: uuid.NewString()},
	})
	if err != nil {
		t.Fatalf("deal: %v", err)
	}
	if err := service.DeleteCompany(ctx, ws, company.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("referenced company delete = %v, want ErrConflict", err)
	}
	if err := service.DeletePipeline(ctx, ws, pipelines[0].ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("default pipeline delete = %v, want ErrConflict", err)
	}
	err = service.DeleteStage(ctx, ws, pipelines[0].ID, pipelines[0].Stages[0].ID)
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "1 deal(s)") {
		t.Fatalf("occupied stage delete = %v, want a conflict naming the deal count", err)
	}
	if err := service.DeleteDeal(ctx, ws, deal.ID); err != nil {
		t.Fatalf("deal delete: %v", err)
	}
	if err := service.DeleteCompany(ctx, ws, company.ID); err != nil {
		t.Fatalf("company delete after clearing references: %v", err)
	}
}

// TestDealPositionsAreFractionalAndPaged covers the numeric-position defect (a
// midpoint must survive the round trip, so one row moves per drag) and keyset
// paging over the deals ordering.
func TestDealPositionsAreFractionalAndPaged(t *testing.T) {
	ctx, pool, ws := newCRMWorkspace(t, "CRM positions")
	service := NewService(NewPgStore(pool))
	pipelines, err := service.ListPipelines(ctx, ws)
	if err != nil {
		t.Fatalf("pipelines: %v", err)
	}
	stage := pipelines[0].Stages[0]
	actor := Actor{Type: "user", ID: uuid.NewString()}
	made := make([]Deal, 0, 3)
	for _, name := range []string{"First", "Second", "Third"} {
		deal, createErr := service.CreateDeal(ctx, ws, DealInput{Name: name, PipelineID: pipelines[0].ID,
			StageID: stage.ID, Currency: "USD", Source: "manual", Actor: actor})
		if createErr != nil {
			t.Fatalf("create %s: %v", name, createErr)
		}
		made = append(made, deal)
	}
	if made[0].Position != 1000 || made[2].Position != 3000 {
		t.Fatalf("appended positions = %v, %v, %v", made[0].Position, made[1].Position, made[2].Position)
	}
	// Drop Third between First and Second: ONE row changes, to the midpoint.
	moved, err := service.MoveDeal(ctx, ws, made[2].ID, MoveDealInput{
		StageID: stage.ID, BeforeDealID: &made[0].ID, AfterDealID: &made[1].ID, Actor: actor,
	})
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if moved.Position != 1500 {
		t.Fatalf("midpoint position = %v, want 1500", moved.Position)
	}
	// Halve the gap again with a DIFFERENT deal: the midpoint of 1000 and the
	// fractional 1500 must survive the numeric round trip.
	regap, err := service.MoveDeal(ctx, ws, made[1].ID, MoveDealInput{
		StageID: stage.ID, BeforeDealID: &made[0].ID, AfterDealID: &made[2].ID, Actor: actor,
	})
	if err != nil || regap.Position != 1250 {
		t.Fatalf("second midpoint = %v (err %v), want 1250", regap.Position, err)
	}

	first, err := service.ListDeals(ctx, ws, PageRequest{Limit: 2})
	if err != nil || len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("page one = %+v, %v", first, err)
	}
	next, err := service.ListDeals(ctx, ws, PageRequest{Limit: 2, Cursor: first.NextCursor})
	if err != nil || len(next.Items) != 1 || next.NextCursor != "" {
		t.Fatalf("page two = %+v, %v", next, err)
	}
	seen := map[uuid.UUID]bool{}
	for _, deal := range append(first.Items, next.Items...) {
		if seen[deal.ID] {
			t.Fatalf("deal %s appeared on both pages", deal.ID)
		}
		seen[deal.ID] = true
	}
	if len(seen) != 3 {
		t.Fatalf("paging lost rows: saw %d of 3", len(seen))
	}
	if _, err := service.ListDeals(ctx, ws, PageRequest{Limit: 2, Cursor: encodeCursor(cursorCompanies, "x", "y")}); !errors.Is(err, ErrValidation) {
		t.Fatalf("foreign cursor = %v, want ErrValidation", err)
	}
}

// TestCompanyAndNotePagingWalksEveryRow proves the other keyset orderings are
// total: no row is skipped or repeated across pages.
func TestCompanyAndNotePagingWalksEveryRow(t *testing.T) {
	ctx, pool, ws := newCRMWorkspace(t, "CRM paging")
	service := NewService(NewPgStore(pool))
	actor := Actor{Type: "user", ID: uuid.NewString()}
	var target Target
	for _, name := range []string{"Alpha", "Bravo", "Charlie", "Delta", "Echo"} {
		company, err := service.CreateCompany(ctx, ws, CompanyInput{Name: name, Currency: "USD"})
		if err != nil {
			t.Fatalf("company %s: %v", name, err)
		}
		target = Target{Type: TargetCompany, ID: company.ID}
	}
	names := walkCompanies(ctx, t, service, ws)
	want := []string{"Alpha", "Bravo", "Charlie", "Delta", "Echo"}
	if len(names) != len(want) {
		t.Fatalf("walked %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("walked %v, want %v", names, want)
		}
	}

	for i := 0; i < 5; i++ {
		if _, err := service.CreateNote(ctx, ws, NoteInput{Body: "note", Target: target, Actor: actor}); err != nil {
			t.Fatalf("note %d: %v", i, err)
		}
	}
	seen := map[uuid.UUID]bool{}
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		page, err := service.ListNotes(ctx, ws, target, PageRequest{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatalf("notes page: %v", err)
		}
		for _, note := range page.Items {
			if seen[note.ID] {
				t.Fatalf("note %s repeated across pages", note.ID)
			}
			seen[note.ID] = true
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(seen) != 5 {
		t.Fatalf("note paging saw %d of 5", len(seen))
	}
}

func walkCompanies(ctx context.Context, t *testing.T, service *Service, ws uuid.UUID) []string {
	t.Helper()
	var names []string
	cursor := ""
	for pages := 0; pages < 10; pages++ {
		page, err := service.ListCompanies(ctx, ws, PageRequest{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatalf("companies page: %v", err)
		}
		for _, company := range page.Items {
			names = append(names, company.Name)
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	return names
}

// TestCreatePipelineSeedsTheSharedStageSet proves the app path and the
// workspace seed produce the same stages — one SQL definition, two callers.
func TestCreatePipelineSeedsTheSharedStageSet(t *testing.T) {
	ctx, pool, ws := newCRMWorkspace(t, "CRM seed")
	service := NewService(NewPgStore(pool))
	seeded, err := service.ListPipelines(ctx, ws)
	if err != nil {
		t.Fatalf("pipelines: %v", err)
	}
	created, err := service.CreatePipeline(ctx, ws, PipelineInput{Name: "Partnerships"})
	if err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	want, got := labels(seeded[0].Stages), labels(created.Stages)
	if len(want) != len(got) {
		t.Fatalf("created stages %v, want %v", got, want)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("created stages %v, want %v", got, want)
		}
	}
}
