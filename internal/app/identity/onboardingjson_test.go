package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// These tests exist for ONE reason: `onboarding_completed_at` must be PRESENT and
// explicitly null while onboarding is pending — never absent.
//
// The SPA distinguishes the two. An explicit null means "this workspace still needs
// onboarding" and opens a blocking overlay; an ABSENT key means "this API predates
// onboarding" and shows nothing. That asymmetry is deliberate: wrongly hiding the
// overlay costs one prompt, while wrongly showing it puts an un-dismissable screen
// in front of every existing user.
//
// Adding `omitempty` to any of these fields would make null and absent serialize
// identically, the overlay would never appear, and NOTHING would fail — not a
// build, not a type check, not any other test. So the assertion here is on key
// PRESENCE in the decoded JSON object, not on the value: that is the only thing
// that catches a well-meant tag "tidy-up".

// requireExplicitNull fails unless body is a JSON object that HAS key, with the
// literal value null.
func requireExplicitNull(t *testing.T, body []byte, key string) {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		t.Fatalf("decode body: %v (body %s)", err, body)
	}
	raw, ok := obj[key]
	if !ok {
		t.Fatalf("key %q is ABSENT from the response; it must be present and null while onboarding is pending (body %s)", key, body)
	}
	if string(raw) != "null" {
		t.Fatalf("key %q = %s, want null", key, raw)
	}
}

// A pending workspace's session response carries the key, as null, at the top level
// AND inside every membership.
func TestSessionResponseAlwaysCarriesOnboardingKey(t *testing.T) {
	store := newFakeStore()
	h := newTestHandler(store)

	w := doRequest(h.register, http.MethodPost, "/register", map[string]string{
		"workspace_name": "Dana's workspace", "email": "dana@axomble.test", "password": "s3cret-pw",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("register: want 200, got %d: %s", w.Code, w.Body.String())
	}
	requireExplicitNull(t, w.Body.Bytes(), "onboarding_completed_at")

	// ...and on each membership, which the SPA reads when switching workspaces.
	var body struct {
		Memberships []map[string]json.RawMessage `json:"memberships"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode memberships: %v", err)
	}
	if len(body.Memberships) != 1 {
		t.Fatalf("want 1 membership, got %d", len(body.Memberships))
	}
	raw, ok := body.Memberships[0]["onboarding_completed_at"]
	if !ok {
		t.Fatalf("membership is missing onboarding_completed_at: %s", w.Body.String())
	}
	if string(raw) != "null" {
		t.Fatalf("membership onboarding_completed_at = %s, want null", raw)
	}
}

// meWithRole drives GET /auth/me for a registered session and returns the body.
func meResponseBody(t *testing.T, h *Handler, reg Session) []byte {
	t.Helper()
	access, err := auth.IssueToken(h.jwtSecret, auth.Claims{
		UserID: reg.UserID.String(), WorkspaceID: reg.WorkspaceID.String(),
		Role: reg.Role, SessionID: reg.SessionID.String(),
	}, h.accessTTL)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/me", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+access)
	w := httptest.NewRecorder()
	auth.RequireAuth(auth.NewJWTVerifier(h.jwtSecret))(http.HandlerFunc(h.me)).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("me: want 200, got %d: %s", w.Code, w.Body.String())
	}
	return w.Body.Bytes()
}

// /auth/me is the endpoint the SPA's gate actually reads on first paint, so it gets
// its own assertion rather than relying on the session response above.
func TestMeResponseAlwaysCarriesOnboardingKey(t *testing.T) {
	store := newFakeStore()
	h := newTestHandler(store)

	reg, err := h.svc.Register(context.Background(), RegisterInput{
		WorkspaceName: "Dana's workspace", Email: "dana@axomble.test", Password: "s3cret-pw",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	requireExplicitNull(t, meResponseBody(t, h, reg), "onboarding_completed_at")
}

// Once onboarding is complete the same key carries an RFC3339 timestamp, so the
// null above is genuinely "pending" and not just "this field never populates".
func TestMeResponseCarriesTimestampOnceComplete(t *testing.T) {
	store := newFakeStore()
	h := newTestHandler(store)

	reg, err := h.svc.Register(context.Background(), RegisterInput{
		WorkspaceName: "Dana's workspace", Email: "dana@axomble.test", Password: "s3cret-pw",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := h.svc.CompleteOnboarding(context.Background(), reg.WorkspaceID, "Axomble Inc"); err != nil {
		t.Fatalf("CompleteOnboarding: %v", err)
	}

	body := meResponseBody(t, h, reg)
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		t.Fatalf("decode: %v", err)
	}
	raw, ok := obj["onboarding_completed_at"]
	if !ok {
		t.Fatalf("key absent after completion: %s", body)
	}
	var ts string
	if err := json.Unmarshal(raw, &ts); err != nil {
		t.Fatalf("want an RFC3339 string after completion, got %s", raw)
	}
	if ts == "" {
		t.Fatal("want a non-empty timestamp after completion")
	}
}

// switch-workspace is the third response the gate reads (moving into a workspace
// that may itself be un-onboarded), so it gets the same guarantee.
func TestSwitchWorkspaceResponseAlwaysCarriesOnboardingKey(t *testing.T) {
	store := newFakeStore()
	h := newTestHandler(store)

	reg, err := h.svc.Register(context.Background(), RegisterInput{
		WorkspaceName: "Acme", Email: "owner@acme.test", Password: "s3cret-pw",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	// A second, un-onboarded workspace to switch into.
	otherWS := uuid.New()
	store.workspaces[otherWS] = gen.Workspace{ID: otherWS, Name: "Other"}
	member := gen.WorkspaceMember{ID: uuid.New(), WorkspaceID: otherWS, UserID: reg.UserID, Role: gen.MemberRoleMember}
	store.memberByPair[[2]uuid.UUID{otherWS, reg.UserID}] = member
	store.members[reg.UserID] = append(store.members[reg.UserID], gen.ListMembersByUserRow{
		ID: member.ID, WorkspaceID: otherWS, UserID: reg.UserID, Role: gen.MemberRoleMember, WorkspaceName: "Other",
	})

	access, err := auth.IssueToken(h.jwtSecret, auth.Claims{
		UserID: reg.UserID.String(), WorkspaceID: reg.WorkspaceID.String(),
		Role: reg.Role, SessionID: reg.SessionID.String(),
	}, h.accessTTL)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	payload, _ := json.Marshal(map[string]string{"workspace_id": otherWS.String()})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/switch-workspace", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+access)
	w := httptest.NewRecorder()
	auth.RequireAuth(auth.NewJWTVerifier(h.jwtSecret))(http.HandlerFunc(h.switchWorkspace)).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("switch: want 200, got %d: %s", w.Code, w.Body.String())
	}
	requireExplicitNull(t, w.Body.Bytes(), "onboarding_completed_at")
}
