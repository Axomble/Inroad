package contact

import (
	"context"
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
)

// serveExport runs one GET /contacts.csv through the real auth middleware, so
// the workspace these tests assert on is the one from the JWT, never a query
// param — matching serveSearch in handler_test.go.
func serveExport(t *testing.T, h *Handler, query string) *httptest.ResponseRecorder {
	t.Helper()
	tok, err := auth.IssueToken(testSecret, auth.Claims{
		UserID: uuid.NewString(), WorkspaceID: testWS.String(), Role: "owner", SessionID: uuid.NewString(),
	}, time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/?"+query, http.NoBody)
	r.Header.Set("Authorization", "Bearer "+tok)

	w := httptest.NewRecorder()
	auth.RequireAuth(auth.NewJWTVerifier(testSecret))(
		http.HandlerFunc(h.exportContactsCSV),
	).ServeHTTP(w, r)
	return w
}

func TestExportContactsCSVRequiresAuth(t *testing.T) {
	store := &fakeStore{searchRows: rows(3), countN: 3}
	w := httptest.NewRecorder()
	auth.RequireAuth(auth.NewJWTVerifier(testSecret))(
		http.HandlerFunc(newHandler(store, true).exportContactsCSV),
	).ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if len(store.searchCalls) != 0 {
		t.Fatal("an unauthenticated request reached the store")
	}
}

func TestExportContactsCSVStatusMapping(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		listExists bool
		want       int
	}{
		{"no params exports the whole workspace", "", true, http.StatusOK},
		{"search", "q=acme", true, http.StatusOK},
		{"one-character query", "q=a", true, http.StatusUnprocessableEntity},
		{"unknown sort", "sort=sideways", true, http.StatusUnprocessableEntity},
		{"list that is not a uuid", "list=nope", true, http.StatusUnprocessableEntity},
		{"unknown or cross-tenant list", "list=" + testList.String(), false, http.StatusNotFound},
		// cursor/limit are simply not read by this endpoint, so a client that
		// still sends them (e.g. a copy-pasted /contacts URL) must not be
		// rejected — it exports the whole filtered set instead of erroring.
		{"cursor and limit are silently ignored", "cursor=!!!&limit=0", true, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := serveExport(t, newHandler(&fakeStore{}, tc.listExists), tc.query)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

// The single most important property of this endpoint: an empty result must
// still produce a readable CSV with a header row, never a zero-byte file that
// reads to an operator as a failed download.
func TestExportContactsCSVEmptyResultStillEmitsHeader(t *testing.T) {
	store := &fakeStore{searchRows: nil}
	w := serveExport(t, newHandler(store, true), "q=nomatch")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	records, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("response body is not valid CSV: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %v, want exactly the header row", records)
	}
	want := []string{"email", "first_name", "last_name", "company"}
	for i := range want {
		if records[0][i] != want[i] {
			t.Fatalf("header = %v, want %v", records[0], want)
		}
	}
}

func TestExportContactsCSVHeadersAndContentType(t *testing.T) {
	w := serveExport(t, newHandler(&fakeStore{}, true), "")
	if got := w.Header().Get("Content-Type"); got != "text/csv; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/csv; charset=utf-8", got)
	}
	if got := w.Header().Get("Content-Disposition"); !strings.Contains(got, "attachment") || !strings.Contains(got, "contacts.csv") {
		t.Errorf("Content-Disposition = %q, want an attachment naming contacts.csv", got)
	}
}

// The list/q filter must reach the store exactly as it does for GET /contacts
// — proven the same way TestSearchCountUsesTheSameFilter is: by inspecting
// what the fake store was actually asked, since a fake cannot enforce a filter
// it doesn't implement.
func TestExportContactsCSVHonoursListAndQueryFilter(t *testing.T) {
	store := &fakeStore{}
	serveExport(t, newHandler(store, true), "q=acme&list="+testList.String())
	if store.lastSearch.Filter.Query != "acme" {
		t.Errorf("filter query = %q, want acme", store.lastSearch.Filter.Query)
	}
	if store.lastSearch.Filter.ListID == nil || *store.lastSearch.Filter.ListID != testList {
		t.Errorf("filter list = %v, want %v", store.lastSearch.Filter.ListID, testList)
	}
}

// A value containing a comma, a quote and a newline must round-trip through
// encoding/csv's quoting rather than corrupt the row it sits in — the
// assertion the brief calls out so a future hand-rolled writer cannot
// regress it silently.
func TestExportContactsCSVQuotesAwkwardValues(t *testing.T) {
	awkward := "Acme, Inc. \"The Rocket Co\"\nSecond line"
	store := &fakeStore{searchRows: []SearchRow{
		{ID: uuid.New(), Email: "a@x.test", FirstName: "Alice", LastName: "A", Company: awkward},
	}}
	w := serveExport(t, newHandler(store, true), "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	records, err := csv.NewReader(strings.NewReader(w.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("response body is not valid CSV: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %v, want a header and one data row", records)
	}
	if records[1][3] != awkward {
		t.Fatalf("company cell = %q, want %q", records[1][3], awkward)
	}
}
