package identity

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/notify"
)

var (
	// ErrInviteExists is returned by CreateInvite when a pending invite
	// already exists for the same (workspace, email) pair - the partial
	// unique index on workspace_invites enforces this at the DB level.
	ErrInviteExists = errors.New("a pending invite already exists for this email")
	// ErrPasswordRequired is returned by AcceptInvite when the invited email
	// has no existing account and the caller didn't supply a password to
	// create one.
	ErrPasswordRequired = errors.New("password required to create an account")
)

// Invite is a single workspace invitation as returned to callers - decoupled
// from the sqlc row shape (never exposes token_hash).
type Invite struct {
	ID        uuid.UUID
	Email     string
	Role      string
	Status    string
	ExpiresAt time.Time
	CreatedAt time.Time
}

func toInvite(i gen.WorkspaceInvite) Invite {
	return Invite{
		ID: i.ID, Email: i.Email, Role: string(i.Role), Status: string(i.Status),
		ExpiresAt: pgxTime(i.ExpiresAt), CreatedAt: pgxTime(i.CreatedAt),
	}
}

// CreateInvite persists a pending invite for email into ws at role and emails
// an accept link, returning the created invite. The invite is persisted
// first and the send is best-effort (logged on failure, not returned): a
// transactional-email outage must not stop an admin from creating an invite
// they can still hand the accept link to the invitee out of band.
func (s *Service) CreateInvite(ctx context.Context, ws, invitedBy uuid.UUID, email, role string) (Invite, error) {
	raw, hash, err := auth.NewOpaqueToken()
	if err != nil {
		return Invite{}, err
	}
	inv, err := s.store.CreateInvite(ctx, gen.CreateInviteParams{
		WorkspaceID: ws, Email: email, Role: gen.MemberRole(role),
		TokenHash: hash, InvitedBy: invitedBy, ExpiresAt: pgxTimestamp(time.Now().Add(s.inviteTTL)),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return Invite{}, ErrInviteExists
		}
		return Invite{}, err
	}
	wsRow, err := s.store.GetWorkspace(ctx, ws)
	if err != nil {
		slog.Error("identity: failed to load workspace for invite email", "err", err, "invite_id", inv.ID)
		return toInvite(inv), nil
	}
	link := s.appBaseURL + "/accept-invite?token=" + url.QueryEscape(raw)
	if err := s.sender.Send(ctx, notify.InviteEmail(wsRow.Name, link)); err != nil {
		slog.Error("identity: failed to send invite email", "err", err, "invite_id", inv.ID)
	}
	return toInvite(inv), nil
}

// ListInvites returns every pending invite for ws.
func (s *Service) ListInvites(ctx context.Context, ws uuid.UUID) ([]Invite, error) {
	rows, err := s.store.ListPendingInvites(ctx, ws)
	if err != nil {
		return nil, err
	}
	out := make([]Invite, len(rows))
	for i, r := range rows {
		out[i] = toInvite(r)
	}
	return out, nil
}

// RevokeInvite revokes a pending invite scoped to ws. An invite belonging to
// a different workspace, already accepted, or already revoked silently
// no-ops - matching the underlying UPDATE ... WHERE's affected-rows behavior.
func (s *Service) RevokeInvite(ctx context.Context, ws, inviteID uuid.UUID) error {
	return s.store.RevokeInvite(ctx, gen.RevokeInviteParams{ID: inviteID, WorkspaceID: ws})
}

// AcceptInvite atomically consumes a workspace invite via Store.AcceptInviteTx:
// validates the token, resolves the invited email to an existing user or
// creates a brand-new one (which requires password to be non-nil), adds
// their membership at the invite's role (leaving an existing membership's
// role untouched), marks the invite accepted, marks the resulting user's
// email verified (the invite itself proves inbox ownership), and issues a
// session - all in one transaction, so a crash mid-accept can never leave a
// half-created account or a consumed invite with no membership. ua/ip are
// recorded on the new session row exactly like Register/Login. Reuses the
// same refresh-token minting as Register/Login (the provider-agnostic
// session-issuance path a future OAuth callback would also use).
func (s *Service) AcceptInvite(ctx context.Context, rawToken string, password *string, ua, ip string) (Session, error) {
	var hash *string
	if password != nil {
		h, err := auth.HashPassword(*password)
		if err != nil {
			return Session{}, err
		}
		hash = &h
	}
	raw, tokHash, err := auth.NewRefreshToken()
	if err != nil {
		return Session{}, err
	}
	res, err := s.store.AcceptInviteTx(ctx, AcceptInviteTxParams{
		RawToken:     rawToken,
		PasswordHash: hash,
		SessionParams: gen.CreateSessionParams{
			TokenHash: tokHash,
			FamilyID:  uuid.New(),
			ExpiresAt: pgxTimestamp(time.Now().Add(s.refreshTTL)),
			UserAgent: ptr(ua),
			Ip:        parseIP(ip),
		},
	})
	if err != nil {
		return Session{}, err // ErrTokenInvalid or ErrPasswordRequired
	}
	mems, _ := s.memberships(ctx, res.UserID)
	return Session{
		UserID: res.UserID, WorkspaceID: res.WorkspaceID, Role: res.Role,
		SessionID: res.SessionID, RawRefresh: raw, Memberships: mems,
	}, nil
}
