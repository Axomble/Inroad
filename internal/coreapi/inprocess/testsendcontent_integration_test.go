//go:build integration

package inprocess

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// These tests exercise GetTestSendContent against Postgres: step content
// loading, real first-contact personalization, the synthetic Alex/Acme fallback
// for an empty list, and cross-tenant isolation. Reuses claimConnect/seedForClaim
// (claim_integration_test.go, same package) for the workspace/campaign/
// mailbox/contact fixture. Docker must be up.

// seedStep inserts one real sequence step directly. GetTestSendContent reads
// sequence_steps (not the campaign's own content projection columns), so a step
// row is required regardless of what CreateCampaign already put on the campaign.
func seedStep(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ws, campaignID uuid.UUID, order int32, subject, bodyText, bodyHTML string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO sequence_steps (workspace_id, campaign_id, step_order, subject, body_text, body_html)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id`, ws, campaignID, order, subject, bodyText, bodyHTML).Scan(&id); err != nil {
		t.Fatalf("insert step: %v", err)
	}
	return id
}

// Content loads from the real step, and personalization vars come from the
// campaign list's real first (earliest-added) contact's first_name/company --
// the only two contact fields GetTestSendContent reads.
func TestGetTestSendContentLoadsStepAndRealFirstContact(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)
	stepID := seedStep(t, ctx, pool, fx.ws, fx.campaignID, 1,
		"Quick question", "Hi {{first_name}}", "<p>Hi {{first_name}}</p>")

	var listID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT list_id FROM campaigns WHERE id = $1`, fx.campaignID).Scan(&listID); err != nil {
		t.Fatalf("load list id: %v", err)
	}
	lead, err := q.UpsertContact(ctx, gen.UpsertContactParams{
		WorkspaceID: fx.ws, Email: "priya-" + uuid.NewString() + "@x.test",
		FirstName: "Priya", Company: "Roc Corp",
	})
	if err != nil {
		t.Fatalf("lead contact: %v", err)
	}
	if err := q.AddListMember(ctx, gen.AddListMemberParams{ListID: listID, ContactID: lead.ID}); err != nil {
		t.Fatalf("add list member: %v", err)
	}

	c := client{q: q}
	got, err := c.GetTestSendContent(ctx, fx.ws.String(), fx.campaignID.String(), stepID.String())
	if err != nil {
		t.Fatalf("GetTestSendContent: %v", err)
	}
	if got.Subject != "Quick question" || got.BodyText != "Hi {{first_name}}" || got.BodyHTML != "<p>Hi {{first_name}}</p>" {
		t.Fatalf("content = %+v, want the seeded step content", got)
	}
	if got.FirstName != "Priya" || got.Company != "Roc Corp" {
		t.Fatalf("vars = %+v, want the real first contact's first_name/company (Priya/Roc Corp)", got)
	}
}

// An empty list (no list_members row at all) falls back to the synthetic
// preview vars, not an error and not blank strings.
func TestGetTestSendContentFallsBackToSyntheticVarsForAnEmptyList(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)
	stepID := seedStep(t, ctx, pool, fx.ws, fx.campaignID, 1, "Hi", "Body", "<p>Body</p>")
	// seedForClaim's own contact is never added to the list, so the list stays
	// empty here.

	c := client{q: q}
	got, err := c.GetTestSendContent(ctx, fx.ws.String(), fx.campaignID.String(), stepID.String())
	if err != nil {
		t.Fatalf("GetTestSendContent: %v", err)
	}
	if got.FirstName != testSendFallbackFirstName || got.Company != testSendFallbackCompany {
		t.Fatalf("vars = %+v, want the synthetic fallback %s/%s", got, testSendFallbackFirstName, testSendFallbackCompany)
	}
}

// A caller in a different workspace requesting this workspace's campaign_id/
// step_id resolves nothing -- the same workspace-pinned-lookup shape as
// TestSenderResolutionIsWorkspacePinned (senderpool_integration_test.go): the
// step lookup is scoped by workspace_id in its own WHERE clause, so a foreign
// workspace id simply finds no row rather than reaching the ErrCrossTenant
// campaign-mismatch check below it.
func TestGetTestSendContentIsWorkspacePinned(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)
	stepID := seedStep(t, ctx, pool, fx.ws, fx.campaignID, 1, "Hi", "Body", "<p>Body</p>")

	c := client{q: q}
	if _, err := c.GetTestSendContent(ctx, fx.foreignWS.String(), fx.campaignID.String(), stepID.String()); err == nil {
		t.Fatal("a cross-tenant GetTestSendContent loaded another workspace's step content")
	}
}
