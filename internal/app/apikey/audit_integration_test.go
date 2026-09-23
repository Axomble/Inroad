//go:build integration

package apikey

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/audit"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
)

func auditPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if err := db.Migrate(dbtest.DSN(t)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(context.Background(), dbtest.DSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func countAudit(t *testing.T, pool *pgxpool.Pool, ws uuid.UUID, action audit.Action, target string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = $2 AND target_id = $3`,
		ws, string(action), target).Scan(&n); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	return n
}

// TestKeyLifecycleIsAuditedInTheSameTransaction proves the in-transaction
// class of the failure policy: create and revoke each land exactly one row,
// attributed to the acting user; a cross-tenant revoke lands none; and an
// audit write that fails takes the key down with it.
func TestKeyLifecycleIsAuditedInTheSameTransaction(t *testing.T) {
	_, svc, _, mint := setup(t)
	pool := auditPool(t)
	ws, uid := mint()
	ctx := audit.WithActor(context.Background(), audit.UserActor(uid))
	ctx = audit.WithRequest(ctx, "192.0.2.10", "it-agent")

	view, _, err := svc.Create(ctx, CreateInput{
		WorkspaceID: ws, CreatedBy: uid, Name: "ci", Scopes: []string{auth.ScopeListsRead},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if n := countAudit(t, pool, ws, audit.ActionAPIKeyCreated, view.ID.String()); n != 1 {
		t.Fatalf("apikey.created rows = %d, want 1", n)
	}
	var actorType, actorID, ip, prefix string
	if err := pool.QueryRow(context.Background(), `
		SELECT actor_type, actor_id, host(ip), metadata->>'prefix' FROM audit_events
		WHERE workspace_id = $1 AND target_id = $2`, ws, view.ID.String()).
		Scan(&actorType, &actorID, &ip, &prefix); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if actorType != "user" || actorID != uid.String() || ip != "192.0.2.10" || prefix != view.Prefix {
		t.Fatalf("row = %s/%s ip=%s prefix=%s", actorType, actorID, ip, prefix)
	}

	if err := svc.Revoke(ctx, ws, view.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if n := countAudit(t, pool, ws, audit.ActionAPIKeyRevoked, view.ID.String()); n != 1 {
		t.Fatalf("apikey.revoked rows = %d, want 1", n)
	}

	// Cross-tenant: another workspace's admin revoking this key changes nothing
	// and therefore records nothing — in either workspace.
	otherWS, _ := mint()
	if err := svc.Revoke(ctx, otherWS, view.ID); err == nil {
		t.Fatal("cross-tenant revoke succeeded")
	}
	if n := countAudit(t, pool, otherWS, audit.ActionAPIKeyRevoked, view.ID.String()); n != 0 {
		t.Fatalf("cross-tenant revoke recorded %d rows", n)
	}
}

func TestKeyIsNotCreatedWhenItsAuditRowCannotBe(t *testing.T) {
	store, _, _, mint := setup(t)
	pool := auditPool(t)
	ws, uid := mint()
	prefix, _, hash, err := newToken()
	if err != nil {
		t.Fatalf("newToken: %v", err)
	}
	// An event that fails validation stands in for any audit write failure.
	bad := audit.New(context.Background(), ws, "apikey.nonsense", auditTarget, "", nil)
	if _, err := store.Create(context.Background(), CreateParams{
		WorkspaceID: ws, CreatedBy: uid, Name: "doomed", Prefix: prefix, SecretHash: hash,
		Scopes: []string{auth.ScopeListsRead},
	}, bad); err == nil {
		t.Fatal("Create succeeded with an unwritable audit event")
	}
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM api_keys WHERE prefix = $1`, prefix).Scan(&n); err != nil {
		t.Fatalf("count keys: %v", err)
	}
	if n != 0 {
		t.Fatalf("key persisted without its audit row (%d rows)", n)
	}
}
