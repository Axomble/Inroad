//go:build integration

package passkey

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	softauth "github.com/descope/virtualwebauthn"
)

// TestWebAuthnCeremonyEndToEnd drives the FULL go-webauthn protocol against a real
// Postgres using an in-process software authenticator (no browser): a real
// attestation registers a credential, then a real assertion discoverably logs in and
// resolves the user strictly from the signed credential. This is the one gap the
// store-level and clone-decision tests cannot cover — it exercises the actual
// cryptographic ceremony, not just the SQL invariants underneath it.
//
// It asserts three things:
//
//	(a) a fresh registration + login round-trips to a resolved session user;
//	(b) the resolved user id is the registering user — proving the assertion's
//	    userHandle is bound back to that user (not a client-supplied id);
//	(c) replaying the very same assertion fails, because the login challenge is
//	    single-use.
func TestWebAuthnCeremonyEndToEnd(t *testing.T) {
	_, store, q := setup(t)
	ctx := context.Background()
	userID := mkUser(t, q)

	const (
		rpID     = "example.test"
		rpOrigin = "https://example.test"
	)
	web, err := NewWebAuthn(rpID, rpOrigin)
	if err != nil {
		t.Fatalf("NewWebAuthn: %v", err)
	}
	svc := NewService(web, store)

	// A software authenticator plus a fresh credential standing in for a real device.
	rp := softauth.RelyingParty{ID: rpID, Name: rpDisplayName, Origin: rpOrigin}
	authenticator := softauth.NewAuthenticator()
	credential := softauth.NewCredential(softauth.KeyTypeEC2)

	// --- Registration ceremony ---
	regOpts, err := svc.BeginRegistration(ctx, userID)
	if err != nil {
		t.Fatalf("BeginRegistration: %v", err)
	}
	attOptionsJSON, err := json.Marshal(regOpts.PublicKey)
	if err != nil {
		t.Fatalf("marshal creation options: %v", err)
	}
	attOptions, err := softauth.ParseAttestationOptions(string(attOptionsJSON))
	if err != nil {
		t.Fatalf("ParseAttestationOptions: %v", err)
	}
	// The authenticator remembers the user handle carried in the creation options and
	// echoes it back as the assertion's userHandle — that echo is what binds a later
	// discoverable login to the registering user.
	authenticator.Options.UserHandle = []byte(attOptions.UserID)
	attResponse := softauth.CreateAttestationResponse(rp, authenticator, credential, *attOptions)

	if err := svc.FinishRegistration(ctx, userID, regOpts.SessionID, []byte(attResponse), "e2e device"); err != nil {
		t.Fatalf("FinishRegistration: %v", err)
	}
	authenticator.AddCredential(credential)

	// The credential really landed in Postgres, owned by the registering user.
	stored, err := store.GetCredentialByCredentialID(ctx, credential.ID)
	if err != nil {
		t.Fatalf("stored credential lookup: %v", err)
	}
	if stored.UserID != userID {
		t.Fatalf("stored credential owner = %v, want %v", stored.UserID, userID)
	}

	// --- Discoverable login ceremony ---
	loginOpts, err := svc.BeginLogin(ctx)
	if err != nil {
		t.Fatalf("BeginLogin: %v", err)
	}
	asrOptionsJSON, err := json.Marshal(loginOpts.PublicKey)
	if err != nil {
		t.Fatalf("marshal request options: %v", err)
	}
	asrOptions, err := softauth.ParseAssertionOptions(string(asrOptionsJSON))
	if err != nil {
		t.Fatalf("ParseAssertionOptions: %v", err)
	}
	// Present a strictly increasing signature counter, the way a real authenticator
	// would, so the login is a clean forward advance (no clone warning).
	credential.Counter++
	assertion := softauth.CreateAssertionResponse(rp, authenticator, credential, *asrOptions)

	// (a) + (b): the ceremony round-trips to a session user, resolved ONLY from the
	// signed credential, and that user is the one who registered.
	resolved, err := svc.FinishLogin(ctx, loginOpts.SessionID, []byte(assertion))
	if err != nil {
		t.Fatalf("FinishLogin: %v", err)
	}
	if resolved != userID {
		t.Fatalf("FinishLogin resolved user = %v, want registering user %v", resolved, userID)
	}

	// (c): replaying the identical assertion against the same session fails — the
	// login challenge was consumed on first use.
	if _, err := svc.FinishLogin(ctx, loginOpts.SessionID, []byte(assertion)); !errors.Is(err, ErrChallengeInvalid) {
		t.Fatalf("assertion replay: got %v, want ErrChallengeInvalid (single-use challenge)", err)
	}
}
