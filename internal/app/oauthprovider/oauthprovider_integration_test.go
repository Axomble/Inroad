//go:build integration

package oauthprovider

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
)

// itSetup migrates + connects a real Postgres and returns a Service over the PgStore
// plus a helper to mint a fresh (workspace, user) pair.
func itSetup(t *testing.T) (*Service, *PgStore, func() (uuid.UUID, uuid.UUID)) {
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

	store := NewPgStore(pool)
	svc := NewService(store, testPublicURL)

	mint := func() (uuid.UUID, uuid.UUID) {
		var ws, uid uuid.UUID
		if err := pool.QueryRow(ctx, "INSERT INTO workspaces (name) VALUES ('oauthprovider-it') RETURNING id").Scan(&ws); err != nil {
			t.Fatalf("insert workspace: %v", err)
		}
		email := "oauthp-" + uuid.NewString() + "@it.test"
		if err := pool.QueryRow(ctx, "INSERT INTO users (email, password_hash) VALUES ($1, 'x') RETURNING id", email).Scan(&uid); err != nil {
			t.Fatalf("insert user: %v", err)
		}
		if _, err := pool.Exec(ctx, "INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, 'member')", ws, uid); err != nil {
			t.Fatalf("insert workspace membership: %v", err)
		}
		return ws, uid
	}
	return svc, store, mint
}

// TestFullRoundTrip drives DCR -> authorize -> consent -> code end to end and asserts
// the persisted code is correctly bound, single-use, and short-TTL'd.
func TestFullRoundTrip(t *testing.T) {
	ctx := context.Background()
	svc, store, mint := itSetup(t)
	ws, uid := mint()

	reg, err := svc.RegisterClient(ctx, RegisterInput{
		ClientName:              "Round Trip",
		RedirectURIs:            []string{testRedirectURI},
		Scope:                   "contacts:read lists:read",
		TokenEndpointAuthMethod: "client_secret_basic",
		CreatedBy:               &uid,
		WorkspaceID:             &ws,
	})
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	if reg.ClientSecret == "" {
		t.Fatal("confidential client must return a secret once")
	}

	owner := Owner{UserID: uid, WorkspaceID: ws}
	in := baseAuthorize(reg.Client.ClientID)
	in.Scope = "contacts:read lists:read" // request both registered scopes
	in.Owner = &owner
	authRes, err := svc.Authorize(ctx, in)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	u, _ := url.Parse(authRes.RedirectTo)
	consentID := u.Query().Get("consent_id")
	if consentID == "" {
		t.Fatalf("no consent handoff: %s", authRes.RedirectTo)
	}

	view, err := svc.ConsentRequest(ctx, consentID, uid)
	if err != nil {
		t.Fatalf("ConsentRequest: %v", err)
	}
	if view.ClientName != "Round Trip" || view.RedirectURI != testRedirectURI {
		t.Fatalf("consent view wrong: %+v", view)
	}

	decRes, err := svc.DecideConsent(ctx, DecideInput{ConsentID: consentID, UserID: uid, Approve: true})
	if err != nil {
		t.Fatalf("DecideConsent approve: %v", err)
	}
	ru, _ := url.Parse(decRes.RedirectTo)
	rawCode := ru.Query().Get("code")
	if rawCode == "" || ru.Query().Get("state") != "st-123" {
		t.Fatalf("approve redirect missing code/state: %s", decRes.RedirectTo)
	}

	// The code persisted, hashed, and bound to every param.
	got, err := store.q.GetOauthAuthCode(ctx, hashSecret(rawCode))
	if err != nil {
		t.Fatalf("GetOauthAuthCode: %v", err)
	}
	if got.ClientID != reg.Client.ClientID || got.RedirectUri != testRedirectURI ||
		got.CodeChallenge != testChallenge || got.CodeChallengeMethod != "S256" ||
		got.UserID != uid || got.WorkspaceID != ws {
		t.Fatalf("code not bound to request params: %+v", got)
	}
	if len(got.Scopes) != 2 {
		t.Fatalf("code scopes wrong: %v", got.Scopes)
	}
	ttl := got.ExpiresAt.Time.Sub(got.CreatedAt.Time)
	if ttl <= 0 || ttl > 5*time.Minute {
		t.Fatalf("code TTL must be short, got %v", ttl)
	}

	// Single-use: a second approve on the (now consumed) request fails.
	if _, err := svc.DecideConsent(ctx, DecideInput{ConsentID: consentID, UserID: uid, Approve: true}); !errors.Is(err, ErrConsentNotFound) {
		t.Fatalf("second approve must fail single-use, got %v", err)
	}

	// The remembered consent now lets a fresh authorize skip straight to a code.
	in2 := baseAuthorize(reg.Client.ClientID)
	in2.Owner = &owner
	skipRes, err := svc.Authorize(ctx, in2)
	if err != nil {
		t.Fatalf("Authorize (prior consent): %v", err)
	}
	su, _ := url.Parse(skipRes.RedirectTo)
	if su.Query().Get("code") == "" {
		t.Fatalf("prior consent should skip to a code, got %s", skipRes.RedirectTo)
	}
}

// TestPriorConsentIsWorkspaceScoped proves the remembered-consent SKIP is
// workspace-scoped over the real DB: a user who consented to a client while active in
// workspace A, re-authorizing the SAME client + scopes in a workspace-B session, must
// NOT skip the consent screen. Before the fix the consent was keyed on (user, client)
// only, so the B-session authorize skipped and minted a code bound to workspace B — a
// cross-tenant grant the user never approved. It must instead persist a fresh pending
// consent request bound to workspace B.
func TestPriorConsentIsWorkspaceScoped(t *testing.T) {
	ctx := context.Background()
	svc, store, mint := itSetup(t)
	wsA, uid := mint()
	wsB, _ := mint() // a second workspace the SAME user is active in
	if _, err := store.pool.Exec(ctx, "INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, 'member')", wsB, uid); err != nil {
		t.Fatalf("insert user's workspace-B membership: %v", err)
	}

	reg, err := svc.RegisterClient(ctx, RegisterInput{
		ClientName: "WS Scoped", RedirectURIs: []string{testRedirectURI},
		Scope: "contacts:read", CreatedBy: &uid, WorkspaceID: &wsA,
	})
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}

	// The user consents in workspace A: authorize -> approve records a consent row
	// bound to (uid, client, wsA).
	inA := baseAuthorize(reg.Client.ClientID)
	ownerA := Owner{UserID: uid, WorkspaceID: wsA}
	inA.Owner = &ownerA
	resA, err := svc.Authorize(ctx, inA)
	if err != nil {
		t.Fatalf("Authorize A: %v", err)
	}
	uA, _ := url.Parse(resA.RedirectTo)
	consentA := uA.Query().Get("consent_id")
	if consentA == "" {
		t.Fatalf("A: expected a consent handoff, got %s", resA.RedirectTo)
	}
	if _, err := svc.DecideConsent(ctx, DecideInput{ConsentID: consentA, UserID: uid, Approve: true}); err != nil {
		t.Fatalf("DecideConsent A: %v", err)
	}

	// Same user, SAME client + scopes, now active in workspace B. The remembered
	// consent belongs to workspace A only, so the consent screen MUST NOT be skipped.
	inB := baseAuthorize(reg.Client.ClientID)
	ownerB := Owner{UserID: uid, WorkspaceID: wsB}
	inB.Owner = &ownerB
	resB, err := svc.Authorize(ctx, inB)
	if err != nil {
		t.Fatalf("Authorize B: %v", err)
	}
	uB, _ := url.Parse(resB.RedirectTo)
	if code := uB.Query().Get("code"); code != "" {
		t.Fatalf("B: a code must NOT be issued for a consent remembered in another workspace, got %s", resB.RedirectTo)
	}
	consentB := uB.Query().Get("consent_id")
	if consentB == "" {
		t.Fatalf("B: cross-workspace authorize must hand off to consent, got %s", resB.RedirectTo)
	}
	req, err := store.q.GetOauthAuthRequest(ctx, consentB)
	if err != nil {
		t.Fatalf("GetOauthAuthRequest B: %v", err)
	}
	if req.WorkspaceID != wsB || req.UserID != uid {
		t.Fatalf("B request not bound to (uid, wsB): user=%s ws=%s", req.UserID, req.WorkspaceID)
	}

	// Belt-and-braces: the workspace-A session still skips straight to a code.
	inA2 := baseAuthorize(reg.Client.ClientID)
	inA2.Owner = &ownerA
	resA2, err := svc.Authorize(ctx, inA2)
	if err != nil {
		t.Fatalf("Authorize A2: %v", err)
	}
	uA2, _ := url.Parse(resA2.RedirectTo)
	if uA2.Query().Get("code") == "" {
		t.Fatalf("A2: workspace-A consent should still skip to a code, got %s", resA2.RedirectTo)
	}
}

// TestForeignWorkspaceCannotListOrRevoke proves client management is tenant-isolated.
func TestForeignWorkspaceCannotListOrRevoke(t *testing.T) {
	ctx := context.Background()
	svc, _, mint := itSetup(t)
	wsA, uidA := mint()
	wsB, _ := mint()

	reg, err := svc.RegisterClient(ctx, RegisterInput{
		ClientName: "A client", RedirectURIs: []string{testRedirectURI}, Scope: "contacts:read",
		CreatedBy: &uidA, WorkspaceID: &wsA,
	})
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}

	bClients, err := svc.ListClients(ctx, wsB)
	if err != nil {
		t.Fatalf("ListClients B: %v", err)
	}
	for _, c := range bClients {
		if c.ClientID == reg.Client.ClientID {
			t.Fatal("workspace B listed workspace A's client")
		}
	}
	if err := svc.RevokeClient(ctx, wsB, reg.Client.ClientID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant revoke: want ErrNotFound, got %v", err)
	}
	if err := svc.RevokeClient(ctx, wsA, reg.Client.ClientID); err != nil {
		t.Fatalf("owner revoke: %v", err)
	}
}

// TestConsentByDifferentUserRejected proves a consent decision by anyone other than
// the resource owner who initiated the request is refused over the real DB.
func TestConsentByDifferentUserRejected(t *testing.T) {
	ctx := context.Background()
	svc, _, mint := itSetup(t)
	ws, uid := mint()
	_, other := mint()

	reg, err := svc.RegisterClient(ctx, RegisterInput{
		ClientName: "X", RedirectURIs: []string{testRedirectURI}, Scope: "contacts:read",
		CreatedBy: &uid, WorkspaceID: &ws,
	})
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	in := baseAuthorize(reg.Client.ClientID)
	owner := Owner{UserID: uid, WorkspaceID: ws}
	in.Owner = &owner
	authRes, err := svc.Authorize(ctx, in)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	u, _ := url.Parse(authRes.RedirectTo)
	consentID := u.Query().Get("consent_id")

	if _, err := svc.ConsentRequest(ctx, consentID, other); !errors.Is(err, ErrConsentNotFound) {
		t.Fatalf("foreign consent read: want ErrConsentNotFound, got %v", err)
	}
	if _, err := svc.DecideConsent(ctx, DecideInput{ConsentID: consentID, UserID: other, Approve: true}); !errors.Is(err, ErrConsentNotFound) {
		t.Fatalf("foreign consent decide: want ErrConsentNotFound, got %v", err)
	}
}
