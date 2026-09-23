package auditlog

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// fakeStore honours the parts of ListAuditEvents a page walk depends on — the
// workspace pin, the action prefix, the keyset seek, ordering and the limit —
// so paging and isolation are exercised rather than assumed. It records the
// last params so filter mapping can be asserted.
type fakeStore struct {
	rows []gen.ListAuditEventsRow
	last gen.ListAuditEventsParams
	err  error
}

func (f *fakeStore) List(_ context.Context, p gen.ListAuditEventsParams) ([]gen.ListAuditEventsRow, error) {
	f.last = p
	if f.err != nil {
		return nil, f.err
	}
	var out []gen.ListAuditEventsRow
	for _, r := range f.rows {
		if r.WorkspaceID != p.WorkspaceID {
			continue
		}
		if p.ActionPrefix != "" && r.Action != p.ActionPrefix && !strings.HasPrefix(r.Action, p.ActionPrefix+".") {
			continue
		}
		if p.Seek && !before(r, p.CursorTime.Time, p.CursorID) {
			continue
		}
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b gen.ListAuditEventsRow) int {
		if c := b.CreatedAt.Time.Compare(a.CreatedAt.Time); c != 0 {
			return c
		}
		return strings.Compare(b.ID.String(), a.ID.String())
	})
	if len(out) > int(p.PageLimit) {
		out = out[:p.PageLimit]
	}
	return out, nil
}

// before reports (r.created_at, r.id) < (t, id), the query's row compare.
func before(r gen.ListAuditEventsRow, t time.Time, id uuid.UUID) bool {
	if !r.CreatedAt.Time.Equal(t) {
		return r.CreatedAt.Time.Before(t)
	}
	return r.ID.String() < id.String()
}

func row(ws uuid.UUID, action string, at time.Time) gen.ListAuditEventsRow {
	return gen.ListAuditEventsRow{
		ID: uuid.New(), WorkspaceID: ws, Action: action, ActorType: "user",
		Metadata: []byte("{}"), CreatedAt: pgtype.Timestamptz{Time: at, Valid: true},
	}
}

func TestListWalksEveryPageExactlyOnce(t *testing.T) {
	ws := uuid.New()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{}
	for i := range 7 {
		// Pairs share a timestamp so the id tiebreak is exercised.
		store.rows = append(store.rows, row(ws, "campaign.paused", base.Add(time.Duration(i/2)*time.Minute)))
	}
	svc := NewService(store)

	seen := map[uuid.UUID]bool{}
	cursor := ""
	pages := 0
	for {
		page, err := svc.List(context.Background(), ws, ListInput{Cursor: cursor, Limit: 3})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		pages++
		for _, e := range page.Events {
			if seen[e.ID] {
				t.Fatalf("event %s served twice", e.ID)
			}
			seen[e.ID] = true
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != 7 || pages != 3 {
		t.Fatalf("saw %d events over %d pages, want 7 over 3", len(seen), pages)
	}
}

func TestListIsPinnedToTheCallersWorkspace(t *testing.T) {
	mine, theirs := uuid.New(), uuid.New()
	now := time.Now()
	store := &fakeStore{rows: []gen.ListAuditEventsRow{row(mine, "auth.login", now), row(theirs, "auth.login", now)}}

	page, err := NewService(store).List(context.Background(), mine, ListInput{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if store.last.WorkspaceID != mine {
		t.Fatalf("store queried workspace %s, want the caller's %s", store.last.WorkspaceID, mine)
	}
	if len(page.Events) != 1 || page.Events[0].WorkspaceID != mine {
		t.Fatalf("got %d events, want only the caller's one", len(page.Events))
	}
}

func TestListMapsFilters(t *testing.T) {
	uid := uuid.New()
	since := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	until := since.Add(24 * time.Hour)
	store := &fakeStore{}
	_, err := NewService(store).List(context.Background(), uuid.New(), ListInput{Filter: Filter{
		ActionPrefix: "member", ActorType: "api_key", ActorID: "k1", ActorUserID: &uid,
		TargetType: "campaign", TargetID: "c1", Since: &since, Until: &until,
	}})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	p := store.last
	if p.ActionPrefix != "member" || p.ActorType != "api_key" || p.ActorID != "k1" ||
		p.TargetType != "campaign" || p.TargetID != "c1" ||
		!p.ActorUserID.Valid || uuid.UUID(p.ActorUserID.Bytes) != uid ||
		!p.Since.Time.Equal(since) || !p.Until.Time.Equal(until) {
		t.Fatalf("params = %+v", p)
	}
	if p.PageLimit != defaultLimit+1 {
		t.Fatalf("page limit = %d, want default+1", p.PageLimit)
	}
}

func TestListCapsTheLimit(t *testing.T) {
	store := &fakeStore{}
	if _, err := NewService(store).List(context.Background(), uuid.New(), ListInput{Limit: 10_000}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if store.last.PageLimit != maxLimit+1 {
		t.Fatalf("page limit = %d, want max+1", store.last.PageLimit)
	}
}

func TestListRejectsACursorFromADifferentFilter(t *testing.T) {
	ws := uuid.New()
	store := &fakeStore{}
	for i := range 3 {
		store.rows = append(store.rows, row(ws, "campaign.paused", time.Now().Add(time.Duration(i)*time.Second)))
	}
	svc := NewService(store)
	page, err := svc.List(context.Background(), ws, ListInput{Filter: Filter{ActionPrefix: "campaign"}, Limit: 1})
	if err != nil || page.NextCursor == "" {
		t.Fatalf("first page: %v, cursor %q", err, page.NextCursor)
	}
	_, err = svc.List(context.Background(), ws, ListInput{Filter: Filter{ActionPrefix: "auth"}, Cursor: page.NextCursor, Limit: 1})
	if !errors.Is(err, ErrBadCursor) {
		t.Fatalf("replayed under another filter: err = %v, want ErrBadCursor", err)
	}
	if _, err := svc.List(context.Background(), ws, ListInput{Cursor: "garbage!"}); !errors.Is(err, ErrBadCursor) {
		t.Fatalf("garbage cursor: err = %v, want ErrBadCursor", err)
	}
}

func TestListRejectsBadFilters(t *testing.T) {
	since := time.Now()
	for name, f := range map[string]Filter{
		"like wildcard in prefix": {ActionPrefix: "campaign%"},
		"uppercase prefix":        {ActionPrefix: "Campaign"},
		"trailing dot":            {ActionPrefix: "campaign."},
		"unknown actor type":      {ActorType: "robot"},
		"inverted range":          {Since: &since, Until: &since},
		"oversized target":        {TargetID: strings.Repeat("x", 201)},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewService(&fakeStore{}).List(context.Background(), uuid.New(), ListInput{Filter: f})
			if !errors.Is(err, ErrInvalidFilter) {
				t.Fatalf("err = %v, want ErrInvalidFilter", err)
			}
		})
	}
}

func TestListPropagatesStoreErrors(t *testing.T) {
	boom := errors.New("db down")
	_, err := NewService(&fakeStore{err: boom}).List(context.Background(), uuid.New(), ListInput{})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapped store error", err)
	}
}
