//go:build integration

package inprocess

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inroad/inroad/internal/app/suppression"
	"github.com/inroad/inroad/internal/coreapi/remote"
	"github.com/inroad/inroad/internal/platform/credbroker"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/mail"
)

const remoteTestToken = "0123456789abcdef0123456789abcdef" // credbroker.MinTokenLen

func remoteQuiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// controlPlane stands the real handler up over the real suppression store on
// real Postgres, and returns the URL a worker would dial.
//
// The other five halves are wired with a pool-less in-process client purely so
// the handler can be BUILT — this file drives the suppression route only, and
// those routes are proven end to end in remotejobs_integration_test.go,
// remoteoutcomes_integration_test.go, remoteinboxsends_integration_test.go and
// remoteinbound_integration_test.go.
func controlPlane(t *testing.T, q *gen.Queries) *httptest.Server {
	t.Helper()
	poolless := New(nil, nil, nil, "", mail.GoogleOAuth{}, mail.MicrosoftOAuth{}, nil, nil)
	h, err := remote.NewHandler(remote.Deps{
		Suppression: suppression.NewStore(q),
		Jobs:        jobReader(t, poolless),
		Outcomes:    outcomeWriter(t, poolless),
		InboxSends:  inboxSendWriter(t, poolless),
		Inbound:     inboundWriter(t, poolless),
		Fleet:       fleetWriter(t, poolless),
	}, remoteTestToken, remoteQuiet())
	if err != nil {
		t.Fatalf("remote.NewHandler: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// remoteCore builds the EXECUTION plane's coreapi client with a NIL POOL. The
// nil is the assertion, not a shortcut: localSuppression would dereference it,
// so every answer this client gives came off the wire.
//
// The credential broker is credbroker.Unconfigured — a real Opener that fails
// closed on every call. The suppression route needs no credential, so a working
// one would prove nothing here, and a broker that refuses makes it impossible
// for this file to pass by accidentally opening one.
func remoteCore(t *testing.T, baseURL string) suppressionCapability {
	t.Helper()
	rc, err := remote.NewClient(baseURL, remoteTestToken, true, credbroker.Unconfigured{}) // httptest speaks http
	if err != nil {
		t.Fatalf("remote.NewClient: %v", err)
	}
	return New(nil, nil, nil, "", mail.GoogleOAuth{}, mail.MicrosoftOAuth{}, nil, nil,
		WithRemoteSuppression(rc)).(suppressionCapability)
}

// THE end-to-end test: a coreapi client that holds no database connection at
// all answers IsSuppressed correctly, through the real handler, against the
// real table — and answers it workspace-pinned.
func TestARemoteCoreAPIClientWithNoPoolAnswersFromTheControlPlane(t *testing.T) {
	ctx := context.Background()
	_, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)
	srv := controlPlane(t, q)
	c := remoteCore(t, srv.URL)

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
		t.Error("the worker did not see a suppression that is in the table; the wire dropped it")
	}

	// Workspace-pinned across the wire: the control plane applies the same
	// workspace_id filter the in-process path does, so the same address
	// suppressed in one workspace is not suppressed in another
	// (docs/security.md invariant 4).
	suppressed, err = c.IsSuppressed(ctx, fx.foreignWS.String(), fx.email)
	if err != nil {
		t.Fatalf("IsSuppressed in the foreign workspace: %v", err)
	}
	if suppressed {
		t.Errorf("%s reads as suppressed in a workspace that never suppressed it", fx.email)
	}
}

// Fail closed, proven where it matters: the address IS suppressed in Postgres,
// and the worker cannot reach the control plane. The only acceptable answer is
// an error. Returning (false, nil) here would send mail to someone who opted
// out — the exact failure a local-read fallback would produce.
func TestAnUnreachableControlPlaneRefusesRatherThanMissingASuppression(t *testing.T) {
	ctx := context.Background()
	_, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)
	if err := q.AddSuppression(ctx, gen.AddSuppressionParams{
		WorkspaceID: fx.ws, Email: fx.email, Reason: "unsubscribe",
	}); err != nil {
		t.Fatalf("AddSuppression: %v", err)
	}
	srv := controlPlane(t, q)
	c := remoteCore(t, srv.URL)

	// It works while the control plane is up...
	if suppressed, err := c.IsSuppressed(ctx, fx.ws.String(), fx.email); err != nil || !suppressed {
		t.Fatalf("IsSuppressed = %v, %v; want true, nil while the control plane is up", suppressed, err)
	}

	srv.Close()

	suppressed, err := c.IsSuppressed(ctx, fx.ws.String(), fx.email)
	if err == nil {
		t.Fatal("IsSuppressed answered with the control plane down; a worker must refuse, not guess")
	}
	if suppressed {
		t.Error("suppressed = true alongside an error; the contract is the zero value")
	}
}

// A worker holding the wrong fleet token learns nothing about the workspace:
// the answer is a refusal, not the suppression state, and not a silent false.
func TestAWorkerWithTheWrongTokenLearnsNothing(t *testing.T) {
	ctx := context.Background()
	_, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)
	if err := q.AddSuppression(ctx, gen.AddSuppressionParams{
		WorkspaceID: fx.ws, Email: fx.email, Reason: "unsubscribe",
	}); err != nil {
		t.Fatalf("AddSuppression: %v", err)
	}
	srv := controlPlane(t, q)

	rc, err := remote.NewClient(srv.URL, strings.Repeat("z", len(remoteTestToken)), true, credbroker.Unconfigured{})
	if err != nil {
		t.Fatalf("remote.NewClient: %v", err)
	}
	c := New(nil, nil, nil, "", mail.GoogleOAuth{}, mail.MicrosoftOAuth{}, nil, nil,
		WithRemoteSuppression(rc)).(suppressionCapability)

	suppressed, err := c.IsSuppressed(ctx, fx.ws.String(), fx.email)
	if err == nil {
		t.Fatal("a wrong token got an answer")
	}
	if suppressed {
		t.Error("suppressed = true from a rejected call")
	}
}
