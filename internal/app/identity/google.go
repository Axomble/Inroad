package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/oauthstate"
)

// ProviderGoogle is the `user_identities.provider` value for Google accounts.
const ProviderGoogle = "google"

// loginStateTTL bounds how long a started sign-in may take to come back. Same
// 10 minutes the mailbox-connect flow uses; unlike that flow, the state here is
// ALSO single-use against a server-side store (see Store.ConsumeLoginState).
const loginStateTTL = 10 * time.Minute

var (
	// ErrGoogleDisabled means the self-hoster configured no Google client
	// id/secret, so the sign-in surface is off (501 rather than a broken redirect).
	ErrGoogleDisabled = errors.New("google sign-in not configured")
	// ErrProviderEmailUnverified means Google reported email_verified=false. It
	// refuses BOTH signup and linking.
	//
	// The tempting objection is that refusing to link protects nothing, because
	// whoever controls that mailbox could take the account over via password
	// reset anyway. That is exactly backwards: the claim tells us they may NOT
	// control it. An unverified address on a Google account is one nobody has
	// proven ownership of, so honoring it would let an attacker register a Google
	// account claiming victim@corp.example and be handed the existing Inroad
	// account for it. Checking the claim is what makes the password-reset
	// comparison hold at all.
	ErrProviderEmailUnverified = errors.New("provider reports this email as unverified")
	// ErrProviderNoEmail means Google returned no address (or no subject) at all --
	// distinct from returning one it says is unverified, because the two need
	// different copy: "we could not read your email" vs "Google has not confirmed
	// that address".
	ErrProviderNoEmail = errors.New("provider returned no email address")
	// ErrInviteNotForIdentity means the invite the caller started the flow from is
	// addressed to someone other than the Google account they authenticated as.
	ErrInviteNotForIdentity = errors.New("invite does not match the authenticated account")
)

// GoogleIdentity is the subset of Google's ID-token claims sign-in needs.
type GoogleIdentity struct {
	// Subject is Google's immutable `sub`. It, and never Email, is what an
	// identity is keyed on: a user who changes their Google address keeps the
	// same subject, whereas matching on email would silently mint a second
	// account for the same person after that change.
	Subject string
	Email   string
	// EmailVerified is Google's `email_verified` claim. Load-bearing: false
	// refuses both signup and linking (see ErrProviderEmailUnverified).
	EmailVerified bool
	// GivenName is the `given_name` claim, used only to derive a first workspace
	// name for a personal (non-Workspace) account.
	GivenName string
	// HostedDomain is the `hd` claim, present only for Google Workspace accounts.
	// It yields a much better default workspace name than a person's first name.
	HostedDomain string
}

// GoogleAuthenticator is the provider seam the sign-in flow depends on: build a
// consent URL, and turn an authorization code into a verified identity. Kept as a
// consumer-defined interface so the service is unit-testable without Google —
// there is no way to exercise the real provider in tests, and a fake here is the
// honest boundary rather than a pretend one deeper in.
type GoogleAuthenticator interface {
	// Enabled reports whether Google credentials are configured.
	Enabled() bool
	// AuthCodeURL builds the consent URL, binding state and the PKCE challenge
	// derived from codeVerifier.
	AuthCodeURL(state, codeVerifier string) string
	// Exchange redeems code (replaying codeVerifier for PKCE) and returns the
	// authenticated identity.
	Exchange(ctx context.Context, code, codeVerifier string) (GoogleIdentity, error)
}

// googleStoreIface lists the Store methods the Google sign-in flow adds on top of
// storeIface. Split out so the seam each piece of the service depends on stays
// readable, not because it is injected separately.
type googleStoreIface interface {
	GetUserIdentity(ctx context.Context, provider, subject string) (gen.UserIdentity, error)
	LinkUserIdentity(ctx context.Context, userID uuid.UUID, provider, subject string) error
	GetLatestPendingInviteByEmail(ctx context.Context, email string) (gen.WorkspaceInvite, error)
	CreateLoginState(ctx context.Context, arg CreateLoginStateParams) error
	ConsumeLoginState(ctx context.Context, nonceHash []byte, purpose string) (LoginState, error)
	FederatedSignupTx(ctx context.Context, arg FederatedSignupTxParams) (RegisterTxResult, error)
	CompleteOnboarding(ctx context.Context, wsID uuid.UUID, name string) (gen.Workspace, error)
}

// StartGoogleSignIn mints the PKCE verifier and the single-use signed state for a
// Google sign-in, persists the server-side half, and returns the consent URL.
//
// inviteToken (optional) is the raw token from an invite link the user chose to
// accept with Google. Only its HASH is persisted, and it is never put in the
// state parameter — an invite token is a bearer credential granting workspace
// membership, and the state travels through Google's servers, the browser's
// history, and any Referer along the way.
func (s *Service) StartGoogleSignIn(ctx context.Context, in StartGoogleSignInInput) (string, error) {
	if s.google == nil || !s.google.Enabled() {
		return "", ErrGoogleDisabled
	}
	verifier := oauth2.GenerateVerifier()
	state, nonce := oauthstate.Sign(s.stateSecret, oauthstate.PurposeLogin, "", time.Now(), loginStateTTL)

	var inviteHash []byte
	if in.InviteToken != "" {
		inviteHash = auth.HashToken(in.InviteToken)
	}
	if err := s.store.CreateLoginState(ctx, CreateLoginStateParams{
		NonceHash:       auth.HashToken(nonce),
		Purpose:         string(oauthstate.PurposeLogin),
		CodeVerifier:    verifier,
		InviteTokenHash: inviteHash,
		// Rejected outright rather than sanitized: a caller-supplied redirect target
		// that doesn't pass is simply dropped, so the SPA falls back to its default
		// landing route.
		ReturnTo:  safeReturnTo(in.ReturnTo),
		ExpiresAt: time.Now().Add(loginStateTTL),
	}); err != nil {
		return "", fmt.Errorf("persist login state: %w", err)
	}
	return s.google.AuthCodeURL(state, verifier), nil
}

// StartGoogleSignInInput carries the two optional things a caller may bring to a
// sign-in: an invite they are accepting, and where in the app to land afterwards.
type StartGoogleSignInInput struct {
	// InviteToken is the raw token from an invite link. Only its hash is persisted,
	// and it never enters the OAuth `state` parameter.
	InviteToken string
	// ReturnTo is an in-app path to navigate to once a session exists. Validated by
	// safeReturnTo and stored server-side, never carried in a URL an attacker could
	// edit.
	ReturnTo string
}

// safeReturnTo returns path if it is a safe SAME-ORIGIN in-app path, and ""
// otherwise. This is an open-redirect guard, so it is an allowlist rather than a
// sanitizer: anything it is not certain about is dropped, and the SPA falls back
// to its default landing route.
//
// Rejected: anything not starting with a single "/" (absolute URLs, and
// scheme-relative "//evil.example" which a browser treats as another origin);
// backslashes, which some browsers normalize to "/" and which therefore smuggle
// "/\evil.example" past a naive prefix check; control characters and whitespace,
// which can split a Location header; and anything over a sane length.
func safeReturnTo(path string) string {
	const maxReturnToLen = 512
	if path == "" || len(path) > maxReturnToLen {
		return ""
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return ""
	}
	if strings.ContainsAny(path, "\\") {
		return ""
	}
	for _, r := range path {
		if r < 0x20 || r == 0x7f || unicode.IsSpace(r) {
			return ""
		}
	}
	return path
}

// CompleteGoogleSignIn is the callback half. It verifies and CONSUMES the state
// (single-use), exchanges the code with PKCE, and resolves the identity to a
// session in a fixed order:
//
//  1. an existing user_identities row for (google, sub) → sign in to their
//     last-seen workspace;
//  2. no identity but the verified email matches an existing user → link the
//     identity to that user, then sign in;
//  3. no identity, no user, but a pending invite for that email → create the user
//     and join the inviting workspace in one transaction, with no password;
//  4. otherwise → create workspace + user + owner membership + session in one
//     transaction, with the email already marked verified.
//
// The workspace a session lands in is derived entirely server-side at every step.
// Nothing about it comes from the callback URL.
func (s *Service) CompleteGoogleSignIn(ctx context.Context, code, state, ua, ip string) (Session, string, error) {
	if s.google == nil || !s.google.Enabled() {
		return Session{}, "", ErrGoogleDisabled
	}
	// The signature+TTL check is stateless; the consume below is what makes the
	// state single-use. Both must pass, and in this order — an unsigned nonce
	// should never reach the database as a lookup key.
	_, nonce, err := oauthstate.Verify(s.stateSecret, oauthstate.PurposeLogin, state, time.Now())
	if err != nil {
		return Session{}, "", ErrStateInvalid
	}
	loginState, err := s.store.ConsumeLoginState(ctx, auth.HashToken(nonce), string(oauthstate.PurposeLogin))
	if err != nil {
		return Session{}, "", err // ErrStateInvalid on replay/expiry/unknown
	}
	// returnTo was validated as a same-origin path before it was stored, and it
	// travels back to the caller on EVERY outcome (including failures) so the SPA can
	// still honor it after the user retries.
	returnTo := loginState.ReturnTo

	id, err := s.google.Exchange(ctx, code, loginState.CodeVerifier)
	if err != nil {
		return Session{}, returnTo, fmt.Errorf("google exchange: %w", err)
	}
	if id.Subject == "" || id.Email == "" {
		return Session{}, returnTo, ErrProviderNoEmail
	}

	// Path 1: this provider account has signed in here before.
	existing, err := s.store.GetUserIdentity(ctx, ProviderGoogle, id.Subject)
	switch {
	case err == nil:
		sess, err := s.StartSessionForUser(ctx, existing.UserID, ua, ip)
		return sess, returnTo, err
	case !errors.Is(err, pgx.ErrNoRows):
		return Session{}, returnTo, err
	}

	// Everything past here either creates an account or attaches this provider
	// identity to one, so the address must actually be proven.
	if !id.EmailVerified {
		return Session{}, returnTo, ErrProviderEmailUnverified
	}

	// Path 2: an account already exists for this verified address — link, sign in.
	user, err := s.store.GetUserByEmail(ctx, id.Email)
	switch {
	case err == nil:
		if err := s.store.LinkUserIdentity(ctx, user.ID, ProviderGoogle, id.Subject); err != nil {
			return Session{}, returnTo, fmt.Errorf("link google identity: %w", err)
		}
		sess, err := s.StartSessionForUser(ctx, user.ID, ua, ip)
		return sess, returnTo, err
	case !errors.Is(err, pgx.ErrNoRows):
		return Session{}, returnTo, err
	}

	// Path 3: brand-new person, but somebody invited them.
	if sess, handled, err := s.googleInviteSignup(ctx, id, loginState.InviteTokenHash, ua, ip); handled {
		return sess, returnTo, err
	}

	// Path 4: brand-new person, brand-new workspace.
	sess, err := s.googleSignup(ctx, id, ua, ip)
	return sess, returnTo, err
}

// googleInviteSignup attempts path 3. handled is false when there is no invite to
// act on at all, which is the caller's cue to fall through to a fresh signup.
//
// An invite the user EXPLICITLY started the flow from (its hash was stashed in the
// login state) is authoritative: if it turns out to be revoked or expired, that is
// reported rather than quietly becoming a brand-new workspace the user did not ask
// for. An invite merely DISCOVERED by address is opportunistic, so the same
// failure falls through to a normal signup.
func (s *Service) googleInviteSignup(ctx context.Context, id GoogleIdentity, stashedHash []byte, ua, ip string) (Session, bool, error) {
	inviteHash, explicit := stashedHash, len(stashedHash) > 0
	if !explicit {
		inv, err := s.store.GetLatestPendingInviteByEmail(ctx, id.Email)
		if errors.Is(err, pgx.ErrNoRows) {
			return Session{}, false, nil
		}
		if err != nil {
			return Session{}, true, err
		}
		inviteHash = inv.TokenHash
	}

	raw, tokHash, err := auth.NewRefreshToken()
	if err != nil {
		return Session{}, true, err
	}
	res, err := s.store.AcceptInviteTx(ctx, AcceptInviteTxParams{
		TokenHash: inviteHash,
		// No PasswordHash: the provider identity IS how this member will sign in.
		Identity:      &IdentityLink{Provider: ProviderGoogle, Subject: id.Subject},
		ExpectedEmail: id.Email,
		SessionParams: s.sessionParams(tokHash, ua, ip),
	})
	switch {
	case errors.Is(err, ErrIdentityEmailMismatch):
		return Session{}, true, ErrInviteNotForIdentity
	case errors.Is(err, ErrTokenInvalid) && !explicit:
		// The invite we found by address went away underneath us; treat this as a
		// normal signup rather than an error the user can do nothing about.
		return Session{}, false, nil
	case err != nil:
		return Session{}, true, err
	}
	mems, _ := s.memberships(ctx, res.UserID)
	return Session{
		UserID: res.UserID, WorkspaceID: res.WorkspaceID, Role: res.Role,
		SessionID: res.SessionID, RawRefresh: raw, Memberships: mems, Email: id.Email,
		OnboardingCompletedAt: onboardingFor(mems, res.WorkspaceID),
	}, true, nil
}

// googleSignup is path 4: a brand-new person with no invite gets their own
// workspace, named from the provider claims, and is its owner. The name only has
// to be sane — the onboarding modal replaces it immediately after.
func (s *Service) googleSignup(ctx context.Context, id GoogleIdentity, ua, ip string) (Session, error) {
	raw, tokHash, err := auth.NewRefreshToken()
	if err != nil {
		return Session{}, err
	}
	res, err := s.store.FederatedSignupTx(ctx, FederatedSignupTxParams{
		WorkspaceName: DeriveWorkspaceName(id.HostedDomain, id.GivenName, id.Email),
		Email:         id.Email,
		Identity:      IdentityLink{Provider: ProviderGoogle, Subject: id.Subject},
		SessionParams: s.sessionParams(tokHash, ua, ip),
	})
	if err != nil {
		return Session{}, err
	}
	mems, _ := s.memberships(ctx, res.UserID)
	return Session{
		UserID: res.UserID, WorkspaceID: res.WorkspaceID, Role: "owner",
		SessionID: res.SessionID, RawRefresh: raw, Memberships: mems, Email: id.Email,
		OnboardingCompletedAt: onboardingFor(mems, res.WorkspaceID),
	}, nil
}

// sessionParams builds the CreateSessionParams every "issue the first session
// inside a transaction" path shares. UserID/WorkspaceID are filled in by the tx.
func (s *Service) sessionParams(tokenHash []byte, ua, ip string) gen.CreateSessionParams {
	return gen.CreateSessionParams{
		TokenHash: tokenHash,
		FamilyID:  uuid.New(),
		ExpiresAt: pgxTimestamp(time.Now().Add(s.refreshTTL)),
		UserAgent: ptr(ua),
		Ip:        parseIP(ip),
	}
}

// maxWorkspaceNameLen matches the register handler's `max=200` on workspace_name,
// so a derived name can never be one the API would have rejected.
const maxWorkspaceNameLen = 200

// DeriveWorkspaceName picks the first workspace name for a federated signup:
// the Google Workspace domain titleized ("axomble.com" → "Axomble") when the `hd`
// claim is present, else "<FirstName>'s workspace", else the same treatment of the
// email's local part for an account with neither claim. Exported for its own test —
// it is pure, and the fallback chain is the part worth pinning down.
func DeriveWorkspaceName(hostedDomain, givenName, email string) string {
	if label := titleizeDomain(hostedDomain); label != "" {
		return truncateName(label)
	}
	if name := strings.TrimSpace(givenName); name != "" {
		return truncateName(name) + "'s workspace"
	}
	if local, _, ok := strings.Cut(email, "@"); ok && local != "" {
		return truncateName(titleizeWord(local)) + "'s workspace"
	}
	return "My workspace"
}

// titleizeDomain turns a hosted domain into a display label by taking the
// left-most label and capitalizing it: "axomble.com" → "Axomble",
// "mail.axomble.co.uk" → "Mail". Returns "" for an empty or malformed domain.
func titleizeDomain(domain string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(domain), ".")
	return titleizeWord(first)
}

// titleizeWord upper-cases the first rune of s and leaves the rest alone (so
// "mcdonald" → "Mcdonald", but an already-cased "axoMble" is not flattened).
func titleizeWord(s string) string {
	if s == "" {
		return ""
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// truncateName clamps a derived name to the same length limit the API validates
// workspace_name against, cutting on a rune boundary so it can't split a
// multi-byte character.
func truncateName(s string) string {
	if len(s) <= maxWorkspaceNameLen {
		return s
	}
	r := []rune(s)
	for len(string(r)) > maxWorkspaceNameLen {
		r = r[:len(r)-1]
	}
	return string(r)
}

// onboardingFor returns ws's onboarding stamp (zero while pending), reading it from
// memberships already loaded for this response so no extra query is needed.
//
// A workspace absent from the list reads as COMPLETED (a non-zero stamp). That
// cannot happen for a principal that is a member of it, and it is the fail-quiet
// direction: the SPA then does not trap a user behind an onboarding modal for a
// workspace it has no other information about.
func onboardingFor(mems []Membership, ws uuid.UUID) time.Time {
	for _, m := range mems {
		if m.WorkspaceID == ws {
			return m.OnboardingCompletedAt
		}
	}
	return time.Unix(0, 0)
}
