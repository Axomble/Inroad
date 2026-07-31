//go:build integration

package inprocess

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCleanupExpiredPurgesOnlyOutsideRetentionWindow(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)

	ws, err := q.CreateWorkspace(ctx, "Retention IT "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	var userID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (email, password_hash) VALUES ($1, 'test-only') RETURNING id`,
		"retention-"+uuid.NewString()+"@example.test").Scan(&userID); err != nil {
		t.Fatalf("user: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, 'member')`,
		ws.ID, userID); err != nil {
		t.Fatalf("membership: %v", err)
	}

	expiredID, freshID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `
		INSERT INTO sessions (id, user_id, workspace_id, token_hash, family_id, expires_at)
		VALUES
			($1, $2, $3, $4, $5, $6),
			($7, $2, $3, $8, $9, $10)`,
		expiredID, userID, ws.ID, []byte("expired-"+uuid.NewString()), uuid.New(), time.Now().Add(-31*24*time.Hour),
		freshID, []byte("fresh-"+uuid.NewString()), uuid.New(), time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("sessions: %v", err)
	}

	deleted, err := (client{q: q}).CleanupExpired(ctx)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if deleted < 1 {
		t.Fatalf("deleted rows = %d, want at least 1", deleted)
	}

	var expiredExists, freshExists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM sessions WHERE id = $1),
		       EXISTS(SELECT 1 FROM sessions WHERE id = $2)`, expiredID, freshID).
		Scan(&expiredExists, &freshExists); err != nil {
		t.Fatalf("verify sessions: %v", err)
	}
	if expiredExists || !freshExists {
		t.Fatalf("cleanup result: expired_exists=%t fresh_exists=%t", expiredExists, freshExists)
	}
}
