package inbox

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// ErrLabelNameTaken is returned when a label name already exists in the
// workspace (case-insensitively). Its own sentinel rather than a generic
// validation error because the picker's search-or-create flow acts on it:
// finding the existing label is the right response, not showing an error.
var ErrLabelNameTaken = errors.New("inbox: a label with that name already exists")

// Label bounds. A name has to fit a chip in a dense list, so the cap is about
// what is legible rather than what Postgres can store.
const (
	MaxLabelNameLength = 40
	// MaxLabelsPerWorkspace stops a runaway integration turning the picker into
	// an unusable wall and the rail into an unbounded render.
	MaxLabelsPerWorkspace = 200
)

// ErrTooManyLabels is returned once a workspace holds MaxLabelsPerWorkspace.
var ErrTooManyLabels = fmt.Errorf("inbox: a workspace may hold at most %d labels", MaxLabelsPerWorkspace)

// hexColor matches the one colour format the API accepts. Validated here
// rather than by a CHECK constraint so the rule can change without a
// migration — see the migration's own comment.
var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// DefaultLabelColor is applied when a caller supplies none, matching the
// column default so a label created either way looks the same.
const DefaultLabelColor = "#94a3b8"

// Label is one operator-created thread label.
//
// Distinct from ReplyLabelRef, which is the CLASSIFIER's taxonomy resolved for
// display: that one is written by the model onto last_reply_class and cannot be
// assigned by hand, while this one is created and applied only by hand. See the
// migration's comment for why they are not one table.
type Label struct {
	ID          uuid.UUID
	WorkspaceID uuid.UUID
	Name        string
	Color       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// LabelCount is one label's thread counts, for the rail.
type LabelCount struct {
	LabelID uuid.UUID
	Total   int64
	Unread  int64
}

// LabelStore is the label half of this domain's persistence. Its own interface,
// like SnoozeStore, so a caller uninterested in labels need not implement it.
type LabelStore interface {
	CreateLabel(ctx context.Context, workspaceID uuid.UUID, name, color string) (Label, error)
	ListLabels(ctx context.Context, workspaceID uuid.UUID) ([]Label, error)
	GetLabel(ctx context.Context, workspaceID, id uuid.UUID) (Label, error)
	FindLabelByName(ctx context.Context, workspaceID uuid.UUID, name string) (Label, error)
	UpdateLabel(ctx context.Context, workspaceID, id uuid.UUID, name, color string) (Label, error)
	DeleteLabel(ctx context.Context, workspaceID, id uuid.UUID) error
	AssignLabel(ctx context.Context, workspaceID, threadID, labelID uuid.UUID) error
	UnassignLabel(ctx context.Context, workspaceID, threadID, labelID uuid.UUID) error
	LabelsForThread(ctx context.Context, workspaceID, threadID uuid.UUID) ([]Label, error)
	// LabelsForThreads returns every listed thread's labels in ONE query,
	// keyed by thread id — the list view renders a page at a time, and a query
	// per row would be N+1.
	LabelsForThreads(ctx context.Context, workspaceID uuid.UUID, threadIDs []uuid.UUID) (map[uuid.UUID][]Label, error)
	CountThreadsByLabel(ctx context.Context, workspaceID uuid.UUID) ([]LabelCount, error)
}

// normalizeLabelName trims surrounding whitespace and collapses internal runs
// to single spaces, so " Big   Deals " and "Big Deals" are the same label
// rather than two that look identical in the picker.
func normalizeLabelName(name string) string {
	return strings.Join(strings.Fields(name), " ")
}

// validateLabel normalizes and bounds a label's fields, returning the values to
// store.
func validateLabel(name, color string) (string, string, error) {
	name = normalizeLabelName(name)
	if name == "" {
		return "", "", fmt.Errorf("%w: label name is required", ErrValidation)
	}
	// Counted in runes, not bytes: a 40-character name in a non-Latin script
	// would otherwise be rejected for being "too long" when it is not.
	if len([]rune(name)) > MaxLabelNameLength {
		return "", "", fmt.Errorf("%w: label name must be at most %d characters", ErrValidation, MaxLabelNameLength)
	}
	if color == "" {
		color = DefaultLabelColor
	}
	if !hexColor.MatchString(color) {
		return "", "", fmt.Errorf("%w: color must be a hex value like #3b82f6", ErrValidation)
	}
	return name, strings.ToLower(color), nil
}

// CreateLabel adds a label to the workspace.
//
// A duplicate name surfaces as ErrLabelNameTaken rather than a raw unique
// violation, so the picker's search-or-create path can resolve it to the
// existing label. The uniqueness check is left to the DATABASE (via the unique
// index) rather than a pre-flight SELECT: two members creating "Invoices" at
// once would both pass a pre-flight check, and only the constraint can
// actually decide.
func (s *Service) CreateLabel(ctx context.Context, workspaceID uuid.UUID, name, color string) (Label, error) {
	name, color, err := validateLabel(name, color)
	if err != nil {
		return Label{}, err
	}
	existing, err := s.labels.ListLabels(ctx, workspaceID)
	if err != nil {
		return Label{}, err
	}
	if len(existing) >= MaxLabelsPerWorkspace {
		return Label{}, ErrTooManyLabels
	}
	return s.labels.CreateLabel(ctx, workspaceID, name, color)
}

// EnsureLabel is the picker's search-or-create: returns the existing label with
// that name, or creates it.
//
// Written as create-then-recover rather than find-then-create because that is
// the only ordering safe under concurrency: two members typing the same new
// name both see "not found", both insert, and one loses — at which point the
// loser resolves to the winner's row instead of failing.
func (s *Service) EnsureLabel(ctx context.Context, workspaceID uuid.UUID, name, color string) (Label, error) {
	label, err := s.CreateLabel(ctx, workspaceID, name, color)
	if err == nil {
		return label, nil
	}
	if !errors.Is(err, ErrLabelNameTaken) {
		return Label{}, err
	}
	return s.labels.FindLabelByName(ctx, workspaceID, normalizeLabelName(name))
}

// ListLabels returns the workspace's labels, alphabetically.
func (s *Service) ListLabels(ctx context.Context, workspaceID uuid.UUID) ([]Label, error) {
	return s.labels.ListLabels(ctx, workspaceID)
}

// UpdateLabel renames or recolours a label.
func (s *Service) UpdateLabel(ctx context.Context, workspaceID, id uuid.UUID, name, color string) (Label, error) {
	name, color, err := validateLabel(name, color)
	if err != nil {
		return Label{}, err
	}
	return s.labels.UpdateLabel(ctx, workspaceID, id, name, color)
}

// DeleteLabel removes a label and unfiles every thread it was on (the join
// rows cascade). That IS what deleting a label means, so it needs no
// confirmation step here — the UI asks.
func (s *Service) DeleteLabel(ctx context.Context, workspaceID, id uuid.UUID) error {
	return s.labels.DeleteLabel(ctx, workspaceID, id)
}

// AssignLabel puts a label on a thread. Idempotent: applying an
// already-applied label succeeds silently (the composite PK makes it a no-op).
//
// Both the thread and the label are verified first, so a foreign id 404s
// rather than reaching the insert — where a foreign-key violation would either
// leak the row's existence or, worse, file another workspace's thread.
func (s *Service) AssignLabel(ctx context.Context, workspaceID, threadID, labelID uuid.UUID) error {
	if _, err := s.store.GetThread(ctx, workspaceID, threadID); err != nil {
		return err
	}
	if _, err := s.labels.GetLabel(ctx, workspaceID, labelID); err != nil {
		return err
	}
	return s.labels.AssignLabel(ctx, workspaceID, threadID, labelID)
}

// UnassignLabel takes a label off a thread. ErrNotFound when it was not
// applied, which also covers an unknown thread or label — neither can have an
// assignment, so this needs no separate existence check.
func (s *Service) UnassignLabel(ctx context.Context, workspaceID, threadID, labelID uuid.UUID) error {
	return s.labels.UnassignLabel(ctx, workspaceID, threadID, labelID)
}

// labelsForThread resolves one thread's labels for display, nil-safe against an
// unconfigured label store (the dependency is optional, like snoozes).
func (s *Service) labelsForThread(ctx context.Context, workspaceID, threadID uuid.UUID) ([]Label, error) {
	if s.labels == nil {
		return nil, nil
	}
	return s.labels.LabelsForThread(ctx, workspaceID, threadID)
}

// --- PgStore ---

func (s *PgStore) CreateLabel(ctx context.Context, workspaceID uuid.UUID, name, color string) (Label, error) {
	row, err := s.q.CreateInboxLabel(ctx, gen.CreateInboxLabelParams{
		WorkspaceID: workspaceID, Name: name, Color: color,
	})
	if err != nil {
		return Label{}, mapLabelNameConflict(err)
	}
	return labelFromRow(row), nil
}

func (s *PgStore) ListLabels(ctx context.Context, workspaceID uuid.UUID) ([]Label, error) {
	rows, err := s.q.ListInboxLabels(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	return labelsFromRows(rows), nil
}

func (s *PgStore) GetLabel(ctx context.Context, workspaceID, id uuid.UUID) (Label, error) {
	row, err := s.q.GetInboxLabel(ctx, gen.GetInboxLabelParams{ID: id, WorkspaceID: workspaceID})
	if err != nil {
		return Label{}, mapNotFound(err)
	}
	return labelFromRow(row), nil
}

func (s *PgStore) FindLabelByName(ctx context.Context, workspaceID uuid.UUID, name string) (Label, error) {
	row, err := s.q.FindInboxLabelByName(ctx, gen.FindInboxLabelByNameParams{
		WorkspaceID: workspaceID, Name: name,
	})
	if err != nil {
		return Label{}, mapNotFound(err)
	}
	return labelFromRow(row), nil
}

func (s *PgStore) UpdateLabel(ctx context.Context, workspaceID, id uuid.UUID, name, color string) (Label, error) {
	row, err := s.q.UpdateInboxLabel(ctx, gen.UpdateInboxLabelParams{
		ID: id, WorkspaceID: workspaceID, Name: name, Color: color,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Label{}, ErrNotFound
		}
		return Label{}, mapLabelNameConflict(err)
	}
	return labelFromRow(row), nil
}

func (s *PgStore) DeleteLabel(ctx context.Context, workspaceID, id uuid.UUID) error {
	n, err := s.q.DeleteInboxLabel(ctx, gen.DeleteInboxLabelParams{ID: id, WorkspaceID: workspaceID})
	return affected(n, err)
}

func (s *PgStore) AssignLabel(ctx context.Context, workspaceID, threadID, labelID uuid.UUID) error {
	return s.q.AssignInboxThreadLabel(ctx, gen.AssignInboxThreadLabelParams{
		ThreadID: threadID, LabelID: labelID, WorkspaceID: workspaceID,
	})
}

func (s *PgStore) UnassignLabel(ctx context.Context, workspaceID, threadID, labelID uuid.UUID) error {
	n, err := s.q.UnassignInboxThreadLabel(ctx, gen.UnassignInboxThreadLabelParams{
		ThreadID: threadID, LabelID: labelID, WorkspaceID: workspaceID,
	})
	return affected(n, err)
}

func (s *PgStore) LabelsForThread(ctx context.Context, workspaceID, threadID uuid.UUID) ([]Label, error) {
	rows, err := s.q.ListLabelsForInboxThread(ctx, gen.ListLabelsForInboxThreadParams{
		ThreadID: threadID, WorkspaceID: workspaceID,
	})
	if err != nil {
		return nil, err
	}
	return labelsFromRows(rows), nil
}

func (s *PgStore) LabelsForThreads(ctx context.Context, workspaceID uuid.UUID, threadIDs []uuid.UUID) (map[uuid.UUID][]Label, error) {
	// An empty page must not issue a query with an empty array — it would be a
	// round trip that cannot match anything.
	if len(threadIDs) == 0 {
		return map[uuid.UUID][]Label{}, nil
	}
	rows, err := s.q.ListLabelsForInboxThreads(ctx, gen.ListLabelsForInboxThreadsParams{
		WorkspaceID: workspaceID, ThreadIds: threadIDs,
	})
	if err != nil {
		return nil, err
	}
	byThread := make(map[uuid.UUID][]Label, len(threadIDs))
	for _, r := range rows {
		byThread[r.ThreadID] = append(byThread[r.ThreadID], Label{
			ID: r.ID, WorkspaceID: r.WorkspaceID, Name: r.Name, Color: r.Color,
			CreatedAt: r.CreatedAt.Time, UpdatedAt: r.UpdatedAt.Time,
		})
	}
	return byThread, nil
}

func (s *PgStore) CountThreadsByLabel(ctx context.Context, workspaceID uuid.UUID) ([]LabelCount, error) {
	rows, err := s.q.CountInboxThreadsByLabel(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]LabelCount, len(rows))
	for i, r := range rows {
		out[i] = LabelCount{LabelID: r.LabelID, Total: r.Total, Unread: r.Unread}
	}
	return out, nil
}

func labelFromRow(row gen.InboxLabel) Label {
	return Label{
		ID:          row.ID,
		WorkspaceID: row.WorkspaceID,
		Name:        row.Name,
		Color:       row.Color,
		CreatedAt:   row.CreatedAt.Time,
		UpdatedAt:   row.UpdatedAt.Time,
	}
}

func labelsFromRows(rows []gen.InboxLabel) []Label {
	out := make([]Label, len(rows))
	for i, r := range rows {
		out[i] = labelFromRow(r)
	}
	return out
}

// pgUniqueViolation is Postgres' SQLSTATE for a unique-constraint breach.
const pgUniqueViolation = "23505"

// mapLabelNameConflict turns the unique index's violation into the domain
// sentinel, so callers never need to inspect a driver error. Any other error
// passes through unchanged.
func mapLabelNameConflict(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
		return ErrLabelNameTaken
	}
	return err
}
