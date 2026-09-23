//go:build integration

package crm

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// TestCompanySearchFiltersPagesAndStaysInItsWorkspace drives the real
// SearchCompanies SQL: substring matching on name and domain, LIKE
// metacharacters taken literally, keyset paging across the filtered set, a
// cursor bound to its query, and no row from another workspace — which in a
// one-workspace fixture would be invisible, so a second workspace holds a
// company that matches every query below.
func TestCompanySearchFiltersPagesAndStaysInItsWorkspace(t *testing.T) {
	ctx, pool, ws := newCRMWorkspace(t, "CRM search")
	otherWorkspace, err := gen.New(pool).CreateWorkspace(ctx, "CRM search other "+uuid.NewString())
	if err != nil {
		t.Fatalf("other workspace: %v", err)
	}
	other := otherWorkspace.ID
	service := NewService(NewPgStore(pool))
	seed := func(workspace uuid.UUID, name, domain string) {
		t.Helper()
		if _, err := service.CreateCompany(ctx, workspace, CompanyInput{Name: name, Domain: domain, Currency: "USD"}); err != nil {
			t.Fatalf("company %s: %v", name, err)
		}
	}
	seed(ws, "Acme Robotics", "acme.io")
	seed(ws, "Beta ACME", "")
	seed(ws, "Gamma", "acmecorp.com")
	seed(ws, "Delta", "delta.test")
	seed(ws, "100%_Pure", "")
	seed(other, "Acme Foreign", "acme-foreign.test")
	seed(other, "Foreign 100%_Pure", "")

	t.Run("matches name or domain case-insensitively, walking every page once", func(t *testing.T) {
		got := walkCompanies(ctx, t, service, ws, CompanyFilter{Query: "ACME"})
		want := []string{"Acme Robotics", "Beta ACME", "Gamma"}
		if !slices.Equal(got, want) {
			t.Fatalf("walked %v, want %v", got, want)
		}
	})

	t.Run("a domain fragment finds its company", func(t *testing.T) {
		got := walkCompanies(ctx, t, service, ws, CompanyFilter{Query: "acme.io"})
		if !slices.Equal(got, []string{"Acme Robotics"}) {
			t.Fatalf("walked %v, want only Acme Robotics", got)
		}
	})

	t.Run("LIKE metacharacters are matched literally", func(t *testing.T) {
		// Unescaped, "%_" is "any string of at least one character" and would
		// return all five companies.
		got := walkCompanies(ctx, t, service, ws, CompanyFilter{Query: "%_"})
		if !slices.Equal(got, []string{"100%_Pure"}) {
			t.Fatalf("walked %v, want only 100%%_Pure", got)
		}
	})

	t.Run("no match is an empty last page", func(t *testing.T) {
		page, err := service.ListCompanies(ctx, ws, CompanyFilter{Query: "zzz"}, PageRequest{Limit: 2})
		if err != nil || len(page.Items) != 0 || page.NextCursor != "" {
			t.Fatalf("page = %+v, err = %v", page, err)
		}
	})

	t.Run("the other workspace sees only its own matches", func(t *testing.T) {
		got := walkCompanies(ctx, t, service, other, CompanyFilter{Query: "acme"})
		if !slices.Equal(got, []string{"Acme Foreign"}) {
			t.Fatalf("walked %v, want only Acme Foreign", got)
		}
	})

	t.Run("a search cursor is refused under a different query or none", func(t *testing.T) {
		first, err := service.ListCompanies(ctx, ws, CompanyFilter{Query: "acme"}, PageRequest{Limit: 1})
		if err != nil || first.NextCursor == "" {
			t.Fatalf("first page = %+v, err = %v", first, err)
		}
		if _, err := service.ListCompanies(ctx, ws, CompanyFilter{Query: "acm"}, PageRequest{Limit: 1, Cursor: first.NextCursor}); !errors.Is(err, ErrValidation) {
			t.Fatalf("changed query: err = %v, want ErrValidation", err)
		}
		if _, err := service.ListCompanies(ctx, ws, CompanyFilter{}, PageRequest{Limit: 1, Cursor: first.NextCursor}); !errors.Is(err, ErrValidation) {
			t.Fatalf("dropped query: err = %v, want ErrValidation", err)
		}
	})

	t.Run("a too-short query is a validation error", func(t *testing.T) {
		if _, err := service.ListCompanies(ctx, ws, CompanyFilter{Query: "a"}, PageRequest{}); !errors.Is(err, ErrValidation) {
			t.Fatalf("err = %v, want ErrValidation", err)
		}
	})
}
