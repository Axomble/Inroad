package auth

import "errors"

// ErrTwoFactorRateLimited is the shared sentinel a TwoFactorGate returns when
// login-gate challenge issuance is throttled for the caller's IP. It lives in auth
// — the package both the identity and twofa domains already depend on — so the
// identity login handler can map it to HTTP 429 without importing the twofa domain
// (the app-packages-don't-import-each-other invariant holds).
var ErrTwoFactorRateLimited = errors.New("two-factor challenge rate limited")

// ErrRateLimited is the sentinel a Verifier returns when a per-credential request
// cap has been exceeded (e.g. an API key's rate_limit_per_min). RequireAuth maps
// it to HTTP 429 — distinct from ErrUnauthorized (401) and a transient store/infra
// failure (500) — so an over-limit caller is told to back off rather than seeing a
// misleading "bad credentials" or "server error".
var ErrRateLimited = errors.New("rate limited")
