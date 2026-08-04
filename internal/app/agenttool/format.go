package agenttool

import (
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// isNoRecord reports whether err means "no such row in this workspace" rather
// than a fault. Every domain store on this path surfaces a missing or
// cross-tenant row as pgx.ErrNoRows, so a tool can turn that into recovery
// text for the model while a real database fault still aborts the run.
func isNoRecord(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// rfc3339 renders a nullable Postgres timestamp for a tool result. An unset
// timestamp is the empty string, which the result structs omit — a model reads
// an absent field as "never happened", where a zero time reads as 1 AD.
func rfc3339(t pgtype.Timestamptz) string {
	if !t.Valid {
		return ""
	}
	return t.Time.UTC().Format(time.RFC3339)
}

func rfc3339Time(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// derefString renders an optional text column.
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// matches is the substring test the cross-object search uses. Search here is
// deliberately in-process over the workspace's own small collections
// (campaigns, mailboxes, lists number in the tens); contacts, which do not,
// go through the domain's indexed search instead.
func matches(haystack, needle string) bool {
	return haystack != "" && strings.Contains(strings.ToLower(haystack), needle)
}
