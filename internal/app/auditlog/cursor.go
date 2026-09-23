package auditlog

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Keyset cursor for the audit list:
//
//	version | kind | filter digest | created_at RFC3339Nano | id
//
// base64url (unpadded). The filter digest binds a cursor to the exact filter
// set it was minted under: replayed under a different filter it would resume
// at an unrelated row's position and silently skip everything above it, so
// that is a 400 rather than a wrong page. A digest rather than the filters
// themselves because the filters are free text and could contain the
// delimiter. Same shape and reasoning as deadletter's cursor (see its DEBT
// note on why the repo's keyset codecs are not yet one).
const (
	cursorVersion = "1"
	cursorKind    = "audit_events"
	cursorFields  = 5
)

// ErrBadCursor is any cursor this listing did not mint for this filter set.
var ErrBadCursor = errors.New("invalid cursor")

type position struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// digest is a stable fingerprint of the normalised filter. Fields are joined
// with a separator that cannot appear in any of them after validation (a NUL).
func (f Filter) digest() string {
	parts := []string{
		f.ActionPrefix, f.ActorType, f.ActorID, uuidOrEmpty(f.ActorUserID),
		f.TargetType, f.TargetID, timeOrEmpty(f.Since), timeOrEmpty(f.Until),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:8])
}

func encodeCursor(f Filter, p position) string {
	payload := strings.Join([]string{
		cursorVersion, cursorKind, f.digest(),
		p.CreatedAt.UTC().Format(time.RFC3339Nano), p.ID.String(),
	}, "|")
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

func decodeCursor(f Filter, raw string) (position, error) {
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return position{}, fmt.Errorf("%w: not a token this server minted", ErrBadCursor)
	}
	parts := strings.Split(string(payload), "|")
	if len(parts) != cursorFields || parts[0] != cursorVersion || parts[1] != cursorKind {
		return position{}, fmt.Errorf("%w: not an audit cursor", ErrBadCursor)
	}
	if parts[2] != f.digest() {
		return position{}, fmt.Errorf("%w: minted for a different filter", ErrBadCursor)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, parts[3])
	if err != nil {
		return position{}, fmt.Errorf("%w: position is not a timestamp", ErrBadCursor)
	}
	id, err := uuid.Parse(parts[4])
	if err != nil {
		return position{}, fmt.Errorf("%w: position is not a row id", ErrBadCursor)
	}
	return position{CreatedAt: createdAt, ID: id}, nil
}

func uuidOrEmpty(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

func timeOrEmpty(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
