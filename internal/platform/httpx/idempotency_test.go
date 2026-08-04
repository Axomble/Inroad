package httpx_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/inroad/inroad/internal/platform/httpx"
)

// fakeRow mirrors the persisted idempotency_keys row shape.
type fakeRow struct {
	requestHash []byte
	statusCode  *int32
	body        []byte
	contentType string
}

// fakeIdempotencyStore is an in-memory httpx.IdempotencyStore fake — no DB, safe
// for concurrent use so the concurrent in-flight test (rule 4) exercises a real
// race between two goroutines rather than a pre-seeded state.
type fakeIdempotencyStore struct {
	mu   sync.Mutex
	rows map[string]fakeRow
}

func newFakeStore() *fakeIdempotencyStore {
	return &fakeIdempotencyStore{rows: map[string]fakeRow{}}
}

func rowKey(workspaceID, key string) string { return workspaceID + "|" + key }

func (f *fakeIdempotencyStore) TryInsert(_ context.Context, workspaceID, key string, requestHash []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := rowKey(workspaceID, key)
	if _, exists := f.rows[k]; exists {
		return false, nil
	}
	f.rows[k] = fakeRow{requestHash: requestHash}
	return true, nil
}

func (f *fakeIdempotencyStore) Get(_ context.Context, workspaceID, key string) (httpx.IdempotencyRecord, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[rowKey(workspaceID, key)]
	if !ok {
		return httpx.IdempotencyRecord{}, false, nil
	}
	return httpx.IdempotencyRecord{
		RequestHash:  row.requestHash,
		StatusCode:   row.statusCode,
		ResponseBody: row.body,
		ContentType:  row.contentType,
	}, true, nil
}

func (f *fakeIdempotencyStore) SetResponse(_ context.Context, workspaceID, key string, statusCode int, body []byte, contentType string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := rowKey(workspaceID, key)
	row := f.rows[k]
	sc := int32(statusCode)
	row.statusCode = &sc
	row.body = body
	row.contentType = contentType
	f.rows[k] = row
	return nil
}

func (f *fakeIdempotencyStore) rowCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rows)
}

func fixedWorkspace(workspaceID string) httpx.WorkspaceIDFunc {
	return func(*http.Request) (string, bool) { return workspaceID, true }
}

func noWorkspace() httpx.WorkspaceIDFunc {
	return func(*http.Request) (string, bool) { return "", false }
}

func countingHandler(calls *int32, status int, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(calls, 1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}

func jsonHandler(calls *int32, status int, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(calls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}

// alwaysConflictNeverFoundStore simulates a store-level inconsistency: TryInsert
// reports a conflict (as if another request already claimed the key), but Get
// then finds nothing for that key. This can never happen against Postgres under
// the documented semantics, so the middleware must fail closed rather than
// silently treat it as a fresh request.
type alwaysConflictNeverFoundStore struct{}

func (alwaysConflictNeverFoundStore) TryInsert(context.Context, string, string, []byte) (bool, error) {
	return false, nil
}

func (alwaysConflictNeverFoundStore) Get(context.Context, string, string) (httpx.IdempotencyRecord, bool, error) {
	return httpx.IdempotencyRecord{}, false, nil
}

func (alwaysConflictNeverFoundStore) SetResponse(context.Context, string, string, int, []byte, string) error {
	return nil
}

// Rule 1: no header, or a safe method (GET/HEAD), passes through untouched —
// the handler runs every time and the store is never consulted.
func TestPassesThroughWithoutIdempotencyKey(t *testing.T) {
	store := newFakeStore()
	var calls int32
	mw := httpx.Idempotency(store, fixedWorkspace("ws-1"))(countingHandler(&calls, http.StatusCreated, "ok"))

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/things", strings.NewReader(`{"a":1}`))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if calls != 1 {
		t.Fatalf("handler calls = %d, want 1", calls)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if n := store.rowCount(); n != 0 {
		t.Fatalf("store rows = %d, want 0 (no header must never touch the store)", n)
	}
}

func TestPassesThroughForSafeMethodsEvenWithHeader(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			store := newFakeStore()
			var calls int32
			mw := httpx.Idempotency(store, fixedWorkspace("ws-1"))(countingHandler(&calls, http.StatusOK, "ok"))

			req := httptest.NewRequestWithContext(context.Background(), method, "/things", http.NoBody)
			req.Header.Set("Idempotency-Key", "k1")
			rec := httptest.NewRecorder()
			mw.ServeHTTP(rec, req)

			if calls != 1 {
				t.Fatalf("handler calls = %d, want 1", calls)
			}
			if n := store.rowCount(); n != 0 {
				t.Fatalf("store rows = %d, want 0 (a safe method must never touch the store)", n)
			}
		})
	}
}

// Rule 2: a fresh key on a mutating request runs the handler once and records
// its status/body/content-type.
func TestFreshKeyRunsHandlerAndRecordsResponse(t *testing.T) {
	store := newFakeStore()
	var calls int32
	mw := httpx.Idempotency(store, fixedWorkspace("ws-1"))(jsonHandler(&calls, http.StatusCreated, `{"id":1}`))

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/things", strings.NewReader(`{"a":1}`))
	req.Header.Set("Idempotency-Key", "k1")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if calls != 1 {
		t.Fatalf("handler calls = %d, want 1", calls)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	if rec.Body.String() != `{"id":1}` {
		t.Fatalf("body = %q, want %q", rec.Body.String(), `{"id":1}`)
	}

	row, found, err := store.Get(context.Background(), "ws-1", "k1")
	if err != nil || !found {
		t.Fatalf("Get(ws-1, k1) = (_, %t, %v), want found", found, err)
	}
	if row.StatusCode == nil || *row.StatusCode != http.StatusCreated {
		t.Fatalf("recorded status = %v, want 201", row.StatusCode)
	}
	if string(row.ResponseBody) != `{"id":1}` {
		t.Fatalf("recorded body = %q, want %q", row.ResponseBody, `{"id":1}`)
	}
	if row.ContentType != "application/json" {
		t.Fatalf("recorded content-type = %q, want application/json", row.ContentType)
	}
}

// Rule 3: a conflicting row with the SAME hash and a recorded response replays
// it verbatim (status + body + content-type) and marks it as a replay, WITHOUT
// re-running the handler.
func TestReplaysStoredResponseForSameHash(t *testing.T) {
	store := newFakeStore()
	var calls int32
	mw := httpx.Idempotency(store, fixedWorkspace("ws-1"))(jsonHandler(&calls, http.StatusCreated, `{"id":1}`))

	body := `{"a":1}`
	first := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/things", strings.NewReader(body))
	first.Header.Set("Idempotency-Key", "k1")
	mw.ServeHTTP(httptest.NewRecorder(), first)

	second := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/things", strings.NewReader(body))
	second.Header.Set("Idempotency-Key", "k1")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, second)

	if calls != 1 {
		t.Fatalf("handler calls = %d, want 1 (replay must not re-run the handler)", calls)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("replayed status = %d, want 201", rec.Code)
	}
	if rec.Body.String() != `{"id":1}` {
		t.Fatalf("replayed body = %q, want %q", rec.Body.String(), `{"id":1}`)
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("replayed content-type = %q, want application/json", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatal(`expected "Idempotency-Replayed: true" header on replay`)
	}
}

// Rule 4: a second request for the SAME key while the first is still in flight
// (row inserted, no response recorded yet) is rejected 409 idempotency_in_flight
// — exercised with real concurrency, not a pre-seeded store state, so the fake
// store's own TryInsert race is what the middleware is actually racing against.
func TestConcurrentInFlightRequestReturns409(t *testing.T) {
	store := newFakeStore()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls int32

	slow := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		close(started)
		<-release
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("done"))
	})
	mw := httpx.Idempotency(store, fixedWorkspace("ws-1"))(slow)

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/things", strings.NewReader(`{"a":1}`))
		req.Header.Set("Idempotency-Key", "k1")
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		firstDone <- rec
	}()

	<-started // the first request's handler is now blocked mid-flight: row inserted, no response yet.

	req2 := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/things", strings.NewReader(`{"a":1}`))
	req2.Header.Set("Idempotency-Key", "k1")
	rec2 := httptest.NewRecorder()
	mw.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusConflict {
		t.Fatalf("concurrent status = %d, want 409", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), "idempotency_in_flight") {
		t.Fatalf("body = %q, want idempotency_in_flight", rec2.Body.String())
	}

	close(release)
	rec1 := <-firstDone
	if rec1.Code != http.StatusOK {
		t.Fatalf("original request status = %d, want 200", rec1.Code)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("handler calls = %d, want 1 (the concurrent request must not re-run it)", calls)
	}
}

// Rule 5: a conflicting row with a DIFFERENT hash (same key reused for a
// different request) is rejected 422 idempotency_key_reuse, without re-running
// the handler.
func TestDifferentHashReturns422KeyReuse(t *testing.T) {
	store := newFakeStore()
	var calls int32
	mw := httpx.Idempotency(store, fixedWorkspace("ws-1"))(jsonHandler(&calls, http.StatusCreated, "ok"))

	first := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/things", strings.NewReader(`{"a":1}`))
	first.Header.Set("Idempotency-Key", "k1")
	mw.ServeHTTP(httptest.NewRecorder(), first)

	second := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/things", strings.NewReader(`{"a":2}`))
	second.Header.Set("Idempotency-Key", "k1")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, second)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "idempotency_key_reuse") {
		t.Fatalf("body = %q, want idempotency_key_reuse", rec.Body.String())
	}
	if calls != 1 {
		t.Fatalf("handler calls = %d, want 1 (reuse must not re-run the handler)", calls)
	}
}

// The request hash must cover method, path AND body — varying any one of the
// three against an otherwise-identical replay must be treated as key reuse.
func TestHashCoversMethodPathAndBody(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"different method", http.MethodPut, "/things", `{"a":1}`},
		{"different path", http.MethodPost, "/other", `{"a":1}`},
		{"different body", http.MethodPost, "/things", `{"a":2}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			var calls int32
			mw := httpx.Idempotency(store, fixedWorkspace("ws-1"))(jsonHandler(&calls, http.StatusCreated, "ok"))

			first := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/things", strings.NewReader(`{"a":1}`))
			first.Header.Set("Idempotency-Key", "k1")
			mw.ServeHTTP(httptest.NewRecorder(), first)

			second := httptest.NewRequestWithContext(context.Background(), tc.method, tc.path, strings.NewReader(tc.body))
			second.Header.Set("Idempotency-Key", "k1")
			rec := httptest.NewRecorder()
			mw.ServeHTTP(rec, second)

			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422 (hash must differ when %s changes)", rec.Code, tc.name)
			}
		})
	}
}

// The 64 KiB cache cap: a response larger than the cap is still delivered to
// the ORIGINAL caller in full (pass-through, unrecorded), but a replay of that
// same key returns 409 idempotency_uncacheable rather than re-running the
// handler or replaying a truncated body.
func TestResponseOverCapPassesThroughUnrecorded(t *testing.T) {
	store := newFakeStore()
	big := strings.Repeat("x", 64*1024+1) // one byte over the 64 KiB cap
	var calls int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(big))
	})
	mw := httpx.Idempotency(store, fixedWorkspace("ws-1"))(handler)

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/things", strings.NewReader(`{"a":1}`))
	req.Header.Set("Idempotency-Key", "k1")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if calls != 1 {
		t.Fatalf("handler calls = %d, want 1", calls)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("original caller must still receive the real response: status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != len(big) {
		t.Fatalf("original caller must still receive the FULL body unrecorded: got %d bytes, want %d", rec.Body.Len(), len(big))
	}

	req2 := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/things", strings.NewReader(`{"a":1}`))
	req2.Header.Set("Idempotency-Key", "k1")
	rec2 := httptest.NewRecorder()
	mw.ServeHTTP(rec2, req2)

	if calls != 1 {
		t.Fatalf("handler re-ran on replay of an uncacheable key: calls = %d, want 1", calls)
	}
	if rec2.Code != http.StatusConflict {
		t.Fatalf("uncacheable replay status = %d, want 409", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), "idempotency_uncacheable") {
		t.Fatalf("body = %q, want idempotency_uncacheable", rec2.Body.String())
	}
}

// Missing principal inside the (supposedly already-authenticated) protected
// group is a programming error, not a normal user error — the middleware must
// fail closed rather than silently skip the idempotency guarantee.
func TestMissingPrincipalFailsClosed(t *testing.T) {
	store := newFakeStore()
	var calls int32
	mw := httpx.Idempotency(store, noWorkspace())(countingHandler(&calls, http.StatusOK, "ok"))

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/things", strings.NewReader(`{}`))
	req.Header.Set("Idempotency-Key", "k1")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if calls != 0 {
		t.Fatalf("handler ran despite a missing principal: calls = %d, want 0", calls)
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (missing principal inside the protected group is a programming error)", rec.Code)
	}
}

// A store that reports a conflict but then can't find the row it conflicted on
// is an invariant violation (this can't happen against Postgres under an
// ON CONFLICT DO NOTHING RETURNING + a same-key Get); the middleware must fail
// closed rather than silently treat it as a fresh request.
func TestConflictRowMissingFailsClosed(t *testing.T) {
	var calls int32
	mw := httpx.Idempotency(alwaysConflictNeverFoundStore{}, fixedWorkspace("ws-1"))(countingHandler(&calls, http.StatusOK, "ok"))

	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/things", strings.NewReader(`{}`))
	req.Header.Set("Idempotency-Key", "k1")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if calls != 0 {
		t.Fatalf("handler ran despite a store inconsistency: calls = %d, want 0", calls)
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (a conflict row must exist; a miss is a store inconsistency)", rec.Code)
	}
}
