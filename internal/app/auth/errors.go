package auth

import "errors"

// ErrTwoFactorRateLimited is the shared sentinel a TwoFactorGate returns when
// login-gate challenge issuance is throttled for the caller's IP. It lives in auth
// — the package both the identity and twofa domains already depend on — so the
// identity login handler can map it to HTTP 429 without importing the twofa domain
// (the app-packages-don't-import-each-other invariant holds).
var ErrTwoFactorRateLimited = errors.New("two-factor challenge rate limited")
