package identity

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
	"github.com/inroad/inroad/internal/platform/validate"
)

// sessionCacheBuster drops a session's cached auth-state so a revoke takes
// effect on the next request even while the verifier's short-TTL cache is warm.
// Satisfied by *SessionVerifier; kept as a tiny interface so the handler can be
// unit-tested (and constructed) without one.
type sessionCacheBuster interface {
	Bust(sid uuid.UUID)
}

// TwoFactorGate is the login-gate seam: after a correct password, login consults
// it to decide whether the user must clear a second factor. A confirmed-2FA user
// gets a single-use challenge (and NO session) instead of tokens; everyone else
// logs in unchanged. Kept as a consumer-defined interface (satisfied by
// twofa.Service and wired at the composition root) so identity never imports the
// twofa domain — the app-packages-don't-import-each-other invariant holds.
type TwoFactorGate interface {
	// ChallengeIfRequired returns (rawChallenge, true, nil) when userID has a
	// confirmed second factor, or ("", false, nil) when login should proceed to
	// issue a session.
	ChallengeIfRequired(ctx context.Context, userID uuid.UUID, ip string) (string, bool, error)
}

// Handler exposes the identity domain (register/login/refresh/logout,
// session introspection + management, and workspace switching) over HTTP.
type Handler struct {
	svc            *Service
	jwtSecret      []byte
	accessTTL      time.Duration
	refreshTTL     time.Duration
	cookieSecure   bool
	cookieDomain   string
	trustedProxies []*net.IPNet
	buster         sessionCacheBuster
	// gate (may be nil) is consulted on login to interpose the 2FA challenge for
	// users with a confirmed second factor. Nil disables the gate entirely (every
	// user logs in with password alone) — the shape before P2 and in unit tests.
	gate TwoFactorGate
}

// NewHandler constructs a Handler backed by svc. accessTTL/refreshTTL size
// the access token and refresh cookie lifetimes; cookieSecure/cookieDomain
// control the cookie attributes (Secure should be true outside local dev).
// trustedProxies is the CIDR list whose X-Forwarded-For / X-Real-IP the
// handler will honor; unparsable entries are silently dropped (loudness
// belongs in cmd startup, not per-request). buster (may be nil) invalidates
// the verifier's session-auth cache when a session is revoked in-process.
func NewHandler(svc *Service, jwtSecret []byte, accessTTL, refreshTTL time.Duration, cookieSecure bool, cookieDomain string, trustedProxies []string, buster sessionCacheBuster, gate TwoFactorGate) *Handler {
	nets := make([]*net.IPNet, 0, len(trustedProxies))
	for _, c := range trustedProxies {
		if _, n, err := net.ParseCIDR(c); err == nil {
			nets = append(nets, n)
		}
	}
	return &Handler{
		svc: svc, jwtSecret: jwtSecret, accessTTL: accessTTL, refreshTTL: refreshTTL,
		cookieSecure: cookieSecure, cookieDomain: cookieDomain, trustedProxies: nets, buster: buster, gate: gate,
	}
}

// bustSession invalidates the verifier's cached auth-state for sid, if a buster
// was wired (nil in unit tests / zero-value handlers).
func (h *Handler) bustSession(sid uuid.UUID) {
	if h.buster != nil {
		h.buster.Bust(sid)
	}
}

type membershipDTO struct {
	WorkspaceID   string `json:"workspace_id"`
	WorkspaceName string `json:"workspace_name"`
	Role          string `json:"role"`
}

type sessionResponse struct {
	AccessToken       string          `json:"access_token"`
	ExpiresIn         int             `json:"expires_in"`
	UserID            string          `json:"user_id"`
	ActiveWorkspaceID string          `json:"active_workspace_id"`
	Role              string          `json:"role"`
	Memberships       []membershipDTO `json:"memberships"`
}

// clientMeta extracts the user-agent and bare client IP from the request.
// RemoteAddr is "host:port" (or "[ipv6]:port"); the service's parseIP wants
// a bare IP (an IP with a stray port fails to parse and is stored as NULL),
// so the port is stripped here before it ever reaches the service layer.
// net.SplitHostPort correctly unwraps bracketed IPv6 addresses, unlike a
// naive split on ":" which mangles them (an IPv6 address itself contains
// colons). If RemoteAddr has no port (or isn't in host:port form), fall
// back to using it as-is.
//
// When RemoteAddr matches one of h.trustedProxies, X-Forwarded-For's
// leftmost IP (or, absent that, X-Real-IP) is preferred — behind a reverse
// proxy those headers carry the original client. Trusting them
// unconditionally would let any caller spoof their IP, so trust is opt-in
// via INROAD_TRUSTED_PROXIES.
func (h *Handler) clientMeta(r *http.Request) (ua, ip string) {
	direct := remoteIPOnly(r.RemoteAddr)
	if h.isTrustedProxy(direct) {
		if v := r.Header.Get("X-Forwarded-For"); v != "" {
			// Leftmost = original client; anything to the right is a hop.
			if i := indexComma(v); i > 0 {
				return r.UserAgent(), trimSpace(v[:i])
			}
			return r.UserAgent(), trimSpace(v)
		}
		if v := r.Header.Get("X-Real-IP"); v != "" {
			return r.UserAgent(), trimSpace(v)
		}
	}
	return r.UserAgent(), direct
}

// remoteIPOnly strips the port from a RemoteAddr, or returns it unchanged
// if no port is present (e.g. a fuzz-test injecting a bare IP).
func remoteIPOnly(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

func (h *Handler) isTrustedProxy(ipStr string) bool {
	if ipStr == "" || len(h.trustedProxies) == 0 {
		return false
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	for _, n := range h.trustedProxies {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// indexComma returns the index of the first comma in s, or -1 if none.
func indexComma(s string) int { return strings.IndexByte(s, ',') }

func trimSpace(s string) string { return strings.TrimSpace(s) }

func toMembershipDTOs(mems []Membership) []membershipDTO {
	dto := make([]membershipDTO, len(mems))
	for i, m := range mems {
		dto[i] = membershipDTO{WorkspaceID: m.WorkspaceID.String(), WorkspaceName: m.WorkspaceName, Role: m.Role}
	}
	return dto
}

// mintAccessToken issues an access token for a session. The RULE for every
// caller: tokenVersion MUST be the session's LIVE token_version — 0 for a
// freshly-created session (register/login/refresh, whose rows default to 0), or
// the value read back from the session row for a SAME-SESSION re-issue against a
// still-live session (switchWorkspace, via RepointSessionWorkspace's RETURNING).
// A same-session re-issue that hardcoded 0 would be instantly rejected by the
// verifier once any later phase bumps that live session's token_version. Session
// TERMINATION is a different operation entirely — a full revoke
// (RevokeAllForUser / RevokeSessionOwned / RevokeFamily), never a bare re-mint.
func (h *Handler) mintAccessToken(userID, workspaceID, role, sessionID string, tokenVersion int) (string, error) {
	return auth.IssueToken(h.jwtSecret, auth.Claims{
		UserID: userID, WorkspaceID: workspaceID, Role: role,
		SessionID: sessionID, TokenVersion: tokenVersion,
	}, h.accessTTL)
}

// issueSession mints an access token for sess, sets the refresh + CSRF
// cookies, and writes the session JSON body. Shared by register/login/refresh.
//
// tv is 0 here: every session this issues (register, login, and refresh all
// create a fresh session row) starts at token_version 0 — the DB default — so a
// 0 claim always matches the session's live value. The OTHER re-issue site,
// switchWorkspace, re-mints for an EXISTING live session and therefore sources
// tv from the session row (never 0). Only a security event that BUMPS a
// still-live session (a later phase: 2FA/passkey change) advances token_version,
// and that flow re-issues the caller's access token with the new tv.
func (h *Handler) issueSession(w http.ResponseWriter, sess Session) {
	access, err := h.mintAccessToken(sess.UserID.String(), sess.WorkspaceID.String(), sess.Role, sess.SessionID.String(), 0)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not issue token")
		return
	}
	h.setRefreshCookie(w, sess.RawRefresh)
	if _, err := h.setCSRFCookie(w); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not issue csrf token")
		return
	}
	httpx.JSON(w, http.StatusOK, sessionResponse{
		AccessToken: access, ExpiresIn: int(h.accessTTL.Seconds()),
		UserID: sess.UserID.String(), ActiveWorkspaceID: sess.WorkspaceID.String(),
		Role: sess.Role, Memberships: toMembershipDTOs(sess.Memberships),
	})
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var body struct {
		WorkspaceName string `json:"workspace_name" validate:"required,min=1,max=200"`
		Email         string `json:"email" validate:"required,email"`
		Password      string `json:"password" validate:"required,min=8"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := validate.Struct(body); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	ua, ip := h.clientMeta(r)
	sess, err := h.svc.Register(r.Context(), RegisterInput{WorkspaceName: body.WorkspaceName, Email: body.Email, Password: body.Password, UserAgent: ua, IP: ip})
	if err != nil {
		if isUniqueViolation(err) {
			// Constant-time noop: burn the same argon2 wall-clock cost the
			// "user exists + wrong password" login path incurs, so a 409
			// response is indistinguishable from a 401 by timing. Without
			// this, an attacker could probe /register to enumerate emails
			// (fast 409 = registered; slow response = brand-new user).
			auth.CheckPassword(dummyHash, body.Password)
			httpx.Error(w, http.StatusConflict, "email already registered")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "could not register")
		return
	}
	h.issueSession(w, sess)
}

// twoFactorRequiredResponse is login's 200 body when the user has a confirmed
// second factor: no tokens are issued, only an opaque single-use challenge the
// client completes at POST /auth/2fa/verify. The absence of a session here is the
// fail-closed gate — a confirmed second factor cannot be skipped.
type twoFactorRequiredResponse struct {
	TwoFactorRequired bool   `json:"two_factor_required"`
	Challenge         string `json:"challenge"`
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	ua, ip := h.clientMeta(r)
	// Password (and workspace-membership) check FIRST — a wrong password returns
	// the same 401 whether or not the account has a second factor, so login never
	// leaks 2FA status to someone without the password.
	uid, err := h.svc.Authenticate(r.Context(), body.Email, body.Password)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	// Fail-closed gate: a confirmed second factor gets a challenge and NO session.
	if h.gate != nil {
		challenge, required, gerr := h.gate.ChallengeIfRequired(r.Context(), uid, ip)
		if gerr != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not start two-factor challenge")
			return
		}
		if required {
			httpx.JSON(w, http.StatusOK, twoFactorRequiredResponse{TwoFactorRequired: true, Challenge: challenge})
			return
		}
	}
	sess, err := h.svc.StartSessionForUser(r.Context(), uid, ua, ip)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	h.issueSession(w, sess)
}

// CompleteLogin issues a first-party session for an already-authenticated user
// and writes the standard login response (access token + refresh/CSRF cookies),
// exactly as a password login would. It is called by the twofa verify handler
// after a challenge is satisfied (satisfying twofa.LoginCompleter), so a 2FA
// login and a password login mint an identical session.
func (h *Handler) CompleteLogin(w http.ResponseWriter, r *http.Request, userID uuid.UUID) {
	ua, ip := h.clientMeta(r)
	sess, err := h.svc.StartSessionForUser(r.Context(), userID, ua, ip)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not issue session")
		return
	}
	h.issueSession(w, sess)
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(refreshCookieName)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "no refresh token")
		return
	}
	ua, ip := h.clientMeta(r)
	sess, err := h.svc.Refresh(r.Context(), c.Value, ua, ip)
	if err != nil {
		h.clearCookies(w)
		httpx.Error(w, http.StatusUnauthorized, "refresh failed")
		return
	}
	h.issueSession(w, sess)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(refreshCookieName); err == nil {
		revoked, err := h.svc.Logout(r.Context(), c.Value)
		if err != nil {
			// Logout stays best-effort (the response is still 200 and cookies
			// are cleared below), but a genuine revoke failure must not be
			// swallowed silently — surface it so a session that failed to revoke
			// server-side is at least observable.
			slog.Error("identity: logout revoke failed", "err", err)
		}
		// Bust the verifier's cached auth-state for every revoked session so a
		// just-logged-out access token is rejected on its next request rather
		// than after the cache TTL (matches revoke-session/revoke-others).
		for _, sid := range revoked {
			h.bustSession(sid)
		}
	}
	h.clearCookies(w)
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.UserFromContext(r.Context())
	uid, err := uuid.Parse(claims.UserID)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "invalid token")
		return
	}
	mems, err := h.svc.Memberships(r.Context(), uid)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not load memberships")
		return
	}
	verified, err := h.svc.IsEmailVerified(r.Context(), uid)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not load verification status")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"user_id": claims.UserID, "active_workspace_id": claims.WorkspaceID,
		"role": claims.Role, "memberships": toMembershipDTOs(mems), "email_verified": verified,
	})
}

func (h *Handler) logoutAll(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.UserFromContext(r.Context())
	uid, err := uuid.Parse(claims.UserID)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "invalid token")
		return
	}
	revoked, err := h.svc.LogoutAll(r.Context(), uid)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not revoke sessions")
		return
	}
	// Bust every revoked session's cached auth-state so a logout-everywhere kills
	// each still-live access token promptly in-process, not after the cache TTL.
	for _, sid := range revoked {
		h.bustSession(sid)
	}
	h.clearCookies(w)
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// switchWorkspace repoints the caller's *current* session at a different
// workspace they belong to. The session being repointed is always the one
// tied to the authenticated access token (claims.SessionID) - it is never
// taken from the request body - so a caller can only ever redirect their own
// session, never someone else's (no session-repointing IDOR).
func (h *Handler) switchWorkspace(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.UserFromContext(r.Context())
	var body struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	target, err := uuid.Parse(body.WorkspaceID)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid workspace id")
		return
	}
	uid, err := uuid.Parse(claims.UserID)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "invalid token")
		return
	}
	sid, err := uuid.Parse(claims.SessionID) // session id comes ONLY from the JWT, never the request body
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "invalid token")
		return
	}
	activeWS, role, tokenVersion, err := h.svc.SwitchWorkspace(r.Context(), sid, uid, target)
	if err != nil {
		httpx.Error(w, http.StatusForbidden, "not a member of that workspace")
		return
	}
	// Same-session re-issue: the session row stays live (only its workspace/role
	// changed), so the new token's `tv` MUST come from that row's current
	// token_version — never a hardcoded 0, which a later tv-bump would turn into
	// an instant self-inflicted 401. See mintAccessToken's rule.
	access, err := h.mintAccessToken(claims.UserID, activeWS.String(), role, claims.SessionID, tokenVersion)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not issue token")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"access_token": access, "expires_in": int(h.accessTTL.Seconds()),
		"active_workspace_id": activeWS.String(), "role": role,
	})
}

// verifyEmail consumes an email_verify token and marks the owning user's
// email verified. Public: the token itself is the credential, so no bearer
// auth is required (a user isn't logged in yet on some flows, e.g. clicking
// the link from a fresh signup on another device).
func (h *Handler) verifyEmail(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token" validate:"required"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := validate.Struct(body); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.svc.VerifyEmail(r.Context(), body.Token); err != nil {
		if errors.Is(err, ErrTokenInvalid) {
			httpx.Error(w, http.StatusBadRequest, "invalid or expired token")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "could not verify email")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// forgotPassword issues a password_reset token and emails a reset link, but
// always answers 204 - whether the email belongs to a real account, is
// rate-limited, or genuinely gets a reset link sent are all indistinguishable
// to the caller. Public: this is exactly the "I forgot my password" entry
// point, so there's no session to require.
func (h *Handler) forgotPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email" validate:"required,email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := validate.Struct(body); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = h.svc.ForgotPassword(r.Context(), body.Email)
	w.WriteHeader(http.StatusNoContent)
}

// resetPassword consumes a password_reset token and sets a new password,
// revoking every existing session for the owning user. Public: like
// verifyEmail, the token itself is the credential.
func (h *Handler) resetPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token       string `json:"token" validate:"required"`
		NewPassword string `json:"new_password" validate:"required,min=8"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	if err := validate.Struct(body); err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	revoked, err := h.svc.ResetPassword(r.Context(), body.Token, body.NewPassword)
	if err != nil {
		if errors.Is(err, ErrTokenInvalid) {
			httpx.Error(w, http.StatusBadRequest, "invalid or expired token")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "could not reset password")
		return
	}
	// Bust every revoked session's cached auth-state so a password reset kills
	// each pre-reset access token promptly in-process (the reset already bumped
	// token_version for cross-replica correctness within the cache TTL).
	for _, sid := range revoked {
		h.bustSession(sid)
	}
	w.WriteHeader(http.StatusNoContent)
}

// resendVerification re-sends the verification email for the authenticated
// caller, rate-limited to at most one every 60 seconds.
func (h *Handler) resendVerification(w http.ResponseWriter, r *http.Request) {
	claims, _ := auth.UserFromContext(r.Context())
	uid, err := uuid.Parse(claims.UserID)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "invalid token")
		return
	}
	if err := h.svc.ResendVerification(r.Context(), uid); err != nil {
		if errors.Is(err, ErrRateLimited) {
			httpx.Error(w, http.StatusTooManyRequests, "too many requests, try again shortly")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "could not resend verification email")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// sessionInfoDTO is one active session in the management list. It never
// carries the token hash (the query doesn't select it); `current` flags the
// session tied to the caller's own access token.
type sessionInfoDTO struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	UserAgent   *string `json:"user_agent"`
	IP          *string `json:"ip"`
	CreatedAt   string  `json:"created_at"`
	ExpiresAt   string  `json:"expires_at"`
	Current     bool    `json:"current"`
}

type sessionsListResponse struct {
	Sessions []sessionInfoDTO `json:"sessions"`
}

// listSessions returns the caller's live sessions, flagging the current one.
// Scoped to the authenticated user id from the JWT (sessions are user-owned,
// not workspace tenant data), never a request param.
func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.UserFromContext(r.Context())
	uid, err := uuid.Parse(p.UserID)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "invalid token")
		return
	}
	rows, err := h.svc.ListSessions(r.Context(), uid)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not list sessions")
		return
	}
	out := make([]sessionInfoDTO, len(rows))
	for i, s := range rows {
		out[i] = sessionInfoDTO{
			ID:          s.ID.String(),
			WorkspaceID: s.WorkspaceID.String(),
			UserAgent:   s.UserAgent,
			IP:          ipToString(s.Ip),
			CreatedAt:   pgxTime(s.CreatedAt).UTC().Format(time.RFC3339),
			ExpiresAt:   pgxTime(s.ExpiresAt).UTC().Format(time.RFC3339),
			Current:     s.ID.String() == p.SessionID,
		}
	}
	httpx.JSON(w, http.StatusOK, sessionsListResponse{Sessions: out})
}

// revokeSession revokes one of the caller's OWN sessions (pinned to the JWT
// user id in SQL). The session id comes from the path; a foreign or unknown id
// matches nothing and returns 404 rather than revealing another user's session.
func (h *Handler) revokeSession(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.UserFromContext(r.Context())
	uid, err := uuid.Parse(p.UserID)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "invalid token")
		return
	}
	sid, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid session id")
		return
	}
	if err := h.svc.RevokeSession(r.Context(), uid, sid); err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			httpx.Error(w, http.StatusNotFound, "session not found")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "could not revoke session")
		return
	}
	h.bustSession(sid)
	w.WriteHeader(http.StatusNoContent)
}

// revokeOtherSessions revokes every session EXCEPT the caller's current one
// (the session named in the JWT), keeping the current device logged in. The
// kept session id comes only from the verified token, never the request body.
func (h *Handler) revokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.UserFromContext(r.Context())
	uid, err := uuid.Parse(p.UserID)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "invalid token")
		return
	}
	keep, err := uuid.Parse(p.SessionID)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "invalid token")
		return
	}
	revoked, err := h.svc.RevokeOtherSessions(r.Context(), uid, keep)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not revoke sessions")
		return
	}
	for _, sid := range revoked {
		h.bustSession(sid)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"revoked": len(revoked)})
}

// ipToString renders an optional stored IP as a nullable JSON string.
func ipToString(ip *netip.Addr) *string {
	if ip == nil {
		return nil
	}
	s := ip.String()
	return &s
}

// isUniqueViolation reports whether err represents a Postgres unique-key
// violation (SQLSTATE 23505), e.g. a duplicate email on registration.
// Typed pgconn.PgError only — the substring fallback would fire on any
// error whose message happened to contain "23505", including a message a
// caller partially controls.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}
