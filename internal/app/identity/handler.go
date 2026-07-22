package identity

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
	"github.com/inroad/inroad/internal/platform/validate"
)

// Handler exposes the identity domain (register/login/refresh/logout,
// session introspection, and workspace switching) over HTTP.
type Handler struct {
	svc            *Service
	jwtSecret      []byte
	accessTTL      time.Duration
	refreshTTL     time.Duration
	cookieSecure   bool
	cookieDomain   string
	trustedProxies []*net.IPNet
}

// NewHandler constructs a Handler backed by svc. accessTTL/refreshTTL size
// the access token and refresh cookie lifetimes; cookieSecure/cookieDomain
// control the cookie attributes (Secure should be true outside local dev).
// trustedProxies is the CIDR list whose X-Forwarded-For / X-Real-IP the
// handler will honor; unparsable entries are silently dropped (loudness
// belongs in cmd startup, not per-request).
func NewHandler(svc *Service, jwtSecret []byte, accessTTL, refreshTTL time.Duration, cookieSecure bool, cookieDomain string, trustedProxies []string) *Handler {
	nets := make([]*net.IPNet, 0, len(trustedProxies))
	for _, c := range trustedProxies {
		if _, n, err := net.ParseCIDR(c); err == nil {
			nets = append(nets, n)
		}
	}
	return &Handler{
		svc: svc, jwtSecret: jwtSecret, accessTTL: accessTTL, refreshTTL: refreshTTL,
		cookieSecure: cookieSecure, cookieDomain: cookieDomain, trustedProxies: nets,
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

// issueSession mints an access token for sess, sets the refresh + CSRF
// cookies, and writes the session JSON body. Shared by register/login/refresh.
func (h *Handler) issueSession(w http.ResponseWriter, sess Session) {
	access, err := auth.IssueToken(h.jwtSecret, auth.Claims{
		UserID: sess.UserID.String(), WorkspaceID: sess.WorkspaceID.String(),
		Role: sess.Role, SessionID: sess.SessionID.String(),
	}, h.accessTTL)
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
		WorkspaceName string `json:"workspace_name"`
		Email         string `json:"email"`
		Password      string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.WorkspaceName == "" || body.Email == "" || len(body.Password) < 8 {
		httpx.Error(w, http.StatusBadRequest, "workspace_name, email, and 8+ char password required")
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
	sess, err := h.svc.Login(r.Context(), body.Email, body.Password, ua, ip)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "invalid credentials")
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
		_ = h.svc.Logout(r.Context(), c.Value)
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
	if err := h.svc.LogoutAll(r.Context(), uid); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not revoke sessions")
		return
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
	activeWS, role, err := h.svc.SwitchWorkspace(r.Context(), sid, uid, target)
	if err != nil {
		httpx.Error(w, http.StatusForbidden, "not a member of that workspace")
		return
	}
	access, err := auth.IssueToken(h.jwtSecret, auth.Claims{
		UserID: claims.UserID, WorkspaceID: activeWS.String(), Role: role, SessionID: claims.SessionID,
	}, h.accessTTL)
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
	if err := h.svc.ResetPassword(r.Context(), body.Token, body.NewPassword); err != nil {
		if errors.Is(err, ErrTokenInvalid) {
			httpx.Error(w, http.StatusBadRequest, "invalid or expired token")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "could not reset password")
		return
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
