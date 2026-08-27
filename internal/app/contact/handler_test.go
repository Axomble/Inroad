package contact

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/cursor"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef")

// serveSearch runs one GET /contacts through the real auth middleware, so the
// workspace these tests assert on is the one from the JWT — never a query
// param. Everything after that is query-string parsing and error mapping.
func serveSearch(t *testing.T, h *Handler, query string) *httptest.ResponseRecorder {
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
		http.HandlerFunc(h.listContacts),
	).ServeHTTP(w, r)
	return w
}

// Without a token the route must never reach the store, let alone return rows.
func TestListContactsRequiresAuth(t *testing.T) {
	store := &fakeStore{searchRows: rows(3), countN: 3}
	w := httptest.NewRecorder()
	auth.RequireAuth(auth.NewJWTVerifier(testSecret))(
		http.HandlerFunc(newHandler(store, true).listContacts),
	).ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if store.lastSearch.Limit != 0 {
		t.Fatal("an unauthenticated request reached the store")
	}
}

func newHandler(store *fakeStore, exists bool) *Handler {
	return NewHandler(NewService(store, &fakeChecker{exists: exists}, &fakeFieldStore{}))
}

func TestListContactsStatusMapping(t *testing.T) {
	goodCursor := cursor.NewEmail(cursor.After, "a@x.test", uuid.New()).Encode()

	tests := []struct {
		name       string
		query      string
		listExists bool
		want       int
	}{
		{"no params is the whole workspace", "", true, http.StatusOK},
		{"search", "q=acme", true, http.StatusOK},
		{"one-character query", "q=a", true, http.StatusUnprocessableEntity},
		{"limit below range", "limit=0", true, http.StatusUnprocessableEntity},
		{"limit above range", "limit=101", true, http.StatusUnprocessableEntity},
		{"limit at the top of the range", "limit=100", true, http.StatusOK},
		{"non-numeric limit", "limit=lots", true, http.StatusUnprocessableEntity},
		{"unknown sort", "sort=sideways", true, http.StatusUnprocessableEntity},
		{"list that is not a uuid", "list=nope", true, http.StatusUnprocessableEntity},
		{"unknown or cross-tenant list", "list=" + testList.String(), false, http.StatusNotFound},
		{"malformed cursor", "cursor=!!!", true, http.StatusBadRequest},
		{"cursor from another sort", "cursor=" + goodCursor, true, http.StatusBadRequest},
		{"cursor with its own sort", "sort=email&cursor=" + goodCursor, true, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := serveSearch(t, newHandler(&fakeStore{}, tc.listExists), tc.query)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

// The response must match the committed ContactPage schema exactly: absent
// neighbours are explicit nulls, not omitted keys, so the client can tell "no
// next page" from "the field is missing".
func TestListContactsResponseShape(t *testing.T) {
	searchRows := rows(1)
	companyID := uuid.New()
	searchRows[0].LastName = "Customer"
	searchRows[0].CompanyID = &companyID
	searchRows[0].CompanyName = "Acme"
	searchRows[0].JobTitle = "VP Sales"
	searchRows[0].LinkedInURL = "https://linkedin.com/in/customer"
	searchRows[0].DealCount = 2
	w := serveSearch(t, newHandler(&fakeStore{searchRows: searchRows, countN: 1}, true), "")
	var got map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not an object: %v (%s)", err, w.Body.String())
	}
	for _, key := range []string{"items", "next_cursor", "prev_cursor", "total", "total_is_capped"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("response is missing %q: %s", key, w.Body.String())
		}
	}
	if string(got["next_cursor"]) != "null" || string(got["prev_cursor"]) != "null" {
		t.Fatalf("absent cursors must serialise as null: %s", w.Body.String())
	}

	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("items: %v", err)
	}
	if len(page.Items) != 1 || len(page.Items[0]) != 9 {
		t.Fatalf("item = %v, want the complete CRM contact context", page.Items)
	}
	for _, key := range []string{"id", "email", "first_name", "last_name", "company_id", "company_name", "job_title", "linkedin_url", "deal_count"} {
		if _, ok := page.Items[0][key]; !ok {
			t.Fatalf("item is missing %q: %v", key, page.Items[0])
		}
	}
}

// An empty result must serialise as [] and not null, or the client has to guard
// every render against a missing array.
func TestListContactsEmptyItemsIsAnArray(t *testing.T) {
	w := serveSearch(t, newHandler(&fakeStore{}, true), "q=nomatch")
	var page struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if page.Items == nil {
		t.Fatalf("items serialised as null: %s", w.Body.String())
	}
}

// offset is gone. A client that still sends it must not silently get page 1 of
// a differently-paged result — the param is simply ignored, and the response is
// still a keyset page.
func TestListContactsIgnoresLegacyOffset(t *testing.T) {
	store := &fakeStore{searchRows: rows(2), countN: 2}
	if w := serveSearch(t, newHandler(store, true), "offset=500"); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if store.lastSearch.Cur != nil {
		t.Fatal("offset leaked into the keyset position")
	}
}
