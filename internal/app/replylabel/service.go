// Package replylabel owns the reply-label taxonomy: the user-editable set of
// buckets an inbound reply is classified into, and — through each label's role
// flags — what automation that classification triggers.
//
// The classifier (internal/platform/replyclassify) still produces a stable
// machine key; this domain decides what that key MEANS. Seven builtin labels
// are seeded per workspace by migration 000047 and reproduce the pre-000047
// hardcoded behaviour exactly; a workspace may add its own on top.
package replylabel

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

var (
	// ErrNotFound is returned when the workspace has no such label.
	ErrNotFound = errors.New("replylabel: not found")
	// ErrValidation is returned for a caller-fixable input problem.
	ErrValidation = errors.New("replylabel: invalid")
	// ErrConflict is returned when the label exists but the operation is
	// refused on it (a duplicate key, or deleting a builtin).
	ErrConflict = errors.New("replylabel: conflict")
)

// maxLabels bounds the taxonomy. Labels are a small, human-curated set that a
// human picks from in a dropdown; a workspace past this has a data problem, not
// a pagination need (the same reasoning as crm's maxPipelines).
const maxLabels = 100

var (
	colorPattern    = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
	keyPattern      = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	nonKeyCharacter = regexp.MustCompile(`[^a-z0-9]+`)
)

// Input is the editable surface of a label. The key is derived on create and
// immutable thereafter, so it is deliberately absent here.
type Input struct {
	Label             string
	Color             string
	StopsEnrollment   bool
	IsAutomated       bool
	SuppressesContact bool
	CapturesDeal      bool
	DefersEnrollment  bool
}

type Service struct{ store Store }

func NewService(store Store) *Service { return &Service{store: store} }

func (s *Service) List(ctx context.Context, ws uuid.UUID) ([]gen.ReplyLabel, error) {
	return s.store.List(ctx, ws)
}

func (s *Service) Get(ctx context.Context, ws, id uuid.UUID) (gen.ReplyLabel, error) {
	label, err := s.store.Get(ctx, ws, id)
	if err != nil {
		return gen.ReplyLabel{}, translateRead(err)
	}
	return label, nil
}

// Resolve maps a classifier key to its label row. ok=false means no label in
// this workspace claims the key — the caller must fall back to its pre-taxonomy
// behaviour rather than inventing a label, because a key can outlive the custom
// label that defined it (enrollment rows keep the raw key on purpose).
func (s *Service) Resolve(ctx context.Context, ws uuid.UUID, key string) (gen.ReplyLabel, bool, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return gen.ReplyLabel{}, false, nil
	}
	return s.store.GetByKey(ctx, ws, key)
}

func (s *Service) Create(ctx context.Context, ws uuid.UUID, in Input) (gen.ReplyLabel, error) {
	if err := validate(&in); err != nil {
		return gen.ReplyLabel{}, err
	}
	existing, err := s.store.List(ctx, ws)
	if err != nil {
		return gen.ReplyLabel{}, err
	}
	if len(existing) >= maxLabels {
		return gen.ReplyLabel{}, fmt.Errorf("%w: a workspace may have at most %d reply labels", ErrConflict, maxLabels)
	}
	key := deriveKey(in.Label)
	if key == "" {
		return gen.ReplyLabel{}, fmt.Errorf("%w: label must contain a letter or number", ErrValidation)
	}
	label, err := s.store.Create(ctx, ws, key, in)
	if err != nil {
		return gen.ReplyLabel{}, translateWrite(err)
	}
	return label, nil
}

// Update edits the display fields and role flags. The key stays fixed even for
// a custom label: historical sequence_enrollments.reply_class values name it as
// free text, so renaming the key would orphan them.
func (s *Service) Update(ctx context.Context, ws, id uuid.UUID, in Input) (gen.ReplyLabel, error) {
	if err := validate(&in); err != nil {
		return gen.ReplyLabel{}, err
	}
	label, err := s.store.Update(ctx, ws, id, in)
	if err != nil {
		return gen.ReplyLabel{}, translateRead(translateWrite(err))
	}
	return label, nil
}

// Reorder rewrites positions to the given order. Every one of the workspace's
// labels must appear exactly once, so a stale client list cannot silently drop
// a label to position 0 alongside another.
func (s *Service) Reorder(ctx context.Context, ws uuid.UUID, ids []uuid.UUID) ([]gen.ReplyLabel, error) {
	current, err := s.store.List(ctx, ws)
	if err != nil {
		return nil, err
	}
	if len(ids) != len(current) {
		return nil, fmt.Errorf("%w: reorder must list all %d labels exactly once", ErrValidation, len(current))
	}
	seen := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			return nil, fmt.Errorf("%w: reorder must list all %d labels exactly once", ErrValidation, len(current))
		}
		seen[id] = true
	}
	for _, label := range current {
		if !seen[label.ID] {
			return nil, fmt.Errorf("%w: reorder is missing label %s", ErrValidation, label.ID)
		}
	}
	if err := s.store.Reorder(ctx, ws, ids); err != nil {
		return nil, err
	}
	return s.store.List(ctx, ws)
}

// Delete removes a CUSTOM label. A builtin is refused here, ahead of the DB
// (mirroring crm.Service.DeleteStage), so the caller gets a 409 that says why
// rather than the 404 a guarded DELETE matching zero rows would produce.
//
// Deleting a custom label deliberately leaves historical rows carrying its
// orphaned key: an enrollment's recorded classification is a fact about what
// happened, not a foreign key, and readers degrade to the raw key.
func (s *Service) Delete(ctx context.Context, ws, id uuid.UUID) error {
	label, err := s.store.Get(ctx, ws, id)
	if err != nil {
		return translateRead(err)
	}
	if label.IsBuiltin {
		return fmt.Errorf("%w: %q is a builtin label and cannot be deleted; edit its name or turn its flags off instead", ErrConflict, label.Key)
	}
	deleted, err := s.store.Delete(ctx, ws, id)
	if err != nil {
		return err
	}
	if !deleted {
		return ErrNotFound
	}
	return nil
}

// validate normalizes and checks the editable fields. Flag combinations are NOT
// constrained beyond the one that is genuinely contradictory: an automated
// label (machine-generated mail) that also stops the enrollment would defeat
// the whole point of the automated family.
func validate(in *Input) error {
	in.Label = strings.TrimSpace(in.Label)
	in.Color = strings.TrimSpace(in.Color)
	if l := len([]rune(in.Label)); l < 1 || l > 80 {
		return fmt.Errorf("%w: label must be between 1 and 80 characters", ErrValidation)
	}
	if !colorPattern.MatchString(in.Color) {
		return fmt.Errorf("%w: color must be a #RRGGBB hex value", ErrValidation)
	}
	if in.IsAutomated && in.StopsEnrollment {
		return fmt.Errorf("%w: an automated label must not stop the enrollment", ErrValidation)
	}
	if in.DefersEnrollment && !in.IsAutomated {
		return fmt.Errorf("%w: only an automated label can defer the enrollment", ErrValidation)
	}
	return nil
}

// deriveKey slugifies a label into the stable machine key, the same derivation
// crm.Service.CreateStage uses. Returns "" when nothing survives.
func deriveKey(label string) string {
	key := strings.Trim(nonKeyCharacter.ReplaceAllString(strings.ToLower(label), "_"), "_")
	if len(key) > 64 {
		key = strings.Trim(key[:64], "_")
	}
	if !keyPattern.MatchString(key) {
		return ""
	}
	return key
}

// translateRead maps "no such row" onto ErrNotFound and leaves everything else
// alone, so a handler distinguishes 404 from 500.
func translateRead(err error) error {
	if isNoRows(err) {
		return ErrNotFound
	}
	return err
}

// translateWrite maps the unique-key violation onto ErrConflict. Two labels
// whose names slugify to the same key is a caller mistake, not a server fault.
func translateWrite(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%w: a reply label with that name already exists", ErrConflict)
	}
	return err
}

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
