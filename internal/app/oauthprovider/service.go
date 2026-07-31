package oauthprovider

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inroad/inroad/internal/app/auth"
)

var (
	// ErrValidation is a generic bad-registration rejection (missing name,
	// unsupported grant/response type or auth method).
	ErrValidation = errors.New("invalid registration")
	// ErrScopeNotGrantable is returned when a registration requests a scope outside
	// the OAuth-grantable allowlist (auth.OAuthGrantableScopes).
	ErrScopeNotGrantable = errors.New("scope not grantable")
	// ErrNotFound is returned when revoking a client absent from the caller's
	// workspace (unknown id or cross-tenant).
	ErrNotFound = errors.New("client not found")
	// ErrConsentNotFound is the SINGLE rejection every consent-lookup/decision
	// failure collapses to — unknown, expired, already consumed, or belonging to a
	// different user — so the endpoint is never an oracle distinguishing them.
	ErrConsentNotFound = errors.New("consent request not found or expired")
	// ErrRequestNotClaimable is returned by the store when an approval cannot consume
	// the pending request (a lost single-use race / expiry). The service maps it to
	// ErrConsentNotFound.
	ErrRequestNotClaimable = errors.New("authorization request not claimable")
)

const (
	// authRequestTTL bounds how long a pending consent request lives before the user
	// must restart the flow.
	authRequestTTL = 5 * time.Minute
	// authCodeTTL is the SHORT lifetime of an issued authorization code — it is
	// exchanged for a token almost immediately (OAuth 2.1 recommends <= 10 min; we
	// use ~60s).
	authCodeTTL = 60 * time.Second
	// clientIDRetries bounds regeneration of a colliding public client_id.
	clientIDRetries = 5
	// consentPath is the SPA route the authorize endpoint hands off to for consent.
	consentPath = "/oauth/consent"
	// loginPath is the SPA route an unauthenticated authorize request is sent to; it
	// resumes via the return_to query param.
	loginPath = "/login"
)

// Service implements the OAuth 2.1 authorization-server business rules over a Store
// and the ResourceOwner seam. It holds no secrets itself: a client secret / auth
// code is generated per-operation, its digest persisted, and the raw value returned
// exactly once (secret) or via the redirect (code).
type Service struct {
	store     Store
	publicURL string
	now       func() time.Time
}

// NewService builds a Service. publicURL is the externally-reachable base the consent
// and login redirects are built from.
func NewService(store Store, publicURL string) *Service {
	return &Service{store: store, publicURL: publicURL, now: time.Now}
}

// ---------------------------------------------------------------------------
// Dynamic client registration (RFC 7591)
// ---------------------------------------------------------------------------

// RegisterInput is a validated dynamic-registration request. Registration is
// admin-authed, so CreatedBy/WorkspaceID carry the registering admin's user +
// workspace (always set via the API), recorded so a workspace admin can later
// list/revoke the client. They are pointers only so the persistence layer can map an
// (unexpected) absent value to SQL NULL rather than a zero UUID.
type RegisterInput struct {
	ClientName              string
	RedirectURIs            []string
	GrantTypes              []string
	ResponseTypes           []string
	Scope                   string // space-delimited (RFC 7591 `scope`)
	TokenEndpointAuthMethod string // "", "none" -> public; client_secret_* -> confidential
	CreatedBy               *uuid.UUID
	WorkspaceID             *uuid.UUID
}

// RegisterResult carries the non-secret client metadata plus, for a confidential
// client only, the raw client secret shown EXACTLY ONCE (empty for a public client).
type RegisterResult struct {
	Client       ClientView
	ClientSecret string
}

// RegisterClient validates a registration and mints a client. Public (PKCE) by
// default: with no or a "none" auth method the client gets no secret. A confidential
// client (client_secret_* auth method) gets a freshly-generated secret returned once;
// only its hash is stored. Requested scopes must all be OAuth-grantable.
func (s *Service) RegisterClient(ctx context.Context, in RegisterInput) (RegisterResult, error) {
	if in.ClientName == "" {
		return RegisterResult{}, fmt.Errorf("%w: client_name is required", ErrValidation)
	}
	redirectURIs, err := validateRedirectURIs(in.RedirectURIs)
	if err != nil {
		return RegisterResult{}, err
	}
	scopes, err := validateGrantableScopes(parseScope(in.Scope))
	if err != nil {
		return RegisterResult{}, err
	}
	grantTypes, err := normalizeGrantTypes(in.GrantTypes)
	if err != nil {
		return RegisterResult{}, err
	}
	responseTypes, err := normalizeResponseTypes(in.ResponseTypes)
	if err != nil {
		return RegisterResult{}, err
	}
	confidential, authMethod, err := resolveAuthMethod(in.TokenEndpointAuthMethod)
	if err != nil {
		return RegisterResult{}, err
	}

	var rawSecret string
	var secretHash []byte
	clientType := clientTypePublic
	if confidential {
		clientType = clientTypeConfidential
		rawSecret, secretHash, err = newClientSecret()
		if err != nil {
			return RegisterResult{}, err
		}
	}

	for attempt := 0; attempt < clientIDRetries; attempt++ {
		clientID, err := newClientID()
		if err != nil {
			return RegisterResult{}, err
		}
		client, err := s.store.CreateClient(ctx, CreateClientParams{
			ClientID:                clientID,
			ClientSecretHash:        secretHash,
			ClientName:              in.ClientName,
			RedirectURIs:            redirectURIs,
			GrantTypes:              grantTypes,
			ResponseTypes:           responseTypes,
			Scopes:                  scopes,
			ClientType:              clientType,
			TokenEndpointAuthMethod: authMethod,
			CreatedBy:               in.CreatedBy,
			WorkspaceID:             in.WorkspaceID,
		})
		if err != nil {
			if isUniqueViolation(err) {
				continue // regenerate a fresh client_id and retry
			}
			return RegisterResult{}, err
		}
		return RegisterResult{Client: viewFromClient(client), ClientSecret: rawSecret}, nil
	}
	return RegisterResult{}, fmt.Errorf("oauthprovider: could not allocate a unique client_id after %d attempts", clientIDRetries)
}

// ListClients returns a workspace's registered clients as non-secret views.
func (s *Service) ListClients(ctx context.Context, ws uuid.UUID) ([]ClientView, error) {
	rows, err := s.store.ListClientsByWorkspace(ctx, ws)
	if err != nil {
		return nil, err
	}
	out := make([]ClientView, 0, len(rows))
	for _, r := range rows {
		out = append(out, viewFromClientRow(r))
	}
	return out, nil
}

// RevokeClient revokes (ws, clientID), tenant-pinned and idempotent. A client absent
// from ws yields ErrNotFound so a caller cannot probe or revoke another workspace's
// client.
func (s *Service) RevokeClient(ctx context.Context, ws uuid.UUID, clientID string) error {
	n, err := s.store.RevokeClient(ctx, ws, clientID)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// Authorization endpoint (OAuth 2.1)
// ---------------------------------------------------------------------------

// AuthorizeOutcome discriminates what the handler must do with an AuthorizeResult.
type AuthorizeOutcome int

const (
	// OutcomeRedirect: 302 the browser to RedirectTo. This covers ALL three
	// redirect targets — the validated client redirect_uri (with code or error), the
	// SPA consent screen, and the SPA login page — so the handler treats them
	// uniformly. A redirect is only ever built for an ALREADY-VALIDATED destination.
	OutcomeRedirect AuthorizeOutcome = iota
	// OutcomeError: render RenderMessage as a plain 400 page and DO NOT redirect.
	// Used only before the redirect_uri is validated (unknown client, redirect_uri
	// mismatch), so an unvalidated URI is never used as a redirect target.
	OutcomeError
)

// AuthorizeResult is the handler-agnostic decision produced by Authorize.
type AuthorizeResult struct {
	Outcome       AuthorizeOutcome
	RedirectTo    string
	RenderMessage string
}

// AuthorizeInput is the parsed /authorize request. Owner is the resource owner the
// handler resolved via the ResourceOwner seam (nil = unauthenticated); it is NEVER
// taken from a request parameter. RawQuery is the verbatim query string, used to
// build the login return_to so the flow resumes after login.
type AuthorizeInput struct {
	ResponseType        string
	ClientID            string
	RedirectURI         string
	Scope               string
	State               string
	CodeChallenge       string
	CodeChallengeMethod string
	RawQuery            string
	Owner               *Owner
}

// Authorize runs the OAuth 2.1 authorization-endpoint validation in the
// security-mandated order and returns the resulting action. The ordering is the
// point: the client and the redirect_uri are validated FIRST and their failures
// render an error WITHOUT redirecting (an unvalidated redirect_uri is never used as a
// redirect target); only AFTER the redirect_uri is proven to match the registered
// allowlist by EXACT string equality may any later error be delivered by redirecting
// to it (with the client's `state` echoed).
func (s *Service) Authorize(ctx context.Context, in AuthorizeInput) (AuthorizeResult, error) {
	// (1) Resolve the client. An unknown/unusable client_id cannot yield a trusted
	// redirect target, so we render — never redirect.
	if in.ClientID == "" {
		return renderErr("missing client_id"), nil
	}
	client, err := s.store.GetClient(ctx, in.ClientID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return renderErr("unknown client"), nil
		}
		return AuthorizeResult{}, err
	}
	if client.RevokedAt.Valid {
		return renderErr("client is revoked"), nil
	}

	// (2) Validate redirect_uri by EXACT string match against the registered
	// allowlist. Until this passes, an error MUST NOT be redirected anywhere.
	if in.RedirectURI == "" || !containsExact(client.RedirectUris, in.RedirectURI) {
		return renderErr("invalid redirect_uri"), nil
	}
	// From here, in.RedirectURI is a trusted target: errors are delivered by
	// redirecting to it with error + state (OAuth 2.1 §4.1.2.1).

	// (3) response_type: OAuth 2.1 supports only the authorization-code grant.
	if in.ResponseType != responseTypeCode {
		return s.redirectErr(in.RedirectURI, "unsupported_response_type", in.State)
	}

	// (3b) The client must have REGISTERED both the `code` response type and the
	// `authorization_code` grant. A client that registered only e.g. refresh_token
	// (or no response types) must not be able to obtain an authorization code, even
	// though the request asked for response_type=code. Delivered on the validated
	// redirect_uri as the spec `unauthorized_client` error.
	if !containsExact(client.ResponseTypes, responseTypeCode) ||
		!containsExact(client.GrantTypes, grantAuthorizationCode) {
		return s.redirectErr(in.RedirectURI, "unauthorized_client", in.State)
	}

	// (4) PKCE is MANDATORY and S256-only (reject missing and `plain`). Per OAuth 2.1
	// these are request errors on a validated redirect_uri, so — like every other
	// post-redirect-validation error — they are delivered via a redirect carrying
	// error=invalid_request&state=..., NOT rendered inline.
	if in.CodeChallenge == "" || in.CodeChallengeMethod == "" ||
		in.CodeChallengeMethod != challengeMethodS256 || !isValidCodeChallenge(in.CodeChallenge) {
		return s.redirectErr(in.RedirectURI, "invalid_request", in.State)
	}

	// (5) scope must be non-empty, a subset of the client's registered scopes, and
	// (belt-and-braces) a subset of the grantable allowlist.
	scopes := parseScope(in.Scope)
	if len(scopes) == 0 || !isSubset(scopes, client.Scopes) || !allGrantable(scopes) {
		return s.redirectErr(in.RedirectURI, "invalid_scope", in.State)
	}

	// (6) Resource-owner authentication. Resolved ONLY from the session (by the
	// handler, via the seam). No session -> send the user to log in and resume.
	if in.Owner == nil {
		return AuthorizeResult{Outcome: OutcomeRedirect, RedirectTo: s.loginRedirect(in.RawQuery)}, nil
	}

	// (7) Prior-consent skip: issue a code immediately without re-prompting ONLY when a
	// remembered consent exists FOR THE CURRENT WORKSPACE (in.Owner.WorkspaceID) and it
	// covers ALL requested scopes. Pinning the lookup to the session's workspace is the
	// anti-cross-tenant guard: a consent the user granted while active in another
	// workspace must never let this authorize mint a code bound to THIS workspace — the
	// user never approved this client for it. A miss (including a consent that exists
	// only in a different workspace) falls through to the consent screen below.
	consent, err := s.store.GetConsent(ctx, in.Owner.UserID, in.ClientID, in.Owner.WorkspaceID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return AuthorizeResult{}, err
	}
	if err == nil && isSubset(scopes, consent.Scopes) {
		redirectTo, err := s.issueCode(ctx, client.ClientID, in, scopes, *in.Owner)
		if err != nil {
			return AuthorizeResult{}, err
		}
		return AuthorizeResult{Outcome: OutcomeRedirect, RedirectTo: redirectTo}, nil
	}

	// (8) Otherwise persist the fully-validated request and hand off to the SPA
	// consent screen.
	consentID, err := newConsentID()
	if err != nil {
		return AuthorizeResult{}, err
	}
	if err := s.store.CreateAuthRequest(ctx, CreateAuthRequestParams{
		ConsentID:           consentID,
		ClientID:            in.ClientID,
		RedirectURI:         in.RedirectURI,
		Scopes:              scopes,
		State:               emptyToNil(in.State),
		CodeChallenge:       in.CodeChallenge,
		CodeChallengeMethod: in.CodeChallengeMethod,
		UserID:              in.Owner.UserID,
		WorkspaceID:         in.Owner.WorkspaceID,
		ExpiresAt:           s.now().Add(authRequestTTL),
	}); err != nil {
		return AuthorizeResult{}, err
	}
	return AuthorizeResult{Outcome: OutcomeRedirect, RedirectTo: s.consentRedirect(consentID)}, nil
}

// issueCode mints a single-use authorization code bound to the request parameters,
// persists its hash, and returns the client redirect carrying code + state.
func (s *Service) issueCode(ctx context.Context, clientID string, in AuthorizeInput, scopes []string, owner Owner) (string, error) {
	rawCode, codeHash, err := newAuthCode()
	if err != nil {
		return "", err
	}
	if err := s.store.CreateAuthCode(ctx, CreateAuthCodeParams{
		CodeHash:            codeHash,
		ClientID:            clientID,
		RedirectURI:         in.RedirectURI,
		CodeChallenge:       in.CodeChallenge,
		CodeChallengeMethod: in.CodeChallengeMethod,
		Scopes:              scopes,
		UserID:              owner.UserID,
		WorkspaceID:         owner.WorkspaceID,
		ExpiresAt:           s.now().Add(authCodeTTL),
	}); err != nil {
		return "", err
	}
	return buildRedirect(in.RedirectURI, map[string]string{"code": rawCode, "state": in.State})
}

// ---------------------------------------------------------------------------
// Consent
// ---------------------------------------------------------------------------

// ConsentView is the data the SPA renders on the consent screen.
type ConsentView struct {
	ClientName      string
	RequestedScopes []string
	RedirectURI     string
}

// ConsentRequest returns the pending consent data for consentID, provided it is live
// (unconsumed, unexpired) and belongs to userID. Every miss collapses to
// ErrConsentNotFound so the endpoint leaks nothing about another user's requests.
func (s *Service) ConsentRequest(ctx context.Context, consentID string, userID uuid.UUID) (ConsentView, error) {
	req, err := s.liveRequestFor(ctx, consentID, userID)
	if err != nil {
		return ConsentView{}, err
	}
	client, err := s.store.GetClient(ctx, req.ClientID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ConsentView{}, ErrConsentNotFound
		}
		return ConsentView{}, err
	}
	return ConsentView{
		ClientName:      client.ClientName,
		RequestedScopes: req.Scopes,
		RedirectURI:     req.RedirectUri,
	}, nil
}

// DecideInput is a consent decision by the authenticated resource owner.
type DecideInput struct {
	ConsentID string
	UserID    uuid.UUID
	Approve   bool
}

// DecideResult carries the URL the SPA should navigate the browser to (the client's
// redirect_uri with code+state on approve, or error=access_denied+state on deny).
type DecideResult struct {
	RedirectTo string
}

// DecideConsent records the resource owner's decision. It re-checks that the pending
// request belongs to this user (defense in depth atop the endpoint's session guard)
// and is still live. On approve it consumes the request, upserts the remembered
// consent, and issues a single-use code — all atomically — then returns the client
// redirect with code + state. On deny it consumes the request and returns the client
// redirect with error=access_denied + state.
func (s *Service) DecideConsent(ctx context.Context, in DecideInput) (DecideResult, error) {
	req, err := s.liveRequestFor(ctx, in.ConsentID, in.UserID)
	if err != nil {
		return DecideResult{}, err
	}
	state := stateValue(req.State)

	if !in.Approve {
		n, err := s.store.ConsumeAuthRequest(ctx, in.ConsentID, in.UserID)
		if err != nil {
			return DecideResult{}, err
		}
		if n == 0 {
			return DecideResult{}, ErrConsentNotFound // lost the single-use race
		}
		redirectTo, err := buildRedirect(req.RedirectUri, map[string]string{"error": "access_denied", "state": state})
		if err != nil {
			return DecideResult{}, err
		}
		return DecideResult{RedirectTo: redirectTo}, nil
	}

	rawCode, codeHash, err := newAuthCode()
	if err != nil {
		return DecideResult{}, err
	}
	err = s.store.Approve(ctx, ApproveParams{
		ConsentID:   in.ConsentID,
		UserID:      in.UserID,
		ClientID:    req.ClientID,
		Scopes:      req.Scopes,
		WorkspaceID: req.WorkspaceID,
		Code: CreateAuthCodeParams{
			CodeHash:            codeHash,
			ClientID:            req.ClientID,
			RedirectURI:         req.RedirectUri,
			CodeChallenge:       req.CodeChallenge,
			CodeChallengeMethod: req.CodeChallengeMethod,
			Scopes:              req.Scopes,
			UserID:              req.UserID,
			WorkspaceID:         req.WorkspaceID,
			ExpiresAt:           s.now().Add(authCodeTTL),
		},
	})
	if err != nil {
		if errors.Is(err, ErrRequestNotClaimable) {
			return DecideResult{}, ErrConsentNotFound
		}
		return DecideResult{}, err
	}
	redirectTo, err := buildRedirect(req.RedirectUri, map[string]string{"code": rawCode, "state": state})
	if err != nil {
		return DecideResult{}, err
	}
	return DecideResult{RedirectTo: redirectTo}, nil
}

// liveRequestFor loads a pending request and enforces that it belongs to userID and
// is still live (unconsumed, unexpired). Every failure is ErrConsentNotFound.
func (s *Service) liveRequestFor(ctx context.Context, consentID string, userID uuid.UUID) (reqRow, error) {
	req, err := s.store.GetAuthRequest(ctx, consentID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return reqRow{}, ErrConsentNotFound
		}
		return reqRow{}, err
	}
	if req.UserID != userID || req.ConsumedAt.Valid || !s.now().Before(timeOrZero(req.ExpiresAt)) {
		return reqRow{}, ErrConsentNotFound
	}
	return reqRow{
		ClientID:            req.ClientID,
		RedirectUri:         req.RedirectUri,
		Scopes:              req.Scopes,
		State:               req.State,
		CodeChallenge:       req.CodeChallenge,
		CodeChallengeMethod: req.CodeChallengeMethod,
		UserID:              req.UserID,
		WorkspaceID:         req.WorkspaceID,
	}, nil
}

// reqRow is the subset of a pending request the consent flows use, in clean types.
type reqRow struct {
	ClientID            string
	RedirectUri         string
	Scopes              []string
	State               *string
	CodeChallenge       string
	CodeChallengeMethod string
	UserID              uuid.UUID
	WorkspaceID         uuid.UUID
}

// ---------------------------------------------------------------------------
// Redirect builders + helpers
// ---------------------------------------------------------------------------

// consentRedirect builds the SPA consent-screen URL for consentID.
func (s *Service) consentRedirect(consentID string) string {
	q := url.Values{"consent_id": {consentID}}
	return s.publicURL + consentPath + "?" + q.Encode()
}

// loginRedirect builds the SPA login URL with a return_to back to this authorize
// request, so the user resumes the flow after logging in.
func (s *Service) loginRedirect(rawQuery string) string {
	returnTo := s.publicURL + "/oauth2/authorize"
	if rawQuery != "" {
		returnTo += "?" + rawQuery
	}
	q := url.Values{"return_to": {returnTo}}
	return s.publicURL + loginPath + "?" + q.Encode()
}

// redirectErr builds an OAuth error redirect to the (already-validated) redirect_uri,
// echoing state.
func (s *Service) redirectErr(redirectURI, code, state string) (AuthorizeResult, error) {
	redirectTo, err := buildRedirect(redirectURI, map[string]string{"error": code, "state": state})
	if err != nil {
		return AuthorizeResult{}, err
	}
	return AuthorizeResult{Outcome: OutcomeRedirect, RedirectTo: redirectTo}, nil
}

// renderErr is a no-redirect authorization error (used only before the redirect_uri
// is validated).
func renderErr(msg string) AuthorizeResult {
	return AuthorizeResult{Outcome: OutcomeError, RenderMessage: msg}
}

// containsExact reports whether v equals any element of list (exact string match, no
// normalization) — the anti-open-redirect redirect_uri check.
func containsExact(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

// allGrantable reports whether every scope is OAuth-grantable.
func allGrantable(scopes []string) bool {
	for _, sc := range scopes {
		if !auth.IsOAuthGrantableScope(sc) {
			return false
		}
	}
	return true
}

// validateRedirectURIs requires at least one URI and validates each against the
// registration allowlist policy, returning them de-duplicated.
func validateRedirectURIs(uris []string) ([]string, error) {
	if len(uris) == 0 {
		return nil, fmt.Errorf("%w: at least one redirect_uri is required", ErrInvalidRedirectURI)
	}
	seen := make(map[string]struct{}, len(uris))
	out := make([]string, 0, len(uris))
	for _, u := range uris {
		if err := validateRedirectURI(u); err != nil {
			return nil, err
		}
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out, nil
}

// validateGrantableScopes requires every requested scope be OAuth-grantable and
// returns them (already de-duplicated by parseScope). An empty set is allowed (a
// client may register with no scopes).
func validateGrantableScopes(scopes []string) ([]string, error) {
	for _, sc := range scopes {
		if !auth.IsOAuthGrantableScope(sc) {
			return nil, fmt.Errorf("%w: %q", ErrScopeNotGrantable, sc)
		}
	}
	return scopes, nil
}

// normalizeGrantTypes defaults to {authorization_code} and rejects any unsupported
// grant type (notably the removed implicit grant).
func normalizeGrantTypes(in []string) ([]string, error) {
	if len(in) == 0 {
		return []string{grantAuthorizationCode}, nil
	}
	for _, g := range in {
		if g != grantAuthorizationCode && g != grantRefreshToken {
			return nil, fmt.Errorf("%w: unsupported grant_type %q", ErrValidation, g)
		}
	}
	return in, nil
}

// normalizeResponseTypes defaults to {code} and rejects anything else (implicit
// token responses are not supported).
func normalizeResponseTypes(in []string) ([]string, error) {
	if len(in) == 0 {
		return []string{responseTypeCode}, nil
	}
	for _, rt := range in {
		if rt != responseTypeCode {
			return nil, fmt.Errorf("%w: unsupported response_type %q", ErrValidation, rt)
		}
	}
	return in, nil
}

// resolveAuthMethod maps the requested token_endpoint_auth_method to (confidential,
// canonical method). Empty or "none" -> public (no secret); a client_secret_* method
// -> confidential (gets a secret). Anything else is rejected.
func resolveAuthMethod(m string) (confidential bool, method string, err error) {
	switch m {
	case "", authMethodNone:
		return false, authMethodNone, nil
	case authMethodClientSecret, "client_secret_post":
		return true, m, nil
	default:
		return false, "", fmt.Errorf("%w: unsupported token_endpoint_auth_method %q", ErrValidation, m)
	}
}

// isUniqueViolation reports whether err is a Postgres unique-constraint violation
// (SQLSTATE 23505) — the client_id collision RegisterClient retries on.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// emptyToNil maps an empty state string to a SQL NULL (nothing echoed) and a
// non-empty one to a stored value.
func emptyToNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// stateValue derefs a stored nullable state to "" when absent.
func stateValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
