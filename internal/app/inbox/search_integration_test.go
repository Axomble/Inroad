//go:build integration

package inbox_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/app/inbox"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// searchCampaign creates a campaign in f's workspace whose steps carry the
// given bodies (step N gets bodies[N-1]) and one contact, for the outbound-leg
// search tests. Returns the campaign, the contact, and the step ids by order.
func searchCampaign(t *testing.T, ctx context.Context, f *fixture, subject string, bodies ...string) (campaignID, contactID uuid.UUID, stepIDs []uuid.UUID) {
	t.Helper()
	list, err := f.q.CreateList(ctx, gen.CreateListParams{WorkspaceID: f.ws, Name: "L-" + uuid.NewString()})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	campaign, err := f.q.CreateCampaign(ctx, gen.CreateCampaignParams{
		WorkspaceID: f.ws, Name: "C", MailboxID: f.mailbox, ListID: list.ID, Subject: subject, BodyText: "b",
	})
	if err != nil {
		t.Fatalf("campaign: %v", err)
	}
	for i, body := range bodies {
		stepSubject := subject
		if i > 0 {
			stepSubject = ""
		}
		step, err := f.q.CreateStep(ctx, gen.CreateStepParams{
			WorkspaceID: f.ws, CampaignID: campaign.ID, StepOrder: int32(i + 1), Subject: stepSubject, BodyText: body,
		})
		if err != nil {
			t.Fatalf("step %d: %v", i+1, err)
		}
		stepIDs = append(stepIDs, step.ID)
	}
	contact, err := f.q.UpsertContact(ctx, gen.UpsertContactParams{
		WorkspaceID: f.ws, Email: "search-" + uuid.NewString() + "@x.test", FirstName: "S",
	})
	if err != nil {
		t.Fatalf("contact: %v", err)
	}
	return campaign.ID, contact.ID, stepIDs
}

// campaignThread records an inbound reply on a campaign thread.
func campaignThread(t *testing.T, ctx context.Context, f *fixture, campaignID, contactID uuid.UUID, replyBody string, at time.Time) inbox.Thread {
	t.Helper()
	th, err := f.store.RecordReply(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, CampaignID: &campaignID, ContactID: &contactID,
		RootMessageID: "<search-" + uuid.NewString() + "@sender.test>", Subject: "Re: hello", LastReplyClass: "neutral",
	}, inbox.InsertMessageInput{
		Direction: "inbound", FromEmail: "them@example.com", Subject: "Re: hello", BodyText: replyBody, OccurredAt: at,
	})
	if err != nil {
		t.Fatalf("RecordReply: %v", err)
	}
	return th
}

// plainThread records an inbound reply on a thread with no campaign link.
func plainThread(t *testing.T, ctx context.Context, f *fixture, subject, body string, at time.Time) inbox.Thread {
	t.Helper()
	th, err := f.store.RecordReply(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox,
		RootMessageID: "<plain-" + uuid.NewString() + "@sender.test>", Subject: subject, LastReplyClass: "neutral",
	}, inbox.InsertMessageInput{
		Direction: "inbound", FromEmail: "them@example.com", Subject: subject, BodyText: body, OccurredAt: at,
	})
	if err != nil {
		t.Fatalf("RecordReply: %v", err)
	}
	return th
}

func search(t *testing.T, ctx context.Context, f *fixture, text string) []inbox.SearchHit {
	t.Helper()
	hits, err := f.store.SearchThreads(ctx, f.ws, inbox.SearchFilter{Text: text})
	if err != nil {
		t.Fatalf("SearchThreads(%q): %v", text, err)
	}
	return hits
}

func hitFor(hits []inbox.SearchHit, id uuid.UUID) (inbox.SearchHit, bool) {
	for _, h := range hits {
		if h.Thread.ID == id {
			return h, true
		}
	}
	return inbox.SearchHit{}, false
}

func matchedText(segments []inbox.HighlightSegment) []string {
	var out []string
	for _, s := range segments {
		if s.Match {
			out = append(out, s.Text)
		}
	}
	return out
}

// The acceptance criterion: a phrase that exists ONLY in what the campaign
// sent — never in any stored message — finds the thread. A search that looked
// at inbox_messages alone would return nothing here.
func TestSearchFindsAThreadByCampaignSendTextAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	token := "zephyrquartz" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	campaignID, contactID, _ := searchCampaign(t, ctx, f, "Hello",
		"Our "+token+" platform cuts onboarding time in half.")
	sentAt := time.Now().UTC().Add(-2 * time.Hour)
	insertSentStep(t, ctx, f, campaignID, contactID, 1, sentAt)
	th := campaignThread(t, ctx, f, campaignID, contactID, "Tell me more please", sentAt.Add(time.Hour))

	hits := search(t, ctx, f, token)
	if len(hits) != 1 || hits[0].Thread.ID != th.ID {
		t.Fatalf("hits = %+v, want exactly the campaign thread", hits)
	}
	h := hits[0]
	if h.MatchedInbound || !h.MatchedOutbound {
		t.Errorf("matched inbound=%v outbound=%v, want outbound only", h.MatchedInbound, h.MatchedOutbound)
	}
	if h.Snippet == nil || h.Snippet.Direction != "outbound" {
		t.Fatalf("snippet = %+v, want one from the outbound send", h.Snippet)
	}
	if got := matchedText(h.Snippet.Body); len(got) != 1 || got[0] != token {
		t.Errorf("highlighted body runs = %q, want exactly [%q]", got, token)
	}
	if !h.Snippet.OccurredAt.Equal(sentAt.Truncate(time.Microsecond)) {
		t.Errorf("snippet occurred_at = %v, want the send's sent_at %v", h.Snippet.OccurredAt, sentAt)
	}
	if h.Thread.ContactID == nil || *h.Thread.ContactID != contactID || h.Thread.ContactEmail == "" {
		t.Errorf("thread = %+v, want the contact joined in like the list", h.Thread)
	}
}

func TestSearchFindsAThreadByInboundReplyTextAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	campaignID, contactID, _ := searchCampaign(t, ctx, f, "Hello", "A perfectly ordinary pitch.")
	at := time.Now().UTC().Add(-time.Hour)
	insertSentStep(t, ctx, f, campaignID, contactID, 1, at.Add(-time.Hour))
	th := campaignThread(t, ctx, f, campaignID, contactID, "We already use a competitor for invoicing.", at)

	hits := search(t, ctx, f, "invoicing")
	if len(hits) != 1 || hits[0].Thread.ID != th.ID {
		t.Fatalf("hits = %+v, want exactly the thread", hits)
	}
	h := hits[0]
	if !h.MatchedInbound || h.MatchedOutbound {
		t.Errorf("matched inbound=%v outbound=%v, want inbound only", h.MatchedInbound, h.MatchedOutbound)
	}
	if h.Snippet == nil || h.Snippet.Direction != "inbound" {
		t.Fatalf("snippet = %+v, want one from the inbound reply", h.Snippet)
	}
	// Stemming: "invoicing" in the query highlights "invoicing" in the text.
	if got := matchedText(h.Snippet.Body); len(got) != 1 || !strings.EqualFold(got[0], "invoicing") {
		t.Errorf("highlighted body runs = %q, want [invoicing]", got)
	}
}

// Both legs match: both are reported, and the snippet is the NEWER message.
func TestSearchReportsBothLegsAndSnippetsTheNewestMatchAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	campaignID, contactID, _ := searchCampaign(t, ctx, f, "Pricing", "Our pricing starts at ten dollars.")
	sentAt := time.Now().UTC().Add(-3 * time.Hour)
	insertSentStep(t, ctx, f, campaignID, contactID, 1, sentAt)
	th := campaignThread(t, ctx, f, campaignID, contactID, "What does the enterprise pricing look like?", sentAt.Add(time.Hour))

	hits := search(t, ctx, f, "pricing")
	h, ok := hitFor(hits, th.ID)
	if !ok || len(hits) != 1 {
		t.Fatalf("hits = %+v, want exactly the thread", hits)
	}
	if !h.MatchedInbound || !h.MatchedOutbound {
		t.Errorf("matched inbound=%v outbound=%v, want both", h.MatchedInbound, h.MatchedOutbound)
	}
	if h.Snippet == nil || h.Snippet.Direction != "inbound" {
		t.Errorf("snippet = %+v, want the newer inbound reply", h.Snippet)
	}
}

// A manual outbound reply is stored in inbox_messages and counts as the
// outbound leg too.
func TestSearchMatchesAManualOutboundReplyAsOutboundAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	th := plainThread(t, ctx, f, "Question", "Can you send details?", time.Now().UTC().Add(-time.Hour))
	if err := f.store.RecordOutboundReply(ctx, th.ID, f.ws, inbox.InsertMessageInput{
		Direction: "outbound", Subject: "Re: Question", BodyText: "Attached is the brochure.", OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordOutboundReply: %v", err)
	}
	hits := search(t, ctx, f, "brochure")
	if len(hits) != 1 || hits[0].MatchedInbound || !hits[0].MatchedOutbound {
		t.Fatalf("hits = %+v, want the thread matched on the outbound leg only", hits)
	}
}

func TestSearchWithNoMatchReturnsNothingAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	campaignID, contactID, _ := searchCampaign(t, ctx, f, "Hello", "Nothing to see here.")
	insertSentStep(t, ctx, f, campaignID, contactID, 1, time.Now().UTC().Add(-time.Hour))
	campaignThread(t, ctx, f, campaignID, contactID, "Nor here.", time.Now().UTC())

	if hits := search(t, ctx, f, "xylophone"); len(hits) != 0 {
		t.Fatalf("hits = %+v, want none", hits)
	}
}

// Content of a step that was never SENT to this contact is not part of the
// thread, so it must not match — the same sent_at rule the reader applies.
func TestSearchIgnoresUnsentStepsAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	campaignID, contactID, _ := searchCampaign(t, ctx, f, "Hello", "Step one copy.", "Step two mentions a marmalade discount.")
	insertSentStep(t, ctx, f, campaignID, contactID, 1, time.Now().UTC().Add(-time.Hour))
	campaignThread(t, ctx, f, campaignID, contactID, "ok", time.Now().UTC())

	if hits := search(t, ctx, f, "marmalade"); len(hits) != 0 {
		t.Fatalf("hits = %+v, want none: step 2 was never sent", hits)
	}
}

// An A/B send carried the VARIANT's copy, so that is what must match — and the
// base copy it did not carry must not.
func TestSearchMatchesTheVariantASendActuallyCarriedAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	campaignID, contactID, steps := searchCampaign(t, ctx, f, "Hello", "Base copy about gondolas.")
	variant, err := f.q.CreateStepVariant(ctx, gen.CreateStepVariantParams{
		WorkspaceID: f.ws, StepID: steps[0], Label: "B", Weight: 1, Subject: "Hello", BodyText: "Variant copy about zeppelins.",
	})
	if err != nil {
		t.Fatalf("variant: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO sends (workspace_id, campaign_id, contact_id, mailbox_id, to_email, status, message_id, sent_at, step_order, variant_id)
		 VALUES ($1,$2,$3,$4,'them@example.com','sent',$5,$6,1,$7)`,
		f.ws, campaignID, contactID, f.mailbox, "<variant-"+uuid.NewString()+"@sender.test>", time.Now().UTC().Add(-time.Hour), variant.ID,
	); err != nil {
		t.Fatalf("insert variant send: %v", err)
	}
	th := campaignThread(t, ctx, f, campaignID, contactID, "ok", time.Now().UTC())

	hits := search(t, ctx, f, "zeppelins")
	if len(hits) != 1 || hits[0].Thread.ID != th.ID || !hits[0].MatchedOutbound {
		t.Fatalf("hits = %+v, want the thread, matched on the variant it was sent", hits)
	}
	if hits[0].Snippet == nil || len(matchedText(hits[0].Snippet.Body)) == 0 {
		t.Errorf("snippet = %+v, want the variant's copy highlighted", hits[0].Snippet)
	}
	if hits := search(t, ctx, f, "gondolas"); len(hits) != 0 {
		t.Fatalf("hits = %+v, want none: this contact was sent the variant, not the base copy", hits)
	}
}

// Isolation, both legs: another workspace's matching replies AND matching
// campaign content must never surface, and must not make one of OUR threads
// match either.
func TestSearchIsWorkspaceScopedOnBothLegsAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	foreign := &fixture{pool: f.pool, q: f.q, store: f.store}
	foreign.ws, foreign.mailbox = seedTenant(t, ctx, f.q)

	token := "cassowary" + strings.ReplaceAll(uuid.NewString()[:6], "-", "")
	fc, fk, _ := searchCampaign(t, ctx, foreign, "Hello", "Foreign "+token+" copy.")
	insertSentStep(t, ctx, foreign, fc, fk, 1, time.Now().UTC().Add(-time.Hour))
	campaignThread(t, ctx, foreign, fc, fk, "Foreign reply about "+token, time.Now().UTC())

	// Our own thread, whose content never mentions the token.
	ownCampaign, ownContact, _ := searchCampaign(t, ctx, f, "Hello", "Our own copy.")
	insertSentStep(t, ctx, f, ownCampaign, ownContact, 1, time.Now().UTC().Add(-time.Hour))
	campaignThread(t, ctx, f, ownCampaign, ownContact, "Our own reply.", time.Now().UTC())

	if hits := search(t, ctx, f, token); len(hits) != 0 {
		t.Fatalf("hits = %+v, want none: every match lives in another workspace", hits)
	}
	if hits := search(t, ctx, foreign, token); len(hits) != 1 {
		t.Fatalf("foreign hits = %+v, want its own thread (the seed must be searchable, or this test proves nothing)", hits)
	}
}

// Paging through the Service: the page size, the has-more probe, and the
// keyset continuing strictly past the last row — including two threads that
// tie on last_message_at, which only the id tiebreak can order.
func TestSearchPaginatesByKeysetAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	svc := inbox.NewService(f.store, inbox.WithSearchStore(f.store), inbox.WithLabelStore(f.store))
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	var ids []uuid.UUID
	for i := 0; i < 5; i++ {
		th := plainThread(t, ctx, f, "Renewal", fmt.Sprintf("Renewal quote number %d", i), base)
		ids = append(ids, th.ID)
	}
	// Two threads share an instant; the rest are spread out.
	for i, id := range ids {
		at := base.Add(time.Duration(i) * time.Minute)
		if i == 4 {
			at = base.Add(3 * time.Minute)
		}
		if _, err := f.pool.Exec(ctx, `UPDATE inbox_threads SET last_message_at = $1 WHERE id = $2`, at, id); err != nil {
			t.Fatalf("set last_message_at: %v", err)
		}
	}
	plainThread(t, ctx, f, "Unrelated", "nothing relevant", base.Add(time.Hour))

	var seen []uuid.UUID
	filter := inbox.SearchFilter{Text: "renewal", ListFilter: inbox.ListFilter{Limit: 2}}
	for pages := 0; ; pages++ {
		if pages > 5 {
			t.Fatal("pagination did not terminate")
		}
		page, err := svc.SearchThreads(ctx, f.ws, filter)
		if err != nil {
			t.Fatalf("SearchThreads: %v", err)
		}
		if len(page.Items) > 2 {
			t.Fatalf("page of %d, want at most the requested 2", len(page.Items))
		}
		for _, h := range page.Items {
			seen = append(seen, h.Thread.ID)
		}
		if !page.HasMore {
			break
		}
		last := page.Items[len(page.Items)-1].Thread
		at, id := last.LastMessageAt, last.ID
		filter.BeforeLastMessageAt, filter.BeforeID = &at, &id
	}
	if len(seen) != 5 {
		t.Fatalf("saw %d threads across pages (%v), want all 5 exactly once", len(seen), seen)
	}
	unique := map[uuid.UUID]bool{}
	for _, id := range seen {
		unique[id] = true
	}
	if len(unique) != 5 {
		t.Fatalf("a thread appeared twice across pages: %v", seen)
	}
}

// Scopes still apply to a search: unread-only drops a matching read thread.
func TestSearchRespectsScopeFiltersAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	read := plainThread(t, ctx, f, "Budget", "Budget approved", time.Now().UTC().Add(-time.Hour))
	unread := plainThread(t, ctx, f, "Budget", "Budget pending", time.Now().UTC())
	if err := f.store.SetUnread(ctx, f.ws, read.ID, false); err != nil {
		t.Fatalf("SetUnread: %v", err)
	}
	hits, err := f.store.SearchThreads(ctx, f.ws, inbox.SearchFilter{Text: "budget", ListFilter: inbox.ListFilter{UnreadOnly: true}})
	if err != nil {
		t.Fatalf("SearchThreads: %v", err)
	}
	if len(hits) != 1 || hits[0].Thread.ID != unread.ID {
		t.Fatalf("hits = %+v, want only the unread thread", hits)
	}
}

// Arbitrary operator input must never become a 500: websearch_to_tsquery
// accepts any string, so quotes, tsquery operators and SQL punctuation all come
// back as ordinary (possibly empty) results — and a query with nothing
// positive to look for is refused as ErrSearchNotSelective, never run.
func TestSearchToleratesOddInputAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	th := plainThread(t, ctx, f, "Contract", `He said "net thirty" terms are fine; O'Brien agrees.`, time.Now().UTC())

	for _, text := range []string{
		`"net thirty`,             // unbalanced quote
		`net & thirty | !x <-> (`, // raw tsquery operators, read as words
		`'; DROP TABLE inbox_messages; --`,
		strings.Repeat("word ", 51), // at the length cap
		"naïve café 日本語 emoji😀",
		`contract -brien`, // an exclusion alongside a positive term is fine
	} {
		if _, err := f.store.SearchThreads(ctx, f.ws, inbox.SearchFilter{Text: text}); err != nil {
			t.Errorf("SearchThreads(%q) errored: %v", text, err)
		}
	}
	for _, text := range []string{
		`the`,     // stop words only: an empty tsquery
		`!!! ???`, // no lexemes at all
		`':* \ ' ''`,
		`-`,
		`OR`,
		`-contract`,          // pure negation: would match nearly everything
		`-contract -brien`,   // still nothing positive
		`contract OR -brien`, // an OR with a negation needs no positive match
	} {
		if _, err := f.store.SearchThreads(ctx, f.ws, inbox.SearchFilter{Text: text}); !errors.Is(err, inbox.ErrSearchNotSelective) {
			t.Errorf("SearchThreads(%q) err = %v, want ErrSearchNotSelective", text, err)
		}
	}
	// And the syntax works as advertised: a quoted phrase matches in order,
	// -exclusion excludes.
	if hits := search(t, ctx, f, `"net thirty"`); len(hits) != 1 || hits[0].Thread.ID != th.ID {
		t.Errorf("phrase hits = %+v, want the thread", hits)
	}
	if hits := search(t, ctx, f, `"thirty net"`); len(hits) != 0 {
		t.Errorf("reversed phrase hits = %+v, want none", hits)
	}
	if hits := search(t, ctx, f, `contract -brien`); len(hits) != 0 {
		t.Errorf("exclusion hits = %+v, want none", hits)
	}
}

// seedMatchingMessages inserts n inbound messages containing token into one
// thread of f's workspace, in a single statement (committed: the store runs
// its own transaction and must see them).
func seedMatchingMessages(t *testing.T, ctx context.Context, f *fixture, token string, n int) {
	t.Helper()
	th := plainThread(t, ctx, f, "Bulk", "bulk seed", time.Now().UTC())
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO inbox_messages (thread_id, workspace_id, mailbox_id, direction, subject, body_text, occurred_at)
		 SELECT $1, $2, $3, 'inbound', 'bulk', $4 || ' number ' || g, now() - make_interval(secs => g)
		 FROM generate_series(1, $5::int) g`,
		th.ID, f.ws, f.mailbox, token, n,
	); err != nil {
		t.Fatalf("seed %d matching messages: %v", n, err)
	}
}

// The candidate cap is a refusal, not a truncation: at exactly
// MaxSearchCandidates matching messages the search answers; one more and it is
// ErrSearchTooBroad, with no partial page. A more specific query over the same
// data still answers.
func TestSearchRefusesAQueryOverTheCandidateCapAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	token := "capword" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	seedMatchingMessages(t, ctx, f, token, inbox.MaxSearchCandidates-1)
	rare := plainThread(t, ctx, f, "Rare", token+" and a rare quokka", time.Now().UTC())

	// Exactly at the cap (cap-1 seeded + the rare thread's one message).
	hits, err := f.store.SearchThreads(ctx, f.ws, inbox.SearchFilter{Text: token})
	if err != nil {
		t.Fatalf("at the cap: err = %v, want an answer", err)
	}
	if len(hits) != 2 {
		t.Fatalf("at the cap: %d hits, want both threads", len(hits))
	}

	plainThread(t, ctx, f, "One more", token+" pushes it over", time.Now().UTC())
	if hits, err := f.store.SearchThreads(ctx, f.ws, inbox.SearchFilter{Text: token}); !errors.Is(err, inbox.ErrSearchTooBroad) || hits != nil {
		t.Fatalf("over the cap: hits=%d err=%v, want no page and ErrSearchTooBroad", len(hits), err)
	}
	if hits := search(t, ctx, f, token+" quokka"); len(hits) != 1 || hits[0].Thread.ID != rare.ID {
		t.Fatalf("narrowed query: hits = %+v, want the rare thread", hits)
	}
}

// A search that runs past its statement_timeout is ErrSearchTimeout (a 503),
// and the timeout is transaction-local: the pooled connection comes back with
// its normal setting, so no later query inherits the search's budget.
func TestSearchTimeoutIsTypedAndTransactionLocalAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	token := "slowword" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	seedMatchingMessages(t, ctx, f, token, 5000)

	// One connection, so the SHOW below is guaranteed to run on the connection
	// the timed-out search used.
	cfg, err := pgxpool.ParseConfig(dbtest.DSN(t))
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.MaxConns, cfg.MinConns = 1, 0
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	var before string
	if err := pool.QueryRow(ctx, `SHOW statement_timeout`).Scan(&before); err != nil {
		t.Fatalf("show before: %v", err)
	}

	slow := inbox.NewPgStoreWithSearchTimeout(pool, time.Millisecond)
	if _, err := slow.SearchThreads(ctx, f.ws, inbox.SearchFilter{Text: token}); !errors.Is(err, inbox.ErrSearchTimeout) {
		t.Fatalf("err = %v, want ErrSearchTimeout", err)
	}

	var after string
	if err := pool.QueryRow(ctx, `SHOW statement_timeout`).Scan(&after); err != nil {
		t.Fatalf("show after: %v", err)
	}
	if after != before {
		t.Fatalf("statement_timeout leaked onto the pooled connection: %q, want %q", after, before)
	}
	// And the default-budget store answers the same search.
	if _, err := inbox.NewPgStore(pool).SearchThreads(ctx, f.ws, inbox.SearchFilter{Text: token}); err != nil {
		t.Fatalf("default timeout: %v", err)
	}
}

// A message cannot forge a highlight by containing the private-use markers the
// store asks ts_headline to emit: they are stripped from the input first.
func TestSearchStripsHighlightMarkersFromMessageTextAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	plainThread(t, ctx, f, "Forged \uE000subject\uE001", "Innocent \uE000forged\uE001 text about lighthouses.", time.Now().UTC())

	hits := search(t, ctx, f, "lighthouses")
	if len(hits) != 1 || hits[0].Snippet == nil {
		t.Fatalf("hits = %+v, want one with a snippet", hits)
	}
	s := hits[0].Snippet
	if got := matchedText(s.Body); len(got) != 1 || got[0] != "lighthouses" {
		t.Errorf("highlighted body runs = %q, want only the real match", got)
	}
	if got := matchedText(s.Subject); len(got) != 0 {
		t.Errorf("highlighted subject runs = %q, want none", got)
	}
	for _, seg := range append(append([]inbox.HighlightSegment{}, s.Subject...), s.Body...) {
		if strings.ContainsAny(seg.Text, "\uE000\uE001") {
			t.Errorf("segment %q still carries a marker", seg.Text)
		}
	}
}

// The index expression is evaluated on INSERT, so a document whose tsvector
// would pass Postgres' 1MB limit would fail the insert — and wedge the reply
// poller on that message. The left() cap in inbox_search_document prevents
// it; remove the cap and this insert fails with "string is too long for
// tsvector".
func TestAHugeMessageBodyStillInsertsAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	var b strings.Builder
	for i := 0; b.Len() < 3<<20; i++ {
		fmt.Fprintf(&b, "w%dx ", i)
	}
	th := plainThread(t, ctx, f, "Huge", b.String(), time.Now().UTC())
	if hits := search(t, ctx, f, "w10x"); len(hits) != 1 || hits[0].Thread.ID != th.ID {
		t.Fatalf("hits = %+v, want the huge thread (text inside the indexed prefix is searchable)", hits)
	}
}

// generatedSQL is the exact statement sqlc generated under constant name,
// read from the generated file so this test cannot drift from it.
func generatedSQL(t *testing.T, constant string) string {
	t.Helper()
	src, err := os.ReadFile("../../platform/db/gen/inboxsearch.sql.go")
	if err != nil {
		t.Fatalf("read generated query: %v", err)
	}
	m := regexp.MustCompile("(?s)const " + constant + " = `(.*?)`").FindSubmatch(src)
	if m == nil {
		t.Fatalf("%s constant not found in the generated file", constant)
	}
	return string(m[1])
}

// An expression index is only used when the query repeats its expression
// exactly. This gives the workspace a realistic volume of messages, steps and
// variants — in a transaction that is rolled back, statistics included — so
// the planner has a reason to prefer the GIN indexes over a workspace-btree
// scan, then asserts each of the three is the access path. A query that had
// drifted from inbox_search_document(subject, body_text) could not use them at
// all, and would plan as a scan of every row in the workspace instead.
func TestSearchQueryUsesTheFullTextIndexesAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	campaignID, _, _ := searchCampaign(t, ctx, f, "Hello")
	th := plainThread(t, ctx, f, "Volume", "seed", time.Now().UTC())
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO inbox_messages (thread_id, workspace_id, mailbox_id, direction, subject, body_text, occurred_at)
		  SELECT $1, $2, $3, 'inbound', 'subject ' || md5(g::text), 'body ' || md5(g::text) || ' ' || md5((g * 7)::text), now()
		  FROM generate_series(1, 5000) g`, []any{th.ID, f.ws, f.mailbox}},
		{`INSERT INTO sequence_steps (workspace_id, campaign_id, step_order, subject, body_text)
		  SELECT $1, $2, g, 'subject ' || md5(g::text), 'body ' || md5(g::text) || ' ' || md5((g * 7)::text)
		  FROM generate_series(1, 2000) g`, []any{f.ws, campaignID}},
		{`INSERT INTO sequence_step_variants (workspace_id, step_id, label, subject, body_text)
		  SELECT workspace_id, id, 'B', subject, body_text FROM sequence_steps WHERE campaign_id = $1`, []any{campaignID}},
		{`INSERT INTO contacts (workspace_id, email)
		  SELECT $1, md5(g::text) || '@volume.test' FROM generate_series(1, 5000) g`, []any{f.ws}},
		{`UPDATE inbox_messages SET from_email = md5(id::text) || '@sender.test' WHERE thread_id = $1`, []any{th.ID}},
		{`ANALYZE contacts`, nil},
		{`ANALYZE inbox_messages`, nil},
		{`ANALYZE sequence_steps`, nil},
		{`ANALYZE sequence_step_variants`, nil},
	} {
		if _, err := tx.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("seed volume (%.40s…): %v", stmt.sql, err)
		}
	}
	// The address predicates are exercised too, so their trigram indexes are
	// asserted alongside the full-text ones.
	pattern := "pricing"
	for _, stmt := range []struct {
		constant string
		params   any
	}{
		{"inboxSearchPrecheck", gen.InboxSearchPrecheckParams{
			WorkspaceID: f.ws, Query: "pricing", CandidateCap: inbox.MaxSearchCandidates + 1, AddressPattern: &pattern,
		}},
		{"searchInboxThreads", gen.SearchInboxThreadsParams{
			HighlightStart: "\uE000", HighlightStop: "\uE001", Query: "pricing", WorkspaceID: f.ws,
			CandidateCap: inbox.MaxSearchCandidates + 1, AddressPattern: &pattern, PageLimit: 26,
		}},
	} {
		rows, err := tx.Query(ctx, "EXPLAIN "+generatedSQL(t, stmt.constant), paramArgs(stmt.params)...)
		if err != nil {
			t.Fatalf("explain %s: %v", stmt.constant, err)
		}
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatalf("collect %s plan: %v", stmt.constant, err)
		}
		plan := strings.Join(lines, "\n")
		for _, index := range []string{
			"idx_inbox_messages_search", "idx_sequence_steps_search", "idx_sequence_step_variants_search",
			"idx_contacts_search", "idx_inbox_messages_from_email_search",
		} {
			if !strings.Contains(plan, index) {
				t.Errorf("%s plan does not use %s:\n%s", stmt.constant, index, plan)
			}
		}
	}
}

// paramArgs flattens a sqlc params struct into positional arguments in field
// order — the order the generated function binds them in — so the EXPLAIN
// above cannot silently bind a value to the wrong placeholder when sqlc
// reorders the struct.
func paramArgs(params any) []any {
	v := reflect.ValueOf(params)
	args := make([]any, v.NumField())
	for i := range args {
		args[i] = v.Field(i).Interface()
	}
	return args
}

// The migration's down path must actually reverse it, and up must re-apply
// cleanly afterwards. Run on a scratch database: rolling back the shared one
// would pull the functions out from under every other package's tests.
func TestInboxFullTextSearchMigrationRollsBackAndReappliesAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	const before, this = 20260921111415, 20260923105701
	dsn := dbtest.ScratchDSN(t, "inbox_fts")
	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	objects := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM pg_proc WHERE proname IN ('inbox_search_document', 'inbox_search_query'))
			     + (SELECT count(*) FROM pg_indexes WHERE indexname IN
			         ('idx_inbox_messages_search', 'idx_sequence_steps_search', 'idx_sequence_step_variants_search'))`,
		).Scan(&n); err != nil {
			t.Fatalf("count objects: %v", err)
		}
		return n
	}
	if got := objects(); got != 5 {
		t.Fatalf("%d search objects after up, want 5", got)
	}
	if err := db.MigrateTo(dsn, before); err != nil {
		t.Fatalf("migrate down to %d: %v", before, err)
	}
	if got := objects(); got != 0 {
		t.Fatalf("%d search objects after down, want 0", got)
	}
	if err := db.MigrateTo(dsn, this); err != nil {
		t.Fatalf("migrate up again to %d: %v", this, err)
	}
	if got := objects(); got != 5 {
		t.Fatalf("%d search objects after re-up, want 5", got)
	}
}
