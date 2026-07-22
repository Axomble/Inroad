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

// ErrOAuthDisabled is returned when a Gmail OAuth action is attempted but no
// Google client credentials are configured (self-hoster left them blank).
var ErrOAuthDisabled = errors.New("gmail oauth not configured")

// TokenExchanger exchanges an auth code for a token and the mailbox's own email
// address. The production impl calls Google (googleExchanger); tests fake it.
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
	if !s.oauth.Enabled() {
		return MailboxSafe{}, ErrOAuthDisabled
	}
	tok, email, err := s.exchanger.Exchange(ctx, code)
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
	ciphertext, err := s.sealer.Seal(raw)
	if err != nil {
		return MailboxSafe{}, err
	}

	return s.store.Create(ctx, gen.CreateMailboxParams{
		WorkspaceID:      workspaceID,
		Provider:         "gmail",
		Email:            email,
		DisplayName:      email,
		SecretCiphertext: ciphertext,
		// SMTP/IMAP fields are unused for gmail; their zero values are fine.
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://openidconnect.googleapis.com/v1/userinfo", nil)
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
