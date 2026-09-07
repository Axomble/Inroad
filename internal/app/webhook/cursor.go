package webhook

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Keyset cursor for the delivery log (GET /webhook-endpoints/{id}/deliveries).
//
// Payload is base64url (unpadded, so no query-string escaping) of
//
//	version | kind | created_at RFC3339Nano | id
//
// Neither RFC3339Nano nor a UUID can contain the '|' delimiter, so the codec
// splits on a fixed field count. The encoding is opaque by contract: clients
// round-trip it untouched. Mirrors internal/app/deadletter/cursor.go; the two
// stay separate hand-rolled codecs for the reason its doc gives.
const (
	deliveryCursorVersion = "1"
	deliveryCursorKind    = "webhook_deliveries"
	deliveryCursorFields  = 4
)

// DeliveryCursor is a decoded position in the delivery-log ordering: the sort
// key plus the id that breaks ties (created_at defaults to now() and a fan-out
// burst shares one value).
type DeliveryCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

func encodeDeliveryCursor(c DeliveryCursor) string {
	payload := strings.Join([]string{
		deliveryCursorVersion,
		deliveryCursorKind,
		c.CreatedAt.UTC().Format(time.RFC3339Nano),
		c.ID.String(),
	}, "|")
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

// decodeDeliveryCursor parses a token minted for this listing. Every failure is
// ErrBadCursor (400): falling back to page one would read to an operator as the
// list losing its place, with no trace.
func decodeDeliveryCursor(raw string) (DeliveryCursor, error) {
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return DeliveryCursor{}, fmt.Errorf("%w: not a token this server minted", ErrBadCursor)
	}
	parts := strings.Split(string(payload), "|")
	if len(parts) != deliveryCursorFields || parts[0] != deliveryCursorVersion || parts[1] != deliveryCursorKind {
		return DeliveryCursor{}, fmt.Errorf("%w: not a webhook-delivery cursor", ErrBadCursor)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, parts[2])
	if err != nil {
		return DeliveryCursor{}, fmt.Errorf("%w: position is not a timestamp", ErrBadCursor)
	}
	id, err := uuid.Parse(parts[3])
	if err != nil {
		return DeliveryCursor{}, fmt.Errorf("%w: position is not a row id", ErrBadCursor)
	}
	return DeliveryCursor{CreatedAt: createdAt, ID: id}, nil
}
