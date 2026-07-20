package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"

	"github.com/inroad/inroad/internal/platform/httpx"
)

const CSRFCookieName = "csrf_token"
const CSRFHeaderName = "X-CSRF-Token"

func NewCSRFToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// RequireCSRF enforces the double-submit pattern on cookie-authenticated endpoints.
func RequireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(CSRFCookieName)
		header := r.Header.Get(CSRFHeaderName)
		if err != nil || header == "" || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(header)) != 1 {
			httpx.Error(w, http.StatusForbidden, "csrf token mismatch")
			return
		}
		next.ServeHTTP(w, r)
	})
}
