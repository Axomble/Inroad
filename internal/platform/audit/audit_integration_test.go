//go:build integration

package audit

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

func connect(t *testing.T) *pgxpool.Pool {
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

func newWorkspace(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	ws, err := gen.New(pool).CreateWorkspace(context.Background(), "audit-it "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	return ws.ID
}

func TestRecordRoundTrips(t *testing.T) {
	pool := connect(t)
	ws := newWorkspace(t, pool)
	uid := uuid.New()
	ctx := WithRequest(WithActor(context.Background(), UserActor(uid)), "2001:db8::7", "it/1.0")

	ev := New(ctx, ws, ActionCampaignPaused, "campaign", "c-1", Metadata{"name": "Q3"})
	if err := NewPgRecorder(pool).Record(ctx, ev); err != nil {
		t.Fatalf("Record: %v", err)
	}
	var actorType, actorID, ip, ua, name string
	var actorUser uuid.UUID
	if err := pool.QueryRow(context.Background(), `
		SELECT actor_type, actor_id, actor_user_id, host(ip), user_agent, metadata->>'name'
		FROM audit_events WHERE workspace_id = $1`, ws).
		Scan(&actorType, &actorID, &actorUser, &ip, &ua, &name); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if actorType != "user" || actorID != uid.String() || actorUser != uid || ip != "2001:db8::7" || ua != "it/1.0" || name != "Q3" {
		t.Fatalf("row = %s %s %s %s %s %s", actorType, actorID, actorUser, ip, ua, name)
	}
}

// The append-only guarantee is enforced by the database, so it is tested
// against the database: UPDATE, DELETE and TRUNCATE are refused through the
// application's own connection, while the two sanctioned deletes — the
// retention door and a workspace cascade — go through.
func TestAuditEventsAreAppendOnly(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	ws := newWorkspace(t, pool)
	if err := NewPgRecorder(pool).Record(ctx, New(ctx, ws, ActionAuthLogin, "", "", nil)); err != nil {
		t.Fatalf("Record: %v", err)
	}

	for name, stmt := range map[string]string{
		"update":   `UPDATE audit_events SET action = 'auth.logout' WHERE workspace_id = $1`,
		"delete":   `DELETE FROM audit_events WHERE workspace_id = $1`,
		"truncate": `TRUNCATE audit_events`,
	} {
		t.Run(name, func(t *testing.T) {
			var err error
			if strings.HasPrefix(stmt, "TRUNCATE") {
				_, err = pool.Exec(ctx, stmt)
			} else {
				_, err = pool.Exec(ctx, stmt, ws)
			}
			if err == nil || !strings.Contains(err.Error(), "append-only") {
				t.Fatalf("%s: err = %v, want the append-only refusal", name, err)
			}
		})
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1`, ws).Scan(&n); err != nil || n != 1 {
		t.Fatalf("row count after refused mutations = %d (%v), want 1", n, err)
	}

	// The retention door is transaction-scoped: set inside a tx, a DELETE
	// passes; after commit the very same connection is refused again.
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if err := gen.New(tx).EnableAuditRetentionPurge(ctx); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM audit_events WHERE workspace_id = $1`, ws)
		return err
	})
	if err != nil {
		t.Fatalf("delete through the retention door: %v", err)
	}
	if err := NewPgRecorder(pool).Record(ctx, New(ctx, ws, ActionAuthLogin, "", "", nil)); err != nil {
		t.Fatalf("Record: %v", err)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `DELETE FROM audit_events WHERE workspace_id = $1`, ws); err == nil {
		t.Fatal("the retention door stayed open after its transaction")
	}

	// Deleting the workspace cascades through the trigger.
	if _, err := pool.Exec(ctx, `DELETE FROM workspaces WHERE id = $1`, ws); err != nil {
		t.Fatalf("workspace delete blocked by the audit trigger: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1`, ws).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows after workspace delete = %d (%v), want 0", n, err)
	}
}

func TestInsertRefusesAnInvalidEvent(t *testing.T) {
	pool := connect(t)
	ws := newWorkspace(t, pool)
	ev := New(context.Background(), ws, ActionAuthLogin, "", "", Metadata{"api_token": "x"})
	if err := NewPgRecorder(pool).Record(context.Background(), ev); err == nil {
		t.Fatal("a token-named metadata key was written")
	}
}
