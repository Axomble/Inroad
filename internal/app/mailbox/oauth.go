package mailbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/mail"
)

// ErrOAuthDisabled is returned when an OAuth connect action is attempted but no
// client credentials are configured for that provider (self-hoster left the
// Google or Microsoft credentials blank). The message is provider-neutral
// because both the Gmail and M365 flows return it; the handler maps it to a
// fixed reason (a 501 on start, oauth_error=disabled on the callback), so this
// sentinel text is internal only.
var ErrOAuthDisabled = errors.New("oauth not configured")

// TokenExchanger exchanges an auth code for a token and the mailbox's own email
// address. Production impls call the provider (googleExchanger / microsoftExchanger);
// tests fake it.
type TokenExchanger interface {
	Exchange(ctx context.Context, code string) (tok *oauth2.Token, email string, err error)
}

// GoogleAuthCodeURL builds the consent URL for the signed state, or
// ErrOAuthDisabled if Gmail OAuth is unconfigured. State signing stays in the
// handler (which holds the secret); the oauth2 details stay behind this seam.
// access_type=offline + prompt=consent force a refresh token every time.
func (s *Service) GoogleAuthCodeURL(state string) (string, error) {
	if !s.oauth.Enabled() {
		return "", ErrOAuthDisabled
	}
	return s.oauth.Config().AuthCodeURL(state,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
	), nil
}

// CompleteGoogleOAuth exchanges the code, learns the connected address, seals
// the token, and persists a new gmail mailbox in workspaceID. workspaceID is
// supplied by the caller (the callback derives it from the verified signed
// state, never a request body), so this write is workspace-pinned. Dedupes on
// email like ConnectSMTP.
func (s *Service) CompleteGoogleOAuth(ctx context.Context, code string, workspaceID uuid.UUID) (MailboxSafe, error) {
	return s.completeOAuth(ctx, code, workspaceID, s.oauth.Enabled(), s.exchanger, "gmail")
}

// completeOAuth is the provider-agnostic body shared by CompleteGoogleOAuth and
// CompleteMicrosoftOAuth. It guards on the provider being configured (enabled),
// exchanges the code for a token + connected address, dedupes on email, seals
// the token under the per-workspace DEK, and persists a workspace-pinned mailbox
// row tagged with provider. workspaceID is supplied by the caller (the callback
// derives it from the verified signed state, never a request body).
func (s *Service) completeOAuth(
	ctx context.Context, code string, workspaceID uuid.UUID,
	enabled bool, exchanger TokenExchanger, provider string,
) (MailboxSafe, error) {
	if !enabled {
		return MailboxSafe{}, ErrOAuthDisabled
	}
	tok, email, err := exchanger.Exchange(ctx, code)
	if err != nil {
		return MailboxSafe{}, fmt.Errorf("oauth exchange: %w", err)
	}
	if email == "" {
		return MailboxSafe{}, fmt.Errorf("%w: no email in userinfo", ErrValidation)
	}

	count, err := s.store.CountByEmail(ctx, workspaceID, email)
	if err != nil {
		return MailboxSafe{}, err
	}
	if count > 0 {
		return MailboxSafe{}, ErrDuplicateMailbox
	}

	raw, err := mail.MarshalToken(tok)
	if err != nil {
		return MailboxSafe{}, err
	}
	sealer, err := s.keyring.SealerFor(ctx, workspaceID)
	if err != nil {
		return MailboxSafe{}, err
	}
	ciphertext, err := sealer.Seal(raw)
	if err != nil {
		return MailboxSafe{}, err
	}

	return s.store.Create(ctx, gen.CreateMailboxParams{
		WorkspaceID:      workspaceID,
		Provider:         provider,
		Email:            email,
		DisplayName:      email,
		SecretCiphertext: ciphertext,
		// SMTP/IMAP fields are unused for OAuth mailboxes; their zero values are fine.
		DailyCap:           defaultDailyCap,
		MinIntervalSeconds: defaultMinIntervalSeconds,
		RampEnabled:        true,
		RampStartCap:       defaultRampStartCap,
		RampDays:           defaultRampDays,
	})
}

// googleExchanger is the production TokenExchanger: it exchanges the code with
// Google and reads the connected address from the OpenID Connect userinfo
// endpoint. Both hosts are fixed Google endpoints (no SSRF surface).
type googleExchanger struct {
	cfg    *oauth2.Config
	client *http.Client
}

// NewGoogleExchanger builds the production exchanger from the app's Google
// OAuth config. The client carries a timeout so neither the code exchange nor
// the userinfo call can hang a request goroutine indefinitely.
func NewGoogleExchanger(oauth mail.GoogleOAuth) TokenExchanger {
	return &googleExchanger{cfg: oauth.Config(), client: &http.Client{Timeout: 10 * time.Second}}
}

func (g *googleExchanger) Exchange(ctx context.Context, code string) (*oauth2.Token, string, error) {
	// Route cfg.Exchange through our bounded client too; otherwise oauth2 falls
	// back to http.DefaultClient, which has no timeout.
	ctx = context.WithValue(ctx, oauth2.HTTPClient, g.client)
	tok, err := g.cfg.Exchange(ctx, code)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://openidconnect.googleapis.com/v1/userinfo", http.NoBody)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("userinfo: status %d", resp.StatusCode)
	}
	var info struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, "", err
	}
	return tok, info.Email, nil
}

// MicrosoftAuthCodeURL builds the consent URL for the signed state, or
// ErrOAuthDisabled if M365 OAuth is unconfigured. Mirrors GoogleAuthCodeURL but
// against s.msOAuth. offline_access is already in the requested scopes, so a
// refresh token is issued; prompt=consent forces it every time. (AccessTypeOffline
// is a Google-only param and is deliberately omitted here.)
func (s *Service) MicrosoftAuthCodeURL(state string) (string, error) {
	if !s.msOAuth.Enabled() {
		return "", ErrOAuthDisabled
	}
	return s.msOAuth.Config().AuthCodeURL(state,
		oauth2.SetAuthURLParam("prompt", "consent"),
	), nil
}

// CompleteMicrosoftOAuth exchanges the code, learns the connected address, seals
// the token, and persists a new m365 mailbox in workspaceID. As with
// CompleteGoogleOAuth, workspaceID is supplied by the caller (the callback
// derives it from the verified signed state, never a request body), so this
// write is workspace-pinned. Dedupes on email like ConnectSMTP.
func (s *Service) CompleteMicrosoftOAuth(ctx context.Context, code string, workspaceID uuid.UUID) (MailboxSafe, error) {
	return s.completeOAuth(ctx, code, workspaceID, s.msOAuth.Enabled(), s.msExchanger, "m365")
}

// microsoftExchanger is the production TokenExchanger for M365: it exchanges the
// code with Azure AD and reads the connected address from the Microsoft Graph
// /me endpoint. Both hosts are fixed Microsoft endpoints (no SSRF surface).
type microsoftExchanger struct {
	cfg    *oauth2.Config
	client *http.Client
}

// NewMicrosoftExchanger builds the production exchanger from the app's Microsoft
// OAuth config. The client carries a timeout so neither the code exchange nor
// the Graph /me call can hang a request goroutine indefinitely.
func NewMicrosoftExchanger(oauth mail.MicrosoftOAuth) TokenExchanger {
	return &microsoftExchanger{cfg: oauth.Config(), client: &http.Client{Timeout: 10 * time.Second}}
}

func (m *microsoftExchanger) Exchange(ctx context.Context, code string) (*oauth2.Token, string, error) {
	// Route cfg.Exchange through our bounded client too; otherwise oauth2 falls
	// back to http.DefaultClient, which has no timeout.
	ctx = context.WithValue(ctx, oauth2.HTTPClient, m.client)
	tok, err := m.cfg.Exchange(ctx, code)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://graph.microsoft.com/v1.0/me", http.NoBody)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("graph /me: status %d", resp.StatusCode)
	}
	// Graph returns the address in "mail"; when a user has no mailbox alias set
	// it can be null, so fall back to userPrincipalName (the sign-in address).
	var info struct {
		Mail              string `json:"mail"`
		UserPrincipalName string `json:"userPrincipalName"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, "", err
	}
	email := info.Mail
	if email == "" {
		email = info.UserPrincipalName
	}
	return tok, email, nil
}
