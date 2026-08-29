package inprocess

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/realtime"
)

// PublishRealtime fans one event out to the workspace's connected browsers.
//
// The worker runs in a separate process from the API (cmd/worker vs
// cmd/inroad), so an in-process Go channel reaches no browser: this goes
// through Redis, which is what the hub is for.
//
// A client built WITHOUT WithRealtime has no hub and returns nil here — a
// deliberate no-op, not a silent failure. The distinction matters: browsers
// then learn about the change on their next poll, which is the pre-socket
// behaviour, so nothing is broken by omitting the option. Reporting an error
// instead would make every caller's log noisy in a configuration where the
// absence is intentional.
func (c client) PublishRealtime(ctx context.Context, in coreapi.RealtimeEventInput) error {
	if c.realtime == nil {
		return nil
	}

	workspaceID, err := uuid.Parse(in.WorkspaceID)
	if err != nil {
		return fmt.Errorf("realtime: workspace id: %w", err)
	}

	// Marshalled here rather than passed as a map into the hub, so the hub's
	// Envelope keeps json.RawMessage and writes bytes once per event instead of
	// re-marshalling per connection.
	var data json.RawMessage
	if in.Data != nil {
		encoded, err := json.Marshal(in.Data)
		if err != nil {
			return fmt.Errorf("realtime: encode data: %w", err)
		}
		data = encoded
	}

	_, err = c.realtime.Publish(ctx, workspaceID, realtime.Envelope{
		Type:    in.Type,
		Subject: realtime.Subject{Kind: in.SubjectKind, ID: in.SubjectID},
		At:      in.OccurredAt,
		ActorID: in.ActorID,
		Data:    data,
	})
	if err != nil {
		return fmt.Errorf("realtime: publish: %w", err)
	}
	return nil
}

// Compile-time proof the in-process client satisfies the optional capability,
// so a worker's type assertion for it cannot silently start failing.
var _ coreapi.RealtimeClient = client{}
