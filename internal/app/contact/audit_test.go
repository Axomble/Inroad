package contact

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/audit"
)

type captureRecorder struct {
	events []audit.Event
	err    error
}

func (c *captureRecorder) Record(_ context.Context, ev audit.Event) error {
	c.events = append(c.events, ev)
	return c.err
}

func TestExportIsRecordedBeforeItStreams(t *testing.T) {
	rec := &captureRecorder{}
	svc := NewService(&fakeStore{}, &fakeChecker{exists: true}, &fakeFieldStore{}, WithAudit(rec))
	uid, listID := uuid.New(), uuid.New()
	ctx := audit.WithActor(context.Background(), audit.UserActor(uid))

	if _, err := svc.PrepareExport(ctx, testWS, SearchRequest{Q: "someone@example.test", ListID: &listID}); err != nil {
		t.Fatalf("PrepareExport: %v", err)
	}
	if len(rec.events) != 1 {
		t.Fatalf("recorded %d events, want 1", len(rec.events))
	}
	ev := rec.events[0]
	if ev.Action != audit.ActionDataExported || ev.WorkspaceID != testWS || ev.Actor.ID != uid.String() {
		t.Fatalf("event = %+v", ev)
	}
	if ev.Metadata["resource"] != "contacts" || ev.Metadata["filtered"] != "true" || ev.Metadata["list_id"] != listID.String() {
		t.Fatalf("metadata = %v", ev.Metadata)
	}
	for k, v := range ev.Metadata {
		if v == "someone@example.test" {
			t.Fatalf("metadata %q carries the search text", k)
		}
	}
}

func TestExportIsRefusedWhenItCannotBeRecorded(t *testing.T) {
	boom := errors.New("audit store down")
	svc := NewService(&fakeStore{}, &fakeChecker{exists: true}, &fakeFieldStore{}, WithAudit(&captureRecorder{err: boom}))
	if _, err := svc.PrepareExport(context.Background(), testWS, SearchRequest{}); !errors.Is(err, boom) {
		t.Fatalf("PrepareExport err = %v, want the audit failure (fail closed)", err)
	}
}

func TestRejectedExportIsNotRecorded(t *testing.T) {
	rec := &captureRecorder{}
	listID := uuid.New()
	svc := NewService(&fakeStore{}, &fakeChecker{exists: false}, &fakeFieldStore{}, WithAudit(rec))
	if _, err := svc.PrepareExport(context.Background(), testWS, SearchRequest{ListID: &listID}); !errors.Is(err, ErrListNotFound) {
		t.Fatalf("err = %v, want ErrListNotFound", err)
	}
	if len(rec.events) != 0 {
		t.Fatalf("recorded %d events for an export that never started", len(rec.events))
	}
}
