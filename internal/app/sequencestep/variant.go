package sequencestep

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// MaxVariantsPerStep caps how many alternatives one step may carry. An A/B test
// needs two arms and occasionally three; past a handful the per-variant sample
// gets too small to conclude anything, and every extra arm is another copy an
// operator has to keep current. The cap counts ALTERNATIVES — the step's own
// content is variant A on top of it (migration 000053).
const MaxVariantsPerStep = 5

// maxVariantLabel matches the column CHECK, so a bad label is a 400 with a
// reason rather than a 23514 the caller cannot act on.
const maxVariantLabel = 40

// Variant errors. Each names a condition a caller can act on.
var (
	ErrVariantNotFound = errors.New("variant not found")
	ErrVariantLabel    = errors.New("variant label must be 1-40 characters")
	ErrVariantWeight   = errors.New("variant weight must be zero or greater")
	ErrLabelTaken      = errors.New("a variant with this label already exists on the step")
	ErrTooManyVariants = errors.New("this step already has the maximum number of variants")
	// ErrNoEligibleVariant is every arm at weight 0 — the step could not send
	// anything. Refused at write time so a running campaign cannot be edited into
	// a state where its sends fail one by one at the send path's backstop.
	ErrNoEligibleVariant = errors.New("at least one variant must have a weight above zero")
)

// Variant is one alternative copy for a step.
type Variant struct {
	ID       uuid.UUID
	StepID   uuid.UUID
	Label    string
	Weight   int32
	Subject  string
	BodyText string
	BodyHTML string
}

// VariantInput is a create/update request, pre-validation.
type VariantInput struct {
	Label    string
	Weight   int32
	Subject  string
	BodyText string
	BodyHTML string
}

// VariantStore is the repository seam for variants. Separate from Store because
// it is a distinct responsibility, and keeping it narrow is what lets the
// weight-invariant logic be unit-tested without a database.
type VariantStore interface {
	Get(ctx context.Context, ws, id uuid.UUID) (Variant, error)
	ListForStep(ctx context.Context, ws, stepID uuid.UUID) ([]Variant, error)
	ListForCampaign(ctx context.Context, ws, campaignID uuid.UUID) (map[uuid.UUID][]Variant, error)
	Create(ctx context.Context, ws, stepID uuid.UUID, in VariantInput) (Variant, error)
	Update(ctx context.Context, ws, id uuid.UUID, in VariantInput) (Variant, error)
	Delete(ctx context.Context, ws, id uuid.UUID) error
	// SetBaseWeight sets the STEP's own weight in the split.
	SetBaseWeight(ctx context.Context, ws, stepID uuid.UUID, weight int32) error
	// SentCount is how many messages went out under this variant. It is what
	// makes a delete refusable: deleting a variant that has sent would orphan its
	// results.
	SentCount(ctx context.Context, ws, variantID uuid.UUID) (int64, error)
}

// validate normalizes and checks a variant request.
func (in VariantInput) validate() (VariantInput, error) {
	label := strings.TrimSpace(in.Label)
	if label == "" || len(label) > maxVariantLabel {
		return VariantInput{}, ErrVariantLabel
	}
	if in.Weight < 0 {
		return VariantInput{}, ErrVariantWeight
	}
	return VariantInput{
		Label: label, Weight: in.Weight,
		Subject: in.Subject, BodyText: in.BodyText, BodyHTML: in.BodyHTML,
	}, nil
}

// ListVariants returns a step's alternatives, ordered as the send path orders
// them (by id — see queries/stepvariant.sql on why that order is load-bearing).
func (s *Service) ListVariants(ctx context.Context, ws, stepID uuid.UUID) ([]Variant, error) {
	if _, err := s.store.Get(ctx, ws, stepID); err != nil {
		return nil, err
	}
	return s.variants.ListForStep(ctx, ws, stepID)
}

// CreateVariant adds an alternative to a step.
//
// Unlike creating a STEP, this is allowed on a running campaign. Adding an arm
// changes what future sends contain, which is the same class of change as
// editing a step's body (already permitted live) — not a structural change to
// the sequence's shape. A/B testing a live campaign is the point of the feature;
// requiring a pause to start one would mean pausing to learn anything.
func (s *Service) CreateVariant(ctx context.Context, ws, stepID uuid.UUID, in VariantInput) (Variant, error) {
	valid, err := in.validate()
	if err != nil {
		return Variant{}, err
	}
	step, err := s.store.Get(ctx, ws, stepID)
	if err != nil {
		return Variant{}, err
	}
	existing, err := s.variants.ListForStep(ctx, ws, stepID)
	if err != nil {
		return Variant{}, err
	}
	if len(existing) >= MaxVariantsPerStep {
		return Variant{}, ErrTooManyVariants
	}
	// The invariant is checked against the state this write would PRODUCE, not
	// the state it starts from: adding a weight-0 arm to a step whose base is
	// also 0 must not be the write that makes the step unsendable.
	if !anyEligible(step.VariantWeight, append(weightsOf(existing), valid.Weight)) {
		return Variant{}, ErrNoEligibleVariant
	}
	return s.variants.Create(ctx, ws, stepID, valid)
}

// UpdateVariant edits one alternative's label, weight and copy.
func (s *Service) UpdateVariant(ctx context.Context, ws, variantID uuid.UUID, in VariantInput) (Variant, error) {
	valid, err := in.validate()
	if err != nil {
		return Variant{}, err
	}
	current, siblings, step, err := s.variantContext(ctx, ws, variantID)
	if err != nil {
		return Variant{}, err
	}
	next := make([]int32, 0, len(siblings))
	for _, v := range siblings {
		if v.ID == current.ID {
			next = append(next, valid.Weight)
			continue
		}
		next = append(next, v.Weight)
	}
	if !anyEligible(step.VariantWeight, next) {
		return Variant{}, ErrNoEligibleVariant
	}
	return s.variants.Update(ctx, ws, variantID, valid)
}

// DeleteVariant removes an alternative that has never sent.
//
// A variant with sends is REFUSED rather than deleted. sends.variant_id is ON
// DELETE SET NULL, so deleting one would silently re-attribute its messages to
// the base copy — the losing arm's results would fold into the winner's and make
// the comparison read better than it was. Weight 0 is the supported way to stop
// an arm, and it keeps the numbers intact.
func (s *Service) DeleteVariant(ctx context.Context, ws, variantID uuid.UUID) error {
	current, siblings, step, err := s.variantContext(ctx, ws, variantID)
	if err != nil {
		return err
	}
	sent, err := s.variants.SentCount(ctx, ws, variantID)
	if err != nil {
		return err
	}
	if sent > 0 {
		return ErrVariantHasSends
	}
	remaining := make([]int32, 0, len(siblings))
	for _, v := range siblings {
		if v.ID != current.ID {
			remaining = append(remaining, v.Weight)
		}
	}
	if !anyEligible(step.VariantWeight, remaining) {
		return ErrNoEligibleVariant
	}
	return s.variants.Delete(ctx, ws, variantID)
}

// ErrVariantHasSends is a delete refused because the variant has delivery
// history. Declared here rather than with the others so its explanation stays
// next to the rule it enforces.
var ErrVariantHasSends = errors.New("this variant has already sent; set its weight to 0 instead of deleting it")

// SetBaseWeight sets the weight of the step's OWN copy in the split — the "A"
// side. Zero retires it without deleting what it said, which is how a winning
// variant is promoted.
func (s *Service) SetBaseWeight(ctx context.Context, ws, stepID uuid.UUID, weight int32) error {
	if weight < 0 {
		return ErrVariantWeight
	}
	if _, err := s.store.Get(ctx, ws, stepID); err != nil {
		return err
	}
	siblings, err := s.variants.ListForStep(ctx, ws, stepID)
	if err != nil {
		return err
	}
	if !anyEligible(weight, weightsOf(siblings)) {
		return ErrNoEligibleVariant
	}
	return s.variants.SetBaseWeight(ctx, ws, stepID, weight)
}

// variantContext loads a variant together with the step and siblings the weight
// invariant is evaluated against. Ownership falls out of the workspace-pinned
// reads: a foreign id is ErrVariantNotFound, never a partial answer.
func (s *Service) variantContext(ctx context.Context, ws, variantID uuid.UUID) (Variant, []Variant, gen.SequenceStep, error) {
	current, err := s.variants.Get(ctx, ws, variantID)
	if err != nil {
		return Variant{}, nil, gen.SequenceStep{}, err
	}
	siblings, err := s.variants.ListForStep(ctx, ws, current.StepID)
	if err != nil {
		return Variant{}, nil, gen.SequenceStep{}, err
	}
	step, err := s.store.Get(ctx, ws, current.StepID)
	if err != nil {
		return Variant{}, nil, gen.SequenceStep{}, err
	}
	return current, siblings, step, nil
}

// anyEligible reports whether at least one arm can be selected — the base copy
// or any alternative. It is the whole weight invariant: a step where everything
// is 0 cannot send, and abtest.Select would (correctly) refuse it at send time,
// one enrollment at a time, long after the edit that caused it.
func anyEligible(baseWeight int32, variantWeights []int32) bool {
	if baseWeight > 0 {
		return true
	}
	for _, w := range variantWeights {
		if w > 0 {
			return true
		}
	}
	return false
}

func weightsOf(variants []Variant) []int32 {
	out := make([]int32, 0, len(variants))
	for _, v := range variants {
		out = append(out, v.Weight)
	}
	return out
}
