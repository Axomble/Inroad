package identity

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/inroad/inroad/internal/platform/httpx"
)

// googleSuccessPath is the SPA route a SUCCESSFUL sign-in lands the browser on.
// It is a route of its own (rather than the login page) because it owns one job:
// exchange the refresh cookie the callback just set for an access token, then move
// on. A `return_to` the caller asked for rides along as a query parameter for that
// route to navigate to once a session exists.
const googleSuccessPath = "/auth/google/callback"

// googleFailurePath is where every FAILED sign-in lands: the login route, with
// `?google_error=<reason>`. A failure has no session, so the user's next action is
// to sign in again — sending them anywhere else would only make them navigate back.
const googleFailurePath = "/"

// googleErrorParam is the query parameter carrying the failure reason. Named for
// the provider rather than reusing the mailbox flow's `oauth_error`, because the
// SPA surfaces these on the LOGIN page with sign-in copy, and the two sets of
// reasons are not interchangeable.
const googleErrorParam = "google_error"

// startGoogleSignIn (PUBLIC) begins a Google sign-in/sign-up and redirects the
// browser straight to Google's consent screen. This is a top-level navigation, not
// a fetch: the SPA does window.location.assign() and reads no body.
//
// Optional `return_to` is an in-app path to land on afterwards. It is validated as
// a same-origin path and stashed server-side against the state nonce (never echoed
// through Google), so it cannot be turned into an open redirect.
//
// A deployment with no Google credentials redirects back to the login page with
// google_error=disabled rather than erroring, so a stray click on a button that
// should have been hidden still lands the user somewhere sensible.
//
// Public by necessity: this is a pre-authentication endpoint, exactly like /login.
// It is NOT the mailbox-connect start endpoint (which sits behind RequireAuth +
// RequireVerified and requests Gmail scopes) — the two flows share a provider and
// nothing else.
func (h *Handler) startGoogleSignIn(w http.ResponseWriter, r *http.Request) {
	authURL, err := h.svc.StartGoogleSignIn(r.Context(), StartGoogleSignInInput{
		ReturnTo: r.URL.Query().Get("return_to"),
	})
	if err != nil {
		if errors.Is(err, ErrGoogleDisabled) {
			h.redirectGoogleFailure(w, r, "disabled")
			return
		}
		slog.Error("identity: could not start google sign-in", "err", err)
		h.redirectGoogleFailure(w, r, "server_error")
		return
	}
	http.Redirect(w, r, authURL, http.StatusFound)
}

// startGoogleSignInJSON (PUBLIC) is the same flow for a caller that wants to learn
// the consent URL WITHOUT being navigated: it answers 501 when Google sign-in is
// unconfigured, which is how a client can hide the button instead of offering one
// that only ever redirects to an error. It is also the only way to pass an invite
// token, which must not travel in a URL the browser records.
func (h *Handler) startGoogleSignInJSON(w http.ResponseWriter, r *http.Request) {
	var body struct {
		// InviteToken (optional) is the raw token from an invite link, when the
		// invitee chose to accept it with Google instead of setting a password.
		InviteToken string `json:"invite_token"`
		// ReturnTo (optional) is an in-app path to land on after sign-in.
		ReturnTo string `json:"return_to"`
	}
	// An absent or empty body is legitimate (a plain sign-in), so only MALFORMED
	// json is an error.
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpx.Error(w, http.StatusBadRequest, "invalid json")
			return
		}
	}
	authURL, err := h.svc.StartGoogleSignIn(r.Context(), StartGoogleSignInInput{
		InviteToken: body.InviteToken, ReturnTo: body.ReturnTo,
	})
	if err != nil {
		if errors.Is(err, ErrGoogleDisabled) {
			httpx.Error(w, http.StatusNotImplemented, "google sign-in not configured")
			return
		}
		slog.Error("identity: could not start google sign-in", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "could not start google sign-in")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"auth_url": authURL})
}

// redirectGoogleFailure sends the browser back to the login page with a
// machine-readable reason. reason is always a hardcoded literal from the switch in
// googleSignInCallback (never provider or user input), so the redirect stays
// injection-safe, and server-side detail is logged rather than put in the URL.
func (h *Handler) redirectGoogleFailure(w http.ResponseWriter, r *http.Request, reason string) {
	http.Redirect(w, r, h.svc.appBaseURL+googleFailurePath+"?"+googleErrorParam+"="+reason, http.StatusFound)
}

// googleSignInCallback (PUBLIC) is the top-level browser navigation Google
// redirects to after consent. It cannot rely on any cookie (SameSite on a
// cross-site redirect), so the signed, single-use `state` IS the authentication —
// and the workspace the resulting session lands in is derived entirely
// server-side from the provider identity, never from a callback query parameter.
//
// Being a browser navigation, it never returns JSON and never returns a 5xx:
// success 302s to googleSuccessPath, every failure 302s to the login page with
// google_error=<reason>.
//
// On success the refresh + CSRF cookies are set and the SPA obtains its access
// token by calling POST /auth/refresh — the same bootstrap it already performs on
// a hard reload. No token is ever placed in the redirect URL, where it would land
// in browser history and any Referer.
func (h *Handler) googleSignInCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("error") != "" || q.Get("code") == "" {
		h.redirectGoogleFailure(w, r, "denied")
		return
	}
	ua, ip := h.clientMeta(r)
	sess, returnTo, err := h.svc.CompleteGoogleSignIn(r.Context(), q.Get("code"), q.Get("state"), ua, ip)
	if err != nil {
		switch {
		case errors.Is(err, ErrStateInvalid):
			h.redirectGoogleFailure(w, r, "bad_state")
		case errors.Is(err, ErrProviderNoEmail):
			h.redirectGoogleFailure(w, r, "no_email")
		case errors.Is(err, ErrProviderEmailUnverified):
			h.redirectGoogleFailure(w, r, "email_unverified")
		case errors.Is(err, ErrGoogleDisabled):
			h.redirectGoogleFailure(w, r, "disabled")
		case errors.Is(err, ErrInviteNotForIdentity), errors.Is(err, ErrTokenInvalid):
			h.redirectGoogleFailure(w, r, "invite_invalid")
		default:
			slog.Error("identity: google sign-in callback failed", "err", err)
			h.redirectGoogleFailure(w, r, "exchange_failed")
		}
		return
	}
	// Cookies only — deliberately not h.issueSession, which writes a JSON body an
	// in-flight browser navigation has nowhere to put.
	h.setRefreshCookie(w, sess.RawRefresh)
	if err := h.setCSRFCookie(w); err != nil {
		slog.Error("identity: could not issue csrf token on google callback", "err", err)
		h.redirectGoogleFailure(w, r, "server_error")
		return
	}
	target := h.svc.appBaseURL + googleSuccessPath + "?signin=ok"
	if returnTo != "" {
		// Already validated as a same-origin path when it was stored (safeReturnTo);
		// escaped here so it survives as ONE parameter value rather than smuggling
		// extra ones into this URL.
		target += "&return_to=" + url.QueryEscape(returnTo)
	}
	http.Redirect(w, r, target, http.StatusFound)
}
