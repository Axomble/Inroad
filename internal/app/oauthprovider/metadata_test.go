package oauthprovider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthorizationServerMetadata(t *testing.T) {
	h := AuthorizationServerMetadata("https://inroad.test/")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/oauth2/.well-known/oauth-authorization-server", http.NoBody))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["issuer"] != "https://inroad.test/oauth2" {
		t.Fatalf("issuer = %#v", body["issuer"])
	}
	if body["authorization_endpoint"] != "https://inroad.test/oauth2/authorize" {
		t.Fatalf("authorization endpoint = %#v", body["authorization_endpoint"])
	}
}

func TestAuthorizationServerMetadataRejectsNonGet(t *testing.T) {
	res := httptest.NewRecorder()
	AuthorizationServerMetadata("https://inroad.test").ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/", http.NoBody))
	if res.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", res.Code)
	}
}
