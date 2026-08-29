//go:build integration

package inprocess

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/app/inbox"
	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/realtime"
	"github.com/inroad/inroad/internal/platform/warmup"
)

// These tests exercise StoreInboundMessage (the coreapi.InboxCaptureClient
// seam) against Postgres, reusing claimConnect/seedForClaim from
// claim_integration_test.go and itKeyring/itMasterKey from
// warmupsendjob_integration_test.go. Docker must be up.

// newInboxCaptureClient builds the in-process coreapi.Client and asserts it
// implements the optional InboxCaptureClient capability — the same
// type-assertion pattern the inbox poller (internal/worker/inbox) uses at
// its own call site, proven here against the real constructor rather than a
// fake.
func newInboxCaptureClient(t *testing.T, pool *pgxpool.Pool, q *gen.Queries) coreapi.InboxCaptureClient {
	t.Helper()
	core := New(pool, itKeyring(t, q), []byte("0123456789abcdef0123456789abcdef"),
		"https://app.test", mail.GoogleOAuth{}, mail.MicrosoftOAuth{},
		[]byte("warmup-secret-0123456789abcdef"), warmup.NewStaticLibrary())
	capture, ok := core.(coreapi.InboxCaptureClient)
	if !ok {
		t.Fatal("in-process coreapi.Client does not implement InboxCaptureClient")
	}
	return capture
}

// The core round trip: StoreInboundMessage persists a thread+message reachable
// through the SAME workspace, and is invisible to a workspace it was never
// granted under — the workspace pin is enforced by the underlying
// inbox.Service/Store, not merely by this seam's own bookkeeping.
func TestStoreInboundMessagePersistsAndIsWorkspacePinned(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)
	client := newInboxCaptureClient(t, pool, q)

	rootMessageID := "<root-" + uuid.NewString() + "@sender.test>"
	err := client.StoreInboundMessage(ctx, coreapi.InboxMessageInput{
		WorkspaceID: fx.ws.String(), MailboxID: fx.mailboxID.String(),
		RootMessageID: rootMessageID, Subject: "Re: intro",
		MessageID: "<reply-" + uuid.NewString() + "@sender.test>",
		FromEmail: "prospect@example.com", ToEmail: fx.email,
		BodyText: "sounds good", ReplyClass: "positive", OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("StoreInboundMessage: %v", err)
	}

	// Read back through the inbox domain's own service — the seam this
	// endpoint is meant to feed, not a raw SQL peek.
	inboxSvc := inbox.NewService(inbox.NewPgStore(pool))
	page, err := inboxSvc.ListThreads(ctx, fx.ws, inbox.ListFilter{})
	if err != nil {
		t.Fatalf("ListThreads (own workspace): %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("own-workspace threads = %+v, want exactly 1", page.Items)
	}
	th := page.Items[0]
	if th.Subject != "Re: intro" || th.LastReplyClass != "positive" || th.RootMessageID != rootMessageID {
		t.Fatalf("thread = %+v, want the persisted subject/class/root", th)
	}
	if th.CampaignID != nil || th.ContactID != nil {
		t.Errorf("thread = %+v, want nil campaign/contact (none were supplied)", th)
	}

	detail, err := inboxSvc.GetThread(ctx, fx.ws, th.ID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if len(detail.Messages) != 1 || detail.Messages[0].BodyText != "sounds good" {
		t.Fatalf("messages = %+v, want the one inbound message just stored", detail.Messages)
	}

	// A workspace this thread was never granted under sees nothing.
	foreignPage, err := inboxSvc.ListThreads(ctx, fx.foreignWS, inbox.ListFilter{})
	if err != nil {
		t.Fatalf("ListThreads (foreign workspace): %v", err)
	}
	if len(foreignPage.Items) != 0 {
		t.Fatalf("foreign workspace saw %d threads, want 0 (workspace pin leaked)", len(foreignPage.Items))
	}
	other := uuid.New() // a workspace that never existed at all
	otherPage, err := inboxSvc.ListThreads(ctx, other, inbox.ListFilter{})
	if err != nil {
		t.Fatalf("ListThreads (unrelated workspace): %v", err)
	}
	if len(otherPage.Items) != 0 {
		t.Fatalf("unrelated workspace saw %d threads, want 0", len(otherPage.Items))
	}
}

// A matched reply's campaign/contact ids round-trip through the *string
// pointer fields into the thread's *uuid.UUID columns.
func TestStoreInboundMessageLinksCampaignAndContactWhenProvided(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)
	client := newInboxCaptureClient(t, pool, q)

	campaignID := fx.campaignID.String()
	contactID := fx.contactID.String()
	err := client.StoreInboundMessage(ctx, coreapi.InboxMessageInput{
		WorkspaceID: fx.ws.String(), MailboxID: fx.mailboxID.String(),
		CampaignID: &campaignID, ContactID: &contactID,
		RootMessageID: "<linked-" + uuid.NewString() + "@sender.test>", Subject: "Re: intro",
		FromEmail: "prospect@example.com", ToEmail: fx.email,
		BodyText: "interested", ReplyClass: "positive", OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("StoreInboundMessage: %v", err)
	}

	inboxSvc := inbox.NewService(inbox.NewPgStore(pool))
	page, err := inboxSvc.ListThreads(ctx, fx.ws, inbox.ListFilter{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("threads = %+v, want exactly 1", page.Items)
	}
	th := page.Items[0]
	if th.CampaignID == nil || *th.CampaignID != fx.campaignID {
		t.Errorf("campaign_id = %v, want %s", th.CampaignID, fx.campaignID)
	}
	if th.ContactID == nil || *th.ContactID != fx.contactID {
		t.Errorf("contact_id = %v, want %s", th.ContactID, fx.contactID)
	}
}

// A malformed id is rejected before anything is written — a coreapi
// implementation detail (uuid.Parse), but worth pinning so a caller sees an
// error rather than a silently no-op'd capture.
func TestStoreInboundMessageRejectsMalformedIDs(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)
	client := newInboxCaptureClient(t, pool, q)

	if err := client.StoreInboundMessage(ctx, coreapi.InboxMessageInput{
		WorkspaceID: "not-a-uuid", MailboxID: fx.mailboxID.String(),
	}); err == nil {
		t.Fatal("want an error for a malformed workspace id, got nil")
	}

	badCampaign := "not-a-uuid"
	if err := client.StoreInboundMessage(ctx, coreapi.InboxMessageInput{
		WorkspaceID: fx.ws.String(), MailboxID: fx.mailboxID.String(), CampaignID: &badCampaign,
	}); err == nil {
		t.Fatal("want an error for a malformed campaign id, got nil")
	}

	inboxSvc := inbox.NewService(inbox.NewPgStore(pool))
	page, err := inboxSvc.ListThreads(ctx, fx.ws, inbox.ListFilter{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("threads = %+v, want 0 (a rejected malformed id must write nothing)", page.Items)
	}
}

// newInboxCaptureClientWithRealtime is newInboxCaptureClient plus a publisher,
// so the realtime emit can be observed on the REAL StoreInboundMessage path
// rather than by calling the unexported helper directly. That distinction is the
// point of these two tests: the swallow-vs-propagate decision lives in the
// caller, so a unit test on the helper cannot see it.
func newInboxCaptureClientWithRealtime(
	t *testing.T, pool *pgxpool.Pool, q *gen.Queries, pub realtime.Publisher,
) coreapi.InboxCaptureClient {
	t.Helper()
	core := New(pool, itKeyring(t, q), []byte("0123456789abcdef0123456789abcdef"),
		"https://app.test", mail.GoogleOAuth{}, mail.MicrosoftOAuth{},
		[]byte("warmup-secret-0123456789abcdef"), warmup.NewStaticLibrary(),
		WithRealtime(pub))
	capture, ok := core.(coreapi.InboxCaptureClient)
	if !ok {
		t.Fatal("in-process coreapi.Client does not implement InboxCaptureClient")
	}
	return capture
}

// A stored inbound reply publishes exactly one realtime event, pinned to the
// storing workspace — the first real event in the system, and the whole point of
// the realtime slice.
func TestStoreInboundMessagePublishesARealtimeEvent(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)
	pub := &recordingPublisher{}
	client := newInboxCaptureClientWithRealtime(t, pool, q, pub)

	if err := client.StoreInboundMessage(ctx, coreapi.InboxMessageInput{
		WorkspaceID: fx.ws.String(),
		MailboxID:   fx.mailboxID.String(),
		MessageID:   "<realtime-1@example.test>",
		FromEmail:   "lead@example.test",
		ToEmail:     "rep@example.test",
		Subject:     "re: quick question",
		BodyText:    "sounds good",
		ReplyClass:  "interested",
		OccurredAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("StoreInboundMessage: %v", err)
	}

	if len(pub.calls) != 1 {
		t.Fatalf("published %d events, want exactly 1", len(pub.calls))
	}
	got := pub.calls[0]
	if got.workspaceID != fx.ws {
		t.Errorf("published to workspace %v, want %v", got.workspaceID, fx.ws)
	}
	if got.envelope.Type != "inbox.message.created" {
		t.Errorf("Type = %q, want inbox.message.created", got.envelope.Type)
	}
	if got.envelope.Subject.Kind != "thread" || got.envelope.Subject.ID == "" {
		t.Errorf("Subject = %+v, want a thread id", got.envelope.Subject)
	}
}

// THE load-bearing test of this slice. By the time the event is published the
// message is already COMMITTED, so a broker outage must not surface as an error:
// the poller would retry, re-read the mailbox, and could re-deliver mail. The
// realtime event is an optimisation over polling, never a reason to fail real
// work.
//
// This must exercise StoreInboundMessage rather than publishInboxMessageCreated,
// because the swallow happens in the caller — a unit test on the helper passes
// even when the caller propagates.
func TestStoreInboundMessageSucceedsWhenRealtimePublishFails(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)
	client := newInboxCaptureClientWithRealtime(t, pool, q,
		&recordingPublisher{err: errors.New("redis is down")})

	if err := client.StoreInboundMessage(ctx, coreapi.InboxMessageInput{
		WorkspaceID: fx.ws.String(),
		MailboxID:   fx.mailboxID.String(),
		MessageID:   "<realtime-2@example.test>",
		FromEmail:   "lead@example.test",
		ToEmail:     "rep@example.test",
		Subject:     "re: quick question",
		BodyText:    "sounds good",
		ReplyClass:  "interested",
		OccurredAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("StoreInboundMessage returned %v; a failed realtime publish must not fail a committed write", err)
	}

	// And the message really is there: the point is that the write survived, not
	// merely that no error came back.
	svc := inbox.NewService(inbox.NewPgStore(pool))
	page, err := svc.ListThreads(ctx, fx.ws, inbox.ListFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(page.Items) == 0 {
		t.Fatal("no thread stored; the reply was lost when the publish failed")
	}
}
