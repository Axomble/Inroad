//go:build integration

package inbox_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/inbox"
)

// Two threads whose last_message_at fall in the SAME second, paged one at a
// time through the real handler and Postgres, exactly as the SPA pages: the
// next request's before_last_message_at is the previous page's last item's
// last_message_at, passed back verbatim. When that value was formatted to whole
// seconds it compared below the row it named, and the second thread — later in
// the same second's sub-second order but older than the first — was skipped.
func TestListThreadsPagesThroughThreadsInTheSameSecondAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	h := inbox.NewHandler(inbox.NewService(f.store))
	second := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	newer := plainThread(t, ctx, f, "Tie A", "a", second)
	older := plainThread(t, ctx, f, "Tie B", "b", second)
	for id, at := range map[uuid.UUID]time.Time{
		newer.ID: second.Add(500 * time.Millisecond),
		older.ID: second.Add(250*time.Millisecond + 7*time.Microsecond),
	} {
		if _, err := f.pool.Exec(ctx, `UPDATE inbox_threads SET last_message_at = $1 WHERE id = $2`, at, id); err != nil {
			t.Fatalf("set last_message_at: %v", err)
		}
	}

	type page struct {
		Items []struct {
			ID            string `json:"id"`
			LastMessageAt string `json:"last_message_at"`
		} `json:"items"`
	}
	list := func(query string) page {
		t.Helper()
		w := do(t, h, http.MethodGet, "/inbox/threads?limit=1"+query, "", bearer(t, f.ws))
		if w.Code != http.StatusOK {
			t.Fatalf("list: want 200, got %d: %s", w.Code, w.Body.String())
		}
		var p page
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return p
	}

	first := list("")
	if len(first.Items) != 1 || first.Items[0].ID != newer.ID.String() {
		t.Fatalf("page 1 = %+v, want the newer thread", first.Items)
	}
	if got, want := first.Items[0].LastMessageAt, "2026-09-01T12:00:00.5Z"; got != want {
		t.Errorf("last_message_at = %q, want full precision %q", got, want)
	}
	last := first.Items[0]
	second2 := list("&before_last_message_at=" + url.QueryEscape(last.LastMessageAt) + "&before_id=" + last.ID)
	if len(second2.Items) != 1 || second2.Items[0].ID != older.ID.String() {
		t.Fatalf("page 2 = %+v, want the older thread from the same second", second2.Items)
	}
	if got, want := second2.Items[0].LastMessageAt, "2026-09-01T12:00:00.250007Z"; got != want {
		t.Errorf("last_message_at = %q, want microsecond precision %q", got, want)
	}
}
