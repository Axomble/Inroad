package sequencestep

import (
	"context"

	"github.com/google/uuid"
)

// fakeVariantStore is the VariantStore half of this domain's persistence. It
// holds one step's variants, because every rule under test (the weight
// invariant, the label cap, the sent-count refusal) is scoped to a single step;
// a fake that modelled multiple steps would be asserting its own indexing.
type fakeVariantStore struct {
	variants []Variant
	// sent is the delivery count per variant id, which is what makes a delete
	// refusable.
	sent map[uuid.UUID]int64

	baseWeight    int32
	baseWeightSet bool

	getErr    error
	listErr   error
	createErr error
}

func (f *fakeVariantStore) Get(_ context.Context, _, id uuid.UUID) (Variant, error) {
	if f.getErr != nil {
		return Variant{}, f.getErr
	}
	for _, v := range f.variants {
		if v.ID == id {
			return v, nil
		}
	}
	return Variant{}, ErrVariantNotFound
}

func (f *fakeVariantStore) ListForStep(context.Context, uuid.UUID, uuid.UUID) ([]Variant, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.variants, nil
}

func (f *fakeVariantStore) ListForCampaign(context.Context, uuid.UUID, uuid.UUID) (map[uuid.UUID][]Variant, error) {
	out := map[uuid.UUID][]Variant{}
	for _, v := range f.variants {
		out[v.StepID] = append(out[v.StepID], v)
	}
	return out, nil
}

func (f *fakeVariantStore) Create(_ context.Context, _, stepID uuid.UUID, in VariantInput) (Variant, error) {
	if f.createErr != nil {
		return Variant{}, f.createErr
	}
	for _, v := range f.variants {
		if v.Label == in.Label {
			return Variant{}, ErrLabelTaken
		}
	}
	created := Variant{
		ID: uuid.New(), StepID: stepID, Label: in.Label, Weight: in.Weight,
		Subject: in.Subject, BodyText: in.BodyText, BodyHTML: in.BodyHTML,
	}
	f.variants = append(f.variants, created)
	return created, nil
}

func (f *fakeVariantStore) Update(_ context.Context, _, id uuid.UUID, in VariantInput) (Variant, error) {
	for i, v := range f.variants {
		if v.ID != id {
			continue
		}
		f.variants[i].Label = in.Label
		f.variants[i].Weight = in.Weight
		f.variants[i].Subject = in.Subject
		f.variants[i].BodyText = in.BodyText
		f.variants[i].BodyHTML = in.BodyHTML
		return f.variants[i], nil
	}
	return Variant{}, ErrVariantNotFound
}

func (f *fakeVariantStore) Delete(_ context.Context, _, id uuid.UUID) error {
	for i, v := range f.variants {
		if v.ID == id {
			f.variants = append(f.variants[:i], f.variants[i+1:]...)
			return nil
		}
	}
	return ErrVariantNotFound
}

func (f *fakeVariantStore) SetBaseWeight(_ context.Context, _, _ uuid.UUID, weight int32) error {
	f.baseWeight, f.baseWeightSet = weight, true
	return nil
}

func (f *fakeVariantStore) SentCount(_ context.Context, _, variantID uuid.UUID) (int64, error) {
	return f.sent[variantID], nil
}
