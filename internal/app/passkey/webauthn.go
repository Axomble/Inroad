// Package passkey implements WebAuthn passkey registration and discoverable
// (usernameless) login over github.com/go-webauthn/webauthn.
//
// A passkey is a USER-level credential (not workspace-scoped) — it follows the
// human across every workspace. Nothing stored here is secret: a WebAuthn public
// key is public and the private key never leaves the authenticator, so credentials
// are persisted in the clear (unlike the TOTP secret, which is sealed). Ceremony
// challenges are stored server-side, single-use and short-TTL, and are never
// trusted from a client echo. A passkey login uses user-verification (a local
// biometric/PIN) and is phishing-resistant, so it counts as strong auth and skips
// the TOTP login gate.
package passkey

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// rpDisplayName labels the Relying Party in the authenticator UI.
const rpDisplayName = "Inroad"

// NewWebAuthn builds the library instance from the Relying Party id and origin.
// Registration and login both REQUIRE a resident (discoverable) key and user
// verification: the resident key enables usernameless login and the UV requirement
// is what makes a passkey strong (second-factor-grade) auth. An empty/invalid rpID
// or rpOrigin returns an error so the caller can disable the feature cleanly rather
// than mis-validate a ceremony against a wrong domain.
func NewWebAuthn(rpID, rpOrigin string) (*webauthn.WebAuthn, error) {
	if rpID == "" || rpOrigin == "" {
		return nil, fmt.Errorf("passkey: RP id and origin must both be set")
	}
	return webauthn.New(&webauthn.Config{
		RPID:          rpID,
		RPDisplayName: rpDisplayName,
		RPOrigins:     []string{rpOrigin},
		// Prefer no attestation: we authenticate possession, not vet the
		// authenticator model, so we neither request nor verify an attestation
		// statement (no MDS). This keeps the ceremony private and avoids storing
		// attestation blobs we would never re-check.
		AttestationPreference: protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			RequireResidentKey: protocol.ResidentKeyRequired(),
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			UserVerification:   protocol.VerificationRequired,
		},
	})
}

// credentialUser adapts an Inroad user to the webauthn.User interface. WebAuthnID
// is the user's UUID bytes (a stable 16-byte handle, well under the 64-byte limit),
// so a discoverable login's userHandle resolves straight back to the user id — and
// the library checks that handle equals this value, closing any handle/user
// mismatch.
type credentialUser struct {
	id          uuid.UUID
	name        string
	credentials []webauthn.Credential
}

// WebAuthnID returns the user's UUID as the opaque user handle.
func (u *credentialUser) WebAuthnID() []byte {
	b := u.id // copy so we never alias the caller's array
	return b[:]
}

// WebAuthnName is the human-palatable account name (the user's email).
func (u *credentialUser) WebAuthnName() string { return u.name }

// WebAuthnDisplayName mirrors the account name; Inroad has no separate display name.
func (u *credentialUser) WebAuthnDisplayName() string { return u.name }

// WebAuthnCredentials returns the user's registered credentials, used both to build
// the registration exclusion list and to verify an assertion (including the stored
// sign counter that drives clone detection).
func (u *credentialUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

// toLibCredential rebuilds the library Credential from a stored row. Every field the
// login verifier reads is restored: the public key + id for signature verification,
// the sign counter for clone detection, and the backup-eligible/state flags (the
// library rejects a login whose backup-eligible flag disagrees with registration).
func toLibCredential(row gen.WebauthnCredential) webauthn.Credential {
	return webauthn.Credential{
		ID:              row.CredentialID,
		PublicKey:       row.PublicKey,
		AttestationType: row.AttestationType,
		Transport:       decodeTransports(row.Transports),
		Flags: webauthn.CredentialFlags{
			BackupEligible: row.BackupEligible,
			BackupState:    row.BackupState,
		},
		Authenticator: webauthn.Authenticator{
			AAGUID:    row.Aaguid,
			SignCount: uint32(row.SignCount),
		},
	}
}
