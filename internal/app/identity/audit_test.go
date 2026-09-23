package identity

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/audit"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

type captureRecorder struct {
	mu     sync.Mutex
	events []audit.Event
}

func (c *captureRecorder) Record(_ context.Context, ev audit.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
	return nil
}

func (c *captureRecorder) byAction(a audit.Action) []audit.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []audit.Event
	for _, ev := range c.events {
		if ev.Action == a {
			out = append(out, ev)
		}
	}
	return out
}

// seedMember adds a password user belonging to every workspace in wss.
func seedMember(t *testing.T, store *fakeStore, email, password string, wss ...uuid.UUID) uuid.UUID {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	uid := uuid.New()
	u := gen.User{ID: uid, Email: email, PasswordHash: &hash}
	store.users[email], store.usersByID[uid] = u, u
	for _, ws := range wss {
		store.workspaces[ws] = gen.Workspace{ID: ws, Name: "WS"}
		store.members[uid] = append(store.members[uid], gen.ListMembersByUserRow{
			ID: uuid.New(), WorkspaceID: ws, UserID: uid, Role: gen.MemberRoleOwner, WorkspaceName: "WS",
		})
	}
	return uid
}

func auditedService(store *fakeStore) (*Service, *captureRecorder) {
	rec := &captureRecorder{}
	svc := newTestService(store)
	svc.audit = rec
	return svc, rec
}

func TestLoginRecordsASignInAttributedToTheUser(t *testing.T) {
	store := newFakeStore()
	svc, rec := auditedService(store)
	ws := uuid.New()
	uid := seedMember(t, store, "a@acme.test", "correct-horse-battery", ws)
	ctx := audit.WithRequest(context.Background(), "198.51.100.1", "Firefox")

	if _, err := svc.Login(ctx, "a@acme.test", "correct-horse-battery", "Firefox", "198.51.100.1"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	got := rec.byAction(audit.ActionAuthLogin)
	if len(got) != 1 {
		t.Fatalf("auth.login events = %d, want 1", len(got))
	}
	ev := got[0]
	if ev.WorkspaceID != ws || ev.Actor.Type != audit.ActorUser || ev.Actor.ID != uid.String() || ev.IP != "198.51.100.1" {
		t.Fatalf("event = %+v", ev)
	}
}

// A failure lands in ONE workspace — the one a successful sign-in would have
// activated — never fanned out to every tenant the account belongs to, since
// the row carries the attempt's IP, user agent and reason (security review M1).
func TestWrongPasswordRecordsOneFailureInTheSignInWorkspace(t *testing.T) {
	store := newFakeStore()
	svc, rec := auditedService(store)
	recent, other := uuid.New(), uuid.New()
	// seedMember appends in order; the fake's ListMembersByUser returns that
	// order, so `recent` stands where the real query's last_seen DESC puts the
	// most-recently-seen workspace.
	uid := seedMember(t, store, "b@acme.test", "correct-horse-battery", recent, other)

	if _, err := svc.Login(context.Background(), "b@acme.test", "wrong", "ua", "ip"); err == nil {
		t.Fatal("Login succeeded with a wrong password")
	}
	got := rec.byAction(audit.ActionAuthLoginFailed)
	if len(got) != 1 {
		t.Fatalf("auth.login_failed events = %d, want exactly 1 (no cross-tenant fan-out)", len(got))
	}
	ev := got[0]
	if ev.WorkspaceID != recent {
		t.Fatalf("failure recorded in %s, want the sign-in workspace %s", ev.WorkspaceID, recent)
	}
	if ev.TargetID != uid.String() || ev.Actor.ID != "" || ev.Actor.UserID != nil || ev.Metadata["reason"] != "bad_password" {
		t.Fatalf("event = %+v; want an unauthenticated actor targeting the account", ev)
	}
	if n := len(rec.byAction(audit.ActionAuthLogin)); n != 0 {
		t.Fatalf("a failed login recorded %d successes", n)
	}

	// The success of the same account lands in the same workspace.
	if _, err := svc.Login(context.Background(), "b@acme.test", "correct-horse-battery", "ua", "ip"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if ok := rec.byAction(audit.ActionAuthLogin); len(ok) != 1 || ok[0].WorkspaceID != recent {
		t.Fatalf("auth.login = %+v, want one row in %s", ok, recent)
	}
}

func TestUnknownEmailRecordsNothing(t *testing.T) {
	store := newFakeStore()
	svc, rec := auditedService(store)
	if _, err := svc.Login(context.Background(), "ghost@acme.test", "x", "ua", "ip"); err == nil {
		t.Fatal("Login succeeded for an unknown email")
	}
	if len(rec.events) != 0 {
		t.Fatalf("recorded %d events for an unknown email, want 0", len(rec.events))
	}
}

func TestFailedLoginAuditRunsOffTheRequestPath(t *testing.T) {
	store := newFakeStore()
	svc, rec := auditedService(store)
	var deferred []func()
	svc.dispatch = func(f func()) { deferred = append(deferred, f) }
	seedMember(t, store, "c@acme.test", "correct-horse-battery", uuid.New())

	if _, err := svc.Login(context.Background(), "c@acme.test", "wrong", "ua", "ip"); err == nil {
		t.Fatal("Login succeeded with a wrong password")
	}
	// Nothing may have been written inline: the membership lookup and write
	// would otherwise distinguish a real account from an unknown email by time.
	if len(rec.events) != 0 || len(deferred) != 1 {
		t.Fatalf("inline events = %d, deferred = %d; want 0 and 1", len(rec.events), len(deferred))
	}
	deferred[0]()
	if len(rec.byAction(audit.ActionAuthLoginFailed)) != 1 {
		t.Fatal("the deferred work did not record the failure")
	}
}

func TestInviteLifecycleHandsTheStoreItsAuditEvents(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)
	ws, admin := uuid.New(), uuid.New()
	store.workspaces[ws] = gen.Workspace{ID: ws, Name: "Acme"}
	ctx := audit.WithActor(context.Background(), audit.UserActor(admin))

	inv, err := svc.CreateInvite(ctx, ws, admin, "hire@acme.test", "admin")
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if err := svc.RevokeInvite(ctx, ws, inv.ID); err != nil {
		t.Fatalf("RevokeInvite: %v", err)
	}
	if len(store.auditEvents) != 2 {
		t.Fatalf("store got %d audit events, want 2", len(store.auditEvents))
	}
	invited, revoked := store.auditEvents[0], store.auditEvents[1]
	if invited.Action != audit.ActionMemberInvited || invited.Metadata["email"] != "hire@acme.test" ||
		invited.Metadata["role"] != "admin" || invited.Actor.ID != admin.String() || invited.TargetID != inv.ID.String() {
		t.Fatalf("invited = %+v", invited)
	}
	if revoked.Action != audit.ActionMemberInviteRevoked || revoked.TargetID != inv.ID.String() {
		t.Fatalf("revoked = %+v", revoked)
	}
	for _, ev := range store.auditEvents {
		if err := audit.Validate(ev); err != nil {
			t.Fatalf("%s would be refused at write: %v", ev.Action, err)
		}
	}
}

func TestGrantRoleIsAttributedToTheOperatorCLI(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store)
	uid := uuid.New()
	store.usersByID[uid] = gen.User{ID: uid, Email: "o@acme.test"}
	store.users["o@acme.test"] = store.usersByID[uid]
	ws := uuid.New()
	store.workspaces[ws] = gen.Workspace{ID: ws, Name: "Acme"}

	if _, err := svc.GrantRole(context.Background(), "o@acme.test", ws, "owner"); err != nil {
		t.Fatalf("GrantRole: %v", err)
	}
	if len(store.auditEvents) != 1 {
		t.Fatalf("audit events = %d, want 1", len(store.auditEvents))
	}
	ev := store.auditEvents[0]
	if ev.Action != audit.ActionMemberRoleChanged || ev.Actor.Type != audit.ActorSystem || ev.Actor.ID != "inroadctl" ||
		ev.TargetID != uid.String() || ev.Metadata["to_role"] != "owner" || ev.WorkspaceID != ws {
		t.Fatalf("event = %+v", ev)
	}
}
