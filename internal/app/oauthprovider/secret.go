package oauthprovider

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// The provider mints four kinds of opaque random tokens. All are URL-safe base64
// (no padding) of a high-entropy random draw. Only DIGESTS of the two secret ones
// (client secret, authorization code) are ever persisted; the identifiers
// (client_id, consent_id) are stored verbatim because they are not secrets.
const (
	// clientIDBytes -> 128-bit public client identifier.
	clientIDBytes = 16
	// clientSecretBytes -> 256-bit client secret (confidential clients only).
	clientSecretBytes = 32
	// authCodeBytes -> 256-bit authorization code (single-use, short-TTL).
	authCodeBytes = 32
	// consentIDBytes -> 256-bit opaque consent-handoff id carried in the SPA URL.
	consentIDBytes = 32
	// accessTokenBytes / refreshTokenBytes -> 256-bit opaque bearer + refresh secrets
	// (P6b). Both are hashed at rest; the raw value is returned to the client once.
	accessTokenBytes  = 32
	refreshTokenBytes = 32
)

const (
	// accessTokenPrefix marks an issued OAuth access token so the oauthVerifier can
	// ENGAGE (vs DEFER) cleanly, distinct from a session JWT and the api-key `inrd_`
	// prefix. A leaked value is recognizable/greppable.
	accessTokenPrefix = "inoa_"
	// refreshTokenPrefix marks a refresh token (presented only at /oauth2/token).
	refreshTokenPrefix = "inor_"
)

// b64 is URL-safe base64 without padding — safe to embed in a URL query and free of
// characters needing escaping.
var b64 = base64.RawURLEncoding

// randToken returns n cryptographically-random bytes encoded as URL-safe base64.
func randToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return b64.EncodeToString(b), nil
}

// newClientID mints a fresh public client identifier, prefixed so a leaked value is
// recognizable/greppable.
func newClientID() (string, error) {
	s, err := randToken(clientIDBytes)
	if err != nil {
		return "", err
	}
	return "inrc_" + s, nil
}

// newClientSecret mints a confidential client's secret and returns both the raw
// secret (shown once) and the SHA-256 digest that is persisted. The raw secret never
// leaves the caller except in the one-time registration response.
func newClientSecret() (secret string, hash []byte, err error) {
	secret, err = randToken(clientSecretBytes)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256([]byte(secret))
	return secret, sum[:], nil
}

// newAuthCode mints a single-use authorization code and returns both the raw code
// (delivered to the client via the redirect) and the SHA-256 digest that is stored.
func newAuthCode() (code string, hash []byte, err error) {
	code, err = randToken(authCodeBytes)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256([]byte(code))
	return code, sum[:], nil
}

// newConsentID mints the opaque consent-handoff id (not a secret; stored verbatim).
func newConsentID() (string, error) { return randToken(consentIDBytes) }

// newAccessToken mints an opaque bearer access token and returns both the raw token
// (returned to the client once) and the SHA-256 digest that is stored. The hash is over
// the FULL prefixed token, exactly what the verifier re-hashes on lookup.
func newAccessToken() (token string, hash []byte, err error) {
	s, err := randToken(accessTokenBytes)
	if err != nil {
		return "", nil, err
	}
	token = accessTokenPrefix + s
	return token, hashSecret(token), nil
}

// newRefreshToken mints an opaque rotating refresh token and returns both the raw token
// (returned once) and the SHA-256 digest that is stored.
func newRefreshToken() (token string, hash []byte, err error) {
	s, err := randToken(refreshTokenBytes)
	if err != nil {
		return "", nil, err
	}
	token = refreshTokenPrefix + s
	return token, hashSecret(token), nil
}

// hashSecret returns the SHA-256 of a presented raw secret/code, for a stored-digest
// comparison (used by tests here and by the P6b token endpoint).
func hashSecret(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}
