package agentchat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestThreadRouteMatchesWithoutTrailingSlash(t *testing.T) {
	handler := NewHandler(nil).Routes()
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/threads/"+uuid.NewString(), http.NoBody)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want route-level unauthorized response", response.Code)
	}
}

func TestStreamOffsetPrefersLastEventID(t *testing.T) {
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/?after_seq=4", http.NoBody)
	request.Header.Set("Last-Event-ID", "9")
	got, err := streamOffset(request)
	if err != nil || got != 9 {
		t.Fatalf("offset=%d error=%v", got, err)
	}
}

func TestStreamOffsetRejectsNegativeValues(t *testing.T) {
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/?after_seq=-1", http.NoBody)
	if _, err := streamOffset(request); err == nil {
		t.Fatal("expected invalid offset error")
	}
}
