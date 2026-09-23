package mailbox

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/audit"
	"github.com/inroad/inroad/internal/platform/mail"
)

type captureRecorder struct{ events []audit.Event }

func (c *captureRecorder) Record(_ context.Context, ev audit.Event) error {
	c.events = append(c.events, ev)
	return nil
}

func TestMailboxLifecycleIsAudited(t *testing.T) {
	store := newFakeStore()
	rec := &captureRecorder{}
	svc := NewService(store, &fakeTester{}, newTestKeyring(t), mail.GoogleOAuth{}, nil, mail.MicrosoftOAuth{}, nil, WithAudit(rec))
	ws, uid := uuid.New(), uuid.New()
	ctx := audit.WithActor(context.Background(), audit.UserActor(uid))

	in := validConnectInput()
	box, err := svc.ConnectSMTP(ctx, ws, in)
	if err != nil {
		t.Fatalf("ConnectSMTP: %v", err)
	}
	if _, err := svc.Pause(ctx, ws, box.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if _, err := svc.Resume(ctx, ws, box.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := svc.Delete(ctx, ws, box.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	want := []audit.Action{audit.ActionMailboxConnected, audit.ActionMailboxPaused, audit.ActionMailboxResumed, audit.ActionMailboxDisconnected}
	if len(rec.events) != len(want) {
		t.Fatalf("recorded %d events, want %d", len(rec.events), len(want))
	}
	for i, ev := range rec.events {
		if ev.Action != want[i] || ev.TargetID != box.ID.String() || ev.WorkspaceID != ws || ev.Actor.ID != uid.String() {
			t.Fatalf("event %d = %+v, want %s on %s", i, ev, want[i], box.ID)
		}
		if ev.Metadata["email"] != box.Email {
			t.Fatalf("event %d email = %q, want %q (delete must name the address it removed)", i, ev.Metadata["email"], box.Email)
		}
		for k, v := range ev.Metadata {
			if strings.Contains(v, in.Secret) || strings.Contains(v, in.SMTPHost) {
				t.Fatalf("event %d metadata %q leaks connection details", i, k)
			}
		}
	}
}

func TestFailedConnectIsNotAudited(t *testing.T) {
	store := newFakeStore()
	rec := &captureRecorder{}
	svc := NewService(store, &fakeTester{smtpErr: context.DeadlineExceeded}, newTestKeyring(t), mail.GoogleOAuth{}, nil, mail.MicrosoftOAuth{}, nil, WithAudit(rec))
	if _, err := svc.ConnectSMTP(context.Background(), uuid.New(), validConnectInput()); err == nil {
		t.Fatal("ConnectSMTP succeeded with a failing tester")
	}
	if err := svc.Delete(context.Background(), uuid.New(), uuid.New()); err == nil {
		t.Fatal("Delete of an unknown mailbox succeeded")
	}
	if len(rec.events) != 0 {
		t.Fatalf("recorded %d events for actions that did not happen", len(rec.events))
	}
}
