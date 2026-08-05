package crm

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestValidateCompanyNormalizesValues(t *testing.T) {
	in := CompanyInput{Name: "  Acme  ", Domain: " EXAMPLE.COM ", Currency: "usd"}
	if err := validateCompany(&in); err != nil {
		t.Fatalf("validateCompany: %v", err)
	}
	if in.Name != "Acme" || in.Domain != "example.com" || in.Currency != "USD" {
		t.Fatalf("unexpected normalization: %+v", in)
	}
}

func TestValidateCompanyRejectsInvalidDomainAndRevenue(t *testing.T) {
	negative := int64(-1)
	for _, in := range []CompanyInput{
		{Name: "Acme", Domain: "https://example.com", Currency: "USD"},
		{Name: "Acme", Currency: "USD", AnnualRevenueMicros: &negative},
	} {
		if err := validateCompany(&in); !errors.Is(err, ErrValidation) {
			t.Fatalf("expected validation error, got %v", err)
		}
	}
}

func TestValidateDealRequiresTenantReferencesAndAttribution(t *testing.T) {
	in := DealInput{Name: "Expansion", Currency: "USD", Actor: Actor{Type: "user"}}
	if err := validateDeal(&in); !errors.Is(err, ErrValidation) {
		t.Fatalf("expected missing references to fail, got %v", err)
	}
	in.PipelineID, in.StageID = uuid.New(), uuid.New()
	if err := validateDeal(&in); err != nil {
		t.Fatalf("valid deal rejected: %v", err)
	}
}

func TestValidateTargetRejectsUnknownType(t *testing.T) {
	if err := validateTarget(Target{Type: "campaign", ID: uuid.New()}); !errors.Is(err, ErrValidation) {
		t.Fatalf("expected validation error, got %v", err)
	}
}

// TestTranslateWriteErrorMapsConstraintViolations pins the one place a driver
// error becomes a domain error. Getting this wrong is what turns a duplicate
// domain into a 500 instead of an actionable 409.
func TestTranslateWriteErrorMapsConstraintViolations(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want error
	}{
		"nil stays nil":              {nil, nil},
		"unique violation is 409":    {&pgconn.PgError{Code: "23505"}, ErrConflict},
		"foreign key is validation":  {&pgconn.PgError{Code: "23503"}, ErrValidation},
		"check is validation":        {&pgconn.PgError{Code: "23514"}, ErrValidation},
		"not null is validation":     {&pgconn.PgError{Code: "23502"}, ErrValidation},
		"wrapped unique still maps":  {fmt.Errorf("insert: %w", &pgconn.PgError{Code: "23505"}), ErrConflict},
		"sentinel passes through":    {ErrNotFound, ErrNotFound},
		"unknown code is not domain": {&pgconn.PgError{Code: "42P01"}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			got := translateWriteError(tc.err)
			switch {
			case tc.want == nil && tc.err == nil:
				if got != nil {
					t.Fatalf("got %v, want nil", got)
				}
			case tc.want == nil:
				// Unmapped driver errors must NOT be dressed up as a domain
				// error: they are infrastructure faults and must fail loud.
				if errors.Is(got, ErrConflict) || errors.Is(got, ErrValidation) || errors.Is(got, ErrNotFound) {
					t.Fatalf("unknown code was classified as a domain error: %v", got)
				}
			default:
				if !errors.Is(got, tc.want) {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestTranslateDeleteErrorReportsReferences proves a delete blocked by a
// child row is a 409, not the 500 a raw FK error would produce.
func TestTranslateDeleteErrorReportsReferences(t *testing.T) {
	if err := translateDeleteError(&pgconn.PgError{Code: "23503"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("referenced delete = %v, want ErrConflict", err)
	}
	if err := translateDeleteError(ErrNotFound); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing delete = %v, want ErrNotFound", err)
	}
}

// TestDeleteRefusalsAreConflictsNotNotFound covers the business-guard split:
// "you may not delete this" must never read as "this does not exist".
func TestDeleteRefusalsAreConflictsNotNotFound(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	svc := NewService(store)
	ws := uuid.New()

	defaultPipeline, plainPipeline := uuid.New(), uuid.New()
	store.isDefault[defaultPipeline], store.isDefault[plainPipeline] = true, false
	if err := svc.DeletePipeline(ctx, ws, defaultPipeline); !errors.Is(err, ErrConflict) {
		t.Fatalf("default pipeline delete = %v, want ErrConflict", err)
	}
	if err := svc.DeletePipeline(ctx, ws, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown pipeline delete = %v, want ErrNotFound", err)
	}
	if err := svc.DeletePipeline(ctx, ws, plainPipeline); err != nil {
		t.Fatalf("plain pipeline delete = %v", err)
	}

	busy, empty := uuid.New(), uuid.New()
	store.stages[busy], store.stages[empty] = true, true
	store.stageDeal[busy] = 2
	if err := svc.DeleteStage(ctx, ws, plainPipeline, busy); !errors.Is(err, ErrConflict) {
		t.Fatalf("occupied stage delete = %v, want ErrConflict", err)
	}
	if err := svc.DeleteStage(ctx, ws, plainPipeline, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown stage delete = %v, want ErrNotFound", err)
	}
	if err := svc.DeleteStage(ctx, ws, plainPipeline, empty); err != nil {
		t.Fatalf("empty stage delete = %v", err)
	}
}

func TestNormalizePageClampsLimit(t *testing.T) {
	for name, tc := range map[string]struct {
		in   int32
		want int32
	}{
		"zero defaults":   {0, defaultPageLimit},
		"negative       ": {-5, defaultPageLimit},
		"in range kept":   {10, 10},
		"over max caps":   {maxPageLimit + 1, maxPageLimit},
	} {
		t.Run(name, func(t *testing.T) {
			if got := normalizePage(PageRequest{Limit: tc.in}).Limit; got != tc.want {
				t.Fatalf("limit %d -> %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestCursorsAreOpaqueAndListScoped proves a cursor cannot be replayed against
// a different listing, and that a corrupt one is a validation error rather
// than a silent restart at page one.
func TestCursorsAreOpaqueAndListScoped(t *testing.T) {
	token := encodeCursor(cursorCompanies, uuid.Nil.String(), "acme|corp")
	keys, err := decodeCursor(cursorCompanies, token, 2)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if keys[0] != uuid.Nil.String() || keys[1] != "acme|corp" {
		t.Fatalf("round trip lost keys: %+v", keys)
	}
	if _, err := decodeCursor(cursorDeals, token, 3); !errors.Is(err, ErrValidation) {
		t.Fatalf("cross-list replay = %v, want ErrValidation", err)
	}
	if _, err := decodeCursor(cursorCompanies, "not-base64!!", 2); !errors.Is(err, ErrValidation) {
		t.Fatalf("malformed cursor = %v, want ErrValidation", err)
	}
	if _, err := decodeCursor(cursorCompanies, token, 3); !errors.Is(err, ErrValidation) {
		t.Fatalf("wrong key count = %v, want ErrValidation", err)
	}
}
