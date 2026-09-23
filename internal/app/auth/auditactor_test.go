package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/audit"
)

func TestRequireAuthAttributesAuditEventsToThePrincipal(t *testing.T) {
	uid, keyID := uuid.New(), uuid.New()
	cases := []struct {
		name      string
		principal Principal
		wantType  audit.ActorType
		wantID    string
	}{
		{"session", Principal{Kind: KindSession, UserID: uid.String()}, audit.ActorUser, uid.String()},
		{"api key", Principal{Kind: KindAPIKey, UserID: uid.String(), CredentialID: keyID.String()}, audit.ActorAPIKey, keyID.String()},
		{"oauth", Principal{Kind: KindOAuth, UserID: uid.String(), CredentialID: "client-123"}, audit.ActorOAuthClient, "client-123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got audit.Actor
			var ok bool
			h := RequireAuth(stubVerifier{principal: tc.principal, ok: true})(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				got, ok = audit.ActorFrom(r.Context())
			}))
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody))

			if !ok {
				t.Fatal("RequireAuth stored no audit actor")
			}
			if got.Type != tc.wantType || got.ID != tc.wantID {
				t.Fatalf("actor = %s/%s, want %s/%s", got.Type, got.ID, tc.wantType, tc.wantID)
			}
			if got.UserID == nil || *got.UserID != uid {
				t.Fatalf("actor user = %v, want the principal's user %s", got.UserID, uid)
			}
		})
	}
}

func TestAuditActorForAKeyWhoseCreatorIsGone(t *testing.T) {
	// An api key whose creator was deleted carries an empty UserID; the actor
	// must still name the key rather than failing or inventing a user.
	a := Principal{Kind: KindAPIKey, CredentialID: "k"}.AuditActor()
	if a.Type != audit.ActorAPIKey || a.ID != "k" || a.UserID != nil {
		t.Fatalf("actor = %+v", a)
	}
}
