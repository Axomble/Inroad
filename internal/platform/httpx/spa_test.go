package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSPA(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("app shell"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.js"), []byte("asset"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, ok := SPA(dir)
	if !ok {
		t.Fatal("expected SPA handler")
	}

	tests := []struct {
		name, target, accept string
		wantStatus           int
		wantBody             string
	}{
		{"asset", "/app.js", "*/*", http.StatusOK, "asset"},
		{"route fallback", "/app/campaigns/123", "text/html", http.StatusOK, "app shell"},
		{"unknown api", "/api/v1/missing", "application/json", http.StatusNotFound, "404 page not found"},
		// A browser-ish Accept must not turn a server-owned route into the shell.
		{"unknown api accepting html", "/api/v1/missing", "text/html", http.StatusNotFound, "404 page not found"},
		{"unknown tracking route", "/t/missing", "text/html", http.StatusNotFound, "404 page not found"},
		{"unknown oauth route", "/oauth/missing", "text/html", http.StatusNotFound, "404 page not found"},
		{"missing asset", "/missing.js", "text/html", http.StatusNotFound, "404 page not found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, tc.target, http.NoBody)
			req.Header.Set("Accept", tc.accept)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if body := strings.TrimSpace(rec.Body.String()); body != tc.wantBody {
				t.Fatalf("body = %q, want %q", body, tc.wantBody)
			}
		})
	}
}

func TestSPANotConfigured(t *testing.T) {
	if _, ok := SPA(""); ok {
		t.Fatal("empty directory must not configure SPA")
	}
}
