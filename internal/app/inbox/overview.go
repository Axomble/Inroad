package inbox

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Overview is the scope rail's counts for one workspace: the headline
// counters, plus a breakdown per mailbox and per reply-class label.
//
// Every number here is counted by Postgres over the whole workspace. The
// alternative the frontend used before this existed — counting one 200-row
// page client-side — was honest about being a sample but silently wrong on
// any inbox larger than the sample.
type Overview struct {
	Total         int64
	Unread        int64
	Today         int64
	ThisWeek      int64
	AwaitingReply int64
	ByMailbox     []MailboxCount
	ByReplyClass  []ReplyClassCount
}

// MailboxCount is one mailbox's thread counts. Mailboxes with no threads at
// all are absent from Overview.ByMailbox rather than present with zeroes —
// the rail renders a row per *connected* mailbox and looks its count up, so
// "absent" and "zero" render identically without this type having to
// enumerate the mailbox list.
type MailboxCount struct {
	MailboxID uuid.UUID
	Total     int64
	Unread    int64
}

// ReplyClassCount is one reply-class key's thread counts. Key is the raw
// `last_reply_class` string; resolving it to a display label is the reply-label
// taxonomy's job (the frontend already holds it), not this domain's.
type ReplyClassCount struct {
	Key    string
	Total  int64
	Unread int64
}

// OverviewWindow carries the time boundaries the "today" and "this week"
// counters are measured against.
//
// These are supplied by the caller rather than computed from the server's
// clock+zone, because "today" is a question about the *viewer's* day: a
// London operator at 00:30 and a server on UTC-8 disagree about which
// threads arrived today, and the server has no basis to overrule the viewer.
// The HTTP layer resolves them from the request (see parseOverviewWindow).
type OverviewWindow struct {
	TodayStart time.Time
	WeekStart  time.Time
}

// GetOverview returns the workspace's scope counts.
func (s *Service) GetOverview(ctx context.Context, workspaceID uuid.UUID, window OverviewWindow) (Overview, error) {
	return s.store.GetOverview(ctx, workspaceID, window)
}

// GetOverview runs the three overview queries and assembles them. They are
// deliberately three round-trips rather than one query with three CTEs: each
// has a different cardinality (one row / one per mailbox / one per class),
// and folding them together would either cross-join the groups or force a
// jsonb aggregation that sqlc could only hand back as interface{}.
func (s *PgStore) GetOverview(ctx context.Context, workspaceID uuid.UUID, window OverviewWindow) (Overview, error) {
	totals, err := s.q.GetInboxOverviewTotals(ctx, gen.GetInboxOverviewTotalsParams{
		WorkspaceID: workspaceID,
		TodayStart:  pgTimestamptzValue(window.TodayStart),
		WeekStart:   pgTimestamptzValue(window.WeekStart),
	})
	if err != nil {
		return Overview{}, err
	}

	mailboxRows, err := s.q.ListInboxOverviewByMailbox(ctx, workspaceID)
	if err != nil {
		return Overview{}, err
	}
	classRows, err := s.q.ListInboxOverviewByReplyClass(ctx, workspaceID)
	if err != nil {
		return Overview{}, err
	}

	byMailbox := make([]MailboxCount, len(mailboxRows))
	for i, r := range mailboxRows {
		byMailbox[i] = MailboxCount{MailboxID: r.MailboxID, Total: r.Total, Unread: r.Unread}
	}
	byClass := make([]ReplyClassCount, len(classRows))
	for i, r := range classRows {
		byClass[i] = ReplyClassCount{Key: r.Key, Total: r.Total, Unread: r.Unread}
	}

	return Overview{
		Total:         totals.Total,
		Unread:        totals.Unread,
		Today:         totals.Today,
		ThisWeek:      totals.ThisWeek,
		AwaitingReply: totals.AwaitingReply,
		ByMailbox:     byMailbox,
		ByReplyClass:  byClass,
	}, nil
}
