package inbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/app/inbox"
	"github.com/inroad/inroad/internal/platform/cursor"
	"github.com/inroad/inroad/internal/platform/throttle"
)

// fakeSearchStore serves canned hits per workspace, newest first, and applies
// the keyset the way the real query does — so paging is exercised end to end
// through the Service and handler. It records the last filter it was handed.
type fakeSearchStore struct {
	hits map[uuid.UUID][]inbox.SearchHit
	last inbox.SearchFilter
	err  error
}

func (f *fakeSearchStore) SearchThreads(_ context.Context, ws uuid.UUID, filter inbox.SearchFilter) ([]inbox.SearchHit, error) {
	f.last = filter
	if f.err != nil {
		return nil, f.err
	}
	var out []inbox.SearchHit
	for _, h := range f.hits[ws] {
		if filter.BeforeLastMessageAt != nil {
			at, id := *filter.BeforeLastMessageAt, *filter.BeforeID
			older := h.Thread.LastMessageAt.Before(at) ||
				(h.Thread.LastMessageAt.Equal(at) && h.Thread.ID.String() < id.String())
			if !older {
				continue
			}
		}
		out = append(out, h)
		if int32(len(out)) == filter.Limit {
			break
		}
	}
	return out, nil
}

// seedHits gives ws n hits, newest first. They sit a millisecond apart WITHIN
// one second, so a cursor that dropped sub-second precision would visibly skip
// rows (the times are distinct, so the id tiebreak never decides).
func seedHits(store *fakeSearchStore, ws uuid.UUID, n int) []inbox.SearchHit {
	if store.hits == nil {
		store.hits = map[uuid.UUID][]inbox.SearchHit{}
	}
	base := time.Date(2026, 9, 1, 12, 0, 0, 900_000_000, time.UTC)
	hits := make([]inbox.SearchHit, n)
	for i := range hits {
		hits[i] = inbox.SearchHit{
			Thread:         inbox.Thread{ID: uuid.New(), WorkspaceID: ws, MailboxID: uuid.New(), Subject: "Re: pricing", LastMessageAt: base.Add(-time.Duration(i) * time.Millisecond)},
			MatchedInbound: true,
			Snippet: &inbox.SearchSnippet{
				Direction: "inbound", OccurredAt: base,
				Subject: []inbox.HighlightSegment{{Text: "Re: "}, {Text: "pricing", Match: true}},
				Body:    []inbox.HighlightSegment{{Text: "our "}, {Text: "pricing", Match: true}},
			},
		}
	}
	store.hits[ws] = hits
	return hits
}

func searchService(store *fakeSearchStore) *inbox.Service {
	return inbox.NewService(newFakeStore(), inbox.WithSearchStore(store))
}

type searchPageJSON struct {
	Items []struct {
		Thread struct {
			ID     string `json:"id"`
			Labels []any  `json:"labels"`
		} `json:"thread"`
		MatchedLegs []string `json:"matched_legs"`
		Snippet     *struct {
			Direction  string `json:"direction"`
			OccurredAt string `json:"occurred_at"`
			Subject    []struct {
				Text  string `json:"text"`
				Match bool   `json:"match"`
			} `json:"subject"`
			Body []struct {
				Text  string `json:"text"`
				Match bool   `json:"match"`
			} `json:"body"`
		} `json:"snippet"`
	} `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

func decodeSearchPage(t *testing.T, body []byte) searchPageJSON {
	t.Helper()
	var page searchPageJSON
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	return page
}

func TestSearchRequiresAuth(t *testing.T) {
	h := inbox.NewHandler(searchService(&fakeSearchStore{}))
	if w := do(t, h, http.MethodGet, "/inbox/search?q=x", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestSearchRejectsBadQueriesWith400(t *testing.T) {
	store := &fakeSearchStore{}
	h := inbox.NewHandler(searchService(store))
	for name, target := range map[string]string{
		"missing q":           "/inbox/search",
		"blank q":             "/inbox/search?q=%20%20",
		"too long":            "/inbox/search?q=" + strings.Repeat("a", inbox.MaxSearchQueryLength+1),
		"NUL":                 "/inbox/search?q=a%00b",
		"invalid UTF-8":       "/inbox/search?q=a%FFb",
		"garbage cursor":      "/inbox/search?q=x&cursor=not-a-cursor",
		"backward cursor":     "/inbox/search?q=x&cursor=" + cursor.NewTime(cursor.SortNewest, cursor.Before, time.Now(), uuid.New()).Encode(),
		"other sort's cursor": "/inbox/search?q=x&cursor=" + cursor.NewEmail(cursor.After, "a@b.c", uuid.New()).Encode(),
		"bad mailbox":         "/inbox/search?q=x&mailbox_id=nope",
		"bad scope":           "/inbox/search?q=x&scope=everything",
		"bad limit":           "/inbox/search?q=x&limit=0",
	} {
		t.Run(name, func(t *testing.T) {
			store.last = inbox.SearchFilter{}
			w := serve(t, h, http.MethodGet, target, "")
			if w.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d: %s", w.Code, w.Body.String())
			}
			if store.last.Text != "" {
				t.Fatalf("store was called for a rejected request: %+v", store.last)
			}
		})
	}
}

// The contract shape the frontend builds against, including a round trip of
// next_cursor into the following page.
func TestSearchReturnsTheContractShapeAndPages(t *testing.T) {
	store := &fakeSearchStore{}
	seeded := seedHits(store, testWS, 3)
	h := inbox.NewHandler(searchService(store))

	w := serve(t, h, http.MethodGet, "/inbox/search?q=%20pricing%20&limit=2", "")
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if store.last.Text != "pricing" {
		t.Errorf("store got text %q, want the trimmed query", store.last.Text)
	}
	if store.last.Limit != 3 {
		t.Errorf("store got limit %d, want page size + 1 (the has-more probe)", store.last.Limit)
	}
	page := decodeSearchPage(t, w.Body.Bytes())
	if len(page.Items) != 2 || page.NextCursor == nil {
		t.Fatalf("page = %+v, want 2 items and a next_cursor", page)
	}
	first := page.Items[0]
	if first.Thread.ID != seeded[0].Thread.ID.String() || first.Thread.Labels == nil {
		t.Errorf("thread = %+v, want the newest hit with labels as []", first.Thread)
	}
	if len(first.MatchedLegs) != 1 || first.MatchedLegs[0] != "inbound" {
		t.Errorf("matched_legs = %v, want [inbound]", first.MatchedLegs)
	}
	if first.Snippet == nil || first.Snippet.Direction != "inbound" || len(first.Snippet.Body) != 2 || !first.Snippet.Body[1].Match {
		t.Errorf("snippet = %+v, want the highlighted body segments", first.Snippet)
	}
	if _, err := time.Parse(time.RFC3339, first.Snippet.OccurredAt); err != nil {
		t.Errorf("occurred_at %q is not RFC3339: %v", first.Snippet.OccurredAt, err)
	}

	w = serve(t, h, http.MethodGet, "/inbox/search?q=pricing&limit=2&cursor="+url.QueryEscape(*page.NextCursor), "")
	if w.Code != http.StatusOK {
		t.Fatalf("page 2: want 200, got %d: %s", w.Code, w.Body.String())
	}
	// The cursor must carry the sub-second last_message_at exactly: a
	// seconds-truncated key would compare below the row it names and skip
	// every row in the rest of that second.
	if store.last.BeforeLastMessageAt == nil || !store.last.BeforeLastMessageAt.Equal(seeded[1].Thread.LastMessageAt) {
		t.Errorf("page 2 keyset = %v, want the full-precision last_message_at of page 1's last row", store.last.BeforeLastMessageAt)
	}
	page2 := decodeSearchPage(t, w.Body.Bytes())
	if len(page2.Items) != 1 || page2.Items[0].Thread.ID != seeded[2].Thread.ID.String() || page2.NextCursor != nil {
		t.Fatalf("page 2 = %+v, want only the last hit and no next_cursor", page2)
	}
}

func TestSearchReportsBothLegsInFixedOrderAndNullSnippet(t *testing.T) {
	store := &fakeSearchStore{hits: map[uuid.UUID][]inbox.SearchHit{testWS: {{
		Thread:         inbox.Thread{ID: uuid.New(), WorkspaceID: testWS, LastMessageAt: time.Now()},
		MatchedInbound: true, MatchedOutbound: true,
	}}}}
	h := inbox.NewHandler(searchService(store))
	w := serve(t, h, http.MethodGet, "/inbox/search?q=x", "")
	page := decodeSearchPage(t, w.Body.Bytes())
	if len(page.Items) != 1 {
		t.Fatalf("items = %+v", page.Items)
	}
	if legs := page.Items[0].MatchedLegs; len(legs) != 2 || legs[0] != "inbound" || legs[1] != "outbound" {
		t.Errorf("matched_legs = %v, want [inbound outbound]", legs)
	}
	if page.Items[0].Snippet != nil {
		t.Errorf("snippet = %+v, want null", page.Items[0].Snippet)
	}
}

// An address match is reported as the "contact" leg, after the text legs.
func TestSearchReportsAnAddressMatchAsTheContactLeg(t *testing.T) {
	for name, tc := range map[string]struct {
		hit  inbox.SearchHit
		want []string
	}{
		"address only": {inbox.SearchHit{MatchedAddress: true}, []string{"contact"}},
		"all three":    {inbox.SearchHit{MatchedInbound: true, MatchedOutbound: true, MatchedAddress: true}, []string{"inbound", "outbound", "contact"}},
	} {
		t.Run(name, func(t *testing.T) {
			tc.hit.Thread = inbox.Thread{ID: uuid.New(), WorkspaceID: testWS, LastMessageAt: time.Now()}
			store := &fakeSearchStore{hits: map[uuid.UUID][]inbox.SearchHit{testWS: {tc.hit}}}
			w := serve(t, inbox.NewHandler(searchService(store)), http.MethodGet, "/inbox/search?q=jo@acme.test", "")
			page := decodeSearchPage(t, w.Body.Bytes())
			if len(page.Items) != 1 || strings.Join(page.Items[0].MatchedLegs, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("items = %+v, want matched_legs %v", page.Items, tc.want)
			}
		})
	}
}

// The workspace comes from the JWT, never the request: another workspace's
// hits are invisible even when present in the same store.
func TestSearchIsScopedToTheCallersWorkspace(t *testing.T) {
	store := &fakeSearchStore{}
	seedHits(store, uuid.New(), 2)
	h := inbox.NewHandler(searchService(store))
	w := serve(t, h, http.MethodGet, "/inbox/search?q=pricing&workspace_id="+uuid.NewString(), "")
	if page := decodeSearchPage(t, w.Body.Bytes()); len(page.Items) != 0 {
		t.Fatalf("items = %+v, want none from a foreign workspace", page.Items)
	}
}

// Search finds snoozed threads unless the operator is IN the snoozed scope —
// the same rule the list's ?q= follows — and passes every other scope through.
func TestSearchScopesAndSnoozeRule(t *testing.T) {
	store := &fakeSearchStore{}
	h := inbox.NewHandler(searchService(store))

	serve(t, h, http.MethodGet, "/inbox/search?q=x", "")
	if store.last.SnoozeHidden || store.last.SnoozedOnly {
		t.Errorf("unscoped search filter = %+v, want snoozed threads included", store.last)
	}
	serve(t, h, http.MethodGet, "/inbox/search?q=x&scope=unread", "")
	if !store.last.UnreadOnly || store.last.SnoozeHidden {
		t.Errorf("unread search filter = %+v, want unread-only with snoozed threads included", store.last)
	}
	serve(t, h, http.MethodGet, "/inbox/search?q=x&scope=snoozed", "")
	if !store.last.SnoozedOnly {
		t.Errorf("snoozed search filter = %+v, want snoozed-only", store.last)
	}
	mailbox := uuid.New()
	serve(t, h, http.MethodGet, "/inbox/search?q=x&mailbox_id="+mailbox.String()+"&reply_class=positive", "")
	if store.last.MailboxID == nil || *store.last.MailboxID != mailbox || store.last.ReplyClass == nil || *store.last.ReplyClass != "positive" {
		t.Errorf("faceted search filter = %+v, want mailbox and reply_class passed through", store.last)
	}
	if store.last.Query != "" {
		t.Errorf("search filter carried a substring query %q", store.last.Query)
	}
}

// Each bounded-work refusal the store can raise has its own status, so the UI
// can tell "fix your query" (400), "narrow your query" (422) and "the server
// gave up" (503) apart without parsing the body — and none of them is a 500.
func TestSearchStoreRefusalsMapToDistinctStatuses(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"not selective": {inbox.ErrSearchNotSelective, http.StatusBadRequest},
		"too broad":     {inbox.ErrSearchTooBroad, http.StatusUnprocessableEntity},
		"timed out":     {fmt.Errorf("%w (%w)", inbox.ErrSearchTimeout, errors.New("57014")), http.StatusServiceUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			h := inbox.NewHandler(searchService(&fakeSearchStore{err: tc.err}))
			w := serve(t, h, http.MethodGet, "/inbox/search?q=x", "")
			if w.Code != tc.want {
				t.Fatalf("want %d, got %d: %s", tc.want, w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "57014") {
				t.Errorf("body %q leaks the wrapped database error", w.Body.String())
			}
		})
	}
}

// countingLimiter allows `limit` requests per key, then refuses. It records
// every key it was asked about.
type countingLimiter struct {
	seen map[string]int
}

func (l *countingLimiter) Allow(_ context.Context, key string, limit int, _ time.Duration) (bool, error) {
	l.seen[key]++
	return l.seen[key] <= limit, nil
}

// The search route sits behind its throttle, keyed per WORKSPACE: a workspace
// over its cap is refused with 429 before the store is reached, while another
// workspace on the same IP is unaffected.
func TestSearchIsThrottledPerWorkspace(t *testing.T) {
	store := &fakeSearchStore{}
	h := inbox.NewHandler(searchService(store))
	limiter := &countingLimiter{seen: map[string]int{}}
	searchThrottle := throttle.Config{
		Limiter: limiter, Window: time.Minute, IPLimit: 100, AcctLimit: 2,
		AcctKey: func(r *http.Request) string {
			p, ok := auth.UserFromContext(r.Context())
			if !ok {
				return ""
			}
			return p.WorkspaceID
		},
	}.Middleware("inbox-search")
	root := chi.NewRouter()
	root.Mount("/inbox", h.Routes(inbox.RouteThrottles{Search: searchThrottle}))
	get := func(ws uuid.UUID) int {
		r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/inbox/search?q=pricing", http.NoBody)
		r.Header.Set("Authorization", bearer(t, ws))
		w := httptest.NewRecorder()
		auth.RequireAuth(auth.NewJWTVerifier(testSecret))(root).ServeHTTP(w, r)
		return w.Code
	}

	for i := 0; i < 2; i++ {
		if code := get(testWS); code != http.StatusOK {
			t.Fatalf("request %d: want 200 under the cap, got %d", i+1, code)
		}
	}
	store.last = inbox.SearchFilter{}
	if code := get(testWS); code != http.StatusTooManyRequests {
		t.Fatalf("third request: want 429, got %d", code)
	}
	if store.last.Text != "" {
		t.Fatal("a throttled search still reached the store")
	}
	if code := get(uuid.New()); code != http.StatusOK {
		t.Fatalf("another workspace: want 200, got %d", code)
	}
	if limiter.seen["inbox-search:acct:"+testWS.String()] != 3 {
		t.Errorf("limiter keys = %v, want the caller's workspace counted", limiter.seen)
	}
}

func TestSearchStoreErrorIs500(t *testing.T) {
	store := &fakeSearchStore{err: errors.New("boom")}
	h := inbox.NewHandler(searchService(store))
	if w := serve(t, h, http.MethodGet, "/inbox/search?q=x", ""); w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", w.Code)
	}
}

func TestSearchThreadsServiceValidation(t *testing.T) {
	ctx := context.Background()
	svc := searchService(&fakeSearchStore{})
	at, id := time.Now(), uuid.New()
	for name, filter := range map[string]inbox.SearchFilter{
		"empty text":        {},
		"substring query":   {Text: "x", ListFilter: inbox.ListFilter{Query: "y"}},
		"half-set cursor":   {Text: "x", ListFilter: inbox.ListFilter{BeforeLastMessageAt: &at}},
		"half-set cursor 2": {Text: "x", ListFilter: inbox.ListFilter{BeforeID: &id}},
		"snooze conflict":   {Text: "x", ListFilter: inbox.ListFilter{SnoozeHidden: true, SnoozedOnly: true}},
	} {
		if _, err := svc.SearchThreads(ctx, testWS, filter); !errors.Is(err, inbox.ErrValidation) {
			t.Errorf("%s: err = %v, want ErrValidation", name, err)
		}
	}
}

func TestSearchThreadsWithoutAStoreFailsLoudly(t *testing.T) {
	svc := inbox.NewService(newFakeStore())
	if _, err := svc.SearchThreads(context.Background(), testWS, inbox.SearchFilter{Text: "x"}); err == nil || errors.Is(err, inbox.ErrValidation) {
		t.Fatalf("err = %v, want a non-validation wiring error", err)
	}
}
