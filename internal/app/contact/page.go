package contact

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/cursor"
)

// Paging bounds. TotalCap is where counting stops: below it the total is exact,
// at it the API says "at least this many" and the UI renders "N+". Counting
// past a cap like this is the one part of a search screen that is unbounded by
// nature, so it is bounded by construction instead.
const (
	DefaultLimit = 50
	MaxLimit     = 100
	MinQueryLen  = 2
	TotalCap     = 10000
)

// Validation errors. These map to 422 (the request is well-formed but asks for
// something out of range); a bad cursor maps to 400 and comes from the cursor
// package instead.
var (
	// ErrQueryTooShort guards the trigram index: below three characters a
	// substring search stops being selective, so a one-character query is
	// rejected rather than answered with a scan pretending to be a search.
	ErrQueryTooShort = fmt.Errorf("search query must be at least %d characters", MinQueryLen)
	// ErrInvalidLimit means limit was supplied but outside 1..MaxLimit. It is
	// rejected rather than clamped: a client asking for 500 rows should learn
	// that it cannot have them, not silently receive 100.
	ErrInvalidLimit = fmt.Errorf("limit must be between 1 and %d", MaxLimit)
)

// SearchRequest is the parsed query string, before validation. Pointers mark
// genuinely optional inputs so "absent" and "zero" stay distinguishable —
// limit=0 must be an error, not a request for the default.
type SearchRequest struct {
	ListID *uuid.UUID
	Q      string
	Sort   string
	Cursor string
	Limit  *int
}

// Page is one page of search results plus the cursors that reach its
// neighbours. A nil cursor means there is no page in that direction.
type Page struct {
	Items         []SearchRow
	NextCursor    *string
	PrevCursor    *string
	Total         int64
	TotalIsCapped bool
}

// normalize validates the request and returns the canonical query text, sort
// and page size. Every rejection is explicit; nothing is silently corrected.
func (r SearchRequest) normalize() (q string, sort cursor.Sort, limit int, err error) {
	q = strings.ToLower(strings.TrimSpace(r.Q))
	if q != "" && utf8.RuneCountInString(q) < MinQueryLen {
		return "", "", 0, ErrQueryTooShort
	}
	sort, err = cursor.ParseSort(r.Sort)
	if err != nil {
		return "", "", 0, err
	}
	limit = DefaultLimit
	if r.Limit != nil {
		limit = *r.Limit
		if limit < 1 || limit > MaxLimit {
			return "", "", 0, ErrInvalidLimit
		}
	}
	return q, sort, limit, nil
}

// IsValidationError reports whether err is a client-side range/format problem
// the handler should answer with 422. Kept next to the errors it names so a new
// validation error cannot be added without landing in this set.
func IsValidationError(err error) bool {
	return errors.Is(err, ErrQueryTooShort) ||
		errors.Is(err, ErrInvalidLimit) ||
		errors.Is(err, cursor.ErrUnknownSort)
}

// IsCursorError reports whether err is a bad cursor, which is a 400: the client
// sent a token this server did not mint, or one from another ordering.
func IsCursorError(err error) bool {
	return errors.Is(err, cursor.ErrMalformed) || errors.Is(err, cursor.ErrSortMismatch)
}
