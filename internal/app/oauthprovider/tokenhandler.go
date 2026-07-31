package oauthprovider

import (
	"errors"
	"net/http"

	"github.com/inroad/inroad/internal/platform/httpx"
)

// The OAuth token/introspection/revocation endpoints are client-authenticated (never
// session-authed) and take an application/x-www-form-urlencoded body per the specs.
// They are registered on the public /oauth2 mount; the client authenticates itself in
// the service (public client by client_id, confidential by secret).

// tokenResponseBody is the RFC 6749 §5.1 successful token response. refresh_token is
// always issued here, so it is not omitempty; scope echoes the granted scopes.
type tokenResponseBody struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

// tokenErrorBody is the RFC 6749 §5.2 error response shape.
type tokenErrorBody struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
}

// introspectBody is the RFC 7662 response. For an inactive token only `active` is
// emitted (everything else omitempty), so an inactive/unknown token leaks nothing.
type introspectBody struct {
	Active    bool   `json:"active"`
	Scope     string `json:"scope,omitempty"`
	ClientID  string `json:"client_id,omitempty"`
	Sub       string `json:"sub,omitempty"`
	Exp       int64  `json:"exp,omitempty"`
	TokenType string `json:"token_type,omitempty"`
}

// token handles POST /oauth2/token (OAuth 2.1 token endpoint).
func (h *Handler) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeTokenError(w, errInvalidRequest)
		return
	}
	res, err := h.svc.Token(r.Context(), TokenRequest{
		GrantType:    r.PostForm.Get("grant_type"),
		Code:         r.PostForm.Get("code"),
		RedirectURI:  r.PostForm.Get("redirect_uri"),
		CodeVerifier: r.PostForm.Get("code_verifier"),
		RefreshToken: r.PostForm.Get("refresh_token"),
		Scope:        r.PostForm.Get("scope"),
		Client:       clientCredsFromRequest(r),
	})
	if err != nil {
		writeTokenError(w, err)
		return
	}
	noStore(w)
	httpx.JSON(w, http.StatusOK, tokenResponseBody(res))
}

// introspect handles POST /oauth2/introspect (RFC 7662). Client-authenticated; returns
// active/inactive metadata with no token-existence oracle.
func (h *Handler) introspect(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeTokenError(w, errInvalidRequest)
		return
	}
	res, err := h.svc.Introspect(r.Context(), r.PostForm.Get("token"), clientCredsFromRequest(r))
	if err != nil {
		writeTokenError(w, err)
		return
	}
	noStore(w)
	httpx.JSON(w, http.StatusOK, introspectBody(res))
}

// revoke handles POST /oauth2/revoke (RFC 7009). Client-authenticated; a client may
// revoke only its own tokens, and an unknown/foreign token is a 200 no-op (no oracle).
func (h *Handler) revoke(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeTokenError(w, errInvalidRequest)
		return
	}
	if err := h.svc.Revoke(r.Context(), r.PostForm.Get("token"), clientCredsFromRequest(r)); err != nil {
		writeTokenError(w, err)
		return
	}
	noStore(w)
	w.WriteHeader(http.StatusOK)
}

// clientCredsFromRequest extracts client credentials, preferring HTTP Basic
// (client_secret_basic) over the form body (client_secret_post). r.ParseForm must have
// run. The provider's client_ids/secrets are URL-safe base64 with no characters needing
// form-decoding, so the Basic-decoded values are used verbatim (avoiding the classic
// '+'->space QueryUnescape pitfall).
func clientCredsFromRequest(r *http.Request) ClientCredentials {
	if id, secret, ok := r.BasicAuth(); ok {
		return ClientCredentials{ID: id, Secret: secret, HasSecret: true}
	}
	secret := r.PostForm.Get("client_secret")
	return ClientCredentials{
		ID:        r.PostForm.Get("client_id"),
		Secret:    secret,
		HasSecret: secret != "",
	}
}

// writeTokenError renders an RFC 6749 §5.2 JSON error. A non-TokenError (an infra fault)
// is mapped to a generic 500 server_error so no internal detail leaks. invalid_client
// additionally carries a Basic WWW-Authenticate challenge (RFC 6749 §5.2).
func writeTokenError(w http.ResponseWriter, err error) {
	var te *TokenError
	if !errors.As(err, &te) {
		te = tokenErr(http.StatusInternalServerError, "server_error", "the request could not be processed")
	}
	if te.Code == "invalid_client" {
		w.Header().Set("WWW-Authenticate", `Basic realm="oauth2"`)
	}
	noStore(w)
	httpx.JSON(w, te.Status, tokenErrorBody{Error: te.Code, ErrorDescription: te.Desc})
}

// noStore sets the cache headers OAuth requires on token responses (never cache a token
// or a token error).
func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}
