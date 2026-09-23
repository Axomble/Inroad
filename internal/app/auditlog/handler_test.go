package auditlog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

type fixedVerifier struct{ p auth.Principal }

func (v fixedVerifier) Verify(context.Context, *http.Request) (auth.Principal, bool, error) {
	return v.p, true, nil
}

func serveAs(t *testing.T, store *fakeStore, p auth.Principal, target string) *httptest.ResponseRecorder {
	t.Helper()
	h := auth.RequireAuth(fixedVerifier{p})(NewHandler(NewService(store)).Routes())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, target, http.NoBody))
	return w
}

func TestRoutesAreOwnerAndAdminOnly(t *testing.T) {
	ws := uuid.New().String()
	for _, tc := range []struct {
		name string
		p    auth.Principal
		want int
	}{
		{"owner", auth.Principal{Kind: auth.KindSession, WorkspaceID: ws, Role: "owner"}, http.StatusOK},
		{"admin", auth.Principal{Kind: auth.KindSession, WorkspaceID: ws, Role: "admin"}, http.StatusOK},
		{"member", auth.Principal{Kind: auth.KindSession, WorkspaceID: ws, Role: "member"}, http.StatusForbidden},
		// A machine principal has no role, whatever scopes it holds.
		{"api key", auth.Principal{Kind: auth.KindAPIKey, WorkspaceID: ws, Scopes: auth.AllScopes}, http.StatusForbidden},
		{"oauth", auth.Principal{Kind: auth.KindOAuth, WorkspaceID: ws, Scopes: auth.AllScopes}, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if w := serveAs(t, &fakeStore{}, tc.p, "/"); w.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

func TestListResponseShape(t *testing.T) {
	ws, uid := uuid.New(), uuid.New()
	ip := netip.MustParseAddr("203.0.113.5")
	email := "owner@example.test"
	at := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	store := &fakeStore{rows: []gen.ListAuditEventsRow{{
		ID: uuid.New(), WorkspaceID: ws, ActorType: "user", ActorID: uid.String(),
		ActorUserID: pgtype.UUID{Bytes: uid, Valid: true}, ActorEmail: &email,
		Action: "campaign.paused", TargetType: "campaign", TargetID: "c-1",
		Ip: &ip, UserAgent: "Mozilla/5.0", Metadata: []byte(`{"name":"Q3"}`),
		CreatedAt: pgtype.Timestamptz{Time: at, Valid: true},
	}, {
		ID: uuid.New(), WorkspaceID: ws, ActorType: "system", Action: "campaign.paused",
		Metadata: []byte(`{}`), CreatedAt: pgtype.Timestamptz{Time: at.Add(-time.Hour), Valid: true},
	}}}
	w := serveAs(t, store, auth.Principal{WorkspaceID: ws.String(), Role: "admin"}, "/?limit=1&action=campaign")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Events []map[string]any `json:"events"`
		Next   *string          `json:"next_cursor"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Events) != 1 || body.Next == nil {
		t.Fatalf("events = %d, next = %v; want 1 and a cursor", len(body.Events), body.Next)
	}
	ev := body.Events[0]
	want := map[string]any{
		"action": "campaign.paused", "actor_type": "user", "actor_id": uid.String(),
		"actor_user_id": uid.String(), "actor_email": email, "target_type": "campaign",
		"target_id": "c-1", "ip": "203.0.113.5", "user_agent": "Mozilla/5.0",
		"created_at": "2026-09-23T10:00:00Z",
	}
	for k, v := range want {
		if ev[k] != v {
			t.Errorf("%s = %v, want %v", k, ev[k], v)
		}
	}
	if md, _ := ev["metadata"].(map[string]any); md["name"] != "Q3" {
		t.Errorf("metadata = %v", ev["metadata"])
	}

	// The second page is the system row, with nulls rather than empty strings.
	w = serveAs(t, store, auth.Principal{WorkspaceID: ws.String(), Role: "admin"}, "/?limit=1&action=campaign&cursor="+*body.Next)
	body.Events, body.Next = nil, nil
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode page 2: %v (%s)", err, w.Body.String())
	}
	if len(body.Events) != 1 || body.Next != nil {
		t.Fatalf("page 2: %d events, next %v", len(body.Events), body.Next)
	}
	for _, k := range []string{"actor_id", "actor_user_id", "actor_email", "target_type", "target_id", "ip", "user_agent"} {
		if v, present := body.Events[0][k]; !present || v != nil {
			t.Errorf("%s = %v (present %v), want explicit null", k, v, present)
		}
	}
}

func TestListRejectsMalformedParams(t *testing.T) {
	p := auth.Principal{WorkspaceID: uuid.NewString(), Role: "owner"}
	for _, q := range []string{
		"?since=yesterday", "?until=2026-13-01", "?limit=0", "?limit=abc",
		"?actor_user_id=nope", "?actor_type=robot", "?action=a%25", "?cursor=xyz",
	} {
		if w := serveAs(t, &fakeStore{}, p, "/"+q); w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, w.Code)
		}
	}
}

func TestListUsesTheJWTWorkspaceNotAQueryParam(t *testing.T) {
	mine := uuid.New()
	store := &fakeStore{}
	serveAs(t, store, auth.Principal{WorkspaceID: mine.String(), Role: "owner"}, "/?workspace_id="+uuid.NewString())
	if store.last.WorkspaceID != mine {
		t.Fatalf("queried %s, want the principal's %s", store.last.WorkspaceID, mine)
	}
}
