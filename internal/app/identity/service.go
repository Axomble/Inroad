package identity

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/notify"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrNoWorkspace        = errors.New("user has no workspace")
	ErrRefreshInvalid     = errors.New("refresh token invalid")
	ErrNotMember          = errors.New("not a member of target workspace")
	ErrRateLimited        = errors.New("too many requests")
)

// dummyHash is a real argon2id hash of an arbitrary, unused password,
// computed once at process start. Login runs auth.CheckPassword against it
// on the "user not found" path so that a nonexistent email takes the same
// wall-clock time as a wrong password on a real account - closing a timing
// side-channel that would otherwise let an attacker enumerate valid emails.
var dummyHash = mustHashDummyPassword()

func mustHashDummyPassword() string {
	h, err := auth.HashPassword("correct-horse-battery-staple-dummy")
	if err != nil {
		// HashPassword only fails if crypto/rand can't be read, which would
		// make the whole process unusable anyway.
		panic("identity: could not compute dummy password hash: " + err.Error())
	}
	return h
}

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
	RegisterTx(ctx context.Context, arg RegisterTxParams) (RegisterTxResult, error)
	GetUserByEmail(ctx context.Context, email string) (gen.User, error)
	ListMembersByUser(ctx context.Context, userID uuid.UUID) ([]gen.ListMembersByUserRow, error)
	GetMember(ctx context.Context, wsID, userID uuid.UUID) (gen.WorkspaceMember, error)
	TouchMemberLastSeen(ctx context.Context, wsID, userID uuid.UUID) error
	CreateSession(ctx context.Context, arg gen.CreateSessionParams) (gen.Session, error)
	GetSessionByHash(ctx context.Context, hash []byte) (gen.Session, error)
	RevokeSession(ctx context.Context, id uuid.UUID) (int64, error)
	RevokeFamily(ctx context.Context, familyID uuid.UUID) error
	RevokeAllForUser(ctx context.Context, userID uuid.UUID) error
	RepointSessionWorkspace(ctx context.Context, id, userID, wsID uuid.UUID) error
	IssueUserToken(ctx context.Context, userID uuid.UUID, kind string, ttl time.Duration) (string, error)
	ConsumeUserToken(ctx context.Context, raw, kind string) (uuid.UUID, error)
	CountRecentUserTokens(ctx context.Context, userID uuid.UUID, kind string, since time.Time) (int64, error)
	SetEmailVerified(ctx context.Context, id uuid.UUID) error
	IsEmailVerified(ctx context.Context, userID uuid.UUID) (bool, error)
	ResetPasswordTx(ctx context.Context, rawToken, kind, newHash string) (uuid.UUID, error)
	CreateInvite(ctx context.Context, arg gen.CreateInviteParams) (gen.WorkspaceInvite, error)
	ListPendingInvites(ctx context.Context, wsID uuid.UUID) ([]gen.WorkspaceInvite, error)
	RevokeInvite(ctx context.Context, arg gen.RevokeInviteParams) error
	GetWorkspace(ctx context.Context, id uuid.UUID) (gen.Workspace, error)
	AcceptInviteTx(ctx context.Context, arg AcceptInviteTxParams) (AcceptInviteTxResult, error)
}

// Service implements the core auth logic: registration, login, refresh
// rotation with reuse detection, logout, workspace switching, and email
// verification.
type Service struct {
	store      storeIface
	refreshTTL time.Duration
	sender     notify.Sender
	appBaseURL string
	verifyTTL  time.Duration
	resetTTL   time.Duration
	inviteTTL  time.Duration

	// dispatch runs a func off the request path. ForgotPassword uses it to
	// defer everything past the initial user lookup (rate-limit check, token
	// issuance, send) so a known email doesn't cost measurably more wall-clock
	// time than an unknown one - defaults to a bare goroutine in production;
	// tests override it to run inline for determinism.
	dispatch func(func())
}

// NewService constructs a Service backed by store, issuing refresh tokens
// that expire after refreshTTL. sender delivers transactional email
// (verify/reset/invite); appBaseURL is the frontend origin links are built
// against; verifyTTL/resetTTL/inviteTTL size the lifetime of each single-use
// token kind.
func NewService(store storeIface, refreshTTL time.Duration, sender notify.Sender, appBaseURL string, verifyTTL, resetTTL, inviteTTL time.Duration) *Service {
	return &Service{
		store: store, refreshTTL: refreshTTL, sender: sender, appBaseURL: appBaseURL,
		verifyTTL: verifyTTL, resetTTL: resetTTL, inviteTTL: inviteTTL,
		dispatch: func(f func()) { go f() },
	}
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

// Register creates a new workspace, owner user, owner membership, AND the
// first refresh-token session — all inside a single database transaction
// via store.RegisterTx. A partial register can no longer leave a user with
// no session (previously two round-trips: RegisterTx then CreateSession
// outside the tx; a crash between them left orphans).
//
// The refresh token is minted here so the raw form never has to be scanned
// out of the DB — only the hash lives in the transaction, and the raw
// token is returned to the caller for the response cookie.
func (s *Service) Register(ctx context.Context, in RegisterInput) (Session, error) {
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return Session{}, err
	}
	raw, tokHash, err := auth.NewRefreshToken()
	if err != nil {
		return Session{}, err
	}
	fam := uuid.New()
	res, err := s.store.RegisterTx(ctx, RegisterTxParams{
		WorkspaceName: in.WorkspaceName,
		Email:         in.Email,
		PasswordHash:  hash,
		SessionParams: gen.CreateSessionParams{
			// UserID/WorkspaceID are filled in by RegisterTx from the rows it
			// just inserted — the caller can't know them yet.
			TokenHash: tokHash,
			FamilyID:  fam,
			ExpiresAt: pgxTimestamp(time.Now().Add(s.refreshTTL)),
			UserAgent: ptr(in.UserAgent),
			Ip:        parseIP(in.IP),
		},
	})
	if err != nil {
		return Session{}, err // handler maps unique-violation -> 409
	}
	// Email verification is best-effort: a failure to issue the token or
	// send the email must never fail registration, or a transactional-email
	// outage would lock every new signup out of an account they legitimately
	// created. The user simply stays unverified until they hit "resend".
	if tokRaw, err := s.store.IssueUserToken(ctx, res.UserID, "email_verify", s.verifyTTL); err != nil {
		slog.Error("identity: failed to issue verification token", "err", err, "user_id", res.UserID)
	} else {
		link := s.appBaseURL + "/verify-email?token=" + url.QueryEscape(tokRaw)
		if err := s.sender.Send(ctx, notify.VerifyEmail(link)); err != nil {
			slog.Error("identity: failed to send verification email", "err", err, "user_id", res.UserID)
		}
	}
	mems, _ := s.memberships(ctx, res.UserID)
	return Session{
		UserID: res.UserID, WorkspaceID: res.WorkspaceID, Role: "owner",
		SessionID: res.SessionID, RawRefresh: raw, Memberships: mems,
	}, nil
}

// VerifyEmail atomically consumes an email_verify token and, if valid, marks
// the owning user's email as verified. ConsumeUserToken enforces single-use
// and expiry server-side in one statement, so there is no separate
// check-then-act window for a token to be replayed in.
func (s *Service) VerifyEmail(ctx context.Context, raw string) error {
	uid, err := s.store.ConsumeUserToken(ctx, raw, "email_verify")
	if err != nil {
		return err // ErrTokenInvalid
	}
	return s.store.SetEmailVerified(ctx, uid)
}

// tokenRateLimitWindow and tokenRateLimitHourlyMax bound how often a single
// user can trigger a fresh single-use token (email verify, password reset,
// ...): at most one per cooldown window, and no more than the hourly cap
// even if each individual request clears the cooldown.
const (
	tokenRateLimitWindow    = time.Minute
	tokenRateLimitHourlyMax = 5
)

// tokenRateLimited reports whether userID has requested a fresh token of
// kind too recently: either one was issued within the last minute (cooldown
// between individual requests), or tokenRateLimitHourlyMax or more have been
// issued within the last hour (a coarser cap bounding how many emails a
// single account can trigger even if every individual request waits out the
// cooldown - e.g. an email-bomb attempt spaced just over 60s apart).
//
// Both checks are a plain count-then-issue read with no locking against the
// caller's later IssueUserToken call, so a tight race between concurrent
// requests could let a handful of extra sends through the window. This is
// accepted for now: the hourly cap bounds the resulting blast radius, and
// DB-level locking (e.g. an advisory lock keyed on user+kind) is deferred as
// a possible future hardening rather than done here.
func (s *Service) tokenRateLimited(ctx context.Context, userID uuid.UUID, kind string) (bool, error) {
	nRecent, err := s.store.CountRecentUserTokens(ctx, userID, kind, time.Now().Add(-tokenRateLimitWindow))
	if err != nil {
		return false, err
	}
	if nRecent > 0 {
		return true, nil
	}
	nHour, err := s.store.CountRecentUserTokens(ctx, userID, kind, time.Now().Add(-time.Hour))
	if err != nil {
		return false, err
	}
	return nHour >= tokenRateLimitHourlyMax, nil
}

// ResendVerification issues a fresh email_verify token and re-sends the
// verification email, rejecting with ErrRateLimited if the caller is within
// the cooldown window or has hit the hourly cap (see tokenRateLimited).
func (s *Service) ResendVerification(ctx context.Context, userID uuid.UUID) error {
	limited, err := s.tokenRateLimited(ctx, userID, "email_verify")
	if err != nil {
		return err
	}
	if limited {
		return ErrRateLimited
	}
	raw, err := s.store.IssueUserToken(ctx, userID, "email_verify", s.verifyTTL)
	if err != nil {
		return err
	}
	link := s.appBaseURL + "/verify-email?token=" + url.QueryEscape(raw)
	return s.sender.Send(ctx, notify.VerifyEmail(link))
}

// ForgotPassword issues a password_reset token and emails a reset link, but
// never signals to the caller whether the address belongs to a real account:
// an unknown email, a rate-limited request, and a successfully sent email all
// return nil, in about the same amount of time. Only the user lookup runs
// synchronously (both the known and unknown path pay its cost); everything
// past it - the rate-limit check, minting a token, sending the email - runs
// on s.dispatch instead of inline. Without that, a known email would cost a
// counting query, an INSERT, and a synchronous SMTP round-trip (up to ~30s)
// more than an unknown one, which is itself the account-existence leak this
// method exists to close. Errors from the deferred work are logged, not
// returned - by the time it runs, ForgotPassword has already answered the
// caller.
func (s *Service) ForgotPassword(ctx context.Context, email string) error {
	user, err := s.store.GetUserByEmail(ctx, email)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Error("identity: forgot-password lookup failed", "err", err)
		}
		return nil // unknown email (or a lookup failure): no account-existence leak
	}
	s.dispatch(func() {
		// The request context is cancelled once ForgotPassword returns, which
		// happens before (or concurrently with) this closure running - so it
		// must not be reused here. A bounded timeout still caps how long a
		// stuck SMTP send can run in the background.
		bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		limited, err := s.tokenRateLimited(bgCtx, user.ID, "password_reset")
		if err != nil {
			slog.Error("identity: forgot-password rate-limit check failed", "err", err, "user_id", user.ID)
			return
		}
		if limited {
			return
		}
		raw, err := s.store.IssueUserToken(bgCtx, user.ID, "password_reset", s.resetTTL)
		if err != nil {
			slog.Error("identity: failed to issue password_reset token", "err", err, "user_id", user.ID)
			return
		}
		link := s.appBaseURL + "/reset-password?token=" + url.QueryEscape(raw)
		if err := s.sender.Send(bgCtx, notify.ResetEmail(link)); err != nil {
			slog.Error("identity: failed to send reset email", "err", err, "user_id", user.ID)
		}
	})
	return nil
}

// ResetPassword hashes newPassword, then atomically consumes the presented
// password_reset token, overwrites the owning user's password hash, and
// revokes every one of their active sessions across all devices, via
// Store.ResetPasswordTx - the three writes either all land or none does, so
// a crash mid-reset can't leave the hash changed with old sessions still
// live (or the reverse). Revoking everything (rather than just the family
// the caller happens to be using, if any) is deliberate: whoever is
// resetting the password - the legitimate owner recovering a compromised
// account, or an attacker who just took it over - the other party's existing
// sessions must not survive the reset.
func (s *Service) ResetPassword(ctx context.Context, raw, newPassword string) error {
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		return err
	}
	_, err = s.store.ResetPasswordTx(ctx, raw, "password_reset", hash)
	return err // ErrTokenInvalid on a bad/expired/already-consumed token
}

// Login verifies credentials, activates the user's most-recently-seen
// workspace (per ListMembersByUser ordering), and starts a new refresh-token
// family.
func (s *Service) Login(ctx context.Context, email, pw, ua, ip string) (Session, error) {
	user, err := s.store.GetUserByEmail(ctx, email)
	if err != nil {
		// No such user: still run an argon2 comparison (against a fixed
		// dummy hash) so this path costs the same as a wrong-password
		// rejection below. Without this, response timing would leak whether
		// an email is registered.
		auth.CheckPassword(dummyHash, pw)
		return Session{}, ErrInvalidCredentials
	}
	if !auth.CheckPassword(user.PasswordHash, pw) {
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
//
// The revoke-then-rotate step is guarded against a TOCTOU race: two
// concurrent Refresh calls for the same token could both pass the
// not-revoked check above before either writes. RevokeSession reports how
// many rows it actually flipped (0 or 1); only the caller that wins the race
// (n==1) proceeds to mint a successor. The loser (n==0) treats this exactly
// like reuse of an already-revoked token and revokes the whole family,
// since observing 0 rows here means some other write revoked this exact
// session between our read and our write - i.e. genuine concurrent use of
// the same refresh token.
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
	n, err := s.store.RevokeSession(ctx, row.ID)
	if err != nil {
		return Session{}, err
	}
	if n == 0 {
		// Lost the race: someone else already revoked/rotated this session
		// between our read and our write. Treat as reuse.
		_ = s.store.RevokeFamily(ctx, row.FamilyID)
		return Session{}, ErrRefreshInvalid
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
// role there. Returns ErrNotMember if the user does not belong to target,
// or if the session id isn't owned by the caller (the SQL WHERE clause
// binds session id + user id together — a mismatched pair yields 0 rows
// affected, surfaced here as ErrNotMember).
func (s *Service) SwitchWorkspace(ctx context.Context, sessionID, userID, target uuid.UUID) (uuid.UUID, string, error) {
	m, err := s.store.GetMember(ctx, target, userID)
	if err != nil {
		return uuid.Nil, "", ErrNotMember
	}
	if err := s.store.RepointSessionWorkspace(ctx, sessionID, userID, target); err != nil {
		return uuid.Nil, "", err
	}
	_ = s.store.TouchMemberLastSeen(ctx, target, userID)
	return target, string(m.Role), nil
}

// Memberships returns every workspace the user belongs to.
func (s *Service) Memberships(ctx context.Context, userID uuid.UUID) ([]Membership, error) {
	return s.memberships(ctx, userID)
}

// IsEmailVerified reports whether userID has confirmed their email address.
// A thin pass-through so /auth/me can surface the same freshly-looked-up
// state RequireVerified gates on (see identity.Store.IsEmailVerified).
func (s *Service) IsEmailVerified(ctx context.Context, userID uuid.UUID) (bool, error) {
	return s.store.IsEmailVerified(ctx, userID)
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
