//go:build integration

package identity

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestSessionEnumerationCrossUserImpossible proves the session-management surface
// is strictly user-owned: user A can neither DELETE nor enumerate user B's
// session. A foreign session id is a 404 (RevokeSessionOwned is pinned to the
// JWT's user id, so it flips 0 rows and never leaks B's session's existence),
// B's session stays live, and A's own list never contains B's session id.
func TestSessionEnumerationCrossUserImpossible(t *testing.T) {
	srv, _ := newIdentityTestServer(t)
	a := registerFull(t, srv)
	b := registerFull(t, srv)

	// A attempts to revoke B's session by id: must be 404 (no cross-user action).
	req, err := http.NewRequestWithContext(t.Context(), http.MethodDelete, srv.URL+"/api/v1/auth/sessions/"+b.sid.String(), http.NoBody)
	if err != nil {
		t.Fatalf("new delete request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.access)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete B session as A: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-user revoke: expected 404, got %d", resp.StatusCode)
	}

	// B's session is untouched: B can still use it.
	if code := meStatus(t, srv, b.access); code != http.StatusOK {
		t.Fatalf("B session after A's failed revoke: expected 200, got %d", code)
	}

	// A's own session list must contain only A's session — never B's.
	listReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/api/v1/auth/sessions", http.NoBody)
	if err != nil {
		t.Fatalf("new list request: %v", err)
	}
	listReq.Header.Set("Authorization", "Bearer "+a.access)
	listResp, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatalf("list A sessions: %v", err)
	}
	defer listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list A sessions: expected 200, got %d", listResp.StatusCode)
	}
	var body struct {
		Sessions []struct {
			ID string `json:"id"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&body); err != nil {
		t.Fatalf("decode session list: %v", err)
	}
	var sawA, sawB bool
	for _, s := range body.Sessions {
		switch s.ID {
		case a.sid.String():
			sawA = true
		case b.sid.String():
			sawB = true
		}
	}
	if !sawA {
		t.Fatalf("expected A's own session %s in A's list, sessions=%+v", a.sid, body.Sessions)
	}
	if sawB {
		t.Fatalf("A's session list leaked B's session %s (cross-user enumeration)", b.sid)
	}
}
