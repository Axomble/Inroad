package identity

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrNoWorkspace        = errors.New("user has no workspace")
	ErrRefreshInvalid     = errors.New("refresh token invalid")
	ErrNotMember          = errors.New("not a member of target workspace")
)

// Membership is a single workspace a user belongs to, as returned to
// callers (handlers, other services) - decoupled from the sqlc row shape.
type Membership struct {
	WorkspaceID   uuid.UUID
	WorkspaceName string
	Role          string
}

// Session is the result of any operation that establishes or rotates a
// refresh-token session: the active identity context plus the raw refresh
// token to hand back to the client (only ever available at issuance time).
type Session struct {
	UserID      uuid.UUID
	WorkspaceID uuid.UUID
	Role        string
	SessionID   uuid.UUID
	RawRefresh  string
	Memberships []Membership
}

// RegisterInput carries the fields needed to create a brand-new workspace,
// owner user, and first session in one call.
type RegisterInput struct {
	WorkspaceName, Email, Password, UserAgent, IP string
}

// storeIface lists exactly the Store methods the service depends on, so
// tests can inject an in-memory fake (dependency inversion; see CLAUDE.md).
type storeIface interface {
	RegisterTx(ctx context.Context, wsName, email, hash string) (uuid.UUID, uuid.UUID, error)
	GetUserByEmail(ctx context.Context, email string) (gen.User, error)
	ListMembersByUser(ctx context.Context, userID uuid.UUID) ([]gen.ListMembersByUserRow, error)
	GetMember(ctx context.Context, wsID, userID uuid.UUID) (gen.WorkspaceMember, error)
	TouchMemberLastSeen(ctx context.Context, wsID, userID uuid.UUID) error
	CreateSession(ctx context.Context, arg gen.CreateSessionParams) (gen.Session, error)
	GetSessionByHash(ctx context.Context, hash []byte) (gen.Session, error)
	RevokeSession(ctx context.Context, id uuid.UUID) error
	RevokeFamily(ctx context.Context, familyID uuid.UUID) error
	RevokeAllForUser(ctx context.Context, userID uuid.UUID) error
	RepointSessionWorkspace(ctx context.Context, id, wsID uuid.UUID) error
}

// Service implements the core auth logic: registration, login, refresh
// rotation with reuse detection, logout, and workspace switching.
type Service struct {
	store      storeIface
	refreshTTL time.Duration
}

// NewService constructs a Service backed by store, issuing refresh tokens
// that expire after refreshTTL.
func NewService(store storeIface, refreshTTL time.Duration) *Service {
	return &Service{store: store, refreshTTL: refreshTTL}
}

func (s *Service) newSessionRow(ctx context.Context, userID, wsID, familyID uuid.UUID, ua, ip string) (uuid.UUID, string, error) {
	raw, hash, err := auth.NewRefreshToken()
	if err != nil {
		return uuid.Nil, "", err
	}
	row, err := s.store.CreateSession(ctx, gen.CreateSessionParams{
		UserID:      userID,
		WorkspaceID: wsID,
		TokenHash:   hash,
		FamilyID:    familyID,
		ExpiresAt:   pgxTimestamp(time.Now().Add(s.refreshTTL)),
		UserAgent:   ptr(ua),
		Ip:          parseIP(ip),
	})
	if err != nil {
		return uuid.Nil, "", err
	}
	return row.ID, raw, nil
}

// Register creates a new workspace, owner user, and first session
// atomically (via store.RegisterTx), then starts a brand-new refresh-token
// family for the session.
func (s *Service) Register(ctx context.Context, in RegisterInput) (Session, error) {
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return Session{}, err
	}
	wsID, userID, err := s.store.RegisterTx(ctx, in.WorkspaceName, in.Email, hash)
	if err != nil {
		return Session{}, err // handler maps unique-violation -> 409
	}
	fam := uuid.New()
	sid, raw, err := s.newSessionRow(ctx, userID, wsID, fam, in.UserAgent, in.IP)
	if err != nil {
		return Session{}, err
	}
	mems, _ := s.memberships(ctx, userID)
	return Session{UserID: userID, WorkspaceID: wsID, Role: "owner", SessionID: sid, RawRefresh: raw, Memberships: mems}, nil
}

// Login verifies credentials, activates the user's most-recently-seen
// workspace (per ListMembersByUser ordering), and starts a new refresh-token
// family.
func (s *Service) Login(ctx context.Context, email, pw, ua, ip string) (Session, error) {
	user, err := s.store.GetUserByEmail(ctx, email)
	if err != nil || !auth.CheckPassword(user.PasswordHash, pw) {
		return Session{}, ErrInvalidCredentials
	}
	mems, err := s.memberships(ctx, user.ID)
	if err != nil || len(mems) == 0 {
		return Session{}, ErrNoWorkspace
	}
	active := mems[0] // ListMembersByUser orders by last_seen desc, created asc
	_ = s.store.TouchMemberLastSeen(ctx, active.WorkspaceID, user.ID)
	fam := uuid.New()
	sid, raw, err := s.newSessionRow(ctx, user.ID, active.WorkspaceID, fam, ua, ip)
	if err != nil {
		return Session{}, err
	}
	return Session{UserID: user.ID, WorkspaceID: active.WorkspaceID, Role: active.Role, SessionID: sid, RawRefresh: raw, Memberships: mems}, nil
}

// Refresh rotates a refresh token: the presented token is looked up by
// hash, revoked, and replaced with a new one in the same family. If the
// presented token is unknown, already revoked, or expired, the entire
// family is revoked (reuse detection) and an error is returned.
func (s *Service) Refresh(ctx context.Context, raw, ua, ip string) (Session, error) {
	row, err := s.store.GetSessionByHash(ctx, auth.HashRefreshToken(raw))
	if err != nil {
		return Session{}, ErrRefreshInvalid
	}
	// Reuse detection: a revoked or expired token kills the whole family.
	if row.RevokedAt.Valid || time.Now().After(pgxTime(row.ExpiresAt)) {
		_ = s.store.RevokeFamily(ctx, row.FamilyID)
		return Session{}, ErrRefreshInvalid
	}
	if err := s.store.RevokeSession(ctx, row.ID); err != nil {
		return Session{}, err
	}
	member, err := s.store.GetMember(ctx, row.WorkspaceID, row.UserID)
	if err != nil {
		return Session{}, ErrRefreshInvalid
	}
	sid, newRaw, err := s.newSessionRow(ctx, row.UserID, row.WorkspaceID, row.FamilyID, ua, ip)
	if err != nil {
		return Session{}, err
	}
	mems, _ := s.memberships(ctx, row.UserID)
	return Session{UserID: row.UserID, WorkspaceID: row.WorkspaceID, Role: string(member.Role), SessionID: sid, RawRefresh: newRaw, Memberships: mems}, nil
}

// Logout revokes the entire refresh-token family for the presented token.
// An unknown token is treated as already logged out (idempotent).
func (s *Service) Logout(ctx context.Context, raw string) error {
	row, err := s.store.GetSessionByHash(ctx, auth.HashRefreshToken(raw))
	if err != nil {
		return nil // already gone; idempotent
	}
	return s.store.RevokeFamily(ctx, row.FamilyID)
}

// LogoutAll revokes every active session belonging to a user, across all
// families and devices.
func (s *Service) LogoutAll(ctx context.Context, userID uuid.UUID) error {
	return s.store.RevokeAllForUser(ctx, userID)
}

// SwitchWorkspace repoints an existing session at a different workspace the
// user is a member of, returning the new active workspace and the user's
// role there. Returns ErrNotMember if the user does not belong to target.
func (s *Service) SwitchWorkspace(ctx context.Context, sessionID, userID, target uuid.UUID) (uuid.UUID, string, error) {
	m, err := s.store.GetMember(ctx, target, userID)
	if err != nil {
		return uuid.Nil, "", ErrNotMember
	}
	if err := s.store.RepointSessionWorkspace(ctx, sessionID, target); err != nil {
		return uuid.Nil, "", err
	}
	_ = s.store.TouchMemberLastSeen(ctx, target, userID)
	return target, string(m.Role), nil
}

// Memberships returns every workspace the user belongs to.
func (s *Service) Memberships(ctx context.Context, userID uuid.UUID) ([]Membership, error) {
	return s.memberships(ctx, userID)
}

func (s *Service) memberships(ctx context.Context, userID uuid.UUID) ([]Membership, error) {
	rows, err := s.store.ListMembersByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]Membership, len(rows))
	for i, r := range rows {
		out[i] = Membership{WorkspaceID: r.WorkspaceID, WorkspaceName: r.WorkspaceName, Role: string(r.Role)}
	}
	return out, nil
}
