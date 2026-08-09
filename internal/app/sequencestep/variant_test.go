package sequencestep

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

var testStepID = uuid.New()

// variantFixture wires a step with a given base weight and existing variants.
func variantFixture(baseWeight int32, variants ...Variant) (*Service, *fakeVariantStore) {
	step := gen.SequenceStep{ID: testStepID, StepOrder: 1, Subject: "hi", VariantWeight: baseWeight}
	store := &fakeStore{getStep: step}
	vs := &fakeVariantStore{variants: variants, sent: map[uuid.UUID]int64{}}
	return NewService(store, fakeChecker{status: "running"}, vs), vs
}

func variant(label string, weight int32) Variant {
	return Variant{ID: uuid.New(), StepID: testStepID, Label: label, Weight: weight}
}

func TestCreateVariantValidatesLabel(t *testing.T) {
	for _, label := range []string{"", "   ", strings.Repeat("x", maxVariantLabel+1)} {
		svc, _ := variantFixture(1)
		_, err := svc.CreateVariant(context.Background(), uuid.New(), testStepID, VariantInput{Label: label, Weight: 1})
		if !errors.Is(err, ErrVariantLabel) {
			t.Errorf("label %q: err = %v, want ErrVariantLabel", label, err)
		}
	}
}

func TestCreateVariantRejectsNegativeWeight(t *testing.T) {
	svc, _ := variantFixture(1)
	_, err := svc.CreateVariant(context.Background(), uuid.New(), testStepID, VariantInput{Label: "B", Weight: -1})
	if !errors.Is(err, ErrVariantWeight) {
		t.Fatalf("err = %v, want ErrVariantWeight", err)
	}
}

func TestCreateVariantEnforcesTheCap(t *testing.T) {
	existing := make([]Variant, 0, MaxVariantsPerStep)
	for i := 0; i < MaxVariantsPerStep; i++ {
		existing = append(existing, variant(string(rune('B'+i)), 1))
	}
	svc, _ := variantFixture(1, existing...)
	_, err := svc.CreateVariant(context.Background(), uuid.New(), testStepID, VariantInput{Label: "Z", Weight: 1})
	if !errors.Is(err, ErrTooManyVariants) {
		t.Fatalf("err = %v, want ErrTooManyVariants", err)
	}
}

// The weight invariant is checked against the state the write would PRODUCE.
// Adding a weight-0 arm to a step whose base is also 0 must not be the write
// that leaves the step unable to send anything.
func TestCreateVariantRefusesTheWriteThatMakesAStepUnsendable(t *testing.T) {
	svc, vs := variantFixture(0)
	_, err := svc.CreateVariant(context.Background(), uuid.New(), testStepID, VariantInput{Label: "B", Weight: 0})
	if !errors.Is(err, ErrNoEligibleVariant) {
		t.Fatalf("err = %v, want ErrNoEligibleVariant", err)
	}
	if len(vs.variants) != 0 {
		t.Error("the refused variant must not have been written")
	}
}

// A zero-weight arm is fine as long as something else can still send — that is
// how an arm is staged before it goes live.
func TestCreateVariantAllowsAZeroWeightArmBesideALiveOne(t *testing.T) {
	svc, _ := variantFixture(1)
	if _, err := svc.CreateVariant(context.Background(), uuid.New(), testStepID,
		VariantInput{Label: "B", Weight: 0}); err != nil {
		t.Fatalf("CreateVariant: %v", err)
	}
}

func TestUpdateVariantRefusesZeroingTheLastEligibleArm(t *testing.T) {
	only := variant("B", 1)
	svc, vs := variantFixture(0, only)

	_, err := svc.UpdateVariant(context.Background(), uuid.New(), only.ID, VariantInput{Label: "B", Weight: 0})
	if !errors.Is(err, ErrNoEligibleVariant) {
		t.Fatalf("err = %v, want ErrNoEligibleVariant", err)
	}
	if vs.variants[0].Weight != 1 {
		t.Error("the refused update must not have been written")
	}
}

// Zeroing an arm IS allowed when the base copy still sends — this is exactly how
// a losing variant is retired.
func TestUpdateVariantAllowsZeroingWhenTheBaseStillSends(t *testing.T) {
	losing := variant("B", 1)
	svc, vs := variantFixture(1, losing)

	if _, err := svc.UpdateVariant(context.Background(), uuid.New(), losing.ID,
		VariantInput{Label: "B", Weight: 0}); err != nil {
		t.Fatalf("UpdateVariant: %v", err)
	}
	if vs.variants[0].Weight != 0 {
		t.Errorf("weight = %d, want 0", vs.variants[0].Weight)
	}
}

// Deleting a variant that has sent would silently re-attribute its messages to
// the base copy (sends.variant_id is ON DELETE SET NULL), making the losing
// arm's results fold into the winner's.
func TestDeleteVariantRefusesOneThatHasSent(t *testing.T) {
	sentArm := variant("B", 1)
	svc, vs := variantFixture(1, sentArm)
	vs.sent[sentArm.ID] = 42

	err := svc.DeleteVariant(context.Background(), uuid.New(), sentArm.ID)
	if !errors.Is(err, ErrVariantHasSends) {
		t.Fatalf("err = %v, want ErrVariantHasSends", err)
	}
	if len(vs.variants) != 1 {
		t.Error("a variant with delivery history must not be deleted")
	}
}

func TestDeleteVariantRemovesOneThatNeverSent(t *testing.T) {
	unsent := variant("B", 1)
	svc, vs := variantFixture(1, unsent)

	if err := svc.DeleteVariant(context.Background(), uuid.New(), unsent.ID); err != nil {
		t.Fatalf("DeleteVariant: %v", err)
	}
	if len(vs.variants) != 0 {
		t.Error("the variant should have been deleted")
	}
}

// Deleting the only eligible arm is refused for the same reason zeroing it is.
func TestDeleteVariantRefusesTheLastEligibleArm(t *testing.T) {
	only := variant("B", 1)
	svc, _ := variantFixture(0, only)

	if err := svc.DeleteVariant(context.Background(), uuid.New(), only.ID); !errors.Is(err, ErrNoEligibleVariant) {
		t.Fatalf("err = %v, want ErrNoEligibleVariant", err)
	}
}

func TestSetBaseWeightRefusesZeroingTheOnlyArm(t *testing.T) {
	svc, vs := variantFixture(1)
	if err := svc.SetBaseWeight(context.Background(), uuid.New(), testStepID, 0); !errors.Is(err, ErrNoEligibleVariant) {
		t.Fatalf("err = %v, want ErrNoEligibleVariant", err)
	}
	if vs.baseWeightSet {
		t.Error("the refused weight must not have been written")
	}
}

// Zeroing the base IS how a winning variant is promoted, so it must be allowed
// whenever an alternative still sends.
func TestSetBaseWeightAllowsPromotingAVariant(t *testing.T) {
	svc, vs := variantFixture(1, variant("B", 1))
	if err := svc.SetBaseWeight(context.Background(), uuid.New(), testStepID, 0); err != nil {
		t.Fatalf("SetBaseWeight: %v", err)
	}
	if !vs.baseWeightSet || vs.baseWeight != 0 {
		t.Errorf("baseWeight = %d (set=%v), want 0", vs.baseWeight, vs.baseWeightSet)
	}
}

func TestSetBaseWeightRejectsNegative(t *testing.T) {
	svc, _ := variantFixture(1)
	if err := svc.SetBaseWeight(context.Background(), uuid.New(), testStepID, -1); !errors.Is(err, ErrVariantWeight) {
		t.Fatalf("err = %v, want ErrVariantWeight", err)
	}
}
