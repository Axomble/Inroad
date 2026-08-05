package crm

import (
	"errors"
	"testing"

	"github.com/google/uuid"
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
