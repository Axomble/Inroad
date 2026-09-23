//go:build integration

package auditlog

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

type seedRow struct {
	action, actorType, actorID string
	actorUser                  *uuid.UUID
	targetType, targetID       string
	at                         time.Time
}

func seed(t *testing.T, pool *pgxpool.Pool, ws uuid.UUID, rows []seedRow) {
	t.Helper()
	for _, r := range rows {
		if _, err := pool.Exec(context.Background(), `
			INSERT INTO audit_events (workspace_id, actor_type, actor_id, actor_user_id, action, target_type, target_id, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			ws, r.actorType, r.actorID, r.actorUser, r.action, r.targetType, r.targetID, r.at); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

func setup(t *testing.T) (*pgxpool.Pool, *Service) {
	t.Helper()
	if err := db.Migrate(dbtest.DSN(t)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(context.Background(), dbtest.DSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, NewService(NewPgStore(pool))
}

func workspace(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	ws, err := gen.New(pool).CreateWorkspace(context.Background(), "auditlog-it "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	return ws.ID
}

func actions(p Page) []string {
	out := make([]string, len(p.Events))
	for i, e := range p.Events {
		out[i] = e.Action
	}
	return out
}

func TestListFiltersPagingAndIsolationAgainstPostgres(t *testing.T) {
	pool, svc := setup(t)
	ctx := context.Background()
	ws, other := workspace(t, pool), workspace(t, pool)

	var uid uuid.UUID
	email := "auditor-" + uuid.NewString() + "@it.test"
	if err := pool.QueryRow(ctx, `INSERT INTO users (email, password_hash) VALUES ($1, 'x') RETURNING id`, email).Scan(&uid); err != nil {
		t.Fatalf("user: %v", err)
	}
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	seed(t, pool, ws, []seedRow{
		{"auth.login", "user", uid.String(), &uid, "user", uid.String(), base},
		{"campaign.paused", "user", uid.String(), &uid, "campaign", "c-1", base.Add(1 * time.Minute)},
		{"campaign.resumed", "api_key", "k-1", &uid, "campaign", "c-1", base.Add(2 * time.Minute)},
		{"campaign.paused", "system", "deliverability_breaker", nil, "campaign", "c-2", base.Add(3 * time.Minute)},
		{"campaigns_x.thing", "system", "", nil, "", "", base.Add(4 * time.Minute)}, // prefix-boundary decoy
		{"apikey.created", "user", uid.String(), &uid, "api_key", "k-1", base.Add(5 * time.Minute)},
	})
	seed(t, pool, other, []seedRow{{"campaign.paused", "user", uuid.NewString(), nil, "campaign", "c-1", base.Add(time.Minute)}})

	list := func(f Filter, limit int, cursor string) Page {
		t.Helper()
		p, err := svc.List(ctx, ws, ListInput{Filter: f, Limit: limit, Cursor: cursor})
		if err != nil {
			t.Fatalf("List(%+v): %v", f, err)
		}
		return p
	}

	all := list(Filter{}, 0, "")
	if len(all.Events) != 6 {
		t.Fatalf("unfiltered = %v, want this workspace's 6 rows only", actions(all))
	}
	if all.Events[0].Action != "apikey.created" {
		t.Fatalf("not newest first: %v", actions(all))
	}
	for _, e := range all.Events {
		if e.WorkspaceID != ws {
			t.Fatalf("row from workspace %s leaked into %s", e.WorkspaceID, ws)
		}
	}
	if all.Events[0].ActorEmail == nil || *all.Events[0].ActorEmail != email {
		t.Fatalf("actor_email join = %v, want %s", all.Events[0].ActorEmail, email)
	}

	for name, tc := range map[string]struct {
		f    Filter
		want int
	}{
		"prefix on a segment boundary": {Filter{ActionPrefix: "campaign"}, 3},
		"exact action":                 {Filter{ActionPrefix: "campaign.paused"}, 2},
		"actor type":                   {Filter{ActorType: "system"}, 2},
		"actor id":                     {Filter{ActorID: "k-1"}, 1},
		"actor user (direct + key)":    {Filter{ActorUserID: &uid}, 4},
		"target":                       {Filter{TargetType: "campaign", TargetID: "c-1"}, 2},
		"date range":                   {Filter{Since: ptr(base.Add(time.Minute)), Until: ptr(base.Add(3 * time.Minute))}, 2},
	} {
		t.Run(name, func(t *testing.T) {
			if got := list(tc.f, 0, ""); len(got.Events) != tc.want {
				t.Fatalf("%s = %v, want %d rows", name, actions(got), tc.want)
			}
		})
	}

	// Paging with a filter walks every matching row exactly once.
	seen := map[uuid.UUID]bool{}
	cursor := ""
	for range 10 {
		p := list(Filter{ActionPrefix: "campaign"}, 1, cursor)
		for _, e := range p.Events {
			if seen[e.ID] {
				t.Fatalf("row %s served twice", e.ID)
			}
			seen[e.ID] = true
		}
		if p.NextCursor == "" {
			break
		}
		cursor = p.NextCursor
	}
	if len(seen) != 3 {
		t.Fatalf("paged through %d campaign rows, want 3", len(seen))
	}

	// The other workspace sees only its own row.
	p, err := svc.List(ctx, other, ListInput{})
	if err != nil || len(p.Events) != 1 {
		t.Fatalf("other workspace list = %d rows (%v), want 1", len(p.Events), err)
	}
}

func ptr[T any](v T) *T { return &v }
