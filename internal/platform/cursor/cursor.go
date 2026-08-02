// Package cursor encodes and decodes the opaque keyset-pagination cursors used
// by contact search. It is deliberately pure — no database, no clock, no
// randomness — so the whole encode/decode contract is table-testable.
//
// A cursor names one row's position in one ordering: the sort it was produced
// for, the direction of travel, the row's sort key, and the row id that breaks
// ties. Because the sort travels inside the cursor, replaying a cursor under a
// different sort is a detectable error rather than a silently wrong page.
//
// The encoding is base64url of a pipe-delimited payload. It is opaque by
// contract: clients round-trip it untouched and must not construct one. The
// format may change without an API break, which is why the version tag leads.
package cursor

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Sort is the ordering a page (and therefore a cursor) belongs to.
type Sort string

// The supported orderings. Each is backed by its own index; see the
// contact-search migration.
const (
	SortNewest Sort = "newest"
	SortOldest Sort = "oldest"
	SortEmail  Sort = "email"
)

// Direction is which way a cursor travels from the row it names.
type Direction string

// After walks toward the next page, Before toward the previous one.
const (
	After  Direction = "after"
	Before Direction = "before"
)

// Sentinel errors. Both map to 400 at the HTTP boundary: a client that cannot
// produce a valid cursor should be told so, never silently reset to page 1
// (which reads as "your search lost its place" and is impossible to debug).
var (
	// ErrMalformed means the value is not a cursor this server minted.
	ErrMalformed = errors.New("malformed cursor")
	// ErrSortMismatch means the cursor is well-formed but belongs to a
	// different ordering than the one requested.
	ErrSortMismatch = errors.New("cursor does not match the requested sort")
	// ErrUnknownSort means the sort name is not one this server supports.
	ErrUnknownSort = errors.New("unknown sort")
)

// version leads the payload so the encoding can be changed later without
// mistaking an old cursor for a new one.
const version = "1"

// Cursor is a decoded position. Exactly one of CreatedAt/Email carries the sort
// key, chosen by Sort; ID always carries the tiebreak. Keeping both fields
// typed (rather than one opaque string) means the store cannot accidentally
// compare a timestamp against an email.
type Cursor struct {
	Sort      Sort
	Direction Direction
	ID        uuid.UUID
	// CreatedAt is the sort key for SortNewest and SortOldest.
	CreatedAt time.Time
	// Email is the lower-cased sort key for SortEmail.
	Email string
}

// ParseSort validates a sort name from the wire. An empty value is the default
// (newest); anything unrecognised is ErrUnknownSort, which the handler maps to
// 422 — a typo'd sort silently falling back to the default would hide the bug.
func ParseSort(s string) (Sort, error) {
	switch Sort(s) {
	case "":
		return SortNewest, nil
	case SortNewest, SortOldest, SortEmail:
		return Sort(s), nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownSort, s)
	}
}

// NewTime builds a cursor for a created_at-ordered page.
func NewTime(sort Sort, dir Direction, createdAt time.Time, id uuid.UUID) Cursor {
	return Cursor{Sort: sort, Direction: dir, CreatedAt: createdAt, ID: id}
}

// NewEmail builds a cursor for an email-ordered page. The caller passes the
// same lower-cased value the index is built on.
func NewEmail(dir Direction, email string, id uuid.UUID) Cursor {
	return Cursor{Sort: SortEmail, Direction: dir, Email: email, ID: id}
}

// Encode renders the cursor as an opaque token.
//
// The sort key goes LAST and is not escaped: an email may legally contain the
// delimiter, so decoding splits a fixed number of times and treats the whole
// remainder as the key. Every earlier field is from a closed set or a UUID, so
// none of them can contain a pipe.
func (c Cursor) Encode() string {
	payload := strings.Join([]string{
		version,
		string(c.Sort),
		string(c.Direction),
		c.ID.String(),
		c.key(),
	}, "|")
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

// Key returns the sort key in the Go type its SQL comparison needs: a
// time.Time for the created_at sorts, the lower-cased email for SortEmail.
// Keeping the choice here means a caller cannot compare the wrong column.
func (c Cursor) Key() any {
	if c.Sort == SortEmail {
		return c.Email
	}
	return c.CreatedAt
}

func (c Cursor) key() string {
	if c.Sort == SortEmail {
		return c.Email
	}
	return c.CreatedAt.UTC().Format(time.RFC3339Nano)
}

// Decode parses raw and asserts it belongs to the want ordering. A cursor for a
// different sort is ErrSortMismatch rather than ErrMalformed so the caller (and
// the operator reading a log line) can tell "you changed the sort mid-page"
// apart from "this value is garbage".
func Decode(raw string, want Sort) (Cursor, error) {
	blob, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: not base64url", ErrMalformed)
	}
	parts := strings.SplitN(string(blob), "|", 5)
	if len(parts) != 5 {
		return Cursor{}, fmt.Errorf("%w: wrong field count", ErrMalformed)
	}
	if parts[0] != version {
		return Cursor{}, fmt.Errorf("%w: unsupported version %q", ErrMalformed, parts[0])
	}
	sort, err := ParseSort(parts[1])
	// An empty sort field would parse as the default, which would let a
	// truncated cursor masquerade as a newest cursor.
	if err != nil || parts[1] == "" {
		return Cursor{}, fmt.Errorf("%w: bad sort", ErrMalformed)
	}
	dir := Direction(parts[2])
	if dir != After && dir != Before {
		return Cursor{}, fmt.Errorf("%w: bad direction", ErrMalformed)
	}
	id, err := uuid.Parse(parts[3])
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: bad id", ErrMalformed)
	}
	if sort != want {
		return Cursor{}, fmt.Errorf("%w: cursor is %q, request is %q", ErrSortMismatch, sort, want)
	}

	c := Cursor{Sort: sort, Direction: dir, ID: id}
	if sort == SortEmail {
		c.Email = parts[4]
		return c, nil
	}
	at, err := time.Parse(time.RFC3339Nano, parts[4])
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: bad timestamp", ErrMalformed)
	}
	c.CreatedAt = at.UTC()
	return c, nil
}
