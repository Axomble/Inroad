package mail

import (
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/microsoft"
)

// microsoftScopes are the OAuth scopes requested when connecting a Microsoft 365
// mailbox: Mail.Send (outbound), Mail.Read (reply/bounce polling), User.Read
// (learn the connected address), offline_access (refresh token), and
// openid/email.
var microsoftScopes = []string{
	"https://graph.microsoft.com/Mail.Send",
	"https://graph.microsoft.com/Mail.Read",
	"https://graph.microsoft.com/User.Read",
	"offline_access",
	"openid",
	"email",
}

// MicrosoftOAuth holds the app's Microsoft (Azure AD) OAuth client credentials.
// Zero value = disabled (self-hoster did not configure M365). Tenant selects the
// Azure AD authority and defaults to "common" when empty.
type MicrosoftOAuth struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Tenant       string
}

// Enabled reports whether M365 OAuth is configured.
func (m MicrosoftOAuth) Enabled() bool { return m.ClientID != "" && m.ClientSecret != "" }

// Config builds the x/oauth2 config for the authorization-code flow and
// TokenSource refresh. Scopes are fixed (microsoftScopes); the endpoint is the
// Azure AD authority for Tenant (defaulting to "common").
func (m MicrosoftOAuth) Config() *oauth2.Config {
	tenant := m.Tenant
	if tenant == "" {
		tenant = "common"
	}
	return &oauth2.Config{
		ClientID:     m.ClientID,
		ClientSecret: m.ClientSecret,
		RedirectURL:  m.RedirectURL,
		Scopes:       microsoftScopes,
		Endpoint:     microsoft.AzureADEndpoint(tenant),
	}
}
