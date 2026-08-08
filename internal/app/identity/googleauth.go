package identity

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// loginScopes are the ONLY scopes a sign-in requests. Deliberately no Gmail
// scopes: those are Google-restricted, they would drag every sign-in through a
// scarier consent screen for permissions login does not need, and a mailbox token
// obtained here would have no per-workspace DEK to be sealed under — at sign-up
// time the workspace does not exist yet. Connecting a mailbox stays the separate,
// authenticated mailbox-connect flow.
var loginScopes = []string{"openid", "email", "profile"}

// googleIssuers are the two `iss` values Google uses for ID tokens.
var googleIssuers = []string{"https://accounts.google.com", "accounts.google.com"}

// GoogleSignIn holds the app's Google OAuth client credentials for the SIGN-IN
// flow. Zero value = disabled (the self-hoster configured no Google client), and
// the redirect URL is this app's own callback, distinct from the mailbox-connect
// one.
type GoogleSignIn struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

// NewGoogleAuthenticator builds the production GoogleAuthenticator. The HTTP
// client carries a timeout so a stalled token exchange cannot hold a request
// goroutine open indefinitely.
func NewGoogleAuthenticator(cfg GoogleSignIn) GoogleAuthenticator {
	return &googleAuthenticator{cfg: cfg, client: &http.Client{Timeout: 10 * time.Second}}
}

type googleAuthenticator struct {
	cfg    GoogleSignIn
	client *http.Client
}

func (g *googleAuthenticator) Enabled() bool {
	return g.cfg.ClientID != "" && g.cfg.ClientSecret != ""
}

func (g *googleAuthenticator) oauthConfig() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     g.cfg.ClientID,
		ClientSecret: g.cfg.ClientSecret,
		RedirectURL:  g.cfg.RedirectURL,
		Scopes:       loginScopes,
		Endpoint:     google.Endpoint,
	}
}

// AuthCodeURL builds the consent URL with the S256 PKCE challenge derived from
// codeVerifier. No access_type=offline and no prompt=consent: sign-in needs one
// ID token, not a long-lived refresh token to store, so asking for offline access
// would request a credential there is nothing to do with.
//
// The fixed accounts.google.com endpoint means no user input reaches the URL host
// (no SSRF surface); state and the challenge are the only interpolated values and
// both are server-minted.
func (g *googleAuthenticator) AuthCodeURL(state, codeVerifier string) string {
	return g.oauthConfig().AuthCodeURL(state, oauth2.S256ChallengeOption(codeVerifier))
}

// Exchange redeems the authorization code, replaying codeVerifier so Google can
// check the PKCE challenge it was handed at /authorize, and reads the identity out
// of the returned ID token.
func (g *googleAuthenticator) Exchange(ctx context.Context, code, codeVerifier string) (GoogleIdentity, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, g.client)
	tok, err := g.oauthConfig().Exchange(ctx, code, oauth2.VerifierOption(codeVerifier))
	if err != nil {
		return GoogleIdentity{}, fmt.Errorf("code exchange: %w", err)
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok || rawID == "" {
		return GoogleIdentity{}, errors.New("google: token response carried no id_token")
	}
	return parseGoogleIDToken(rawID, g.cfg.ClientID, time.Now())
}

// googleClaims is the ID-token payload subset sign-in reads.
type googleClaims struct {
	Issuer        string `json:"iss"`
	Audience      string `json:"aud"`
	Subject       string `json:"sub"`
	Expiry        int64  `json:"exp"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	GivenName     string `json:"given_name"`
	HostedDomain  string `json:"hd"`
}

// parseGoogleIDToken reads the claims out of an ID token WITHOUT verifying its
// signature, and that is deliberate and safe here for one specific reason: this
// token was not presented by a client. It came back in the body of a direct
// server-to-server TLS request that WE made to Google's token endpoint, with our
// client secret, in exchange for a code bound to our PKCE verifier. There is no
// party between us and Google to have substituted it, which is why Google's own
// documentation says signature validation may be skipped on tokens received
// straight from the token endpoint (and why fetching + caching JWKS here would
// add a network dependency and a cache-invalidation problem for no security gain).
//
// The issuer, audience, and expiry ARE checked, as cheap protection against a
// token that came from somewhere other than where we think — most importantly the
// audience, so a token minted for a DIFFERENT Google client can never authenticate
// here even if it somehow reached this function.
//
// If this function is ever reused for a token that arrived from a client (an
// implicit flow, a mobile app posting an id_token), full signature verification
// against Google's JWKS becomes mandatory.
func parseGoogleIDToken(raw, wantAudience string, now time.Time) (GoogleIdentity, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return GoogleIdentity{}, errors.New("google: id_token is not a three-part JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return GoogleIdentity{}, fmt.Errorf("google: id_token payload not base64url: %w", err)
	}
	var c googleClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return GoogleIdentity{}, fmt.Errorf("google: id_token payload not json: %w", err)
	}
	if !issuerAllowed(c.Issuer) {
		return GoogleIdentity{}, fmt.Errorf("google: unexpected id_token issuer %q", c.Issuer)
	}
	if c.Audience != wantAudience {
		return GoogleIdentity{}, errors.New("google: id_token audience is not this client")
	}
	if c.Expiry == 0 || now.After(time.Unix(c.Expiry, 0)) {
		return GoogleIdentity{}, errors.New("google: id_token expired")
	}
	if c.Subject == "" {
		return GoogleIdentity{}, errors.New("google: id_token carried no subject")
	}
	return GoogleIdentity{
		Subject:       c.Subject,
		Email:         c.Email,
		EmailVerified: c.EmailVerified,
		GivenName:     c.GivenName,
		HostedDomain:  c.HostedDomain,
	}, nil
}

func issuerAllowed(iss string) bool {
	for _, want := range googleIssuers {
		if iss == want {
			return true
		}
	}
	return false
}
