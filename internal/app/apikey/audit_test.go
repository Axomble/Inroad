package apikey

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/audit"
)

func TestCreateAndRevokeHandTheStoreAnAttributedEvent(t *testing.T) {
	store := newFakeStore()
	store.revokeRows = 1
	svc := newTestService(store)
	ws, uid := uuid.New(), uuid.New()
	ctx := audit.WithActor(context.Background(), audit.UserActor(uid))

	_, token, err := svc.Create(ctx, CreateInput{
		WorkspaceID: ws, CreatedBy: uid, Name: "ci",
		Scopes: []string{auth.ScopeListsRead, auth.ScopeContactsRead},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	keyID := uuid.New()
	if err := svc.Revoke(ctx, ws, keyID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	if len(store.events) != 2 {
		t.Fatalf("store got %d events, want 2", len(store.events))
	}
	created, revoked := store.events[0], store.events[1]
	if created.Action != audit.ActionAPIKeyCreated || created.WorkspaceID != ws || created.Actor.ID != uid.String() {
		t.Fatalf("created event = %+v", created)
	}
	if created.Metadata["prefix"] != store.created[0].Prefix || created.Metadata["scopes"] != "lists:read contacts:read" {
		t.Fatalf("created metadata = %v", created.Metadata)
	}
	// The one-time token and its secret half must appear nowhere in the event.
	secret := strings.TrimPrefix(token, tokenScheme+store.created[0].Prefix)
	for k, v := range created.Metadata {
		if strings.Contains(v, secret) || strings.Contains(v, token) {
			t.Fatalf("metadata %q carries the secret", k)
		}
	}
	if err := audit.Validate(created); err != nil {
		t.Fatalf("created event would be refused at write: %v", err)
	}
	if revoked.Action != audit.ActionAPIKeyRevoked || revoked.TargetType != "api_key" || revoked.TargetID != keyID.String() {
		t.Fatalf("revoked event = %+v", revoked)
	}
}
