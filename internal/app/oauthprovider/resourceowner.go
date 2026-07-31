package oauthprovider

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

// Owner is the authenticated resource owner (the end-user granting consent) plus the
// workspace their session is active in. It is resolved ONLY from the P1 session,
// never from a request parameter.
type Owner struct {
	UserID      uuid.UUID
	WorkspaceID uuid.UUID
}

// ResourceOwner resolves the current resource owner from the P1 session carried on a
// request. This is the NARROW seam that lets the authorization server learn who is
// logged in WITHOUT re-implementing (or importing) login: oauthprovider defines the
// interface; the composition root backs it with the identity session verifier.
//
// Resolve returns:
//   - (owner, true, nil)  — a valid session; owner is the logged-in user.
//   - (_, false, nil)     — no/invalid session (the caller redirects to login).
//   - (_, false, err)     — an infrastructure failure (the caller renders a 500).
//
// A definitive "not authenticated" MUST be reported as (false, nil), never an error,
// so the authorize endpoint can send the user to log in rather than failing hard.
type ResourceOwner interface {
	Resolve(ctx context.Context, r *http.Request) (Owner, bool, error)
}
