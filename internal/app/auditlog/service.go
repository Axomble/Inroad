package auditlog

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/audit"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Page size bounds. Over-large requests are capped rather than refused: a
// page size is a convenience, and the capped page is still the page asked for.
const (
	defaultLimit = 50
	maxLimit     = 200
)

// ErrInvalidFilter is a filter value the list cannot honour (400).
var ErrInvalidFilter = errors.New("invalid audit filter")

// actionPrefixPattern admits a whole dotted name or any leading run of its
// segments ("campaign", "campaign.paused") — nothing that could be read as a
// pattern by the query.
var actionPrefixPattern = regexp.MustCompile(`^[a-z][a-z_]*(\.[a-z][a-z_]*)*$`)

// Filter narrows the list. Zero values mean "any".
type Filter struct {
	// ActionPrefix matches a full action or a dotted prefix of one on a
	// segment boundary: "campaign" matches every campaign.* action.
	ActionPrefix string
	ActorType    string
	ActorID      string
	// ActorUserID matches every event done on a user's authority — directly,
	// through their api keys, or by an agent they delegated to.
	ActorUserID *uuid.UUID
	TargetType  string
	TargetID    string
	// Since is inclusive and Until exclusive.
	Since *time.Time
	Until *time.Time
}

func (f Filter) validate() error {
	switch {
	case f.ActionPrefix != "" && (len(f.ActionPrefix) > 100 || !actionPrefixPattern.MatchString(f.ActionPrefix)):
		return fmt.Errorf("%w: action must be a dotted action name or prefix", ErrInvalidFilter)
	case f.ActorType != "" && !audit.IsKnownActorType(audit.ActorType(f.ActorType)):
		return fmt.Errorf("%w: unknown actor_type %q", ErrInvalidFilter, f.ActorType)
	case len(f.ActorID) > 200, len(f.TargetID) > 200, len(f.TargetType) > 50:
		return fmt.Errorf("%w: filter value too long", ErrInvalidFilter)
	case f.Since != nil && f.Until != nil && !f.Since.Before(*f.Until):
		return fmt.Errorf("%w: since must be before until", ErrInvalidFilter)
	}
	return nil
}

// ListInput is one page request.
type ListInput struct {
	Filter Filter
	Cursor string
	// Limit <= 0 takes the default; above maxLimit is capped.
	Limit int
}

// Page is one page of events, newest first. NextCursor is "" on the last page.
type Page struct {
	Events     []gen.ListAuditEventsRow
	NextCursor string
}

// Service implements the audit viewer's read rules over a Store.
type Service struct{ store Store }

// NewService builds a Service over store.
func NewService(store Store) *Service { return &Service{store: store} }

// List returns one page of ws's audit events. ws comes from the caller's JWT
// (the handler), never from the request.
func (s *Service) List(ctx context.Context, ws uuid.UUID, in ListInput) (Page, error) {
	if err := in.Filter.validate(); err != nil {
		return Page{}, err
	}
	limit := in.Limit
	switch {
	case limit <= 0:
		limit = defaultLimit
	case limit > maxLimit:
		limit = maxLimit
	}
	params := gen.ListAuditEventsParams{
		WorkspaceID:  ws,
		ActionPrefix: in.Filter.ActionPrefix,
		ActorType:    in.Filter.ActorType,
		ActorID:      in.Filter.ActorID,
		ActorUserID:  pgUUID(in.Filter.ActorUserID),
		TargetType:   in.Filter.TargetType,
		TargetID:     in.Filter.TargetID,
		Since:        pgTime(in.Filter.Since),
		Until:        pgTime(in.Filter.Until),
		// One extra row answers "is there a next page" without a COUNT.
		PageLimit: int32(limit + 1), // limit is bounded to [1, maxLimit] above
	}
	if in.Cursor != "" {
		pos, err := decodeCursor(in.Filter, in.Cursor)
		if err != nil {
			return Page{}, err
		}
		params.Seek = true
		params.CursorTime = pgtype.Timestamptz{Time: pos.CreatedAt, Valid: true}
		params.CursorID = pos.ID
	}
	rows, err := s.store.List(ctx, params)
	if err != nil {
		return Page{}, fmt.Errorf("auditlog: list: %w", err)
	}
	page := Page{Events: rows}
	if len(rows) > limit {
		page.Events = rows[:limit]
		last := page.Events[limit-1]
		page.NextCursor = encodeCursor(in.Filter, position{CreatedAt: last.CreatedAt.Time, ID: last.ID})
	}
	return page, nil
}

func pgUUID(id *uuid.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}

func pgTime(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}
