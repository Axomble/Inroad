package oauthprovider

import (
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// ClientView is the non-secret projection of a registered client returned by
// RegisterClient (as metadata) and ListClients. It carries NO client_secret_hash by
// construction — neither source type exposes one to it.
type ClientView struct {
	ClientID                string
	ClientName              string
	RedirectURIs            []string
	GrantTypes              []string
	ResponseTypes           []string
	Scopes                  []string
	ClientType              string
	TokenEndpointAuthMethod string
	CreatedAt               time.Time
	RevokedAt               *time.Time
}

// viewFromClient projects a freshly-created client row (RETURNING *). It reads only
// non-secret columns; client_secret_hash is deliberately ignored.
func viewFromClient(c gen.OauthClient) ClientView {
	return ClientView{
		ClientID:                c.ClientID,
		ClientName:              c.ClientName,
		RedirectURIs:            c.RedirectUris,
		GrantTypes:              c.GrantTypes,
		ResponseTypes:           c.ResponseTypes,
		Scopes:                  c.Scopes,
		ClientType:              c.ClientType,
		TokenEndpointAuthMethod: c.TokenEndpointAuthMethod,
		CreatedAt:               timeOrZero(c.CreatedAt),
		RevokedAt:               nullableTime(c.RevokedAt),
	}
}

// viewFromClientRow projects a management-list row (already omits the secret hash in
// SQL).
func viewFromClientRow(r gen.ListOauthClientsByWorkspaceRow) ClientView {
	return ClientView{
		ClientID:                r.ClientID,
		ClientName:              r.ClientName,
		RedirectURIs:            r.RedirectUris,
		GrantTypes:              r.GrantTypes,
		ResponseTypes:           r.ResponseTypes,
		Scopes:                  r.Scopes,
		ClientType:              r.ClientType,
		TokenEndpointAuthMethod: r.TokenEndpointAuthMethod,
		CreatedAt:               timeOrZero(r.CreatedAt),
		RevokedAt:               nullableTime(r.RevokedAt),
	}
}

// nullableTime maps a nullable timestamptz to a *time.Time (NULL -> nil).
func nullableTime(ts pgtype.Timestamptz) *time.Time {
	if !ts.Valid {
		return nil
	}
	t := ts.Time
	return &t
}
