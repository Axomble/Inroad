package sendingdomain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/dnsauth"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef")

// serve runs one request through the REAL auth middleware and the domain's own
// router, so the workspace these tests assert on is the one from the JWT and the
// {domain} path parameter is parsed by chi exactly as in production.
func serve(t *testing.T, h *Handler, method, target string, authed bool) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(context.Background(), method, target, http.NoBody)
	if authed {
		tok, err := auth.IssueToken(testSecret, auth.Claims{
			UserID: uuid.NewString(), WorkspaceID: testWS.String(), Role: "owner", SessionID: uuid.NewString(),
		}, time.Hour)
		if err != nil {
			t.Fatalf("IssueToken: %v", err)
		}
		r.Header.Set("Authorization", "Bearer "+tok)
	}
	w := httptest.NewRecorder()
	auth.RequireAuth(auth.NewJWTVerifier(testSecret))(h.Routes()).ServeHTTP(w, r)
	return w
}

func handlerWith(store *fakeStore, res *fakeResolver) *Handler {
	return NewHandler(NewService(store, res))
}

func TestListEmitsTheFrozenWireShape(t *testing.T) {
	checked := recordedAt
	store := &fakeStore{list: []Domain{
		{
			Domain: ownDomain, MailboxCount: 2, State: dnsauth.StatePassing,
			SPFFound: true, SPFRecord: "v=spf1 -all",
			DKIMFound: true, DKIMSelector: "google",
			DMARCFound: true, DMARCPolicy: "none",
			CheckedAt: &checked,
		},
		{Domain: "new.test", MailboxCount: 1, State: dnsauth.StateUnknown},
	}}
	w := serve(t, handlerWith(store, &fakeResolver{}), http.MethodGet, "/", true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}

	var got []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body)
	}
	if len(got) != 2 {
		t.Fatalf("items = %d, want 2", len(got))
	}
	first := got[0]
	want := map[string]any{
		"domain":        ownDomain,
		"state":         "passing",
		"spf":           map[string]any{"found": true, "record": "v=spf1 -all"},
		"dmarc":         map[string]any{"found": true, "policy": "none"},
		"dkim":          map[string]any{"found": true, "selector": "google"},
		"mailbox_count": float64(2),
		"checked_at":    checked.Format(time.RFC3339),
	}
	for k, v := range want {
		if gotV, ok := first[k]; !ok {
			t.Errorf("field %q missing from the response", k)
		} else if !jsonEqual(gotV, v) {
			t.Errorf("%s = %#v, want %#v", k, gotV, v)
		}
	}
	// A never-checked domain is present with a null checked_at, not omitted:
	// "we haven't looked yet" is a state an operator has to be able to see.
	second := got[1]
	if second["state"] != "unknown" {
		t.Errorf("state = %v, want unknown", second["state"])
	}
	if v, ok := second["checked_at"]; !ok || v != nil {
		t.Errorf("checked_at = %#v (present=%t), want an explicit null", v, ok)
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func TestCheckReturnsUpdatedStatus(t *testing.T) {
	store := &fakeStore{get: map[string]Domain{ownDomain: {Domain: ownDomain, MailboxCount: 4}}}
	h := handlerWith(store, &fakeResolver{txt: authenticated()})

	w := serve(t, h, http.MethodPost, "/"+ownDomain+"/check", true)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	var got sendingDomainResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.State != "passing" || got.MailboxCount != 4 || got.CheckedAt == nil {
		t.Fatalf("response = %+v, want a passing, freshly checked domain", got)
	}
}

// A domain the workspace has no mailbox on is a 404 and never reaches DNS.
func TestCheckForeignDomainIs404AndNeverResolves(t *testing.T) {
	store := &fakeStore{get: map[string]Domain{ownDomain: {Domain: ownDomain}}}
	res := &fakeResolver{txt: authenticated()}

	w := serve(t, handlerWith(store, res), http.MethodPost, "/victim.example/check", true)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", w.Code, w.Body)
	}
	if len(res.calls) != 0 {
		t.Fatalf("lookups performed: %v", res.calls)
	}
}

func TestRoutesRequireAuth(t *testing.T) {
	store := &fakeStore{list: []Domain{{Domain: ownDomain}}, get: map[string]Domain{ownDomain: {Domain: ownDomain}}}
	res := &fakeResolver{txt: authenticated()}
	h := handlerWith(store, res)

	for _, tc := range []struct{ method, target string }{
		{http.MethodGet, "/"},
		{http.MethodPost, "/" + ownDomain + "/check"},
	} {
		w := serve(t, h, tc.method, tc.target, false)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want 401", tc.method, tc.target, w.Code)
		}
	}
	if len(res.calls) != 0 || len(store.getCalls) != 0 {
		t.Fatal("an unauthenticated request reached the store or the resolver")
	}
}
