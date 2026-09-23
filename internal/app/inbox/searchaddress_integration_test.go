//go:build integration

package inbox_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/inbox"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/dbtest"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// addressThread records a thread linked to a contact with the given email, whose
// only inbound message mentions nothing of the address in its text.
func addressThread(t *testing.T, ctx context.Context, f *fixture, email string, at time.Time) inbox.Thread {
	t.Helper()
	contact, err := f.q.UpsertContact(ctx, gen.UpsertContactParams{WorkspaceID: f.ws, Email: email, FirstName: "Jo"})
	if err != nil {
		t.Fatalf("contact: %v", err)
	}
	th, err := f.store.RecordReply(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, ContactID: &contact.ID,
		RootMessageID: "<addr-" + uuid.NewString() + "@sender.test>", Subject: "Re: hello", LastReplyClass: "neutral",
	}, inbox.InsertMessageInput{
		Direction: "inbound", FromEmail: "someone-else@elsewhere.test", Subject: "Re: hello", BodyText: "sounds good", OccurredAt: at,
	})
	if err != nil {
		t.Fatalf("RecordReply: %v", err)
	}
	return th
}

func uniqueDomain() string {
	return "acme" + strings.ReplaceAll(uuid.NewString()[:8], "-", "") + ".test"
}

// The regression PART C closes: the inbox list's old search matched the
// contact's email, so an operator typing it into the new search must still find
// the thread — exactly, by a fragment, and regardless of case — reported as a
// "contact" match with the thread's newest message as its snippet.
func TestSearchMatchesTheContactsEmailAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	domain := uniqueDomain()
	th := addressThread(t, ctx, f, "jo.smith@"+domain, time.Now().UTC())

	for name, q := range map[string]string{
		"exact":            "jo.smith@" + domain,
		"domain fragment":  domain,
		"local part + @":   "jo.smith@" + domain[:6],
		"case-insensitive": strings.ToUpper("Jo.Smith@" + domain),
	} {
		t.Run(name, func(t *testing.T) {
			hits := search(t, ctx, f, q)
			if len(hits) != 1 || hits[0].Thread.ID != th.ID {
				t.Fatalf("hits = %+v, want exactly the contact's thread", hits)
			}
			h := hits[0]
			if !h.MatchedAddress || h.MatchedInbound || h.MatchedOutbound {
				t.Errorf("matched address=%v inbound=%v outbound=%v, want address only", h.MatchedAddress, h.MatchedInbound, h.MatchedOutbound)
			}
			if h.Snippet == nil || h.Snippet.Direction != "inbound" || len(h.Snippet.Body) == 0 || h.Snippet.Body[0].Text != "sounds good" {
				t.Errorf("snippet = %+v, want the thread's newest message", h.Snippet)
			}
		})
	}
}

// A name or company that contains the text is NOT an address match: the
// contact index covers name and company too, and the email re-check must keep
// them out.
func TestSearchAddressMatchIgnoresContactNamesAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	token := "zanzibar" + strings.ReplaceAll(uuid.NewString()[:6], "-", "")
	contact, err := f.q.UpsertContact(ctx, gen.UpsertContactParams{
		WorkspaceID: f.ws, Email: "plain-" + uuid.NewString()[:8] + "@x.test", FirstName: token,
	})
	if err != nil {
		t.Fatalf("contact: %v", err)
	}
	if _, err := f.store.RecordReply(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, ContactID: &contact.ID,
		RootMessageID: "<name-" + uuid.NewString() + "@sender.test>", Subject: "hi", LastReplyClass: "neutral",
	}, inbox.InsertMessageInput{Direction: "inbound", BodyText: "hello", OccurredAt: time.Now().UTC()}); err != nil {
		t.Fatalf("RecordReply: %v", err)
	}
	if hits := search(t, ctx, f, token); len(hits) != 0 {
		t.Fatalf("hits = %+v, want none: the text is the contact's NAME, not an address", hits)
	}
}

// A thread with no linked contact (a legacy match) is still findable by the
// From address of its inbound mail — but never by an OUTBOUND message's From,
// which is the workspace's own mailbox and would match every thread it sent.
func TestSearchMatchesTheInboundFromAddressAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	domain := uniqueDomain()
	legacy, err := f.store.RecordReply(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, Subject: "Question", LastReplyClass: "neutral",
	}, inbox.InsertMessageInput{
		Direction: "inbound", FromEmail: "Pat@" + strings.ToUpper(domain), BodyText: "a question", OccurredAt: time.Now().UTC().Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("RecordReply: %v", err)
	}
	ownDomain := uniqueDomain()
	if err := f.store.RecordOutboundReply(ctx, legacy.ID, f.ws, inbox.InsertMessageInput{
		Direction: "outbound", FromEmail: "me@" + ownDomain, BodyText: "an answer", OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordOutboundReply: %v", err)
	}

	hits := search(t, ctx, f, "pat@"+domain)
	if len(hits) != 1 || hits[0].Thread.ID != legacy.ID || !hits[0].MatchedAddress {
		t.Fatalf("hits = %+v, want the legacy thread matched by its sender", hits)
	}
	// The newest message is the outbound answer.
	if s := hits[0].Snippet; s == nil || s.Direction != "outbound" {
		t.Errorf("snippet = %+v, want the thread's newest message (the outbound answer)", s)
	}
	if hits := search(t, ctx, f, ownDomain); len(hits) != 0 {
		t.Fatalf("hits = %+v, want none: an outbound From is our own mailbox", hits)
	}
}

// Text and address together: both reasons are reported, and the snippet is the
// text match (highlighted), not merely the newest message.
func TestSearchReportsTextAndAddressMatchesTogetherAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	domain := uniqueDomain()
	contact, err := f.q.UpsertContact(ctx, gen.UpsertContactParams{WorkspaceID: f.ws, Email: "lee@" + domain})
	if err != nil {
		t.Fatalf("contact: %v", err)
	}
	th, err := f.store.RecordReply(ctx, inbox.UpsertThreadInput{
		WorkspaceID: f.ws, MailboxID: f.mailbox, ContactID: &contact.ID,
		RootMessageID: "<both-" + uuid.NewString() + "@sender.test>", Subject: "Re: hi", LastReplyClass: "neutral",
	}, inbox.InsertMessageInput{
		Direction: "inbound", FromEmail: "lee@" + domain, BodyText: "write to me at lee@" + domain + " please", OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("RecordReply: %v", err)
	}
	hits := search(t, ctx, f, "lee@"+domain)
	if len(hits) != 1 || hits[0].Thread.ID != th.ID {
		t.Fatalf("hits = %+v, want the thread", hits)
	}
	if h := hits[0]; !h.MatchedInbound || !h.MatchedAddress || h.Snippet == nil || len(matchedText(h.Snippet.Body)) == 0 {
		t.Errorf("hit = %+v, want inbound + address with a highlighted snippet", h)
	}
}

// Another workspace's contact with a matching email is never an address match
// here, and a query shorter than MinAddressQueryLength is not address-matched
// at all (it would defeat the trigram index).
func TestSearchAddressMatchIsWorkspaceScopedAndLengthGatedAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	foreign := &fixture{pool: f.pool, q: f.q, store: f.store}
	foreign.ws, foreign.mailbox = seedTenant(t, ctx, f.q)
	domain := uniqueDomain()
	addressThread(t, ctx, foreign, "kim@"+domain, time.Now().UTC())

	if hits := search(t, ctx, f, "kim@"+domain); len(hits) != 0 {
		t.Fatalf("hits = %+v, want none: the contact belongs to another workspace", hits)
	}
	if hits := search(t, ctx, foreign, "kim@"+domain); len(hits) != 1 {
		t.Fatalf("foreign hits = %+v, want its own thread (else this test proves nothing)", hits)
	}

	short := addressThread(t, ctx, f, "zq@"+uniqueDomain(), time.Now().UTC())
	if hits := search(t, ctx, f, "zq"); len(hits) != 0 {
		t.Fatalf("hits = %+v (thread %s), want none: a 2-character query is not address-matched", hits, short.ID)
	}
}

// Address candidates are capped like text candidates: more than
// MaxSearchCandidates contacts whose email contains the query is a refusal,
// never a page drawn from an arbitrary subset of them.
func TestSearchRefusesAnAddressQueryOverTheCandidateCapAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)
	domain := uniqueDomain()
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO contacts (workspace_id, email) SELECT $1, 'c' || g || '@' || $2 FROM generate_series(1, $3::int) g`,
		f.ws, domain, inbox.MaxSearchCandidates+1,
	); err != nil {
		t.Fatalf("seed contacts: %v", err)
	}
	if _, err := f.store.SearchThreads(ctx, f.ws, inbox.SearchFilter{Text: domain}); !errors.Is(err, inbox.ErrSearchTooBroad) {
		t.Fatalf("err = %v, want ErrSearchTooBroad", err)
	}
	if hits := search(t, ctx, f, "c42@"+domain); len(hits) != 0 {
		t.Fatalf("hits = %+v, want none (the contact has no thread) and no refusal", hits)
	}
}

// The sender-address index migration reverses and re-applies cleanly, on a
// scratch database for the reason the full-text migration's test gives.
func TestInboxSenderAddressSearchMigrationRollsBackAndReappliesAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	const before = 20260923105701
	dsn := dbtest.ScratchDSN(t, "inbox_sender_search")
	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	indexes := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM pg_indexes WHERE indexname = 'idx_inbox_messages_from_email_search'`).Scan(&n); err != nil {
			t.Fatalf("count index: %v", err)
		}
		return n
	}
	if got := indexes(); got != 1 {
		t.Fatalf("%d indexes after up, want 1", got)
	}
	if err := db.MigrateTo(dsn, before); err != nil {
		t.Fatalf("migrate down to %d: %v", before, err)
	}
	if got := indexes(); got != 0 {
		t.Fatalf("%d indexes after down, want 0", got)
	}
	if err := db.Migrate(dsn); err != nil {
		t.Fatalf("migrate up again: %v", err)
	}
	if got := indexes(); got != 1 {
		t.Fatalf("%d indexes after re-up, want 1", got)
	}
}
