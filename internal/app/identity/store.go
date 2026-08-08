// Package identity owns authentication: users, workspace membership, and sessions.
package identity

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// ErrTokenInvalid is returned by ConsumeUserToken when the presented raw
// token doesn't match a stored hash for the given kind, or the matching row
// is already consumed or expired.
var ErrTokenInvalid = errors.New("token invalid or expired")

// ErrIdentityEmailMismatch is returned by AcceptInviteTx when a FEDERATED caller
// presents an invite addressed to someone other than the provider-verified
// address they authenticated with. See AcceptInviteTxParams.ExpectedEmail.
var ErrIdentityEmailMismatch = errors.New("invite was issued to a different email address")

// ErrStateInvalid is returned by ConsumeLoginState when a sign-in state cannot be
// claimed: unknown nonce, wrong purpose, expired, or already used (a replay).
// Deliberately one error for all four so the callback exposes no oracle.
var ErrStateInvalid = errors.New("oauth state invalid or already used")

// Store wraps the sqlc-generated queries for the identity domain (users,
// workspaces, workspace members, sessions) and adds the one multi-statement
// operation (RegisterTx) that must run atomically.
type Store struct {
	pool *pgxpool.Pool
	q    *gen.Queries
}

// NewStore constructs a Store backed by the given connection pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, q: gen.New(pool)}
}

// RegisterTx creates a workspace, an owner user, membership, AND the first
// refresh-token session for that user — all in a single database
// transaction. Either every row lands or none does; a partial register can
// no longer leave a user with no workspace or a workspace with no session.
//
// Returns the new workspace id, user id, and session id. The session row is
// built from arg.SessionParams; the caller minted the token hash and family
// id (see identity.Service.Register).
func (s *Store) RegisterTx(ctx context.Context, arg RegisterTxParams) (RegisterTxResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RegisterTxResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed

	qtx := s.q.WithTx(tx)

	res, err := createWorkspaceOwner(ctx, qtx, arg.WorkspaceName, arg.Email, &arg.PasswordHash)
	if err != nil {
		return RegisterTxResult{}, err
	}

	// Session is created inside the same tx: if any earlier step (or the
	// commit itself) fails, no session row lingers for a user that isn't
	// there. UserID/WorkspaceID come from the just-inserted rows above.
	sp := arg.SessionParams
	sp.UserID = res.UserID
	sp.WorkspaceID = res.WorkspaceID
	session, err := qtx.CreateSession(ctx, sp)
	if err != nil {
		return RegisterTxResult{}, err
	}
	res.SessionID = session.ID

	if err := tx.Commit(ctx); err != nil {
		return RegisterTxResult{}, err
	}
	return res, nil
}

// createWorkspaceOwner inserts the workspace / user / owner-membership triple
// that BOTH signup paths (password register and federated sign-up) begin with,
// inside the caller's transaction. passwordHash is nil for a federated account,
// which has no password at all — the users.password_hash column is nullable for
// exactly that case (migration 000051), and password login treats NULL as "no
// password" rather than comparing against an empty string.
//
// The returned RegisterTxResult has SessionID unset; the caller mints the session
// inside the same transaction, since each path passes different session params.
func createWorkspaceOwner(ctx context.Context, qtx *gen.Queries, workspaceName, email string, passwordHash *string) (RegisterTxResult, error) {
	ws, err := qtx.CreateWorkspace(ctx, workspaceName)
	if err != nil {
		return RegisterTxResult{}, err
	}
	user, err := qtx.CreateUser(ctx, gen.CreateUserParams{Email: email, PasswordHash: passwordHash})
	if err != nil {
		return RegisterTxResult{}, err
	}
	if _, err := qtx.CreateMember(ctx, gen.CreateMemberParams{
		WorkspaceID: ws.ID,
		UserID:      user.ID,
		Role:        gen.MemberRoleOwner,
	}); err != nil {
		return RegisterTxResult{}, err
	}
	return RegisterTxResult{WorkspaceID: ws.ID, UserID: user.ID}, nil
}

// RegisterTxParams carries the inputs RegisterTx needs. SessionParams
// carries the token hash, family id, expires_at, and client metadata for
// the initial session row — UserID and WorkspaceID are ignored here
// (RegisterTx fills them in from the rows it just inserted).
type RegisterTxParams struct {
	WorkspaceName string
	Email         string
	PasswordHash  string
	SessionParams gen.CreateSessionParams
}

// RegisterTxResult is the tuple of ids the caller needs to keep going
// (issue an access token, set cookies, load memberships).
type RegisterTxResult struct {
	WorkspaceID uuid.UUID
	UserID      uuid.UUID
	SessionID   uuid.UUID
}

// GetUserByEmail returns the user with the given email.
func (s *Store) GetUserByEmail(ctx context.Context, email string) (gen.User, error) {
	return s.q.GetUserByEmail(ctx, email)
}

// GetUserByID returns the user with the given ID.
func (s *Store) GetUserByID(ctx context.Context, id uuid.UUID) (gen.User, error) {
	return s.q.GetUserByID(ctx, id)
}

// IsEmailVerified reports whether userID has confirmed their email address.
// Satisfies auth.VerifiedChecker so RequireVerified can gate routes without
// the auth package importing identity. Reuses GetUserByID rather than adding
// a new sqlc query since email_verified_at is already selected there.
func (s *Store) IsEmailVerified(ctx context.Context, userID uuid.UUID) (bool, error) {
	u, err := s.q.GetUserByID(ctx, userID)
	if err != nil {
		return false, err
	}
	return u.EmailVerifiedAt.Valid, nil
}

// ListMembersByUser returns every workspace membership (with workspace name)
// for the given user, most recently seen first.
func (s *Store) ListMembersByUser(ctx context.Context, userID uuid.UUID) ([]gen.ListMembersByUserRow, error) {
	return s.q.ListMembersByUser(ctx, userID)
}

// GetMember returns the membership linking a workspace and a user.
func (s *Store) GetMember(ctx context.Context, wsID, userID uuid.UUID) (gen.WorkspaceMember, error) {
	return s.q.GetMember(ctx, gen.GetMemberParams{WorkspaceID: wsID, UserID: userID})
}

// TouchMemberLastSeen updates a membership's last_seen_at to now.
func (s *Store) TouchMemberLastSeen(ctx context.Context, wsID, userID uuid.UUID) error {
	return s.q.TouchMemberLastSeen(ctx, gen.TouchMemberLastSeenParams{WorkspaceID: wsID, UserID: userID})
}

// CreateSession persists a new session row.
func (s *Store) CreateSession(ctx context.Context, arg gen.CreateSessionParams) (gen.Session, error) {
	return s.q.CreateSession(ctx, arg)
}

// GetSessionByHash looks up a session by its token hash.
func (s *Store) GetSessionByHash(ctx context.Context, tokenHash []byte) (gen.Session, error) {
	return s.q.GetSessionByHash(ctx, tokenHash)
}

// RevokeSession marks a single session as revoked, returning the number of
// rows actually flipped (0 if the session was already revoked or doesn't
// exist, letting the caller detect a concurrent revoke).
func (s *Store) RevokeSession(ctx context.Context, id uuid.UUID) (int64, error) {
	return s.q.RevokeSession(ctx, id)
}

// RevokeFamily marks every still-live session in a refresh-token family as
// revoked, returning the ids actually flipped so an in-process caller can bust
// the verifier's cached auth-state for each (mirrors RevokeOtherSessionsForUser).
func (s *Store) RevokeFamily(ctx context.Context, familyID uuid.UUID) ([]uuid.UUID, error) {
	return s.q.RevokeFamily(ctx, familyID)
}

// RevokeAllForUser marks every active session belonging to a user as revoked,
// returning the ids actually flipped so an in-process caller (logout-all) can
// bust the verifier's cached auth-state for each.
func (s *Store) RevokeAllForUser(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	return s.q.RevokeAllForUser(ctx, userID)
}

// IssueUserToken mints a new opaque single-use token of the given kind
// (email verify, password reset, ...) for userID, persisting only its
// SHA-256 hash with an expiry of ttl from now. Returns the raw token for the
// caller to embed in a link/email; the raw value is never stored.
func (s *Store) IssueUserToken(ctx context.Context, userID uuid.UUID, kind string, ttl time.Duration) (string, error) {
	raw, hash, err := auth.NewOpaqueToken()
	if err != nil {
		return "", err
	}
	_, err = s.q.CreateUserToken(ctx, gen.CreateUserTokenParams{
		UserID:    userID,
		Kind:      gen.UserTokenKind(kind),
		TokenHash: hash,
		ExpiresAt: pgxTimestamp(time.Now().Add(ttl)),
	})
	if err != nil {
		return "", err
	}
	return raw, nil
}

// ConsumeUserToken looks up a user token by the hash of raw and kind, and
// atomically marks it consumed (single-use). Returns ErrTokenInvalid if no
// matching, unconsumed, unexpired row exists — a wrong token, a kind
// mismatch, a replay, or an expired token all look identical to the caller.
func (s *Store) ConsumeUserToken(ctx context.Context, raw, kind string) (uuid.UUID, error) {
	uid, err := s.q.ConsumeUserToken(ctx, gen.ConsumeUserTokenParams{
		TokenHash: auth.HashToken(raw),
		Kind:      gen.UserTokenKind(kind),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrTokenInvalid
	}
	if err != nil {
		return uuid.Nil, err
	}
	return uid, nil
}

// CountRecentUserTokens returns how many tokens of kind have been issued to
// userID since the given time, for rate-limiting repeated issuance (e.g.
// password-reset requests).
func (s *Store) CountRecentUserTokens(ctx context.Context, userID uuid.UUID, kind string, since time.Time) (int64, error) {
	return s.q.CountRecentUserTokens(ctx, gen.CountRecentUserTokensParams{
		UserID:    userID,
		Kind:      gen.UserTokenKind(kind),
		CreatedAt: pgxTimestamp(since),
	})
}

// SetEmailVerified marks a user's email as verified (sets email_verified_at
// to now).
func (s *Store) SetEmailVerified(ctx context.Context, id uuid.UUID) error {
	return s.q.SetEmailVerified(ctx, id)
}

// UpdatePasswordHash overwrites a user's password_hash (used by password reset).
// The column is nullable (a federated account has none), but this takes a plain
// string: setting a password NEVER means clearing one. A reset on a Google-only
// account legitimately GIVES it a password -- that is account recovery for an
// address the provider already verified -- and there is no code path that should be
// able to strip a password by passing nil here.
func (s *Store) UpdatePasswordHash(ctx context.Context, id uuid.UUID, hash string) error {
	return s.q.UpdatePasswordHash(ctx, gen.UpdatePasswordHashParams{ID: id, PasswordHash: &hash})
}

// ResetPasswordTx atomically consumes a password_reset token, overwrites the
// owning user's password_hash, and revokes every one of their sessions - all
// in a single transaction, so a crash between steps can never leave the hash
// updated with old sessions still live (or the reverse). Mirrors RegisterTx's
// pattern of running several statements as one qtx-scoped unit.
func (s *Store) ResetPasswordTx(ctx context.Context, rawToken, kind, newHash string) ([]uuid.UUID, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed

	qtx := s.q.WithTx(tx)

	uid, err := qtx.ConsumeUserToken(ctx, gen.ConsumeUserTokenParams{
		TokenHash: auth.HashToken(rawToken),
		Kind:      gen.UserTokenKind(kind),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTokenInvalid
	}
	if err != nil {
		return nil, err
	}

	if err := qtx.UpdatePasswordHash(ctx, gen.UpdatePasswordHashParams{ID: uid, PasswordHash: &newHash}); err != nil {
		return nil, err
	}
	// The ids RevokeAllForUser flips are returned so the caller can bust each
	// revoked session's cached auth-state in-process — a just-reset access token
	// is then rejected on its next request rather than after the cache TTL.
	revoked, err := qtx.RevokeAllForUser(ctx, uid)
	if err != nil {
		return nil, err
	}
	// Belt-and-braces with RevokeAllForUser: advance every session's
	// token_version so any access token already minted for this user is
	// rejected by the verifier even in the (impossible-by-construction) case a
	// session escaped revocation. A password reset is a full security event.
	if err := qtx.BumpTokenVersionForUser(ctx, uid); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return revoked, nil
}

// GetSessionAuthState returns the minimal per-request validation state for a
// session (owning user, revocation, expiry, token_version), mapped off the
// pgx row into plain Go types so the verifier stays free of persistence types.
// A missing session surfaces as pgx.ErrNoRows for the caller to treat as an
// unknown (rejected) session.
func (s *Store) GetSessionAuthState(ctx context.Context, sid uuid.UUID) (SessionAuthState, error) {
	row, err := s.q.GetSessionAuthState(ctx, sid)
	if err != nil {
		return SessionAuthState{}, err
	}
	return SessionAuthState{
		UserID:       row.UserID,
		Revoked:      row.RevokedAt.Valid,
		ExpiresAt:    pgxTime(row.ExpiresAt),
		TokenVersion: int(row.TokenVersion),
	}, nil
}

// BumpSessionTokenVersion advances a single session's token_version, rejecting
// every access token previously minted for it. The primitive later phases hook
// for 2FA/passkey changes on a session that stays otherwise live.
func (s *Store) BumpSessionTokenVersion(ctx context.Context, sid uuid.UUID) error {
	return s.q.BumpSessionTokenVersion(ctx, sid)
}

// ListActiveSessionsForUser returns a user's live sessions (never any token
// hash) for the session-management surface.
func (s *Store) ListActiveSessionsForUser(ctx context.Context, userID uuid.UUID) ([]gen.ListActiveSessionsForUserRow, error) {
	return s.q.ListActiveSessionsForUser(ctx, userID)
}

// RevokeSessionOwned revokes one session, pinned to its owning user so a
// caller can never revoke another user's session. Returns rows affected (0 if
// the id is foreign, unknown, or already revoked).
func (s *Store) RevokeSessionOwned(ctx context.Context, sid, userID uuid.UUID) (int64, error) {
	return s.q.RevokeSessionOwned(ctx, gen.RevokeSessionOwnedParams{ID: sid, UserID: userID})
}

// RevokeOtherSessionsForUser revokes all of a user's active sessions except
// keepSID, returning the ids actually revoked (for cache invalidation).
func (s *Store) RevokeOtherSessionsForUser(ctx context.Context, userID, keepSID uuid.UUID) ([]uuid.UUID, error) {
	return s.q.RevokeOtherSessionsForUser(ctx, gen.RevokeOtherSessionsForUserParams{UserID: userID, ID: keepSID})
}

// CreateInvite persists a new pending workspace invite. A second pending
// invite for the same (workspace, email) pair fails with a unique-violation
// (the partial index on workspace_invites) - the caller (Service.CreateInvite)
// maps that to ErrInviteExists.
func (s *Store) CreateInvite(ctx context.Context, arg gen.CreateInviteParams) (gen.WorkspaceInvite, error) {
	return s.q.CreateInvite(ctx, arg)
}

// ListPendingInvites returns every pending invite for a workspace.
func (s *Store) ListPendingInvites(ctx context.Context, wsID uuid.UUID) ([]gen.WorkspaceInvite, error) {
	return s.q.ListPendingInvites(ctx, wsID)
}

// RevokeInvite marks a pending invite revoked, scoped to its workspace. An
// invite that's missing, belongs to a different workspace, or is no longer
// pending silently affects 0 rows - matching the underlying UPDATE ... WHERE.
func (s *Store) RevokeInvite(ctx context.Context, arg gen.RevokeInviteParams) error {
	return s.q.RevokeInvite(ctx, arg)
}

// GetWorkspace returns the workspace with the given id.
func (s *Store) GetWorkspace(ctx context.Context, id uuid.UUID) (gen.Workspace, error) {
	return s.q.GetWorkspace(ctx, id)
}

// IdentityLink is an external (federated) identity to attach to a local user:
// the provider name and the provider's own immutable subject id. Never an email —
// see migration 000051 for why the subject is the key.
type IdentityLink struct {
	Provider string
	Subject  string
}

// AcceptInviteTxParams carries the inputs AcceptInviteTx needs.
// SessionParams mirrors RegisterTx's convention: UserID/WorkspaceID are
// ignored here - the tx fills them in from the invite it resolves.
//
// TokenHash is the SHA-256 of the raw invite token; the caller hashes it, so the
// store never has to know the token's wire format.
//
// Exactly one of PasswordHash or Identity establishes how a NEWLY created user
// will authenticate (both nil, for an email with no existing account, is
// ErrPasswordRequired). Identity is also linked to the resolved user inside the
// same transaction, so a federated invite acceptance can never leave a member
// with no way to sign back in.
//
// ExpectedEmail is REQUIRED whenever Identity is set and must equal the invite's
// address: the invite token is a bearer credential, so without this check
// whoever obtained a link addressed to alice@ could present it while
// authenticating to the provider as bob@ and be joined to a workspace nobody
// invited them to. It is unused (and ignored) on the password path, where the
// token alone is the credential and the account created IS the invited address.
type AcceptInviteTxParams struct {
	TokenHash     []byte
	PasswordHash  *string
	Identity      *IdentityLink
	ExpectedEmail string
	SessionParams gen.CreateSessionParams
}

// AcceptInviteTxResult is the tuple of ids/role the caller needs to build a
// Session.
type AcceptInviteTxResult struct {
	WorkspaceID uuid.UUID
	UserID      uuid.UUID
	Role        string
	SessionID   uuid.UUID
}

// AcceptInviteTx atomically consumes a workspace invite in a single
// transaction: validates the token by hash (pending, unexpired), resolves the
// invited email to an existing user or creates one (requiring
// arg.PasswordHash), adds their workspace_members row at the invite's role
// (or leaves an existing membership's role untouched - see below), marks the
// invite accepted, marks the resolved user's email verified (the invite
// itself proves inbox ownership), and mints the first session - mirroring
// RegisterTx's "everything or nothing" shape so a crash mid-accept can never
// leave a consumed invite with no membership, or a new account with no
// session.
func (s *Store) AcceptInviteTx(ctx context.Context, arg AcceptInviteTxParams) (AcceptInviteTxResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return AcceptInviteTxResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed

	qtx := s.q.WithTx(tx)

	invite, err := qtx.GetInviteByHash(ctx, arg.TokenHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AcceptInviteTxResult{}, ErrTokenInvalid
		}
		return AcceptInviteTxResult{}, err
	}
	if invite.Status != gen.InviteStatusPending || time.Now().After(pgxTime(invite.ExpiresAt)) {
		return AcceptInviteTxResult{}, ErrTokenInvalid
	}
	// The federated path must prove the provider-authenticated address IS the
	// invited one; see AcceptInviteTxParams.ExpectedEmail. Checked here, inside the
	// transaction that reads the invite, so no caller can skip it. Case-insensitive
	// because workspace_invites.email is CITEXT.
	if arg.Identity != nil && !strings.EqualFold(arg.ExpectedEmail, invite.Email) {
		return AcceptInviteTxResult{}, ErrIdentityEmailMismatch
	}

	var userID uuid.UUID
	existing, err := qtx.GetUserByEmail(ctx, invite.Email)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if arg.PasswordHash == nil && arg.Identity == nil {
			return AcceptInviteTxResult{}, ErrPasswordRequired
		}
		created, err := qtx.CreateUser(ctx, gen.CreateUserParams{Email: invite.Email, PasswordHash: arg.PasswordHash})
		if err != nil {
			return AcceptInviteTxResult{}, err
		}
		userID = created.ID
	case err != nil:
		return AcceptInviteTxResult{}, err
	default:
		userID = existing.ID
	}
	if err := qtx.SetEmailVerified(ctx, userID); err != nil {
		return AcceptInviteTxResult{}, err
	}
	if arg.Identity != nil {
		if err := qtx.CreateUserIdentity(ctx, gen.CreateUserIdentityParams{
			UserID: userID, Provider: arg.Identity.Provider, ProviderSubject: arg.Identity.Subject,
		}); err != nil {
			return AcceptInviteTxResult{}, err
		}
	}

	// Accepting an invite must ADD a membership, never mutate an existing
	// one's role - a role change is a separate, explicit admin action. Without
	// this check, an owner or admin invited (accidentally or maliciously) at
	// a lower role would be silently downgraded the moment they accepted.
	role := invite.Role
	member, err := qtx.GetMember(ctx, gen.GetMemberParams{WorkspaceID: invite.WorkspaceID, UserID: userID})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if _, err := qtx.CreateMember(ctx, gen.CreateMemberParams{
			WorkspaceID: invite.WorkspaceID, UserID: userID, Role: invite.Role,
		}); err != nil {
			return AcceptInviteTxResult{}, err
		}
	case err != nil:
		return AcceptInviteTxResult{}, err
	default:
		role = member.Role // already a member: keep their existing role
	}
	// Guarded UPDATE ... WHERE status='pending': 0 rows means a concurrent
	// accept already flipped this invite between our earlier status check and
	// here. Row-level locking inside the transaction serializes a race here -
	// the first accept's UPDATE wins and this one sees ErrNoRows.
	if _, err := qtx.MarkInviteAccepted(ctx, invite.ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AcceptInviteTxResult{}, ErrTokenInvalid
		}
		return AcceptInviteTxResult{}, err
	}

	sp := arg.SessionParams
	sp.UserID = userID
	sp.WorkspaceID = invite.WorkspaceID
	session, err := qtx.CreateSession(ctx, sp)
	if err != nil {
		return AcceptInviteTxResult{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return AcceptInviteTxResult{}, err
	}

	return AcceptInviteTxResult{
		WorkspaceID: invite.WorkspaceID, UserID: userID, Role: string(role), SessionID: session.ID,
	}, nil
}

// FederatedSignupTxParams carries the inputs FederatedSignupTx needs.
// SessionParams follows RegisterTx's convention: UserID/WorkspaceID are ignored
// (the tx fills them in from the rows it inserts).
type FederatedSignupTxParams struct {
	WorkspaceName string
	Email         string
	Identity      IdentityLink
	SessionParams gen.CreateSessionParams
}

// FederatedSignupTx creates a workspace, a PASSWORD-LESS owner user with their
// email already verified, the owner membership, the provider identity link, and
// the first session — all in one transaction, mirroring RegisterTx's
// everything-or-nothing shape (and sharing its createWorkspaceOwner core).
//
// The email is marked verified because the caller only reaches here after the
// provider asserted `email_verified` for it; a federated signup with an
// unverified address is refused before this point (see Service.CompleteGoogleSignIn).
func (s *Store) FederatedSignupTx(ctx context.Context, arg FederatedSignupTxParams) (RegisterTxResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RegisterTxResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once committed

	qtx := s.q.WithTx(tx)

	// nil password hash: a federated account has no password. See
	// createWorkspaceOwner and migration 000051.
	res, err := createWorkspaceOwner(ctx, qtx, arg.WorkspaceName, arg.Email, nil)
	if err != nil {
		return RegisterTxResult{}, err
	}
	if err := qtx.SetEmailVerified(ctx, res.UserID); err != nil {
		return RegisterTxResult{}, err
	}
	if err := qtx.CreateUserIdentity(ctx, gen.CreateUserIdentityParams{
		UserID: res.UserID, Provider: arg.Identity.Provider, ProviderSubject: arg.Identity.Subject,
	}); err != nil {
		return RegisterTxResult{}, err
	}
	sp := arg.SessionParams
	sp.UserID = res.UserID
	sp.WorkspaceID = res.WorkspaceID
	session, err := qtx.CreateSession(ctx, sp)
	if err != nil {
		return RegisterTxResult{}, err
	}
	res.SessionID = session.ID

	if err := tx.Commit(ctx); err != nil {
		return RegisterTxResult{}, err
	}
	return res, nil
}

// GetUserIdentity resolves an external (provider, subject) pair to its local
// identity row. pgx.ErrNoRows means this provider account has never signed in
// here, which is the caller's cue to try linking or signing up.
func (s *Store) GetUserIdentity(ctx context.Context, provider, subject string) (gen.UserIdentity, error) {
	return s.q.GetUserIdentity(ctx, gen.GetUserIdentityParams{Provider: provider, ProviderSubject: subject})
}

// LinkUserIdentity attaches an external identity to an EXISTING local user. A
// unique-key violation means another user already claimed that provider identity
// and surfaces as an error rather than a silent no-op (see the query's comment).
func (s *Store) LinkUserIdentity(ctx context.Context, userID uuid.UUID, provider, subject string) error {
	return s.q.CreateUserIdentity(ctx, gen.CreateUserIdentityParams{
		UserID: userID, Provider: provider, ProviderSubject: subject,
	})
}

// GetLatestPendingInviteByEmail returns the newest unexpired pending invite for
// an address, across all workspaces. pgx.ErrNoRows means nobody has invited them.
func (s *Store) GetLatestPendingInviteByEmail(ctx context.Context, email string) (gen.WorkspaceInvite, error) {
	return s.q.GetLatestPendingInviteByEmail(ctx, email)
}

// CreateLoginState persists the server-side half of a federated sign-in state:
// the nonce hash, the flow purpose, the PKCE verifier, and (optionally) the hash
// of a pending invite token to resolve on the callback.
func (s *Store) CreateLoginState(ctx context.Context, arg CreateLoginStateParams) error {
	return s.q.CreateOauthLoginState(ctx, gen.CreateOauthLoginStateParams{
		NonceHash:       arg.NonceHash,
		Purpose:         arg.Purpose,
		CodeVerifier:    arg.CodeVerifier,
		InviteTokenHash: arg.InviteTokenHash,
		ReturnTo:        ptr(arg.ReturnTo),
		ExpiresAt:       pgxTimestamp(arg.ExpiresAt),
	})
}

// CreateLoginStateParams carries one row of federated sign-in state.
type CreateLoginStateParams struct {
	NonceHash       []byte
	Purpose         string
	CodeVerifier    string
	InviteTokenHash []byte
	// ReturnTo is an already-validated same-origin path (see safeReturnTo); "" is
	// stored as NULL.
	ReturnTo  string
	ExpiresAt time.Time
}

// LoginState is what a consumed sign-in state hands back to the callback.
type LoginState struct {
	CodeVerifier    string
	InviteTokenHash []byte
	ReturnTo        string
}

// ConsumeLoginState atomically claims a sign-in state exactly once, returning the
// PKCE verifier and any stashed invite-token hash. An unknown nonce, a
// purpose mismatch, an expired row, and a replay are indistinguishable: all
// affect 0 rows and surface as ErrStateInvalid, so a leaked state URL gives an
// attacker no oracle and is usable at most once.
func (s *Store) ConsumeLoginState(ctx context.Context, nonceHash []byte, purpose string) (LoginState, error) {
	row, err := s.q.ConsumeOauthLoginState(ctx, gen.ConsumeOauthLoginStateParams{
		NonceHash: nonceHash, Purpose: purpose,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return LoginState{}, ErrStateInvalid
	}
	if err != nil {
		return LoginState{}, err
	}
	returnTo := ""
	if row.ReturnTo != nil {
		returnTo = *row.ReturnTo
	}
	return LoginState{
		CodeVerifier: row.CodeVerifier, InviteTokenHash: row.InviteTokenHash, ReturnTo: returnTo,
	}, nil
}

// CompleteOnboarding sets a workspace's name and stamps onboarding complete in a
// single statement — atomic by construction, and idempotent: an already-completed
// workspace keeps its stored name and original timestamp (see the query). An
// unknown id surfaces as pgx.ErrNoRows.
func (s *Store) CompleteOnboarding(ctx context.Context, wsID uuid.UUID, name string) (gen.Workspace, error) {
	return s.q.CompleteWorkspaceOnboarding(ctx, gen.CompleteWorkspaceOnboardingParams{ID: wsID, Name: name})
}

// RepointSessionWorkspace switches a session's active workspace (used when a
// user swaps workspace context without re-authenticating) and returns the
// session's live token_version so the caller can re-mint an access token whose
// `tv` matches the row (a same-session re-issue must never hardcode 0). The
// userID is checked in the WHERE clause so callers can only ever repoint their
// own sessions; a non-matching (session, user) pair updates 0 rows, surfacing
// as pgx.ErrNoRows, mapped here to ErrNotMember.
func (s *Store) RepointSessionWorkspace(ctx context.Context, id, userID, wsID uuid.UUID) (int, error) {
	tv, err := s.q.RepointSessionWorkspace(ctx, gen.RepointSessionWorkspaceParams{
		ID: id, WorkspaceID: wsID, UserID: userID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotMember
	}
	if err != nil {
		return 0, err
	}
	return int(tv), nil
}
