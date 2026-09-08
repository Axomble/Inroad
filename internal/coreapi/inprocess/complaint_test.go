package inprocess

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/deliverability"
	"github.com/inroad/inroad/internal/coreapi"
)

// The permanent/transient split is the whole reason coreapi.ErrInvalidComplaint
// exists: the inbox poller retries a transient failure and SKIPS a permanent one,
// because retrying a permanent one holds the mailbox's cursor forever.
//
// A nil Store is deliberate and not a shortcut — deliverability.Service.Ingest
// validates its input BEFORE it touches the store, so this exercises the real
// service, the real validation and the real error value with no database. If a
// refactor ever moved a store call ahead of validation this test would panic,
// which is the right way to find out.
func TestIngestComplaintReportsAPermanentRejectionAsErrInvalidComplaint(t *testing.T) {
	c := client{breaker: deliverability.NewService(nil)}
	sendID := uuid.NewString()

	// A blank contact address is the one permanently-invalid input actually
	// reachable from the ARF path (a send row whose to_email is empty).
	err := c.IngestComplaint(context.Background(), coreapi.ComplaintInput{
		WorkspaceID: uuid.NewString(), Email: "",
		ProviderEventID: "arf:" + sendID, SendID: sendID,
	})
	if err == nil {
		t.Fatal("expected a blank email to be rejected")
	}
	if !errors.Is(err, coreapi.ErrInvalidComplaint) {
		t.Fatalf("err = %v, want it to wrap coreapi.ErrInvalidComplaint so the poller skips "+
			"rather than retrying a complaint that can never be accepted", err)
	}
	// The service's own error survives the translation, so the log line the poller
	// (or an operator reading a wrapped error) sees still says WHY.
	if !errors.Is(err, deliverability.ErrInvalid) {
		t.Errorf("err = %v, want the underlying deliverability.ErrInvalid preserved", err)
	}
}

// A caller that reaches this method with no send id has a bug, not a case: the ARF
// path resolves the send BEFORE ingesting precisely because a complaint attributed
// to no campaign reaches no breaker. It must fail loudly rather than being filed as
// a permanently-invalid complaint the poller would quietly skip.
func TestIngestComplaintWithoutASendIDIsALoudError(t *testing.T) {
	c := client{breaker: deliverability.NewService(nil)}

	err := c.IngestComplaint(context.Background(), coreapi.ComplaintInput{
		WorkspaceID: uuid.NewString(), Email: "someone@corp.example",
		ProviderEventID: "arf:x",
	})
	if err == nil {
		t.Fatal("expected a missing send id to be an error")
	}
	if errors.Is(err, coreapi.ErrInvalidComplaint) {
		t.Errorf("err = %v, want a plain error: a caller bug must not be skipped as "+
			"an unacceptable complaint", err)
	}
}
