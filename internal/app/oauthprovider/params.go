package oauthprovider

import "strings"

// OAuth 2.1 fixed vocabulary this provider supports.
const (
	// responseTypeCode is the ONLY response_type allowed: OAuth 2.1 removes the
	// implicit grant, so response_type=token (and anything else) is rejected.
	responseTypeCode = "code"
	// challengeMethodS256 is the ONLY code_challenge_method allowed: `plain` is
	// rejected (it offers no protection if the challenge leaks).
	challengeMethodS256 = "S256"

	clientTypePublic       = "public"
	clientTypeConfidential = "confidential"

	authMethodNone         = "none"
	authMethodClientSecret = "client_secret_basic"

	// PKCE code_challenge length bounds (RFC 7636 §4.1: the verifier is 43–128
	// chars; the S256 challenge is a base64url SHA-256, exactly 43 chars, but we
	// accept the full verifier range defensively since we only store it here).
	minChallengeLen = 43
	maxChallengeLen = 128
)

// isValidCodeChallenge reports whether c is a syntactically valid PKCE
// code_challenge: within the length bounds and drawn only from the unreserved
// URL-safe alphabet [A-Za-z0-9-._~] (RFC 7636). We validate syntax only here; the
// actual S256 verification happens at the P6b token exchange.
func isValidCodeChallenge(c string) bool {
	if len(c) < minChallengeLen || len(c) > maxChallengeLen {
		return false
	}
	for i := 0; i < len(c); i++ {
		if !isUnreserved(c[i]) {
			return false
		}
	}
	return true
}

func isUnreserved(b byte) bool {
	switch {
	case b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z', b >= '0' && b <= '9':
		return true
	case b == '-' || b == '.' || b == '_' || b == '~':
		return true
	default:
		return false
	}
}

// parseScope splits a space-delimited OAuth `scope` string into a de-duplicated,
// order-preserving slice, dropping empty fields (collapsing runs of whitespace).
func parseScope(s string) []string {
	fields := strings.Fields(s)
	seen := make(map[string]struct{}, len(fields))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if _, dup := seen[f]; dup {
			continue
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	return out
}

// isSubset reports whether every element of want is present in have.
func isSubset(want, have []string) bool {
	set := make(map[string]struct{}, len(have))
	for _, h := range have {
		set[h] = struct{}{}
	}
	for _, w := range want {
		if _, ok := set[w]; !ok {
			return false
		}
	}
	return true
}
