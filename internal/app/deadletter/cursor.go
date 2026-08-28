package deadletter

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Keyset cursors for the dead-letter triage list.
//
// A cursor names one row's position in ONE listing's ordering. The payload is
// base64url (unpadded, so it needs no escaping in a query string) of
//
//	version | kind | status | created_at RFC3339Nano | id
//
// and every field before the timestamp comes from a closed set, so none of them
// can contain the delimiter: version and kind are constants here, and status is
// one of the three CHECK-constrained values or "" — Service.List validates it
// against isKnownStatus BEFORE a cursor is minted or decoded, so an arbitrary
// caller string never reaches this frame. RFC3339Nano and a UUID cannot contain
// a pipe either, which is why this codec splits on a fixed field count rather
// than base64-ing each key the way crm's does.
//
// The encoding is opaque by contract: clients round-trip it untouched and must
// not construct one. The version tag leads so the format can change later
// without an old token being mistaken for a new one.
//
// WHY NOT internal/platform/cursor: that package's discriminator is contact
// search's Sort enum, so a dead-letter cursor and a contacts cursor would
// validate against each other's endpoints — the exact cross-listing confusion a
// discriminator exists to prevent.
//
// DEBT, stated rather than hidden: this is now the THIRD hand-rolled
// (created_at, id) keyset codec in the repo, alongside internal/platform/cursor
// and internal/app/crm/pagination.go. They differ in real ways today (contacts
// carries a sort enum and a direction, crm carries per-key base64 because a
// company name can contain the delimiter, this one carries a status filter), so
// consolidating them means designing a discriminator scheme that covers all
// three. That is a separate change with its own tests, not a drive-by on a
// pagination bugfix.
const (
	cursorVersion = "1"
	// cursorKind is this listing's discriminator. Baked into the frame so a
	// cursor from any other paginated endpoint is rejected rather than silently
	// resolving to some unrelated row's position.
	cursorKind = "dead_letters"
	// cursorFields is the exact number of pipe-separated fields in the frame.
	// Decoding requires an exact match: a shorter or longer payload is not a
	// token this server minted.
	cursorFields = 5
)

// Cursor is a decoded position in the dead-letter ordering: the sort key and the
// id that breaks ties on it. Both are needed because created_at defaults to
// now() and a burst of retry exhaustions shares one value.
type Cursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// encodeCursor renders a row's position as an opaque token, scoped to the status
// filter the page was read under.
//
// The timestamp is normalised to UTC so the token for a given instant is stable
// regardless of what location pgx handed back — a client comparing two cursors,
// or a cache keyed on one, should not see a difference that is not one.
func encodeCursor(status string, c Cursor) string {
	payload := strings.Join([]string{
		cursorVersion,
		cursorKind,
		status,
		c.CreatedAt.UTC().Format(time.RFC3339Nano),
		c.ID.String(),
	}, "|")
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

// decodeCursor parses a token minted for this listing under this status filter.
//
// Every failure is ErrBadCursor, which the handler reports as 400. Falling back
// to page one instead would be worse than an error: to the operator it reads as
// "the list lost your place", and to whoever debugs it later there is no trace
// that anything went wrong.
//
// The status check is the load-bearing one. A cursor minted on the unfiltered
// tab names a position in the all-statuses ordering; replayed against
// status=pending it would land somewhere in the middle of the pending rows and
// silently hide every pending row above it, which is indistinguishable from
// missing data.
func decodeCursor(status, raw string) (Cursor, error) {
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: not a token this server minted", ErrBadCursor)
	}
	parts := strings.Split(string(payload), "|")
	if len(parts) != cursorFields || parts[0] != cursorVersion || parts[1] != cursorKind {
		return Cursor{}, fmt.Errorf("%w: not a dead-letter cursor", ErrBadCursor)
	}
	if parts[2] != status {
		return Cursor{}, fmt.Errorf("%w: minted for a different status filter", ErrBadCursor)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, parts[3])
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: position is not a timestamp", ErrBadCursor)
	}
	id, err := uuid.Parse(parts[4])
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: position is not a row id", ErrBadCursor)
	}
	return Cursor{CreatedAt: createdAt, ID: id}, nil
}
