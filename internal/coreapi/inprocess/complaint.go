package inprocess

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/deliverability"
	"github.com/inroad/inroad/internal/coreapi"
)

// IngestComplaint records one complaint the inbox poller parsed out of an inbound
// RFC 5965 feedback report.
//
// It satisfies coreapi.DeliverabilityComplaintClient, consumed by the poller
// through a type assertion rather than through Client — the maintenance.Cleaner
// pattern, for the same reason EvaluateCampaignBreaker uses it.
//
// All the judgement is delegated to the SAME app/deliverability service the HTTP
// ingest endpoint calls, exactly as EvaluateCampaignBreaker delegates the breaker.
// That is the point of this method: the suppression, the idempotency on
// provider_event_id, the score and the breaker evaluation are one implementation,
// so a complaint that arrived as mail and a complaint POSTed by an SES subscriber
// cannot be treated differently.
//
// A duplicate is NOT an error. The service reports it, and the caller has nothing
// to do about it: the whole point of the idempotency key is that a re-polled or
// redelivered report writes nothing and causes nothing.
func (c client) IngestComplaint(ctx context.Context, in coreapi.ComplaintInput) error {
	ws, err := uuid.Parse(in.WorkspaceID)
	if err != nil {
		return fmt.Errorf("ingest complaint: workspace id: %w", err)
	}
	// Required, not optional (see coreapi.ComplaintInput): the send is what
	// attributes the complaint to a campaign, and a complaint that reaches no
	// campaign reaches no breaker. A caller that could not resolve one was supposed
	// to decline the report, so an empty value here is a bug rather than a case.
	if in.SendID == "" {
		return errors.New("ingest complaint: send id is required")
	}
	sendID, err := uuid.Parse(in.SendID)
	if err != nil {
		return fmt.Errorf("ingest complaint: send id: %w", err)
	}
	_, err = c.breaker.Ingest(ctx, ws, deliverability.EventInput{
		Kind:            deliverability.EventKindComplaint,
		Email:           in.Email,
		ProviderEventID: in.ProviderEventID,
		SendID:          &sendID,
	})
	if err != nil {
		// deliverability.ErrInvalid is the service's "this input is not acceptable
		// and never will be" — today, reachable here only via a send row with a
		// blank contact address. It is translated to the seam's own sentinel rather
		// than passed through, because the worker cannot import an app package to
		// recognise it (workers reach the control plane ONLY through coreapi) and
		// must not have to: which inputs are permanently invalid is the control
		// plane's business, and "permanent vs transient" is the only part of that
		// answer the caller can act on.
		if errors.Is(err, deliverability.ErrInvalid) {
			return fmt.Errorf("%w: %w", coreapi.ErrInvalidComplaint, err)
		}
		return fmt.Errorf("ingest complaint: %w", err)
	}
	return nil
}
