//go:build integration

package inprocess

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func requireForeignKeyViolation(t *testing.T, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("want foreign_key_violation (23503), got %v", err)
	}
}

// TestDatabaseRejectsCrossWorkspaceRelationships proves tenant ownership is a
// database invariant, not merely an API convention. It intentionally bypasses
// every service-layer workspace check and writes directly through PostgreSQL.
func TestDatabaseRejectsCrossWorkspaceRelationships(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	a := seedForClaim(t, ctx, q)
	b := seedForClaim(t, ctx, q)

	_, err := pool.Exec(ctx, `UPDATE campaigns SET mailbox_id = $1 WHERE id = $2`, b.mailboxID, a.campaignID)
	requireForeignKeyViolation(t, err)

	var listA uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT list_id FROM campaigns WHERE id = $1`, a.campaignID).Scan(&listA); err != nil {
		t.Fatalf("load list A: %v", err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO list_members (list_id, contact_id) VALUES ($1, $2)`, listA, b.contactID)
	requireForeignKeyViolation(t, err)

	_, err = pool.Exec(ctx, `
		INSERT INTO sends (workspace_id, campaign_id, contact_id, mailbox_id, to_email)
		VALUES ($1, $2, $3, $4, $5)`,
		a.ws, a.campaignID, b.contactID, a.mailboxID, b.email)
	requireForeignKeyViolation(t, err)

	var sendB uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO sends (workspace_id, campaign_id, contact_id, mailbox_id, to_email)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		b.ws, b.campaignID, b.contactID, b.mailboxID, b.email).Scan(&sendB); err != nil {
		t.Fatalf("create tenant-B send: %v", err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO tracking_events (workspace_id, campaign_id, send_id, kind)
		VALUES ($1, $2, $3, 'open')`, a.ws, a.campaignID, sendB)
	requireForeignKeyViolation(t, err)

	var userID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (email, password_hash) VALUES ($1, 'test-only') RETURNING id`,
		"tenant-integrity-"+uuid.NewString()+"@example.test").Scan(&userID); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workspace_members (workspace_id, user_id, role) VALUES ($1, $2, 'member')`,
		a.ws, userID); err != nil {
		t.Fatalf("create membership: %v", err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO sessions (user_id, workspace_id, token_hash, family_id, expires_at)
		VALUES ($1, $2, $3, $4, $5)`,
		userID, b.ws, []byte(uuid.NewString()), uuid.New(), time.Now().Add(time.Hour))
	requireForeignKeyViolation(t, err)
}

func TestCampaignContentProjectionFollowsFirstSequenceStep(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)

	var stepID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO sequence_steps
			(workspace_id, campaign_id, step_order, subject, body_text, body_html)
		VALUES ($1, $2, 1, 'Canonical subject', 'Canonical text', '<p>Canonical</p>')
		RETURNING id`, fx.ws, fx.campaignID).Scan(&stepID); err != nil {
		t.Fatalf("insert first step: %v", err)
	}

	var subject, bodyText, bodyHTML string
	if err := pool.QueryRow(ctx, `
		SELECT subject, body_text, body_html FROM campaigns WHERE id = $1`,
		fx.campaignID).Scan(&subject, &bodyText, &bodyHTML); err != nil {
		t.Fatalf("load campaign projection: %v", err)
	}
	if subject != "Canonical subject" || bodyText != "Canonical text" || bodyHTML != "<p>Canonical</p>" {
		t.Fatalf("campaign projection drifted: subject=%q text=%q html=%q", subject, bodyText, bodyHTML)
	}

	if _, err := pool.Exec(ctx, `UPDATE sequence_steps SET subject = 'Updated subject' WHERE id = $1`, stepID); err != nil {
		t.Fatalf("update first step: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT subject FROM campaigns WHERE id = $1`, fx.campaignID).Scan(&subject); err != nil {
		t.Fatalf("reload campaign projection: %v", err)
	}
	if subject != "Updated subject" {
		t.Fatalf("campaign subject = %q, want updated projection", subject)
	}
}

func TestDeletingSendCascadesTrackingEvents(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)

	var sendID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO sends (workspace_id, campaign_id, contact_id, mailbox_id, to_email)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		fx.ws, fx.campaignID, fx.contactID, fx.mailboxID, fx.email).Scan(&sendID); err != nil {
		t.Fatalf("create send: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO tracking_events (workspace_id, campaign_id, send_id, kind)
		VALUES ($1, $2, $3, 'open')`, fx.ws, fx.campaignID, sendID); err != nil {
		t.Fatalf("create tracking event: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM sends WHERE id = $1`, sendID); err != nil {
		t.Fatalf("delete send: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tracking_events WHERE send_id = $1`, sendID).Scan(&count); err != nil {
		t.Fatalf("count tracking events: %v", err)
	}
	if count != 0 {
		t.Fatalf("tracking events remaining after send deletion: %d", count)
	}
}
