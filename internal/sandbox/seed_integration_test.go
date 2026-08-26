//go:build integration

package sandbox_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/app/inbox"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/sandbox"
)

// seedScratch brings up an isolated database, migrates it, seeds a workspace
// through the harness, and returns everything the assertions need.
//
// It uses ScratchDSN rather than the shared test database on purpose: the
// shared one is migrated to whatever version the working tree has, and a
// sandbox run has to be verifiable on a branch whose migration set differs
// from whatever else migrated that database last.
func seedScratch(t *testing.T, name string, now time.Time) (*gen.Queries, uuid.UUID, sandbox.Result, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	dsn := dbtest.ScratchDSN(t, name)
	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	q := gen.New(pool)
	ws, err := q.CreateWorkspace(ctx, "Sandbox IT")
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}

	store := sandbox.NewPgStore(pool)
	res, err := sandbox.NewSeeder(q, store, store).Seed(ctx, sandbox.SeedInput{
		WorkspaceID: ws.ID,
		Contacts:    sandbox.DefaultContacts,
		Options:     sandbox.Options{Now: now, Window: 21 * 24 * time.Hour},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return q, ws.ID, res, pool
}

// This is the test the whole harness exists to make possible, and the one that
// catches the failure mode a fake store cannot: a seeded workspace that looks
// correct in the database but renders wrong in the product.
//
// It seeds a real workspace and then reads it back through the REAL inbox
// domain, because the outbound leg of a thread is synthesized at read time by
// joining sends to sequence_steps. Nothing short of the actual reader proves
// that join lands.
func TestSeededWorkspaceRendersThroughTheInbox(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	_, wsID, res, pool := seedScratch(t, "inbox", now)
	if res.Threads == 0 {
		t.Fatal("seed produced no threads")
	}
	t.Logf("seeded: %s", res)

	svc := inbox.NewService(inbox.NewPgStore(pool))

	page, err := svc.ListThreads(ctx, wsID, inbox.ListFilter{Limit: inbox.MaxThreadPageLimit})
	if err != nil {
		t.Fatalf("list threads: %v", err)
	}
	if len(page.Items) != res.Threads {
		t.Errorf("inbox lists %d threads, seed reported %d", len(page.Items), res.Threads)
	}

	var withOutbound, withInbound int
	for _, th := range page.Items {
		// A thread the contact join missed would render with no name at all.
		if th.ContactEmail == "" {
			t.Errorf("thread %s has no joined contact", th.ID)
		}
		if th.LastMessageAt.After(now.Add(time.Minute)) {
			t.Errorf("thread %s is dated in the future (%v)", th.ID, th.LastMessageAt)
		}
		// The reply label has to resolve, or the UI falls back to a raw key.
		if th.ReplyLabel == nil {
			t.Errorf("thread %s reply class %q resolved to no label", th.ID, th.LastReplyClass)
		}

		detail, err := svc.GetThread(ctx, wsID, th.ID)
		if err != nil {
			t.Fatalf("get thread %s: %v", th.ID, err)
		}

		var inbound, outbound int
		var lastAt time.Time
		for _, m := range detail.Messages {
			switch m.Direction {
			case "inbound":
				inbound++
			case "outbound":
				outbound++
				// The synthesized leg must carry real content; an empty body
				// or subject means the sequence_steps join missed.
				if m.BodyText == "" || m.Subject == "" {
					t.Errorf("thread %s: synthesized outbound message is blank (subject=%q)", th.ID, m.Subject)
				}
			default:
				t.Errorf("thread %s: unexpected direction %q", th.ID, m.Direction)
			}
			// The reader merges both legs by occurred_at; out-of-order
			// messages would render the conversation scrambled.
			if !lastAt.IsZero() && m.OccurredAt.Before(lastAt) {
				t.Errorf("thread %s: messages are not in chronological order", th.ID)
			}
			lastAt = m.OccurredAt
		}

		if inbound == 0 {
			t.Errorf("thread %s has no inbound message", th.ID)
		} else {
			withInbound++
		}
		// THE assertion: every seeded thread must show the campaign messages
		// that preceded the reply, synthesized from `sends`. If the harness
		// had written the outbound leg into inbox_messages instead, this
		// would still pass — but the duplicate check below would not.
		if outbound == 0 {
			t.Errorf("thread %s shows a reply with no outbound conversation above it", th.ID)
		} else {
			withOutbound++
		}

		// No duplicate outbound message: writing the outbound leg into
		// inbox_messages as well as leaving it to be synthesized would render
		// each campaign message twice.
		seen := map[string]bool{}
		for _, m := range detail.Messages {
			if m.Direction != "outbound" {
				continue
			}
			if seen[m.MessageID] {
				t.Errorf("thread %s: outbound message %s appears twice", th.ID, m.MessageID)
			}
			seen[m.MessageID] = true
		}
	}

	if withOutbound != len(page.Items) || withInbound != len(page.Items) {
		t.Errorf("of %d threads, %d had inbound and %d had outbound legs",
			len(page.Items), withInbound, withOutbound)
	}
}

// The seeded state must also satisfy the awaiting-reply rule, which spans BOTH
// legs (inbox_messages and sends). A rule evaluated over one leg only would be
// wrong here, and a fake store would happily agree with it.
func TestSeededThreadsAwaitingReplyScopeIsCoherent(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	_, wsID, _, pool := seedScratch(t, "await", now)

	svc := inbox.NewService(inbox.NewPgStore(pool))
	all, err := svc.ListThreads(ctx, wsID, inbox.ListFilter{Limit: inbox.MaxThreadPageLimit})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	awaiting, err := svc.ListThreads(ctx, wsID, inbox.ListFilter{
		AwaitingReplyOnly: true, Limit: inbox.MaxThreadPageLimit,
	})
	if err != nil {
		t.Fatalf("list awaiting: %v", err)
	}

	// Every seeded thread ends with the prospect's reply — the sequence stops
	// there, so no campaign send follows it — which means the whole seeded
	// inbox is waiting on the operator.
	if len(awaiting.Items) != len(all.Items) {
		t.Errorf("awaiting-reply scope holds %d of %d threads; every seeded thread ends with the contact speaking last",
			len(awaiting.Items), len(all.Items))
	}

	// And the overview counter must agree with the list it links to.
	ov, err := svc.GetOverview(ctx, wsID, inbox.OverviewWindow{
		TodayStart: now.Truncate(24 * time.Hour), WeekStart: now.Add(-7 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if int(ov.AwaitingReply) != len(awaiting.Items) {
		t.Errorf("overview counts %d awaiting, list returned %d", ov.AwaitingReply, len(awaiting.Items))
	}
}
