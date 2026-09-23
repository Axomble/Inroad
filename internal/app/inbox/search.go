package inbox

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inroad/inroad/internal/platform/db"
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

// MaxSearchCandidates caps how many matching rows ONE search may consider, per
// candidate set (matching stored messages; matching campaign step/variant
// copy; contacts whose email contains the query; inbound messages whose From
// address does). A query matching more is refused with ErrSearchTooBroad rather than
// answered from an arbitrary subset: a capped set cannot say which matches it
// dropped, so "some of the results" would silently miss newer threads.
// Refusing keeps every page it does return exactly correct, and the operator's
// fix — add a word — is obvious. 10,000 matches the contact search's count cap.
const MaxSearchCandidates = 10_000

// DefaultSearchTimeout is the statement_timeout one search transaction runs
// under. A search the candidate cap admits finishes in milliseconds; this is
// the backstop for the one it does not foresee (a pathological phrase recheck,
// a cold cache), so a single request can never hold a connection and a CPU for
// longer.
const DefaultSearchTimeout = 3 * time.Second

// ErrSearchNotSelective rejects a query with nothing positive to look for:
// only stop words or punctuation ("the", "!!!"), or only exclusions ("-foo",
// "foo OR -bar"). The first can match nothing; the second matches nearly every
// message and can only be answered by scanning the whole index. It wraps
// ErrValidation, so it is a 400.
var ErrSearchNotSelective = fmt.Errorf("%w: q must contain at least one word to look for (only common words or exclusions were given)", ErrValidation)

// ErrSearchTooBroad rejects a query matching more than MaxSearchCandidates rows.
// A 422: the request is well-formed, and the answer is to narrow it.
var ErrSearchTooBroad = fmt.Errorf("inbox: search matches more than %d messages, campaign steps or contacts; add words to narrow it", MaxSearchCandidates)

// ErrSearchTimeout is a search that ran past DefaultSearchTimeout and was
// cancelled by Postgres. A 503: the query was valid, the server declined to
// finish it, and a narrower one will likely succeed.
var ErrSearchTimeout = errors.New("inbox: search took too long; try a more specific query")

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
	highlightStart = "\xee\x80\x80" // U+E000
	highlightStop  = "\xee\x80\x81" // U+E001
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
// which leg's TEXT matched: inbound is the contact's replies, outbound is
// everything we sent — campaign steps and manual replies alike.
// MatchedAddress says the query matched an ADDRESS instead: the linked
// contact's email, or the From address of one of the thread's inbound
// messages (see addressPattern). At least one of the three is always true.
type SearchHit struct {
	Thread          Thread
	MatchedInbound  bool
	MatchedOutbound bool
	MatchedAddress  bool
	// Snippet is the newest text-matching message, highlighted — or, for a
	// thread found only by address, its newest message, unhighlighted. nil
	// only for a thread with no message on either leg.
	Snippet *SearchSnippet
}

// MinAddressQueryLength is the shortest query that is also matched against
// addresses. Below three characters a trigram index has nothing to narrow
// on, so a substring match would read every contact in the workspace; such a
// query is matched as text only.
const MinAddressQueryLength = 3

// addressPattern is the query as the address predicates take it: lower-cased
// (the indexes are over lower()) and LIKE-escaped, so a typed "%" or "_" is a
// literal rather than a wildcard. nil turns address matching off.
func addressPattern(text string) *string {
	if utf8.RuneCountInString(text) < MinAddressQueryLength {
		return nil
	}
	pattern := db.EscapeLike(strings.ToLower(text))
	return &pattern
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
// Whether the query has anything to LOOK FOR (a positive term, rather than
// only stop words or exclusions) depends on how Postgres parses it, so that is
// decided by the store — see ErrSearchNotSelective.
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

// sqlStateQueryCanceled is Postgres' query_canceled, raised when a statement
// runs past statement_timeout.
const sqlStateQueryCanceled = "57014"

// SearchThreads runs one search inside a single read-only REPEATABLE READ
// transaction bounded by a statement_timeout:
//
//  1. InboxSearchPrecheck refuses a query with nothing positive to look for
//     (ErrSearchNotSelective) or with more than MaxSearchCandidates matching
//     rows (ErrSearchTooBroad), before any thread is walked.
//  2. SearchInboxThreads answers it.
//
// REPEATABLE READ makes both statements read one snapshot, so a burst of new
// mail between them cannot push the candidate sets past the cap the precheck
// just approved (the search's own LIMIT would otherwise truncate them).
//
// The limit is clamped to one past the page cap (the Service's has-more probe)
// so no caller can make a single search render an unbounded number of
// snippets.
func (s *PgStore) SearchThreads(ctx context.Context, workspaceID uuid.UUID, filter SearchFilter) (hits []SearchHit, err error) {
	limit := filter.Limit
	if limit <= 0 || limit > MaxSearchPageLimit+1 {
		limit = MaxSearchPageLimit + 1
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("search inbox threads: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // read-only: nothing to keep
	defer func() { err = mapSearchError(ctx, err) }()
	qtx := s.q.WithTx(tx)

	if err := qtx.SetInboxSearchStatementTimeout(ctx, strconv.FormatInt(s.searchTimeoutOrDefault().Milliseconds(), 10)+"ms"); err != nil {
		return nil, fmt.Errorf("search inbox threads: statement timeout: %w", err)
	}
	address := addressPattern(filter.Text)
	pre, err := qtx.InboxSearchPrecheck(ctx, gen.InboxSearchPrecheckParams{
		WorkspaceID: workspaceID, Query: filter.Text, CandidateCap: MaxSearchCandidates + 1,
		AddressPattern: address,
	})
	if err != nil {
		return nil, fmt.Errorf("search inbox threads: precheck: %w", err)
	}
	if pre.Tree == "T" || pre.Tree == "" {
		return nil, ErrSearchNotSelective
	}
	for _, candidates := range []int64{pre.MessageCandidates, pre.StepCandidates, pre.ContactCandidates, pre.SenderCandidates} {
		if candidates > MaxSearchCandidates {
			return nil, ErrSearchTooBroad
		}
	}

	rows, err := qtx.SearchInboxThreads(ctx, gen.SearchInboxThreadsParams{
		HighlightStart: highlightStart,
		HighlightStop:  highlightStop,
		Query:          filter.Text,
		WorkspaceID:    workspaceID,
		CandidateCap:   MaxSearchCandidates + 1,
		AddressPattern: address,
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
	hits = make([]SearchHit, len(rows))
	for i, row := range rows {
		hits[i] = searchHitFromRow(row)
	}
	return hits, nil
}

// searchTimeoutOrDefault is the statement_timeout a search runs under: the
// store's own override (set only by tests, see export_test.go) or
// DefaultSearchTimeout.
func (s *PgStore) searchTimeoutOrDefault() time.Duration {
	if s.searchTimeout > 0 {
		return s.searchTimeout
	}
	return DefaultSearchTimeout
}

// mapSearchError turns Postgres cancelling a statement for running past
// statement_timeout into ErrSearchTimeout. A cancellation caused by the
// caller's own context (the client went away) is NOT a timeout of ours and is
// passed through as the context's error, so it is never reported as the
// server declining the query.
func mapSearchError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("search inbox threads: %w", ctxErr)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == sqlStateQueryCanceled {
		return fmt.Errorf("%w (%w)", ErrSearchTimeout, err)
	}
	return err
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
		MatchedAddress:  row.MatchedAddress,
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
