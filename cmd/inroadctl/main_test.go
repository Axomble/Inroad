package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inroad/inroad/internal/app/identity"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// fakeIdentityService is an in-memory identityService for CLI unit tests — no
// Postgres, matching the fake-Store pattern the identity package's own tests
// use one layer down.
type fakeIdentityService struct {
	registerCalls     []identity.RegisterInput
	registerErr       error
	registerResult    identity.Session
	createMemberCalls []struct {
		workspace             uuid.UUID
		email, password, role string
	}
	createMemberErr    error
	createMemberResult uuid.UUID

	setPasswordCalls   []struct{ email, password string }
	setPasswordErr     error
	setPasswordRevoked []uuid.UUID

	grantRoleCalls []struct {
		email     string
		workspace uuid.UUID
		role      string
	}
	grantRoleErr    error
	grantRoleResult identity.Membership

	workspaces []gen.Workspace
	users      []gen.User
	listErr    error
}

func (f *fakeIdentityService) Register(_ context.Context, in identity.RegisterInput) (identity.Session, error) {
	f.registerCalls = append(f.registerCalls, in)
	return f.registerResult, f.registerErr
}

func (f *fakeIdentityService) CreateMember(_ context.Context, workspace uuid.UUID, email, password, role string) (uuid.UUID, error) {
	f.createMemberCalls = append(f.createMemberCalls, struct {
		workspace             uuid.UUID
		email, password, role string
	}{workspace, email, password, role})
	return f.createMemberResult, f.createMemberErr
}

func (f *fakeIdentityService) SetPassword(_ context.Context, email, password string) ([]uuid.UUID, error) {
	f.setPasswordCalls = append(f.setPasswordCalls, struct{ email, password string }{email, password})
	return f.setPasswordRevoked, f.setPasswordErr
}

func (f *fakeIdentityService) GrantRole(_ context.Context, email string, workspace uuid.UUID, role string) (identity.Membership, error) {
	f.grantRoleCalls = append(f.grantRoleCalls, struct {
		email     string
		workspace uuid.UUID
		role      string
	}{email, workspace, role})
	return f.grantRoleResult, f.grantRoleErr
}

func (f *fakeIdentityService) ListWorkspaces(context.Context) ([]gen.Workspace, error) {
	return f.workspaces, f.listErr
}

func (f *fakeIdentityService) ListUsers(context.Context) ([]gen.User, error) {
	return f.users, f.listErr
}

// newTestEnv wires an env against a fake service, a fixed-string password
// stub (never a real terminal read), and buffers for in/out so a test can
// both drive confirmation prompts and assert on printed output.
func newTestEnv(svc identityService, confirmAnswer, fixedPassword string) (*env, *bytes.Buffer) {
	out := &bytes.Buffer{}
	return &env{
		ctx: context.Background(),
		svc: svc,
		in:  strings.NewReader(confirmAnswer + "\n"),
		out: out,
		readPW: func(string) (string, error) {
			return fixedPassword, nil
		},
	}, out
}

// --- create-user ---------------------------------------------------------

func TestCreateUserWithoutWorkspaceRegistersANewOne(t *testing.T) {
	svc := &fakeIdentityService{registerResult: identity.Session{
		UserID: uuid.New(), WorkspaceID: uuid.New(),
	}}
	e, out := newTestEnv(svc, "y", "s3cret-pw-123")

	if err := e.createUser([]string{"--email", "owner@acme.test", "--yes"}); err != nil {
		t.Fatalf("createUser: %v", err)
	}
	if len(svc.registerCalls) != 1 {
		t.Fatalf("expected exactly one Register call, got %d", len(svc.registerCalls))
	}
	got := svc.registerCalls[0]
	if got.Email != "owner@acme.test" || got.Password != "s3cret-pw-123" {
		t.Fatalf("unexpected Register input: %+v", got)
	}
	if !strings.Contains(out.String(), svc.registerResult.UserID.String()) {
		t.Fatalf("expected the created user id in output, got %q", out.String())
	}
}

func TestCreateUserWithWorkspaceAddsToExistingOne(t *testing.T) {
	svc := &fakeIdentityService{createMemberResult: uuid.New()}
	e, _ := newTestEnv(svc, "y", "s3cret-pw-123")
	wsID := uuid.New()

	if err := e.createUser([]string{"--email", "admin@acme.test", "--workspace", wsID.String(), "--role", "admin", "--yes"}); err != nil {
		t.Fatalf("createUser: %v", err)
	}
	if len(svc.createMemberCalls) != 1 {
		t.Fatalf("expected exactly one CreateMember call, got %d", len(svc.createMemberCalls))
	}
	got := svc.createMemberCalls[0]
	if got.workspace != wsID || got.email != "admin@acme.test" || got.role != "admin" {
		t.Fatalf("unexpected CreateMember input: %+v", got)
	}
	if len(svc.registerCalls) != 0 {
		t.Fatal("expected Register NOT to be called when --workspace is given")
	}
}

func TestCreateUserRequiresEmail(t *testing.T) {
	svc := &fakeIdentityService{}
	e, _ := newTestEnv(svc, "y", "s3cret-pw-123")

	if err := e.createUser([]string{"--yes"}); err == nil {
		t.Fatal("expected an error for a missing --email")
	}
}

func TestCreateUserRejectsRoleWithoutWorkspace(t *testing.T) {
	svc := &fakeIdentityService{}
	e, _ := newTestEnv(svc, "y", "s3cret-pw-123")

	err := e.createUser([]string{"--email", "x@acme.test", "--role", "admin", "--yes"})
	if err == nil {
		t.Fatal("expected an error for --role without --workspace")
	}
	if len(svc.registerCalls) != 0 {
		t.Fatal("expected Register NOT to be called when validation fails")
	}
}

func TestCreateUserAbortsWithoutConfirmation(t *testing.T) {
	svc := &fakeIdentityService{}
	e, out := newTestEnv(svc, "n", "s3cret-pw-123") // no --yes, answers "n"

	if err := e.createUser([]string{"--email", "owner@acme.test"}); err != nil {
		t.Fatalf("createUser: %v", err)
	}
	if len(svc.registerCalls) != 0 {
		t.Fatal("expected no Register call when the operator declines confirmation")
	}
	if !strings.Contains(out.String(), "aborted") {
		t.Fatalf("expected an 'aborted' message, got %q", out.String())
	}
}

func TestCreateUserDuplicateEmailIsReportedClearly(t *testing.T) {
	// A real *pgconn.PgError carrying the unique-violation code, exactly the
	// shape identity.Service.Register/CreateMember pass through unmapped —
	// see errors.go's isUniqueViolation.
	svc := &fakeIdentityService{registerErr: &pgconn.PgError{Code: "23505"}}
	e, _ := newTestEnv(svc, "y", "s3cret-pw-123")

	err := e.createUser([]string{"--email", "taken@acme.test", "--yes"})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected an 'already exists' message, got %v", err)
	}
}

// --- set-password ----------------------------------------------------------

func TestSetPasswordHappyPath(t *testing.T) {
	sid := uuid.New()
	svc := &fakeIdentityService{setPasswordRevoked: []uuid.UUID{sid}}
	e, out := newTestEnv(svc, "y", "new-password-456")

	if err := e.setPassword([]string{"--email", "owner@acme.test", "--yes"}); err != nil {
		t.Fatalf("setPassword: %v", err)
	}
	if len(svc.setPasswordCalls) != 1 || svc.setPasswordCalls[0].email != "owner@acme.test" {
		t.Fatalf("unexpected SetPassword calls: %+v", svc.setPasswordCalls)
	}
	if !strings.Contains(out.String(), "1 session(s) revoked") {
		t.Fatalf("expected the revoked-session count in output, got %q", out.String())
	}
}

func TestSetPasswordUnknownEmailIsReportedClearly(t *testing.T) {
	svc := &fakeIdentityService{setPasswordErr: identity.ErrUserNotFound}
	e, _ := newTestEnv(svc, "y", "new-password-456")

	err := e.setPassword([]string{"--email", "nobody@acme.test", "--yes"})
	if err == nil || !strings.Contains(err.Error(), "no user with email") {
		t.Fatalf("expected a 'no user with email' message, got %v", err)
	}
}

func TestSetPasswordAbortsWithoutConfirmation(t *testing.T) {
	svc := &fakeIdentityService{}
	e, _ := newTestEnv(svc, "n", "new-password-456")

	if err := e.setPassword([]string{"--email", "owner@acme.test"}); err != nil {
		t.Fatalf("setPassword: %v", err)
	}
	if len(svc.setPasswordCalls) != 0 {
		t.Fatal("expected no SetPassword call when the operator declines confirmation")
	}
}

func TestSetPasswordRequiresEmail(t *testing.T) {
	svc := &fakeIdentityService{}
	e, _ := newTestEnv(svc, "y", "new-password-456")

	if err := e.setPassword([]string{"--yes"}); err == nil {
		t.Fatal("expected an error for a missing --email")
	}
}

// --- grant-role --------------------------------------------------------

func TestGrantRoleHappyPath(t *testing.T) {
	svc := &fakeIdentityService{grantRoleResult: identity.Membership{Role: "owner", WorkspaceName: "Acme"}}
	e, out := newTestEnv(svc, "y", "")
	wsID := uuid.New()

	if err := e.grantRole([]string{"--email", "owner@acme.test", "--workspace", wsID.String(), "--role", "owner", "--yes"}); err != nil {
		t.Fatalf("grantRole: %v", err)
	}
	if len(svc.grantRoleCalls) != 1 {
		t.Fatalf("expected exactly one GrantRole call, got %d", len(svc.grantRoleCalls))
	}
	got := svc.grantRoleCalls[0]
	if got.email != "owner@acme.test" || got.workspace != wsID || got.role != "owner" {
		t.Fatalf("unexpected GrantRole input: %+v", got)
	}
	if !strings.Contains(out.String(), "granted owner@acme.test role owner") {
		t.Fatalf("expected a confirmation line, got %q", out.String())
	}
}

func TestGrantRoleRequiresAllThreeFlags(t *testing.T) {
	svc := &fakeIdentityService{}
	e, _ := newTestEnv(svc, "y", "")

	cases := [][]string{
		{"--workspace", uuid.New().String(), "--role", "owner", "--yes"},
		{"--email", "x@acme.test", "--role", "owner", "--yes"},
		{"--email", "x@acme.test", "--workspace", uuid.New().String(), "--yes"},
	}
	for _, args := range cases {
		if err := e.grantRole(args); err == nil {
			t.Errorf("args %v: expected an error for missing required flags", args)
		}
	}
	if len(svc.grantRoleCalls) != 0 {
		t.Fatal("expected no GrantRole call for any invalid invocation")
	}
}

func TestGrantRoleInvalidWorkspaceUUID(t *testing.T) {
	svc := &fakeIdentityService{}
	e, _ := newTestEnv(svc, "y", "")

	if err := e.grantRole([]string{"--email", "x@acme.test", "--workspace", "not-a-uuid", "--role", "owner", "--yes"}); err == nil {
		t.Fatal("expected an error for a malformed --workspace UUID")
	}
}

func TestGrantRoleUnknownWorkspaceIsReportedClearly(t *testing.T) {
	svc := &fakeIdentityService{grantRoleErr: identity.ErrWorkspaceNotFound}
	e, _ := newTestEnv(svc, "y", "")

	err := e.grantRole([]string{"--email", "x@acme.test", "--workspace", uuid.New().String(), "--role", "owner", "--yes"})
	if err == nil || !strings.Contains(err.Error(), "no workspace") {
		t.Fatalf("expected a 'no workspace' message, got %v", err)
	}
}

func TestGrantRoleAbortsWithoutConfirmation(t *testing.T) {
	svc := &fakeIdentityService{}
	e, _ := newTestEnv(svc, "n", "")

	if err := e.grantRole([]string{"--email", "x@acme.test", "--workspace", uuid.New().String(), "--role", "owner"}); err != nil {
		t.Fatalf("grantRole: %v", err)
	}
	if len(svc.grantRoleCalls) != 0 {
		t.Fatal("expected no GrantRole call when the operator declines confirmation")
	}
}

// --- listings ------------------------------------------------------------

func TestListWorkspacesPrintsEachRow(t *testing.T) {
	wsID := uuid.New()
	svc := &fakeIdentityService{workspaces: []gen.Workspace{{ID: wsID, Name: "Acme"}}}
	e, out := newTestEnv(svc, "", "")

	if err := e.listWorkspaces(nil); err != nil {
		t.Fatalf("listWorkspaces: %v", err)
	}
	if !strings.Contains(out.String(), wsID.String()) || !strings.Contains(out.String(), "Acme") {
		t.Fatalf("expected the workspace id and name in output, got %q", out.String())
	}
}

func TestListWorkspacesEmpty(t *testing.T) {
	svc := &fakeIdentityService{}
	e, out := newTestEnv(svc, "", "")

	if err := e.listWorkspaces(nil); err != nil {
		t.Fatalf("listWorkspaces: %v", err)
	}
	if !strings.Contains(out.String(), "no workspaces") {
		t.Fatalf("expected a 'no workspaces' message, got %q", out.String())
	}
}

// TestListUsersNeverPrintsPasswordHash is the load-bearing test for list.go's
// printer: gen.User carries PasswordHash, and this proves the CLI's output
// never renders it even when the fake row has one set, not merely that the
// happy-path row (which might have a nil hash) looks fine.
func TestListUsersNeverPrintsPasswordHash(t *testing.T) {
	secret := "$argon2id$this-must-never-appear-on-a-terminal$"
	svc := &fakeIdentityService{users: []gen.User{{ID: uuid.New(), Email: "owner@acme.test", PasswordHash: &secret}}}
	e, out := newTestEnv(svc, "", "")

	if err := e.listUsers(nil); err != nil {
		t.Fatalf("listUsers: %v", err)
	}
	if strings.Contains(out.String(), secret) {
		t.Fatalf("password hash leaked into listUsers output: %q", out.String())
	}
	if !strings.Contains(out.String(), "owner@acme.test") {
		t.Fatalf("expected the user's email in output, got %q", out.String())
	}
}

func TestListUsersEmpty(t *testing.T) {
	svc := &fakeIdentityService{}
	e, out := newTestEnv(svc, "", "")

	if err := e.listUsers(nil); err != nil {
		t.Fatalf("listUsers: %v", err)
	}
	if !strings.Contains(out.String(), "no users") {
		t.Fatalf("expected a 'no users' message, got %q", out.String())
	}
}
