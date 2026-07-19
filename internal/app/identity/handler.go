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
)

// Handler exposes the identity domain (register/login/refresh/logout,
// session introspection, and workspace switching) over HTTP.
type Handler struct {
	svc          *Service
	jwtSecret    []byte
	accessTTL    time.Duration
	refreshTTL   time.Duration
	cookieSecure bool
	cookieDomain string
}

// NewHandler constructs a Handler backed by svc. accessTTL/refreshTTL size
// the access token and refresh cookie lifetimes; cookieSecure/cookieDomain
// control the cookie attributes (Secure should be true outside local dev).
func NewHandler(svc *Service, jwtSecret []byte, accessTTL, refreshTTL time.Duration, cookieSecure bool, cookieDomain string) *Handler {
	return &Handler{svc: svc, jwtSecret: jwtSecret, accessTTL: accessTTL, refreshTTL: refreshTTL, cookieSecure: cookieSecure, cookieDomain: cookieDomain}
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
func clientMeta(r *http.Request) (ua, ip string) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.UserAgent(), r.RemoteAddr
	}
	return r.UserAgent(), host
}

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
	ua, ip := clientMeta(r)
	sess, err := h.svc.Register(r.Context(), RegisterInput{WorkspaceName: body.WorkspaceName, Email: body.Email, Password: body.Password, UserAgent: ua, IP: ip})
	if err != nil {
		if isUniqueViolation(err) {
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
	ua, ip := clientMeta(r)
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
	ua, ip := clientMeta(r)
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
	httpx.JSON(w, http.StatusOK, map[string]any{
		"user_id": claims.UserID, "active_workspace_id": claims.WorkspaceID,
		"role": claims.Role, "memberships": toMembershipDTOs(mems),
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

// isUniqueViolation reports whether err represents a Postgres unique-key
// violation (SQLSTATE 23505), e.g. a duplicate email on registration. It
// checks the typed pgconn.PgError first, falling back to a substring match
// so tests can simulate the condition without a real database error.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return strings.Contains(err.Error(), "23505")
}
