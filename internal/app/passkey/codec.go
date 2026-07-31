package passkey

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/go-webauthn/webauthn/protocol"
)

// encodeTransports joins the authenticator transport hints into the comma-separated
// TEXT column form. An empty slice yields "".
func encodeTransports(ts []protocol.AuthenticatorTransport) string {
	if len(ts) == 0 {
		return ""
	}
	parts := make([]string, len(ts))
	for i, t := range ts {
		parts[i] = string(t)
	}
	return strings.Join(parts, ",")
}

// decodeTransports splits the stored comma-separated transports back into the
// library type, dropping empty segments so a "" column decodes to a nil slice.
func decodeTransports(s string) []protocol.AuthenticatorTransport {
	if s == "" {
		return nil
	}
	segs := strings.Split(s, ",")
	out := make([]protocol.AuthenticatorTransport, 0, len(segs))
	for _, seg := range segs {
		if seg == "" {
			continue
		}
		out = append(out, protocol.AuthenticatorTransport(seg))
	}
	return out
}

// transportsList splits the stored transports into a plain []string for the manage
// DTO, dropping empty segments so "" yields a nil slice.
func transportsList(s string) []string {
	if s == "" {
		return nil
	}
	segs := strings.Split(s, ",")
	out := make([]string, 0, len(segs))
	for _, seg := range segs {
		if seg != "" {
			out = append(out, seg)
		}
	}
	return out
}

// pgxTimestamp wraps a time.Time as a non-null pgx timestamptz.
func pgxTimestamp(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// pgxTime extracts a time.Time from a pgx timestamptz, returning the zero time for
// NULL.
func pgxTime(ts pgtype.Timestamptz) time.Time {
	if !ts.Valid {
		return time.Time{}
	}
	return ts.Time
}

// pgxUUIDPtr wraps an optional user id as the nullable pgx UUID a discoverable-login
// challenge stores (nil → NULL user_id, since the user is unknown until the
// authenticated credential resolves one).
func pgxUUIDPtr(id *uuid.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}
