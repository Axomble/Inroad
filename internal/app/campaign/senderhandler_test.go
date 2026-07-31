package campaign

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/rotation"
)

// newSendersRequest builds an authed request against /campaigns/{id}/senders,
// routed through RequireAuth exactly as the protected group does.
func newSendersRequest(t *testing.T, secret []byte, ws, campaignID uuid.UUID, method, body string) *http.Request {
	t.Helper()
	tok, err := auth.IssueToken(secret, auth.Claims{
		UserID: uuid.New().String(), WorkspaceID: ws.String(), Role: "owner", SessionID: uuid.New().String(),
	}, time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	var reader io.Reader = http.NoBody
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/campaigns/"+campaignID.String()+"/senders", reader)
	req.Header.Set("Authorization", "Bearer "+tok)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", campaignID.String())
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// The response is a frozen contract the frontend is built against, so this
// asserts the field NAMES and null-ability, not just the decoded values.
func TestGetSendersEmitsTheContractShape(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	ws, id, mailbox := uuid.New(), uuid.New(), uuid.New()
	assigned := time.Date(2026, 7, 30, 8, 30, 0, 0, time.UTC)
	store := &fakeStore{
		campaigns: map[[2]uuid.UUID]gen.Campaign{
			{ws, id}: {ID: id, WorkspaceID: ws, RotationMode: rotation.ModeWeighted},
		},
		senders: []Sender{
			{
				MailboxID: mailbox, Email: "a@x.test", Provider: "smtp", Status: "active",
				Weight: 4, Enabled: true, AssignedCount: 12, LastAssignedAt: &assigned,
			},
			{MailboxID: uuid.New(), Email: "b@x.test", Provider: "gmail", Status: "active", Weight: 1, Enabled: false},
		},
	}
	h := NewHandler(NewService(store, okChecker{active: true}), &fakeEnqueuer{})

	w := httptest.NewRecorder()
	req := newSendersRequest(t, secret, ws, id, http.MethodGet, "")
	auth.RequireAuth(auth.NewJWTVerifier(secret))(http.HandlerFunc(h.getSenders)).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var raw struct {
		RotationMode string           `json:"rotation_mode"`
		Senders      []map[string]any `json:"senders"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if raw.RotationMode != rotation.ModeWeighted {
		t.Errorf("rotation_mode = %q", raw.RotationMode)
	}
	if len(raw.Senders) != 2 {
		t.Fatalf("senders = %d, want 2", len(raw.Senders))
	}
	first := raw.Senders[0]
	for _, key := range []string{"mailbox_id", "email", "provider", "status", "weight", "enabled", "assigned_count", "last_assigned_at"} {
		if _, ok := first[key]; !ok {
			t.Errorf("response is missing %q; got %v", key, first)
		}
	}
	if first["mailbox_id"] != mailbox.String() {
		t.Errorf("mailbox_id = %v, want %s", first["mailbox_id"], mailbox)
	}
	if first["last_assigned_at"] != "2026-07-30T08:30:00Z" {
		t.Errorf("last_assigned_at = %v, want RFC3339 UTC", first["last_assigned_at"])
	}
	// A never-assigned mailbox must serialize as JSON null, not a zero timestamp.
	if second := raw.Senders[1]; second["last_assigned_at"] != nil {
		t.Errorf("last_assigned_at = %v for a never-assigned mailbox, want null", second["last_assigned_at"])
	}
}

// weight and enabled are optional per member; omitting them must yield 1/true
// rather than 0/false (a 0 weight would then be rejected as out of range).
func TestPutSendersAppliesPerMemberDefaults(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	ws, id := uuid.New(), uuid.New()
	mailbox := uuid.New()
	store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws}}}
	h := NewHandler(NewService(store, okChecker{active: true}), &fakeEnqueuer{})

	body := `{"rotation_mode":"round_robin","senders":[{"mailbox_id":"` + mailbox.String() + `"}]}`
	w := httptest.NewRecorder()
	req := newSendersRequest(t, secret, ws, id, http.MethodPut, body)
	auth.RequireAuth(auth.NewJWTVerifier(secret))(http.HandlerFunc(h.putSenders)).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(store.replacedSenders) != 1 {
		t.Fatalf("persisted senders = %+v, want 1", store.replacedSenders)
	}
	got := store.replacedSenders[0]
	if got.MailboxID != mailbox || got.Weight != defaultSenderWeight || !got.Enabled {
		t.Errorf("persisted sender = %+v, want weight 1 and enabled", got)
	}
	if store.replacedRotationMode != rotation.ModeRoundRobin {
		t.Errorf("persisted mode = %q", store.replacedRotationMode)
	}
}

func TestPutSendersRejectsBadInput(t *testing.T) {
	mailbox := uuid.New().String()
	cases := []struct {
		name     string
		body     string
		active   bool
		wantCode int
	}{
		{"malformed json", `{"rotation_mode":`, true, http.StatusBadRequest},
		{"malformed mailbox id", `{"rotation_mode":"weighted","senders":[{"mailbox_id":"not-a-uuid"}]}`, true, http.StatusBadRequest},
		{"empty sender list", `{"rotation_mode":"weighted","senders":[]}`, true, http.StatusUnprocessableEntity},
		{"unknown rotation mode", `{"rotation_mode":"per_send","senders":[{"mailbox_id":"` + mailbox + `"}]}`, true, http.StatusUnprocessableEntity},
		{"weight out of range", `{"rotation_mode":"weighted","senders":[{"mailbox_id":"` + mailbox + `","weight":250}]}`, true, http.StatusUnprocessableEntity},
		{
			name:     "duplicate mailbox",
			body:     `{"rotation_mode":"weighted","senders":[{"mailbox_id":"` + mailbox + `"},{"mailbox_id":"` + mailbox + `"}]}`,
			active:   true,
			wantCode: http.StatusUnprocessableEntity,
		},
		{
			name:     "mailbox outside the workspace or inactive",
			body:     `{"rotation_mode":"weighted","senders":[{"mailbox_id":"` + mailbox + `"}]}`,
			active:   false,
			wantCode: http.StatusUnprocessableEntity,
		},
	}

	secret := []byte("0123456789abcdef0123456789abcdef")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws, id := uuid.New(), uuid.New()
			store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{{ws, id}: {ID: id, WorkspaceID: ws}}}
			h := NewHandler(NewService(store, okChecker{active: tc.active}), &fakeEnqueuer{})

			w := httptest.NewRecorder()
			req := newSendersRequest(t, secret, ws, id, http.MethodPut, tc.body)
			auth.RequireAuth(auth.NewJWTVerifier(secret))(http.HandlerFunc(h.putSenders)).ServeHTTP(w, req)

			if w.Code != tc.wantCode {
				t.Fatalf("code = %d, want %d: %s", w.Code, tc.wantCode, w.Body.String())
			}
			if store.replaceSendersCalls != 0 {
				t.Errorf("a rejected pool was written (%d replace calls)", store.replaceSendersCalls)
			}
			// The 422s carry the server's reason so the panel can surface it.
			if tc.wantCode == http.StatusUnprocessableEntity && w.Body.Len() == 0 {
				t.Error("422 carried no reason")
			}
		})
	}
}

// A campaign in another workspace must 404, not leak or replace its pool.
func TestSenderEndpointsAreWorkspaceScoped(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	ownerWS, id := uuid.New(), uuid.New()
	intruder := uuid.New()

	for _, tc := range []struct {
		name    string
		method  string
		body    string
		handler func(*Handler) http.HandlerFunc
	}{
		{"get", http.MethodGet, "", func(h *Handler) http.HandlerFunc { return h.getSenders }},
		{
			name:   "put",
			method: http.MethodPut,
			body:   `{"rotation_mode":"weighted","senders":[{"mailbox_id":"` + uuid.New().String() + `"}]}`,
			handler: func(h *Handler) http.HandlerFunc {
				return h.putSenders
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{campaigns: map[[2]uuid.UUID]gen.Campaign{{ownerWS, id}: {ID: id, WorkspaceID: ownerWS}}}
			h := NewHandler(NewService(store, okChecker{active: true}), &fakeEnqueuer{})

			w := httptest.NewRecorder()
			req := newSendersRequest(t, secret, intruder, id, tc.method, tc.body)
			auth.RequireAuth(auth.NewJWTVerifier(secret))(tc.handler(h)).ServeHTTP(w, req)

			if w.Code != http.StatusNotFound {
				t.Fatalf("code = %d, want 404: %s", w.Code, w.Body.String())
			}
			if store.replaceSendersCalls != 0 {
				t.Errorf("a cross-tenant request reached the store (%d replace calls)", store.replaceSendersCalls)
			}
		})
	}
}
