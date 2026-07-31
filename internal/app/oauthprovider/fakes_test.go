package oauthprovider

import (
	"context"
	"encoding/hex"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// fakeStore is an in-memory Store for unit tests: no DB, no network. It mirrors the
// PgStore's tenancy + single-use semantics faithfully enough to assert behavior. Its
// clock is shared with the service under test so TTL/consume checks line up.
type fakeStore struct {
	clients  map[string]gen.OauthClient               // by client_id
	requests map[string]gen.OauthAuthorizationRequest // by consent_id
	consents map[string]gen.OauthConsent              // by userID|clientID
	codes    map[string]gen.OauthAuthorizationCode    // by hex(code_hash)
	access   map[string]gen.OauthAccessToken          // by hex(token_hash)
	refresh  map[string]gen.OauthRefreshToken         // by hex(token_hash)
	now      func() time.Time

	createClientErr error // injectable to exercise the collision-retry path
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		clients:  map[string]gen.OauthClient{},
		requests: map[string]gen.OauthAuthorizationRequest{},
		consents: map[string]gen.OauthConsent{},
		codes:    map[string]gen.OauthAuthorizationCode{},
		access:   map[string]gen.OauthAccessToken{},
		refresh:  map[string]gen.OauthRefreshToken{},
		now:      time.Now,
	}
}

// consentKey mirrors the PgStore's (user_id, client_id, workspace_id) uniqueness so the
// fake's consent skip is workspace-scoped exactly like the real store.
func consentKey(userID uuid.UUID, clientID string, ws uuid.UUID) string {
	return userID.String() + "|" + clientID + "|" + ws.String()
}

func (f *fakeStore) CreateClient(_ context.Context, p CreateClientParams) (gen.OauthClient, error) {
	if f.createClientErr != nil {
		err := f.createClientErr
		f.createClientErr = nil // one-shot: the retry then succeeds
		return gen.OauthClient{}, err
	}
	c := gen.OauthClient{
		ID:                      uuid.New(),
		ClientID:                p.ClientID,
		ClientSecretHash:        p.ClientSecretHash,
		ClientName:              p.ClientName,
		RedirectUris:            p.RedirectURIs,
		GrantTypes:              p.GrantTypes,
		ResponseTypes:           p.ResponseTypes,
		Scopes:                  p.Scopes,
		ClientType:              p.ClientType,
		TokenEndpointAuthMethod: p.TokenEndpointAuthMethod,
		CreatedByUserID:         pgUUIDPtr(p.CreatedBy),
		WorkspaceID:             pgUUIDPtr(p.WorkspaceID),
		CreatedAt:               pgTime(f.now()),
	}
	f.clients[p.ClientID] = c
	return c, nil
}

func (f *fakeStore) GetClient(_ context.Context, clientID string) (gen.OauthClient, error) {
	c, ok := f.clients[clientID]
	if !ok {
		return gen.OauthClient{}, pgx.ErrNoRows
	}
	return c, nil
}

func (f *fakeStore) ListClientsByWorkspace(_ context.Context, ws uuid.UUID) ([]gen.ListOauthClientsByWorkspaceRow, error) {
	var out []gen.ListOauthClientsByWorkspaceRow
	for _, c := range f.clients {
		if c.WorkspaceID.Valid && uuid.UUID(c.WorkspaceID.Bytes) == ws {
			out = append(out, gen.ListOauthClientsByWorkspaceRow{
				ID: c.ID, ClientID: c.ClientID, ClientName: c.ClientName,
				RedirectUris: c.RedirectUris, GrantTypes: c.GrantTypes,
				ResponseTypes: c.ResponseTypes, Scopes: c.Scopes, ClientType: c.ClientType,
				TokenEndpointAuthMethod: c.TokenEndpointAuthMethod,
				CreatedByUserID:         c.CreatedByUserID, WorkspaceID: c.WorkspaceID,
				CreatedAt: c.CreatedAt, RevokedAt: c.RevokedAt,
			})
		}
	}
	return out, nil
}

func (f *fakeStore) RevokeClient(_ context.Context, ws uuid.UUID, clientID string) (int64, error) {
	c, ok := f.clients[clientID]
	if !ok || !c.WorkspaceID.Valid || uuid.UUID(c.WorkspaceID.Bytes) != ws {
		return 0, nil
	}
	if !c.RevokedAt.Valid {
		c.RevokedAt = pgTime(f.now())
		f.clients[clientID] = c
	}
	return 1, nil
}

func (f *fakeStore) CreateAuthRequest(_ context.Context, p CreateAuthRequestParams) error {
	f.requests[p.ConsentID] = gen.OauthAuthorizationRequest{
		ID: uuid.New(), ConsentID: p.ConsentID, ClientID: p.ClientID,
		RedirectUri: p.RedirectURI, Scopes: p.Scopes, State: p.State,
		CodeChallenge: p.CodeChallenge, CodeChallengeMethod: p.CodeChallengeMethod,
		UserID: p.UserID, WorkspaceID: p.WorkspaceID, ExpiresAt: pgTime(p.ExpiresAt),
		CreatedAt: pgTime(f.now()),
	}
	return nil
}

func (f *fakeStore) GetAuthRequest(_ context.Context, consentID string) (gen.OauthAuthorizationRequest, error) {
	req, ok := f.requests[consentID]
	if !ok {
		return gen.OauthAuthorizationRequest{}, pgx.ErrNoRows
	}
	return req, nil
}

func (f *fakeStore) ConsumeAuthRequest(_ context.Context, consentID string, userID uuid.UUID) (int64, error) {
	req, ok := f.requests[consentID]
	if !ok || req.ConsumedAt.Valid || req.UserID != userID || !f.now().Before(req.ExpiresAt.Time) {
		return 0, nil
	}
	req.ConsumedAt = pgTime(f.now())
	f.requests[consentID] = req
	return 1, nil
}

func (f *fakeStore) GetConsent(_ context.Context, userID uuid.UUID, clientID string, ws uuid.UUID) (gen.OauthConsent, error) {
	c, ok := f.consents[consentKey(userID, clientID, ws)]
	if !ok {
		return gen.OauthConsent{}, pgx.ErrNoRows
	}
	return c, nil
}

func (f *fakeStore) CreateAuthCode(_ context.Context, p CreateAuthCodeParams) error {
	f.codes[hex.EncodeToString(p.CodeHash)] = gen.OauthAuthorizationCode{
		ID: uuid.New(), CodeHash: p.CodeHash, ClientID: p.ClientID,
		RedirectUri: p.RedirectURI, CodeChallenge: p.CodeChallenge,
		CodeChallengeMethod: p.CodeChallengeMethod, Scopes: p.Scopes,
		UserID: p.UserID, WorkspaceID: p.WorkspaceID, ExpiresAt: pgTime(p.ExpiresAt),
		CreatedAt: pgTime(f.now()),
	}
	return nil
}

func (f *fakeStore) Approve(ctx context.Context, p ApproveParams) error {
	n, err := f.ConsumeAuthRequest(ctx, p.ConsentID, p.UserID)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrRequestNotClaimable
	}
	f.consents[consentKey(p.UserID, p.ClientID, p.WorkspaceID)] = gen.OauthConsent{
		ID: uuid.New(), UserID: p.UserID, ClientID: p.ClientID,
		Scopes: p.Scopes, WorkspaceID: p.WorkspaceID,
		CreatedAt: pgTime(f.now()), UpdatedAt: pgTime(f.now()),
	}
	return f.CreateAuthCode(ctx, p.Code)
}

// --- P6b token-endpoint fakes ------------------------------------------------

// ConsumeAuthCode mirrors the atomic single-use consume: it claims the code only if
// unconsumed and unexpired, stamping consumed_at, else returns pgx.ErrNoRows.
func (f *fakeStore) ConsumeAuthCode(_ context.Context, codeHash []byte) (gen.OauthAuthorizationCode, error) {
	key := hex.EncodeToString(codeHash)
	c, ok := f.codes[key]
	if !ok || c.ConsumedAt.Valid || !f.now().Before(c.ExpiresAt.Time) {
		return gen.OauthAuthorizationCode{}, pgx.ErrNoRows
	}
	c.ConsumedAt = pgTime(f.now())
	f.codes[key] = c
	return c, nil
}

func (f *fakeStore) IssueTokenPair(_ context.Context, access CreateAccessTokenParams, refresh CreateRefreshTokenParams) error {
	f.access[hex.EncodeToString(access.TokenHash)] = gen.OauthAccessToken{
		ID: uuid.New(), TokenHash: access.TokenHash, ClientID: access.ClientID,
		UserID: access.UserID, WorkspaceID: access.WorkspaceID, Scopes: access.Scopes,
		ExpiresAt: pgTime(access.ExpiresAt), CreatedAt: pgTime(f.now()),
	}
	f.refresh[hex.EncodeToString(refresh.TokenHash)] = gen.OauthRefreshToken{
		ID: uuid.New(), TokenHash: refresh.TokenHash, FamilyID: refresh.FamilyID,
		ClientID: refresh.ClientID, UserID: refresh.UserID, WorkspaceID: refresh.WorkspaceID,
		Scopes: refresh.Scopes, ExpiresAt: pgTime(refresh.ExpiresAt), CreatedAt: pgTime(f.now()),
	}
	return nil
}

func (f *fakeStore) GetAccessToken(_ context.Context, tokenHash []byte) (gen.OauthAccessToken, error) {
	t, ok := f.access[hex.EncodeToString(tokenHash)]
	if !ok {
		return gen.OauthAccessToken{}, pgx.ErrNoRows
	}
	return t, nil
}

func (f *fakeStore) GetRefreshToken(_ context.Context, tokenHash []byte) (gen.OauthRefreshToken, error) {
	t, ok := f.refresh[hex.EncodeToString(tokenHash)]
	if !ok {
		return gen.OauthRefreshToken{}, pgx.ErrNoRows
	}
	return t, nil
}

func (f *fakeStore) ConsumeRefreshToken(_ context.Context, tokenHash []byte) (int64, error) {
	key := hex.EncodeToString(tokenHash)
	t, ok := f.refresh[key]
	if !ok || t.ConsumedAt.Valid || t.RevokedAt.Valid || !f.now().Before(t.ExpiresAt.Time) {
		return 0, nil
	}
	t.ConsumedAt = pgTime(f.now())
	f.refresh[key] = t
	return 1, nil
}

func (f *fakeStore) RevokeRefreshFamily(_ context.Context, familyID uuid.UUID) (int64, error) {
	var n int64
	for k, t := range f.refresh {
		if t.FamilyID == familyID && !t.RevokedAt.Valid {
			t.RevokedAt = pgTime(f.now())
			f.refresh[k] = t
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) RevokeAccessToken(_ context.Context, tokenHash []byte, clientID string) (int64, error) {
	key := hex.EncodeToString(tokenHash)
	t, ok := f.access[key]
	if !ok || t.ClientID != clientID {
		return 0, nil
	}
	if !t.RevokedAt.Valid {
		t.RevokedAt = pgTime(f.now())
		f.access[key] = t
	}
	return 1, nil
}

// codeByHash looks a stored code up by its raw code (test helper).
func (f *fakeStore) codeByRaw(rawCode string) (gen.OauthAuthorizationCode, bool) {
	c, ok := f.codes[hex.EncodeToString(hashSecret(rawCode))]
	return c, ok
}

// seedConsent records a prior consent (test helper for the skip path).
func (f *fakeStore) seedConsent(userID uuid.UUID, clientID string, scopes []string, ws uuid.UUID) {
	f.consents[consentKey(userID, clientID, ws)] = gen.OauthConsent{
		ID: uuid.New(), UserID: userID, ClientID: clientID, Scopes: scopes, WorkspaceID: ws,
		CreatedAt: pgTime(f.now()), UpdatedAt: pgTime(f.now()),
	}
}

var _ Store = (*fakeStore)(nil)
