package events

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/realtime"
)

// hubPublisher adapts the platform fan-out hub to the Publisher seam.
//
// It lives here rather than in a domain because this is the ONE place that may
// know both sides: every app/* service depends on Publisher alone, and this
// file is what the composition root passes in. Putting it beside the interface
// keeps the adapter and its contract from drifting.
type hubPublisher struct {
	hub realtime.Publisher
}

// NewHubPublisher wires the realtime hub behind the Publisher seam.
//
// Returns nil (rather than a wrapper around nil) when hub is nil, so a caller
// that has no hub hands services a nil Publisher — which Emit already treats as
// "realtime disabled". Wrapping would produce a non-nil interface holding a nil
// pointer, the classic Go trap where `p == nil` is false and the no-op path is
// silently skipped.
func NewHubPublisher(hub realtime.Publisher) Publisher {
	if hub == nil {
		return nil
	}
	return hubPublisher{hub: hub}
}

func (p hubPublisher) Publish(ctx context.Context, workspaceID string, ev Event) error {
	id, err := uuid.Parse(workspaceID)
	if err != nil {
		return fmt.Errorf("events: workspace id: %w", err)
	}

	// Marshalled here so the hub stores and writes bytes once per event rather
	// than re-marshalling per connection.
	var data json.RawMessage
	if ev.Data != nil {
		encoded, err := json.Marshal(ev.Data)
		if err != nil {
			return fmt.Errorf("events: encode data: %w", err)
		}
		data = encoded
	}

	if _, err := p.hub.Publish(ctx, id, realtime.Envelope{
		Type:    ev.Type,
		Subject: realtime.Subject{Kind: ev.SubjectKind, ID: ev.SubjectID},
		At:      ev.OccurredAt,
		ActorID: ev.ActorID,
		Data:    data,
	}); err != nil {
		return fmt.Errorf("events: publish: %w", err)
	}
	return nil
}
