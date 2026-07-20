package identity

import (
	"net/http"
	"time"

	"github.com/inroad/inroad/internal/app/auth"
)

const refreshCookieName = "inroad_refresh"

// setRefreshCookie sets the httpOnly refresh-token cookie, scoped to the
// auth endpoints only (so it is never sent to the rest of the API surface).
// SameSite=Strict: the refresh cookie should never accompany a cross-site
// request, which is stricter than the CSRF cookie (Lax, because the CSRF
// header still gates state changes).
func (h *Handler) setRefreshCookie(w http.ResponseWriter, raw string) {
	http.SetCookie(w, &http.Cookie{
		Name: refreshCookieName, Value: raw, Path: "/api/v1/auth",
		Domain: h.cookieDomain, HttpOnly: true, Secure: h.cookieSecure,
		SameSite: http.SameSiteStrictMode, MaxAge: int(h.refreshTTL.Seconds()),
	})
}

// setCSRFCookie sets a readable (non-httpOnly) CSRF cookie the frontend must
// echo back via the X-CSRF-Token header on cookie-authenticated requests
// (double-submit pattern; see auth.RequireCSRF).
func (h *Handler) setCSRFCookie(w http.ResponseWriter) (string, error) {
	tok, err := auth.NewCSRFToken()
	if err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{
		Name: auth.CSRFCookieName, Value: tok, Path: "/",
		Domain: h.cookieDomain, HttpOnly: false, Secure: h.cookieSecure,
		SameSite: http.SameSiteLaxMode, MaxAge: int(h.refreshTTL.Seconds()),
	})
	return tok, nil
}

// clearCookies expires both the refresh and CSRF cookies (logout / failed refresh).
func (h *Handler) clearCookies(w http.ResponseWriter) {
	for _, c := range []struct{ name, path string }{{refreshCookieName, "/api/v1/auth"}, {auth.CSRFCookieName, "/"}} {
		http.SetCookie(w, &http.Cookie{Name: c.name, Value: "", Path: c.path, Domain: h.cookieDomain,
			HttpOnly: c.name == refreshCookieName, Secure: h.cookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(0, 0)})
	}
}
