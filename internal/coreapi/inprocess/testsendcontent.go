package inprocess

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// testSendFallbackFirstName / testSendFallbackCompany are the synthetic
// personalization values substituted when the campaign's list has no contact
// yet, so an operator can preview a template before adding an audience.
// Mirrors the wording documented on POST /campaigns/{id}/test-send.
const (
	testSendFallbackFirstName = "Alex"
	testSendFallbackCompany   = "Acme"
)

// GetTestSendContent loads the raw (unrendered) step content plus the
// preview personalization vars for one test-send: the step's
// subject/body_text/body_html, and the campaign list's first (earliest-added)
// contact's first_name/company -- or the synthetic fallback when the list has
// no contact yet. workspaceID is pinned on both lookups. A step that does not
// belong to campaignID (defense in depth on top of the API's own ownership
// check, which resolved this same campaignID/stepID pair before enqueuing) is
// coreapi.ErrCrossTenant.
func (c client) GetTestSendContent(ctx context.Context, workspaceID, campaignID, stepID string) (coreapi.TestSendContent, error) {
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return coreapi.TestSendContent{}, err
	}
	cid, err := uuid.Parse(campaignID)
	if err != nil {
		return coreapi.TestSendContent{}, err
	}
	sid, err := uuid.Parse(stepID)
	if err != nil {
		return coreapi.TestSendContent{}, err
	}

	step, err := c.q.GetStep(ctx, gen.GetStepParams{ID: sid, WorkspaceID: ws})
	if err != nil {
		return coreapi.TestSendContent{}, err
	}
	if step.CampaignID != cid {
		return coreapi.TestSendContent{}, coreapi.ErrCrossTenant
	}

	// The synthetic fallback is the default; a real first contact overrides it.
	// A genuine lookup failure (not "list is empty") must not fail the whole
	// test-send over a personalization PREVIEW -- fall back, but log it so a
	// persistent failure (a bad join, a broken migration) is observable rather
	// than silently eaten forever.
	firstName, company := testSendFallbackFirstName, testSendFallbackCompany
	fc, ferr := c.q.GetCampaignFirstContact(ctx, gen.GetCampaignFirstContactParams{ID: cid, WorkspaceID: ws})
	switch {
	case ferr == nil:
		firstName, company = fc.FirstName, fc.Company
	case errors.Is(ferr, pgx.ErrNoRows):
		// Empty list: the fallback above is the documented behavior.
	default:
		slog.WarnContext(ctx, "testsend_first_contact_lookup_failed", "campaign_id", campaignID, "err", ferr)
	}

	return coreapi.TestSendContent{
		Subject: step.Subject, BodyText: step.BodyText, BodyHTML: step.BodyHtml,
		FirstName: firstName, Company: company,
	}, nil
}

// IsSuppressed reports whether `to` is on workspaceID's suppression list --
// the SAME (workspace_id, lower(email))-indexed lookup GetStepSendJob
// uses for a real send. Consumed through the narrow
// testsend.Core interface (defense-in-depth re-check right before the
// testsend:send task dials, since the control-plane check in
// campaign.Service.TestSend can race an incoming unsubscribe).
func (c client) IsSuppressed(ctx context.Context, workspaceID, to string) (bool, error) {
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return false, err
	}
	return c.q.IsSuppressed(ctx, gen.IsSuppressedParams{WorkspaceID: ws, Lower: to})
}
