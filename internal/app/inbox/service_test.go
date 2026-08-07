package inbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/inbox"
)

type fakeStore struct {
	threads  map[uuid.UUID]inbox.Thread
	messages map[uuid.UUID][]inbox.Message
	// lastListFilter records the filter the most recent ListThreads call
	// received, so handler tests can assert the HTTP layer actually threads a
	// query param through to the Store without needing a real Postgres LIKE
	// match to prove it.
	lastListFilter inbox.ListFilter
}

func newFakeStore() *fakeStore {
	return &fakeStore{threads: map[uuid.UUID]inbox.Thread{}, messages: map[uuid.UUID][]inbox.Message{}}
}

func (f *fakeStore) UpsertThread(_ context.Context, in inbox.UpsertThreadInput) (inbox.Thread, error) {
	for _, t := range f.threads {
		if t.WorkspaceID == in.WorkspaceID && t.MailboxID == in.MailboxID && in.RootMessageID != "" && t.RootMessageID == in.RootMessageID {
			t.LastReplyClass, t.Unread, t.LastMessageAt = in.LastReplyClass, true, time.Now().UTC()
			f.threads[t.ID] = t
			return t, nil
		}
	}
	th := inbox.Thread{ID: uuid.New(), WorkspaceID: in.WorkspaceID, MailboxID: in.MailboxID,
		CampaignID: in.CampaignID, ContactID: in.ContactID, RootMessageID: in.RootMessageID,
		Subject: in.Subject, LastReplyClass: in.LastReplyClass, Unread: true, LastMessageAt: time.Now().UTC()}
	f.threads[th.ID] = th
	return th, nil
}

func (f *fakeStore) InsertMessage(_ context.Context, in inbox.InsertMessageInput) error {
	f.messages[in.ThreadID] = append(f.messages[in.ThreadID], inbox.Message{
		ThreadID: in.ThreadID, Direction: in.Direction, MessageID: in.MessageID,
		FromEmail: in.FromEmail, BodyText: in.BodyText, BodyHTML: in.BodyHTML,
		ReplyClass: in.ReplyClass, OccurredAt: in.OccurredAt,
	})
	return nil
}

// RecordReply is the fake's analogue of PgStore's single-transaction method:
// it calls its own UpsertThread + InsertMessage internally. No real
// transaction is needed for an in-memory fake (there is nothing to roll back
// to), but the call sequence — and the ThreadID/WorkspaceID override — mirror
// PgStore.RecordReply exactly, so Service's thin pass-through is exercised
// the same way here as against real Postgres.
func (f *fakeStore) RecordReply(ctx context.Context, threadIn inbox.UpsertThreadInput, msgIn inbox.InsertMessageInput) (inbox.Thread, error) {
	th, err := f.UpsertThread(ctx, threadIn)
	if err != nil {
		return inbox.Thread{}, err
	}
	msgIn.ThreadID = th.ID
	msgIn.WorkspaceID = threadIn.WorkspaceID
	if err := f.InsertMessage(ctx, msgIn); err != nil {
		return inbox.Thread{}, err
	}
	return th, nil
}

func (f *fakeStore) ListThreads(_ context.Context, ws uuid.UUID, filter inbox.ListFilter) (inbox.ThreadPage, error) {
	f.lastListFilter = filter
	var items []inbox.Thread
	for _, t := range f.threads {
		if t.WorkspaceID == ws {
			items = append(items, t)
		}
	}
	return inbox.ThreadPage{Items: items}, nil
}

func (f *fakeStore) GetThread(_ context.Context, ws, id uuid.UUID) (inbox.ThreadDetail, error) {
	t, ok := f.threads[id]
	if !ok || t.WorkspaceID != ws {
		return inbox.ThreadDetail{}, inbox.ErrNotFound
	}
	return inbox.ThreadDetail{Thread: t, Messages: f.messages[id]}, nil
}

func (f *fakeStore) SetUnread(_ context.Context, ws, id uuid.UUID, unread bool) error {
	t, ok := f.threads[id]
	if !ok || t.WorkspaceID != ws {
		return inbox.ErrNotFound
	}
	t.Unread = unread
	f.threads[id] = t
	return nil
}

// RecordOutboundReply is the fake's analogue of PgStore's single-transaction
// method: it appends the message and bumps last_message_at, mirroring
// RecordReply's fake above but WITHOUT flipping unread — the real behavioral
// difference Reply's tests rely on.
func (f *fakeStore) RecordOutboundReply(_ context.Context, threadID, ws uuid.UUID, msgIn inbox.InsertMessageInput) error {
	t, ok := f.threads[threadID]
	if !ok || t.WorkspaceID != ws {
		return inbox.ErrNotFound
	}
	f.messages[threadID] = append(f.messages[threadID], inbox.Message{
		ThreadID: threadID, Direction: msgIn.Direction, MessageID: msgIn.MessageID,
		FromEmail: msgIn.FromEmail, FromName: msgIn.FromName, ToEmail: msgIn.ToEmail,
		Subject: msgIn.Subject, BodyText: msgIn.BodyText, BodyHTML: msgIn.BodyHTML,
		ReplyClass: msgIn.ReplyClass, OccurredAt: msgIn.OccurredAt,
	})
	t.LastMessageAt = time.Now().UTC()
	f.threads[threadID] = t
	return nil
}

func TestUpsertThreadCreatesOnFirstReplyThenUpdatesOnSecond(t *testing.T) {
	store := newFakeStore()
	svc := inbox.NewService(store)
	ws, mailbox := uuid.New(), uuid.New()

	first, err := svc.RecordReply(context.Background(), inbox.RecordReplyInput{
		WorkspaceID: ws, MailboxID: mailbox, RootMessageID: "root-1", Subject: "Hi",
		LastReplyClass: "neutral", Message: inbox.InsertMessageInput{Direction: "inbound", BodyText: "ok"},
	})
	if err != nil {
		t.Fatalf("first reply: %v", err)
	}
	if len(store.messages[first.ID]) != 1 {
		t.Fatalf("expected 1 stored message after first reply, got %d", len(store.messages[first.ID]))
	}

	second, err := svc.RecordReply(context.Background(), inbox.RecordReplyInput{
		WorkspaceID: ws, MailboxID: mailbox, RootMessageID: "root-1", Subject: "Hi",
		LastReplyClass: "positive", Message: inbox.InsertMessageInput{Direction: "inbound", BodyText: "actually yes"},
	})
	if err != nil {
		t.Fatalf("second reply: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("second reply created a NEW thread (%s) instead of reusing %s", second.ID, first.ID)
	}
	if len(store.messages[first.ID]) != 2 {
		t.Fatalf("expected 2 stored messages after second reply, got %d", len(store.messages[first.ID]))
	}
}

func TestGetThreadCrossWorkspaceIsNotFound(t *testing.T) {
	store := newFakeStore()
	svc := inbox.NewService(store)
	wsA, wsB := uuid.New(), uuid.New()
	th, _ := svc.RecordReply(context.Background(), inbox.RecordReplyInput{
		WorkspaceID: wsA, MailboxID: uuid.New(), RootMessageID: "root-2", LastReplyClass: "neutral",
		Message: inbox.InsertMessageInput{Direction: "inbound"},
	})
	if _, err := svc.GetThread(context.Background(), wsB, th.ID); !errors.Is(err, inbox.ErrNotFound) {
		t.Fatalf("cross-workspace get = %v, want ErrNotFound", err)
	}
}

func TestSetUnreadTogglesBothDirections(t *testing.T) {
	store := newFakeStore()
	svc := inbox.NewService(store)
	ws := uuid.New()
	th, _ := svc.RecordReply(context.Background(), inbox.RecordReplyInput{
		WorkspaceID: ws, MailboxID: uuid.New(), RootMessageID: "root-3", LastReplyClass: "neutral",
		Message: inbox.InsertMessageInput{Direction: "inbound"},
	})
	if err := svc.SetUnread(context.Background(), ws, th.ID, false); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	got, _ := svc.GetThread(context.Background(), ws, th.ID)
	if got.Thread.Unread {
		t.Fatal("thread still unread after SetUnread(false)")
	}
	if err := svc.SetUnread(context.Background(), ws, th.ID, true); err != nil {
		t.Fatalf("mark unread: %v", err)
	}
	got, _ = svc.GetThread(context.Background(), ws, th.ID)
	if !got.Thread.Unread {
		t.Fatal("thread still read after SetUnread(true)")
	}
}

// RecordReply is the ONLY write path the worker will use (Task 3/4), so its
// input validation must fail loudly rather than let a malformed direction
// reach the store — the migration's CHECK constraint would reject it anyway,
// but a 500 from a constraint violation is a worse failure mode than a typed
// ErrValidation the caller can branch on.
func TestRecordReplyRejectsInvalidDirection(t *testing.T) {
	svc := inbox.NewService(newFakeStore())
	_, err := svc.RecordReply(context.Background(), inbox.RecordReplyInput{
		WorkspaceID: uuid.New(), MailboxID: uuid.New(), RootMessageID: "root-4",
		Message: inbox.InsertMessageInput{Direction: "sideways"},
	})
	if err == nil {
		t.Fatal("want an error for an invalid direction, got nil")
	}
}

func TestRecordReplyRejectsMissingWorkspaceOrMailbox(t *testing.T) {
	svc := inbox.NewService(newFakeStore())
	base := inbox.RecordReplyInput{
		WorkspaceID: uuid.New(), MailboxID: uuid.New(),
		Message: inbox.InsertMessageInput{Direction: "inbound"},
	}

	missingWorkspace := base
	missingWorkspace.WorkspaceID = uuid.Nil
	if _, err := svc.RecordReply(context.Background(), missingWorkspace); err == nil {
		t.Fatal("want an error for a missing workspace_id, got nil")
	}

	missingMailbox := base
	missingMailbox.MailboxID = uuid.Nil
	if _, err := svc.RecordReply(context.Background(), missingMailbox); err == nil {
		t.Fatal("want an error for a missing mailbox_id, got nil")
	}
}

// A keyset cursor must be BOTH halves or NEITHER: one without the other is a
// malformed request, not "no cursor, give me page one" — silently treating it
// as the first page would hide a client bug (or a URL a user hand-edited) as
// a page that quietly reset to the top.
func TestListThreadsRejectsAHalfSetCursor(t *testing.T) {
	svc := inbox.NewService(newFakeStore())
	ws := uuid.New()
	at := time.Now()

	if _, err := svc.ListThreads(context.Background(), ws, inbox.ListFilter{BeforeLastMessageAt: &at}); err == nil {
		t.Fatal("want an error for before_last_message_at without before_id, got nil")
	}
	id := uuid.New()
	if _, err := svc.ListThreads(context.Background(), ws, inbox.ListFilter{BeforeID: &id}); err == nil {
		t.Fatal("want an error for before_id without before_last_message_at, got nil")
	}
	// Neither set (the first page) is valid.
	if _, err := svc.ListThreads(context.Background(), ws, inbox.ListFilter{}); err != nil {
		t.Fatalf("first page (no cursor) must not error: %v", err)
	}
}

// ListThreads only ever returns threads in the caller's own workspace — the
// fake's ListThreads filters on ws, and this proves the Service passes the
// workspace straight through rather than, say, silently defaulting it.
func TestListThreadsIsWorkspaceScoped(t *testing.T) {
	store := newFakeStore()
	svc := inbox.NewService(store)
	wsA, wsB := uuid.New(), uuid.New()
	if _, err := svc.RecordReply(context.Background(), inbox.RecordReplyInput{
		WorkspaceID: wsA, MailboxID: uuid.New(), RootMessageID: "root-a", LastReplyClass: "neutral",
		Message: inbox.InsertMessageInput{Direction: "inbound"},
	}); err != nil {
		t.Fatalf("seed A: %v", err)
	}

	page, err := svc.ListThreads(context.Background(), wsB, inbox.ListFilter{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("workspace B sees %d threads that belong to workspace A", len(page.Items))
	}
}
