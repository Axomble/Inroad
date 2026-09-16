//go:build integration

package inprocess

import (
	"context"
	"testing"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/mail"
)

// The self-host path, proven against Postgres rather than asserted: a client
// built by New with NO options answers IsSuppressed from the database,
// workspace-pinned, with no HTTP server, no fleet token and no listener
// existing anywhere in this process.
//
// Reuses claimConnect/seedForClaim (claim_integration_test.go, same package).
func TestTheDefaultSuppressionSourceReadsPostgres(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)

	c := New(pool, nil, nil, "", mail.GoogleOAuth{}, mail.MicrosoftOAuth{}, nil, nil).(suppressionCapability)

	suppressed, err := c.IsSuppressed(ctx, fx.ws.String(), fx.email)
	if err != nil {
		t.Fatalf("IsSuppressed before suppressing: %v", err)
	}
	if suppressed {
		t.Fatalf("%s reads as suppressed before anything suppressed it", fx.email)
	}

	if err := q.AddSuppression(ctx, gen.AddSuppressionParams{
		WorkspaceID: fx.ws, Email: fx.email, Reason: "unsubscribe",
	}); err != nil {
		t.Fatalf("AddSuppression: %v", err)
	}

	suppressed, err = c.IsSuppressed(ctx, fx.ws.String(), fx.email)
	if err != nil {
		t.Fatalf("IsSuppressed after suppressing: %v", err)
	}
	if !suppressed {
		t.Errorf("%s reads as not suppressed after being suppressed", fx.email)
	}

	// Workspace-pinned: the same address suppressed in one workspace is not
	// suppressed in another (docs/security.md invariant 4).
	suppressed, err = c.IsSuppressed(ctx, fx.foreignWS.String(), fx.email)
	if err != nil {
		t.Fatalf("IsSuppressed in the foreign workspace: %v", err)
	}
	if suppressed {
		t.Errorf("%s reads as suppressed in a workspace that never suppressed it", fx.email)
	}

	// A malformed workspace id is an error, not a silent "not suppressed" —
	// the behaviour the remote transport has to match, since a transport that
	// answered where the local path refused would be a behaviour change
	// wearing a network hop.
	if _, err := c.IsSuppressed(ctx, "not-a-uuid", fx.email); err == nil {
		t.Error("IsSuppressed accepted a malformed workspace id, want an error")
	}
}
