package oauthprovider

import (
	"context"
	"testing"
)

func TestPgStoreCreateClientRequiresWorkspaceOwnership(t *testing.T) {
	store := &PgStore{}
	_, err := store.CreateClient(context.Background(), CreateClientParams{ClientID: "client-without-owner"})
	if err == nil {
		t.Fatal("CreateClient accepted missing workspace ownership")
	}
}
