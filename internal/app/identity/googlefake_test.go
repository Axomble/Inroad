package identity

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// This file holds the fakeStore half of the federated sign-in seam plus the fake
// Google provider. Both live here rather than in service_test.go so the sign-in
// flow's test doubles read as one unit.

// identityKey mirrors the UNIQUE (provider, provider_subject) constraint.
func identityKey(provider, subject string) string { return provider + "|" + subject }

func (f *fakeStore) GetUserIdentity(_ context.Context, provider, subject string) (gen.UserIdentity, error) {
	row, ok := f.identities[identityKey(provider, subject)]
	if !ok {
		return gen.UserIdentity{}, pgx.ErrNoRows
	}
	return row, nil
}

// LinkUserIdentity mirrors the real store's plain INSERT: an identity already
// claimed by SOME user is an error, never a silent no-op.
func (f *fakeStore) LinkUserIdentity(_ context.Context, userID uuid.UUID, provider, subject string) error {
	key := identityKey(provider, subject)
	if _, exists := f.identities[key]; exists {
		return errors.New("fake: duplicate (provider, provider_subject)")
	}
	f.identities[key] = gen.UserIdentity{
		ID: uuid.New(), UserID: userID, Provider: provider, ProviderSubject: subject,
	}
	return nil
}

func (f *fakeStore) GetLatestPendingInviteByEmail(_ context.Context, email string) (gen.WorkspaceInvite, error) {
	var newest gen.WorkspaceInvite
	found := false
	for _, inv := range f.invites {
		if inv.Email != email || inv.Status != gen.InviteStatusPending || time.Now().After(pgxTime(inv.ExpiresAt)) {
			continue
		}
		if !found || pgxTime(inv.CreatedAt).After(pgxTime(newest.CreatedAt)) {
			newest, found = inv, true
		}
	}
	if !found {
		return gen.WorkspaceInvite{}, pgx.ErrNoRows
	}
	return newest, nil
}

func (f *fakeStore) CreateLoginState(_ context.Context, arg CreateLoginStateParams) error {
	f.loginStates[hashKey(arg.NonceHash)] = LoginState{
		CodeVerifier: arg.CodeVerifier, InviteTokenHash: arg.InviteTokenHash, ReturnTo: arg.ReturnTo,
	}
	return nil
}

// ConsumeLoginState deletes the row it returns, so a second call for the same
// nonce fails exactly as the real single-use UPDATE does.
func (f *fakeStore) ConsumeLoginState(_ context.Context, nonceHash []byte, _ string) (LoginState, error) {
	key := hashKey(nonceHash)
	row, ok := f.loginStates[key]
	if !ok {
		return LoginState{}, ErrStateInvalid
	}
	delete(f.loginStates, key)
	return row, nil
}

func (f *fakeStore) FederatedSignupTx(_ context.Context, arg FederatedSignupTxParams) (RegisterTxResult, error) {
	key := identityKey(arg.Identity.Provider, arg.Identity.Subject)
	if _, exists := f.identities[key]; exists {
		return RegisterTxResult{}, errors.New("fake: duplicate (provider, provider_subject)")
	}
	wsID, userID, sessionID := uuid.New(), uuid.New(), uuid.New()

	f.workspaces[wsID] = gen.Workspace{ID: wsID, Name: arg.WorkspaceName}
	// PasswordHash stays nil: a federated account has none.
	user := gen.User{ID: userID, Email: arg.Email, EmailVerifiedAt: pgxTimestamp(time.Now())}
	f.users[arg.Email] = user
	f.usersByID[userID] = user

	member := gen.WorkspaceMember{ID: uuid.New(), WorkspaceID: wsID, UserID: userID, Role: gen.MemberRoleOwner}
	f.memberByPair[[2]uuid.UUID{wsID, userID}] = member
	f.members[userID] = append(f.members[userID], gen.ListMembersByUserRow{
		ID: member.ID, WorkspaceID: wsID, UserID: userID, Role: gen.MemberRoleOwner,
		WorkspaceName: arg.WorkspaceName,
	})
	f.identities[key] = gen.UserIdentity{
		ID: uuid.New(), UserID: userID, Provider: arg.Identity.Provider, ProviderSubject: arg.Identity.Subject,
	}

	sp := arg.SessionParams
	f.sessions[sessionID] = gen.Session{
		ID: sessionID, UserID: userID, WorkspaceID: wsID,
		TokenHash: sp.TokenHash, FamilyID: sp.FamilyID, ExpiresAt: sp.ExpiresAt,
		UserAgent: sp.UserAgent, Ip: sp.Ip,
	}
	f.sessionsByHash[hashKey(sp.TokenHash)] = sessionID

	return RegisterTxResult{WorkspaceID: wsID, UserID: userID, SessionID: sessionID}, nil
}

// CompleteOnboarding mirrors the real single statement: idempotent, and it never
// renames an already-completed workspace.
func (f *fakeStore) CompleteOnboarding(_ context.Context, wsID uuid.UUID, name string) (gen.Workspace, error) {
	ws, ok := f.workspaces[wsID]
	if !ok {
		return gen.Workspace{}, pgx.ErrNoRows
	}
	if !ws.OnboardingCompletedAt.Valid {
		ws.Name = name
		ws.OnboardingCompletedAt = pgxTimestamp(time.Now())
		f.workspaces[wsID] = ws
		// ListMembersByUser joins the workspace row, so every membership pointing at
		// this workspace now reports the stamp. f.members is keyed by USER id, hence
		// the scan across users.
		for userID, rows := range f.members {
			for i, m := range rows {
				if m.WorkspaceID == wsID {
					f.members[userID][i].OnboardingCompletedAt = ws.OnboardingCompletedAt
				}
			}
		}
	}
	return ws, nil
}

// fakeGoogle is a stand-in for Google. There is NO way to exercise the real
// provider from a test — no network call to accounts.google.com belongs in one —
// so this seam is where the coverage honestly stops: everything from the
// authorization code onward is tested, and the code exchange itself is not.
type fakeGoogle struct {
	enabled  bool
	identity GoogleIdentity
	err      error

	// gotVerifier records the PKCE verifier Exchange was called with, so a test can
	// assert the callback replayed the one that was persisted at start.
	gotVerifier string
	// lastAuthURLVerifier records what AuthCodeURL was given.
	lastAuthURLVerifier string
}

func (g *fakeGoogle) Enabled() bool { return g.enabled }

func (g *fakeGoogle) AuthCodeURL(state, codeVerifier string) string {
	g.lastAuthURLVerifier = codeVerifier
	return "https://accounts.google.test/consent?state=" + state
}

func (g *fakeGoogle) Exchange(_ context.Context, _, codeVerifier string) (GoogleIdentity, error) {
	g.gotVerifier = codeVerifier
	if g.err != nil {
		return GoogleIdentity{}, g.err
	}
	return g.identity, nil
}
