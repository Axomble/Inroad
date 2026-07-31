package captcha

import (
	"net/http"

	"github.com/inroad/inroad/internal/platform/httpx"
)

// TokenHeader is the request header carrying the client-solved captcha token.
const TokenHeader = "X-Captcha-Token"

// Middleware guards a route with the captcha verifier: it reads the client token
// from the X-Captcha-Token header, resolves the client IP (best-effort, passed to
// providers that accept it), and rejects with 403 unless Verify passes. Because
// Verify is fail-closed, a missing token, an invalid token, or a provider outage
// all reject here. With the no-op verifier (unconfigured deployment) every request
// passes through untouched.
func Middleware(v Verifier, resolver httpx.ClientIPResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := r.Header.Get(TokenHeader)
			var ip string
			if addr := resolver.ClientIP(r); addr.IsValid() {
				ip = addr.String()
			}
			if err := v.Verify(r.Context(), token, ip); err != nil {
				httpx.Error(w, http.StatusForbidden, "captcha verification failed")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
