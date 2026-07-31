package passkey

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

var (
	// ErrNotConfigured is returned when the WebAuthn Relying Party is not configured
	// (no RP id/origin). Every ceremony fails cleanly with this so the feature is
	// effectively off rather than mis-validating against a wrong domain.
	ErrNotConfigured = errors.New("passkeys are not configured")
	// ErrChallengeInvalid is returned when a ceremony's server-side challenge is
	// unknown, already consumed, expired, of the wrong kind, or (for registration)
	// bound to a different user — a dead ceremony.
	ErrChallengeInvalid = errors.New("passkey challenge invalid or expired")
	// ErrRegistration is returned when an attestation fails to parse or verify
	// against the stored challenge / RP.
	ErrRegistration = errors.New("passkey registration failed")
	// ErrAssertion is a FLAT failure for any login-assertion problem (unknown
	// credential, bad signature, RP/UV mismatch). It is intentionally
	// undifferentiated so a caller cannot learn whether a credential exists.
	ErrAssertion = errors.New("passkey authentication failed")
	// ErrCloneDetected is returned when the assertion's signature counter did not
	// increase — a signal the authenticator may be cloned. The login is rejected and
	// the stored counter is NOT advanced.
	ErrCloneDetected = errors.New("passkey signature counter regression (possible clone)")
	// ErrCredentialExists is returned when the presented authenticator is already
	// registered (duplicate credential id).
	ErrCredentialExists = errors.New("passkey already registered")
	// ErrNotFound is returned when deleting a credential that does not belong to the
	// caller or does not exist.
	ErrNotFound = errors.New("passkey not found")
)

const (
	kindRegister = "register"
	kindLogin    = "login"
	// challengeTTL bounds how long a begun ceremony may be finished. WebAuthn
	// ceremonies are interactive and complete in seconds; five minutes is generous
	// while keeping the single-use challenge short-lived.
	challengeTTL = 5 * time.Minute
)

// Service implements the passkey ceremonies over the go-webauthn library. web may
// be nil when the RP is unconfigured, in which case every ceremony returns
// ErrNotConfigured (List/Delete still work — they touch no RP config).
type Service struct {
	web   *webauthn.WebAuthn
	store Store
	// now is overridable in tests for deterministic challenge timing.
	now func() time.Time
}

// NewService builds a Service. web may be nil to disable the ceremonies cleanly.
func NewService(web *webauthn.WebAuthn, store Store) *Service {
	return &Service{web: web, store: store, now: time.Now}
}

// RegistrationOptions is the begin-registration payload: an opaque session id that
// binds the ceremony server-side plus the CredentialCreation options the browser
// passes to navigator.credentials.create.
type RegistrationOptions struct {
	SessionID string
	PublicKey *protocol.PublicKeyCredentialCreationOptions
}

// LoginOptions is the begin-login payload: an opaque session id plus the
// CredentialRequest options for navigator.credentials.get. For discoverable login
// the allowed-credentials list is empty (the authenticator offers resident keys).
type LoginOptions struct {
	SessionID string
	PublicKey *protocol.PublicKeyCredentialRequestOptions
}

// CredentialInfo is the minimal manage-surface projection of a stored credential.
// It carries NO signature-counter or attestation material — only what the settings
// UI needs to list and delete a passkey.
type CredentialInfo struct {
	ID         string
	Label      string
	Transports []string
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

// BeginRegistration starts adding a passkey to the authenticated user's account. It
// builds the creation options (excluding already-registered authenticators) and
// stores the resulting challenge server-side, bound to the user.
func (s *Service) BeginRegistration(ctx context.Context, userID uuid.UUID) (*RegistrationOptions, error) {
	if s.web == nil {
		return nil, ErrNotConfigured
	}
	email, err := s.store.GetUserEmail(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.ListCredentialsByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	user := &credentialUser{id: userID, name: email, credentials: toLibCredentials(rows)}

	exclusions := make([]protocol.CredentialDescriptor, len(user.credentials))
	for i, c := range user.credentials {
		exclusions[i] = c.Descriptor()
	}

	creation, sessionData, err := s.web.BeginRegistration(user, webauthn.WithExclusions(exclusions))
	if err != nil {
		return nil, err
	}
	sessionID, err := s.persistChallenge(ctx, &userID, sessionData, kindRegister)
	if err != nil {
		return nil, err
	}
	return &RegistrationOptions{SessionID: sessionID, PublicKey: &creation.Response}, nil
}

// FinishRegistration consumes the registration challenge, verifies the attestation,
// and stores the new credential. The challenge must be a live register challenge
// bound to this same user (belt-and-braces over the authed context). A duplicate
// authenticator returns ErrCredentialExists.
func (s *Service) FinishRegistration(ctx context.Context, userID uuid.UUID, sessionID string, credentialJSON []byte, label string) error {
	if s.web == nil {
		return ErrNotConfigured
	}
	ch, err := s.consume(ctx, sessionID, kindRegister)
	if err != nil {
		return err
	}
	// The register challenge is user-bound: reject one minted for another user even
	// if the token somehow reached this caller.
	if !ch.UserID.Valid || uuid.UUID(ch.UserID.Bytes) != userID {
		return ErrChallengeInvalid
	}

	var sessionData webauthn.SessionData
	if err := json.Unmarshal(ch.SessionData, &sessionData); err != nil {
		return err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(credentialJSON)
	if err != nil {
		return ErrRegistration
	}
	// WebAuthnID must equal the session's stored UserID; name is irrelevant here.
	user := &credentialUser{id: userID}
	cred, err := s.web.CreateCredential(user, sessionData, parsed)
	if err != nil {
		return ErrRegistration
	}

	_, err = s.store.CreateCredential(ctx, gen.CreateWebAuthnCredentialParams{
		UserID:          userID,
		CredentialID:    cred.ID,
		PublicKey:       cred.PublicKey,
		SignCount:       int64(cred.Authenticator.SignCount),
		Aaguid:          cred.Authenticator.AAGUID,
		Transports:      encodeTransports(cred.Transport),
		AttestationType: cred.AttestationType,
		BackupEligible:  cred.Flags.BackupEligible,
		BackupState:     cred.Flags.BackupState,
		Label:           label,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return ErrCredentialExists
		}
		return err
	}
	return nil
}

// BeginLogin starts a discoverable (usernameless) login: the assertion options
// carry no allowed-credentials, so the authenticator offers its resident keys. The
// challenge is stored server-side with no user (the user is unknown until the
// authenticated credential resolves one).
func (s *Service) BeginLogin(ctx context.Context) (*LoginOptions, error) {
	if s.web == nil {
		return nil, ErrNotConfigured
	}
	assertion, sessionData, err := s.web.BeginDiscoverableLogin()
	if err != nil {
		return nil, err
	}
	sessionID, err := s.persistChallenge(ctx, nil, sessionData, kindLogin)
	if err != nil {
		return nil, err
	}
	return &LoginOptions{SessionID: sessionID, PublicKey: &assertion.Response}, nil
}

// FinishLogin consumes the login challenge, resolves the user ONLY from the
// authenticated credential, verifies the assertion (RP id/origin + user
// verification), rejects a non-increasing signature counter (clone detection), and
// on success advances the stored counter and returns the resolved user id so the
// caller mints a session. Any verification problem is a flat ErrAssertion.
func (s *Service) FinishLogin(ctx context.Context, sessionID string, assertionJSON []byte) (uuid.UUID, error) {
	if s.web == nil {
		return uuid.Nil, ErrNotConfigured
	}
	ch, err := s.consume(ctx, sessionID, kindLogin)
	if err != nil {
		return uuid.Nil, err
	}
	var sessionData webauthn.SessionData
	if err := json.Unmarshal(ch.SessionData, &sessionData); err != nil {
		return uuid.Nil, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(assertionJSON)
	if err != nil {
		return uuid.Nil, ErrAssertion
	}

	// The handler resolves the user strictly from the credential presented in the
	// signed assertion (its raw id) — never from a client-supplied user id. The
	// library additionally checks the assertion's userHandle equals this user's
	// WebAuthnID, and that the credential is owned by the resolved user.
	var (
		resolvedUserID uuid.UUID
		credRowID      uuid.UUID
	)
	handler := func(rawID, _ []byte) (webauthn.User, error) {
		row, err := s.store.GetCredentialByCredentialID(ctx, rawID)
		if err != nil {
			return nil, err
		}
		rows, err := s.store.ListCredentialsByUser(ctx, row.UserID)
		if err != nil {
			return nil, err
		}
		resolvedUserID = row.UserID
		credRowID = row.ID
		return &credentialUser{id: row.UserID, credentials: toLibCredentials(rows)}, nil
	}

	_, cred, err := s.web.ValidatePasskeyLogin(handler, sessionData, parsed)
	if err != nil {
		return uuid.Nil, ErrAssertion
	}
	// Clone detection: the library sets CloneWarning when the presented signature
	// counter did not exceed the stored one (for a counter-supporting authenticator).
	// Reject and do NOT advance the counter.
	if cred.Authenticator.CloneWarning {
		return uuid.Nil, ErrCloneDetected
	}
	// Advance the stored counter (forward-only in SQL) and stamp last_used_at.
	if _, err := s.store.TouchSignCount(ctx, credRowID, resolvedUserID, int64(cred.Authenticator.SignCount)); err != nil {
		return uuid.Nil, err
	}
	return resolvedUserID, nil
}

// List returns the caller's credentials for the manage surface (no key material).
func (s *Service) List(ctx context.Context, userID uuid.UUID) ([]CredentialInfo, error) {
	rows, err := s.store.ListCredentialsByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]CredentialInfo, len(rows))
	for i, r := range rows {
		info := CredentialInfo{
			ID:         r.ID.String(),
			Label:      r.Label,
			Transports: transportsList(r.Transports),
			CreatedAt:  pgxTime(r.CreatedAt),
		}
		if r.LastUsedAt.Valid {
			t := r.LastUsedAt.Time
			info.LastUsedAt = &t
		}
		out[i] = info
	}
	return out, nil
}

// Delete removes one of the caller's credentials (own-only). A foreign or missing
// id returns ErrNotFound so a caller cannot probe another user's credentials.
func (s *Service) Delete(ctx context.Context, userID, id uuid.UUID) error {
	n, err := s.store.DeleteCredential(ctx, id, userID)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// persistChallenge mints an opaque session token, stores the serialized SessionData
// under its hash (single-use, TTL'd, bound to the ceremony kind and optional user),
// and returns the raw token for the client to echo on finish.
func (s *Service) persistChallenge(ctx context.Context, userID *uuid.UUID, sessionData *webauthn.SessionData, kind string) (string, error) {
	raw, hash, err := auth.NewOpaqueToken()
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(sessionData)
	if err != nil {
		return "", err
	}
	if _, err := s.store.CreateChallenge(ctx, hash, userID, data, kind, s.now().Add(challengeTTL)); err != nil {
		return "", err
	}
	return raw, nil
}

// consume atomically claims the live challenge for sessionID and checks its kind.
// A dead challenge (unknown/consumed/expired) or a kind mismatch maps to
// ErrChallengeInvalid.
func (s *Service) consume(ctx context.Context, sessionID, kind string) (gen.WebauthnChallenge, error) {
	ch, err := s.store.ConsumeChallenge(ctx, auth.HashToken(sessionID))
	if err != nil {
		return gen.WebauthnChallenge{}, ErrChallengeInvalid
	}
	if ch.Kind != kind {
		return gen.WebauthnChallenge{}, ErrChallengeInvalid
	}
	return ch, nil
}

// toLibCredentials maps stored rows to the library credential type.
func toLibCredentials(rows []gen.WebauthnCredential) []webauthn.Credential {
	creds := make([]webauthn.Credential, len(rows))
	for i, r := range rows {
		creds[i] = toLibCredential(r)
	}
	return creds
}

// isUniqueViolation reports whether err is a Postgres unique-key violation
// (SQLSTATE 23505) — a duplicate credential id. Typed error only.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}
