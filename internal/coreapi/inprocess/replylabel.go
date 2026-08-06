package inprocess

import (
	"context"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/replylabel"
	"github.com/inroad/inroad/internal/coreapi"
)

// ResolveReplyLabel resolves a classified reply key to the workspace's label
// row, flattened to the role flags the execution plane acts on.
//
// ok=false means no label claims the key — a custom label deleted after the
// enrollment recorded it, or a key the workspace never had. The poller falls
// back to its pre-taxonomy switch in that case, so a missing label degrades to
// today's behaviour rather than to no automation at all.
func (c client) ResolveReplyLabel(ctx context.Context, workspaceID, key string) (coreapi.ReplyLabel, bool, error) {
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return coreapi.ReplyLabel{}, false, err
	}
	row, ok, err := replyLabelService(c).Resolve(ctx, ws, key)
	if err != nil || !ok {
		return coreapi.ReplyLabel{}, false, err
	}
	return coreapi.ReplyLabel{
		Key:               row.Key,
		StopsEnrollment:   row.StopsEnrollment,
		IsAutomated:       row.IsAutomated,
		SuppressesContact: row.SuppressesContact,
		CapturesDeal:      row.CapturesDeal,
		DefersEnrollment:  row.DefersEnrollment,
	}, true, nil
}

// replyLabelService builds the reply-label service over this client's pool. It
// is constructed per call, like the CRM service in crm.go: the service is
// stateless and the pool is the only dependency, so caching it would buy
// nothing but a lifetime to manage.
func replyLabelService(c client) *replylabel.Service {
	return replylabel.NewService(replylabel.NewPgStore(c.pool))
}

var _ coreapi.ReplyLabelClient = client{}
