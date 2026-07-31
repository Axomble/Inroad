package oauthprovider

import (
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
	"github.com/inroad/inroad/internal/platform/validate"
)

// Handler exposes the OAuth 2.1 authorization-server surface: dynamic client
// registration, admin client management, the authorization endpoint, and the consent
// data + decision endpoints. Route-level auth (session/admin/CSRF) and the
// registration rate limit are applied in Routes; the authorize + register handlers
// resolve any resource owner through the ResourceOwner seam, never a request param.
type Handler struct {
	svc   *Service
	owner ResourceOwner
}

// NewHandler builds a Handler over svc and the resource-owner seam.
func NewHandler(svc *Service, owner ResourceOwner) *Handler {
	return &Handler{svc: svc, owner: owner}
}

// ---------------------------------------------------------------------------
// DTOs
// ---------------------------------------------------------------------------

type registerRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	Scope                   string   `json:"scope"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

// clientResponse is the non-secret client projection. client_secret is present ONLY
// on the one-time registration response for a confidential client (omitempty), never
// on a management list.
type clientResponse struct {
	ClientID                string   `json:"client_id"`
	ClientSecret            string   `json:"client_secret,omitempty"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	Scope                   string   `json:"scope"`
	ClientType              string   `json:"client_type"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	CreatedAt               string   `json:"created_at"`
	RevokedAt               *string  `json:"revoked_at"`
}

type listClientsResponse struct {
	Clients []clientResponse `json:"clients"`
}

type consentResponse struct {
	ClientName      string   `json:"client_name"`
	RequestedScopes []string `json:"requested_scopes"`
	RedirectURI     string   `json:"redirect_uri"`
}

type decideRequest struct {
	ConsentID string `json:"consent_id" validate:"required"`
	Decision  string `json:"decision" validate:"required,oneof=approve deny"`
}

type decideResponse struct {
	RedirectTo string `json:"redirect_to"`
}

// ---------------------------------------------------------------------------
// Dynamic client registration + management
// ---------------------------------------------------------------------------

// register handles RFC 7591 dynamic client registration. It is public but
// rate-limited (see Routes). When the request carries a valid session, the caller's
// user + workspace are recorded so a workspace admin can later list/revoke the
// client; an anonymous registration records neither.
func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var body registerRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	in := RegisterInput{
		ClientName:              body.ClientName,
		RedirectURIs:            body.RedirectURIs,
		GrantTypes:              body.GrantTypes,
		ResponseTypes:           body.ResponseTypes,
		Scope:                   body.Scope,
		TokenEndpointAuthMethod: body.TokenEndpointAuthMethod,
	}
	// Best-effort: record the registering user + workspace when a session is present.
	// A resolution failure never blocks an (otherwise anonymous) registration.
	if owner, ok, err := h.owner.Resolve(r.Context(), r); err == nil && ok {
		uid, wid := owner.UserID, owner.WorkspaceID
		in.CreatedBy, in.WorkspaceID = &uid, &wid
	}

	res, err := h.svc.RegisterClient(r.Context(), in)
	if err != nil {
		switch {
		case errors.Is(err, ErrValidation), errors.Is(err, ErrInvalidRedirectURI),
			errors.Is(err, ErrScopeNotGrantable):
			httpx.Error(w, http.StatusBadRequest, err.Error())
		default:
			httpx.Error(w, http.StatusInternalServerError, "could not register client")
		}
		return
	}
	httpx.JSON(w, http.StatusCreated, toClientResponse(res.Client, res.ClientSecret))
}

// listClients returns the caller's workspace's registered clients, without secrets.
func (h *Handler) listClients(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	views, err := h.svc.ListClients(r.Context(), ws)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not list clients")
		return
	}
	out := make([]clientResponse, 0, len(views))
	for _, v := range views {
		out = append(out, toClientResponse(v, ""))
	}
	httpx.JSON(w, http.StatusOK, listClientsResponse{Clients: out})
}

// revokeClient revokes a client owned by the caller's workspace. A foreign or unknown
// client_id returns 404 so a caller cannot probe or revoke another workspace's client.
func (h *Handler) revokeClient(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	clientID := chi.URLParam(r, "client_id")
	if clientID == "" {
		httpx.Error(w, http.StatusBadRequest, "missing client_id")
		return
	}
	if err := h.svc.RevokeClient(r.Context(), ws, clientID); err != nil {
		if errors.Is(err, ErrNotFound) {
			httpx.Error(w, http.StatusNotFound, "client not found")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "could not revoke client")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Authorization endpoint
// ---------------------------------------------------------------------------

// authorize is the OAuth 2.1 authorization endpoint (a top-level browser
// navigation). It resolves the resource owner via the seam, runs the ordered
// validation in the service, and either renders an error (before the redirect_uri is
// trusted) or 302-redirects (to the client, the consent screen, or login).
func (h *Handler) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	in := AuthorizeInput{
		ResponseType:        q.Get("response_type"),
		ClientID:            q.Get("client_id"),
		RedirectURI:         q.Get("redirect_uri"),
		Scope:               q.Get("scope"),
		State:               q.Get("state"),
		CodeChallenge:       q.Get("code_challenge"),
		CodeChallengeMethod: q.Get("code_challenge_method"),
		RawQuery:            r.URL.RawQuery,
	}
	// Resource owner resolved ONLY from the session (never a query param). A resolver
	// infra error fails the request; "no session" leaves Owner nil -> login redirect.
	owner, ok, err := h.owner.Resolve(r.Context(), r)
	if err != nil {
		renderAuthorizeError(w, "could not verify your session")
		return
	}
	if ok {
		in.Owner = &owner
	}

	res, err := h.svc.Authorize(r.Context(), in)
	if err != nil {
		renderAuthorizeError(w, "the authorization request could not be processed")
		return
	}
	switch res.Outcome {
	case OutcomeError:
		renderAuthorizeError(w, res.RenderMessage)
	case OutcomeRedirect:
		http.Redirect(w, r, res.RedirectTo, http.StatusFound)
	}
}

// ---------------------------------------------------------------------------
// Consent data + decision
// ---------------------------------------------------------------------------

// consentData returns the pending consent request's display data for the SPA. It is
// session-authed; the service additionally binds the request to the session user.
func (h *Handler) consentData(w http.ResponseWriter, r *http.Request) {
	uid, ok := callerUserID(w, r)
	if !ok {
		return
	}
	consentID := chi.URLParam(r, "consent_id")
	view, err := h.svc.ConsentRequest(r.Context(), consentID, uid)
	if err != nil {
		if errors.Is(err, ErrConsentNotFound) {
			httpx.Error(w, http.StatusNotFound, "consent request not found")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "could not load consent request")
		return
	}
	httpx.JSON(w, http.StatusOK, consentResponse(view))
}

// decideConsent records the resource owner's approve/deny decision and returns the
// URL the SPA should navigate the browser to. Session-authed + CSRF-guarded; the
// service re-checks the session user owns the request.
func (h *Handler) decideConsent(w http.ResponseWriter, r *http.Request) {
	uid, ok := callerUserID(w, r)
	if !ok {
		return
	}
	var body decideRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := validate.Struct(body); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := h.svc.DecideConsent(r.Context(), DecideInput{
		ConsentID: body.ConsentID,
		UserID:    uid,
		Approve:   body.Decision == "approve",
	})
	if err != nil {
		if errors.Is(err, ErrConsentNotFound) {
			httpx.Error(w, http.StatusNotFound, "consent request not found")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "could not record consent decision")
		return
	}
	httpx.JSON(w, http.StatusOK, decideResponse(res))
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// callerUserID pulls and parses the authenticated caller's user id.
func callerUserID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	p, ok := auth.UserFromContext(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthorized")
		return uuid.Nil, false
	}
	uid, err := uuid.Parse(p.UserID)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "invalid token")
		return uuid.Nil, false
	}
	return uid, true
}

// renderAuthorizeError writes a minimal, static HTML error page (400) for the
// no-redirect authorization failures. msg is always a server-owned literal; it is
// still HTML-escaped defensively so it can never carry markup.
func renderAuthorizeError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	page := "<!doctype html><html><head><title>Authorization error</title></head>" +
		"<body><h1>Authorization error</h1><p>" + html.EscapeString(msg) + "</p></body></html>"
	_, _ = w.Write([]byte(page))
}

// toClientResponse renders a ClientView (plus an optional one-time secret) as the
// wire DTO. The scope slice is space-joined per RFC 7591; RevokedAt is RFC3339 or nil.
func toClientResponse(v ClientView, secret string) clientResponse {
	return clientResponse{
		ClientID:                v.ClientID,
		ClientSecret:            secret,
		ClientName:              v.ClientName,
		RedirectURIs:            v.RedirectURIs,
		GrantTypes:              v.GrantTypes,
		ResponseTypes:           v.ResponseTypes,
		Scope:                   strings.Join(v.Scopes, " "),
		ClientType:              v.ClientType,
		TokenEndpointAuthMethod: v.TokenEndpointAuthMethod,
		CreatedAt:               v.CreatedAt.UTC().Format(time.RFC3339),
		RevokedAt:               rfc3339Ptr(v.RevokedAt),
	}
}

// rfc3339Ptr renders an optional time as an RFC3339 (UTC) string pointer.
func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}
