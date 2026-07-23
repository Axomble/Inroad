package mail

import (
	"encoding/json"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// gmailScopes are the OAuth scopes requested when connecting a Gmail mailbox:
// send (outbound), readonly (reply/bounce polling), and openid/email (learn the
// connected address).
var gmailScopes = []string{
	"https://www.googleapis.com/auth/gmail.send",
	"https://www.googleapis.com/auth/gmail.readonly",
	"openid",
	"email",
}

// GoogleOAuth holds the app's Google OAuth client credentials. Zero value =
// disabled (self-hoster did not configure Google).
type GoogleOAuth struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

// Enabled reports whether Gmail OAuth is configured.
func (g GoogleOAuth) Enabled() bool { return g.ClientID != "" && g.ClientSecret != "" }

// Config builds the x/oauth2 config for the authorization-code flow and
// TokenSource refresh. Scopes are fixed (gmailScopes).
func (g GoogleOAuth) Config() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     g.ClientID,
		ClientSecret: g.ClientSecret,
		RedirectURL:  g.RedirectURL,
		Scopes:       gmailScopes,
		Endpoint:     google.Endpoint,
	}
}

// MarshalToken serializes an OAuth token for sealing into secret_ciphertext.
func MarshalToken(t *oauth2.Token) ([]byte, error) { return json.Marshal(t) }

// UnmarshalToken parses the sealed OAuth token JSON.
func UnmarshalToken(b []byte) (*oauth2.Token, error) {
	var t oauth2.Token
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, err
	}
	return &t, nil
}
