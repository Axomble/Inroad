package httpx

import (
	"net/http"
	"strings"
)

const (
	maxJSONBody      = 1 << 20  // 1 MiB
	maxMultipartBody = 11 << 20 // 10 MiB upload plus multipart framing
)

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'; img-src 'self' data:; font-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self' https:")
		h.Set("Cross-Origin-Opener-Policy", "same-origin-allow-popups")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		// /oauth/ is included alongside /oauth2/: the mailbox-connect callback
		// carries an authorization code in its query string, which must never be
		// held by an intermediary cache.
		for _, prefix := range []string{"/api/", "/oauth/", "/oauth2/"} {
			if strings.HasPrefix(r.URL.Path, prefix) {
				h.Set("Cache-Control", "no-store")
				break
			}
		}
		next.ServeHTTP(w, r)
	})
}

func limitRequestBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Body != http.NoBody {
			limit := int64(maxJSONBody)
			if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
				limit = maxMultipartBody
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}
