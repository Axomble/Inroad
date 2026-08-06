package crm

import (
	"encoding/base64"
	"strings"
)

// Keyset cursors. A cursor names one row's position in ONE listing's ordering,
// so replaying a companies cursor against the deals list is a detectable
// error rather than a silently wrong page. The payload is base64url of
// "version|kind|<base64 key>…" — every key is encoded individually so a value
// containing the delimiter (a company name, say) cannot corrupt the frame.
//
// The encoding is opaque by contract: clients round-trip it untouched. The
// version tag leads so the format can change without mistaking an old cursor
// for a new one.
const cursorVersion = "1"

type cursorKind string

const (
	cursorCompanies cursorKind = "companies"
	cursorDeals     cursorKind = "deals"
	cursorNotes     cursorKind = "notes"
	cursorTasks     cursorKind = "tasks"
)

func encodeCursor(kind cursorKind, keys ...string) string {
	parts := make([]string, 0, len(keys)+2)
	parts = append(parts, cursorVersion, string(kind))
	for _, key := range keys {
		parts = append(parts, base64.RawURLEncoding.EncodeToString([]byte(key)))
	}
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Join(parts, "|")))
}

// decodeCursor returns want keys, or ErrValidation. A malformed or foreign
// cursor is rejected loudly: silently restarting at page one reads as "the
// listing lost its place" and is impossible to debug.
func decodeCursor(kind cursorKind, raw string, want int) ([]string, error) {
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, validation("cursor is malformed")
	}
	parts := strings.Split(string(payload), "|")
	if len(parts) != want+2 || parts[0] != cursorVersion || parts[1] != string(kind) {
		return nil, validation("cursor does not belong to this listing")
	}
	keys := make([]string, want)
	for i := range keys {
		key, err := base64.RawURLEncoding.DecodeString(parts[i+2])
		if err != nil {
			return nil, validation("cursor is malformed")
		}
		keys[i] = string(key)
	}
	return keys, nil
}
