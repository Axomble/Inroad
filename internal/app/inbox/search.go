package inbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// MaxSearchQueryLength bounds the full-text query, in characters. Generous for
// anything a person types into a search box, and small enough that the parsed
// tsquery — whose size, and so the cost of evaluating it against every
// candidate, grows with the term count — stays trivially cheap.
const MaxSearchQueryLength = 256

// DefaultSearchPageLimit and MaxSearchPageLimit bound a search page. The cap is
// lower than MaxThreadPageLimit because every row of a search page pays for two
// ts_headline calls, which re-parse the matching message's text; the list page
// pays for nothing comparable.
const (
	DefaultSearchPageLimit = int32(25)
	MaxSearchPageLimit     = int32(50)
)

// errSearchNotConfigured is returned by SearchThreads on a Service built
// without WithSearchStore. It is a wiring bug, not a caller error, so it maps
// to a 500 rather than to any 4xx.
var errSearchNotConfigured = errors.New("inbox: search store not configured")

// The highlight markers the store asks ts_headline to wrap matches in. Unicode
// private-use code points, because no real mail uses them and they cannot be
// mistaken for markup: the response carries plain-text segments, never an
// HTML string a client would be tempted to render unescaped. The store strips
// both from the text BEFORE highlighting, so a message containing one cannot
// forge a highlight.
const (
	highlightStart = ""
	highlightStop  = ""
)

// SearchFilter narrows SearchThreads. Text is the operator's query in web
// search syntax ("quoted phrase", OR, -exclude). The embedded ListFilter carries
// every scope and facet ListThreads accepts, so a search inside a scope is a
// subset of that scope; its keyset pair (BeforeLastMessageAt/BeforeID) is the
// search cursor's position. ListFilter.Query — the list's substring match — has
// no meaning here and is rejected rather than silently ignored.
type SearchFilter struct {
	ListFilter
	Text string
}

// SearchHit is one thread a search matched. MatchedInbound/MatchedOutbound say
// which leg(s) the match was found on: inbound is the contact's replies,
// outbound is everything we sent — campaign steps and manual replies alike.
// At least one is always true.
type SearchHit struct {
	Thread          Thread
	MatchedInbound  bool
	MatchedOutbound bool
	// Snippet is the newest matching message, highlighted. nil only if it
	// could not be re-derived for a thread the search did match — which the
	// query's shape makes unreachable, but a row is never dropped over it.
	Snippet *SearchSnippet
}

// SearchSnippet is the matching message a hit is shown with.
type SearchSnippet struct {
	// Direction is "inbound" or "outbound", the same vocabulary as
	// Message.Direction.
	Direction  string
	OccurredAt time.Time
	// Subject is the message's whole subject; Body is up to two fragments of
	// its body around the matches. Both are split into highlighted and plain
	// runs of text.
	Subject []HighlightSegment
	Body    []HighlightSegment
}

// HighlightSegment is a run of plain text, marked when it is a matched term.
type HighlightSegment struct {
	Text  string
	Match bool
}

// SearchPage is one page of SearchThreads, newest first. HasMore reports
// whether a further page exists, which the handler turns into next_cursor.
type SearchPage struct {
	Items   []SearchHit
	HasMore bool
}

// SearchStore is the persistence this domain's search needs. Separate from
// Store, like SnoozeStore and LabelStore, so a caller that never searches is
// not made to implement it. The workspace id is always the authenticated one
// and every predicate is pinned on it — see docs/security.md.
//
// Filter.Limit is passed through as the number of rows wanted (the Service
// asks for one more than the page size to learn whether another page exists);
// the store clamps it defensively.
type SearchStore interface {
	SearchThreads(ctx context.Context, workspaceID uuid.UUID, filter SearchFilter) ([]SearchHit, error)
}

// WithSearchStore supplies the full-text search persistence. PgStore
// implements it alongside the other store interfaces.
func WithSearchStore(search SearchStore) ServiceOption {
	return func(s *Service) { s.search = search }
}

// SearchThreads returns one page of the workspace's threads whose messages —
// on either leg — match filter.Text.
func (s *Service) SearchThreads(ctx context.Context, workspaceID uuid.UUID, filter SearchFilter) (SearchPage, error) {
	text, err := normalizeSearchText(filter.Text)
	if err != nil {
		return SearchPage{}, err
	}
	if filter.Query != "" {
		return SearchPage{}, fmt.Errorf("%w: a full-text search cannot also carry a substring query", ErrValidation)
	}
	if (filter.BeforeLastMessageAt == nil) != (filter.BeforeID == nil) {
		return SearchPage{}, fmt.Errorf("%w: %s", ErrValidation, errHalfSetCursor)
	}
	if filter.SnoozeHidden && filter.SnoozedOnly {
		return SearchPage{}, fmt.Errorf("%w: snooze_hidden and snoozed_only are mutually exclusive", ErrValidation)
	}
	if s.search == nil {
		return SearchPage{}, errSearchNotConfigured
	}

	limit := normalizeSearchLimit(filter.Limit)
	filter.Text = text
	filter.Limit = limit + 1
	hits, err := s.search.SearchThreads(ctx, workspaceID, filter)
	if err != nil {
		return SearchPage{}, err
	}
	page := SearchPage{Items: hits}
	if int32(len(hits)) > limit {
		page.Items, page.HasMore = hits[:limit], true
	}

	threads := make([]Thread, len(page.Items))
	for i, h := range page.Items {
		threads[i] = h.Thread
	}
	if err := s.attachLabels(ctx, workspaceID, threads); err != nil {
		return SearchPage{}, err
	}
	for i := range page.Items {
		page.Items[i].Thread.Labels = threads[i].Labels
	}
	return page, nil
}

// normalizeSearchText validates the operator's query at the boundary and
// returns it trimmed. An empty query is REJECTED rather than treated as "match
// everything": that is what the thread list is for, and a search endpoint that
// quietly degraded into a list would hide a client that forgot to send q.
//
// A query that is non-empty but has no searchable words ("the", "!!!") is NOT
// an error — it simply matches nothing, which is the honest answer and what a
// mail client's search box shows.
//
// NUL and invalid UTF-8 are rejected here because Postgres rejects them in a
// text parameter; letting them through would turn a malformed request into a
// 500.
func normalizeSearchText(raw string) (string, error) {
	text := strings.TrimSpace(raw)
	switch {
	case text == "":
		return "", fmt.Errorf("%w: q is required", ErrValidation)
	case !utf8.ValidString(text):
		return "", fmt.Errorf("%w: q must be valid UTF-8", ErrValidation)
	case strings.ContainsRune(text, 0):
		return "", fmt.Errorf("%w: q must not contain NUL", ErrValidation)
	case utf8.RuneCountInString(text) > MaxSearchQueryLength:
		return "", fmt.Errorf("%w: q must be at most %d characters", ErrValidation, MaxSearchQueryLength)
	}
	return text, nil
}

// normalizeSearchLimit resolves a search page's size: the default when unset,
// clamped to MaxSearchPageLimit — the same clamp-not-reject convention as
// NormalizeLimit.
func normalizeSearchLimit(requested int32) int32 {
	switch {
	case requested <= 0:
		return DefaultSearchPageLimit
	case requested > MaxSearchPageLimit:
		return MaxSearchPageLimit
	default:
		return requested
	}
}

// highlightSegments splits a ts_headline result, marked with highlightStart/
// highlightStop, into plain and matched runs. Pure, so its edge cases are
// table-tested: an empty input is no segments; an unterminated marker (which
// ts_headline never produces) keeps its text as a match rather than losing
// it; empty runs between adjacent markers are dropped.
func highlightSegments(marked string) []HighlightSegment {
	var segments []HighlightSegment
	appendRun := func(text string, match bool) {
		if text != "" {
			segments = append(segments, HighlightSegment{Text: text, Match: match})
		}
	}
	rest := marked
	for rest != "" {
		before, after, found := strings.Cut(rest, highlightStart)
		appendRun(stripMarkers(before), false)
		if !found {
			break
		}
		matched, tail, _ := strings.Cut(after, highlightStop)
		appendRun(stripMarkers(matched), true)
		rest = tail
	}
	return segments
}

// stripMarkers removes a stray marker from a run. The store strips both from
// its input, so ts_headline only ever emits them in matched pairs; this is the
// belt-and-braces that keeps a private-use code point from ever reaching a
// client.
func stripMarkers(s string) string {
	return strings.NewReplacer(highlightStart, "", highlightStop, "").Replace(s)
}

var _ SearchStore = (*PgStore)(nil)

// SearchThreads runs SearchInboxThreads. The limit is clamped to one past the
// page cap (the Service's has-more probe) so no caller can make a single
// search render an unbounded number of snippets.
func (s *PgStore) SearchThreads(ctx context.Context, workspaceID uuid.UUID, filter SearchFilter) ([]SearchHit, error) {
	limit := filter.Limit
	if limit <= 0 || limit > MaxSearchPageLimit+1 {
		limit = MaxSearchPageLimit + 1
	}
	rows, err := s.q.SearchInboxThreads(ctx, gen.SearchInboxThreadsParams{
		HighlightStart: highlightStart,
		HighlightStop:  highlightStop,
		Query:          filter.Text,
		WorkspaceID:    workspaceID,
		MailboxID:      pgUUID(filter.MailboxID),
		ReplyClass:     filter.ReplyClass,
		// The list's "before" keyset is the search's "after" cursor: both name
		// the row a page continues past in (last_message_at, id) DESC order.
		AfterLastMessageAt: pgTimestamptz(filter.BeforeLastMessageAt),
		AfterID:            pgUUID(filter.BeforeID),
		UnreadOnly:         filter.UnreadOnly,
		SinceLastMessageAt: pgTimestamptz(filter.SinceLastMessageAt),
		AwaitingReplyOnly:  filter.AwaitingReplyOnly,
		SnoozeHidden:       filter.SnoozeHidden,
		SnoozedOnly:        filter.SnoozedOnly,
		LabelID:            pgUUID(filter.LabelID),
		PageLimit:          limit,
	})
	if err != nil {
		return nil, fmt.Errorf("search inbox threads: %w", err)
	}
	hits := make([]SearchHit, len(rows))
	for i, row := range rows {
		hits[i] = searchHitFromRow(row)
	}
	return hits, nil
}

// searchHitFromRow maps one SearchInboxThreads row to the domain type, through
// the same thread() assembler every other row mapper uses.
func searchHitFromRow(row gen.SearchInboxThreadsRow) SearchHit {
	hit := SearchHit{
		Thread: thread(threadFields{
			ID: row.ID, WorkspaceID: row.WorkspaceID, MailboxID: row.MailboxID,
			CampaignID: row.CampaignID, ContactID: row.ContactID, RootMessageID: row.RootMessageID,
			Subject: row.Subject, LastReplyClass: row.LastReplyClass, Unread: row.Unread,
			LastMessageAt: row.LastMessageAt, CreatedAt: row.CreatedAt,
			ContactEmail: row.ContactEmail, ContactFirstName: row.ContactFirstName, ContactLastName: row.ContactLastName,
			ReplyLabelLabel: row.ReplyLabelLabel, ReplyLabelColor: row.ReplyLabelColor,
		}),
		MatchedInbound:  row.MatchedInbound,
		MatchedOutbound: row.MatchedOutbound,
	}
	// '' is the query's "no snippet re-derived" signal — see SearchInboxThreads.
	if row.SnippetDirection != "" {
		hit.Snippet = &SearchSnippet{
			Direction:  row.SnippetDirection,
			OccurredAt: row.SnippetOccurredAt.Time,
			Subject:    highlightSegments(row.SnippetSubject),
			Body:       highlightSegments(row.SnippetBody),
		}
	}
	return hit
}
