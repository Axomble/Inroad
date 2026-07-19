package workspace

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegisterHandlerValidation(t *testing.T) {
	h := NewHandler(nil, []byte("0123456789abcdef0123456789abcdef"))
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for empty body", w.Code)
	}
}
