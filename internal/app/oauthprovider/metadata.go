package oauthprovider

import (
	"net/http"
	"strings"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
)

// AuthorizationServerMetadata serves RFC 8414 discovery for MCP and other
// OAuth clients. It contains endpoint locations only; no workspace or client
// data is disclosed.
func AuthorizationServerMetadata(publicURL string) http.Handler {
	base := strings.TrimRight(publicURL, "/") + "/oauth2"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			httpx.Error(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{
			"issuer":                                base,
			"authorization_endpoint":                base + "/authorize",
			"token_endpoint":                        base + "/token",
			"registration_endpoint":                 base + "/register",
			"revocation_endpoint":                   base + "/revoke",
			"introspection_endpoint":                base + "/introspect",
			"response_types_supported":              []string{"code"},
			"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
			"code_challenge_methods_supported":      []string{"S256"},
			"token_endpoint_auth_methods_supported": []string{"none", "client_secret_post", "client_secret_basic"},
			"scopes_supported":                      auth.OAuthGrantableScopes,
		})
	})
}
