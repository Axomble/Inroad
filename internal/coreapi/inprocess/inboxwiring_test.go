package inprocess

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/inbox"
)

// The execution plane's inbox service must be able to CLAIM and COMPLETE the
// sends it is handed. Its compose and pending-reply stores are optional
// ServiceOptions, and the worker's service was built with none of them:
//
//	inbox.NewService(inbox.NewPgStore(pool))
//
// That left s.compose and s.pending unset, so every claim path failed its
// ComposeClaimer / PendingReplyClaimer type assertion and returned
// "this compose store cannot claim". No composed email or deferred reply could
// EVER be sent: the row stayed `scheduled` with an empty last_error while the
// asynq task retried to exhaustion and was archived, so the UI showed a queued
// message that silently never left.
//
// Nothing caught it. The claim paths are only reachable from the worker, whose
// tests inject a fake ComposeCore rather than a real inbox.Service, and the
// API's own service IS wired correctly, so every unit test passed against a
// send path that could not run in production.
//
// These tests assert the WIRING, not the persistence: they call each claim
// method with a nil-store service and require that the failure is no longer the
// "cannot claim" validation error. A wired service gets past the assertion and
// then fails on the database (which is absent here), and either a nil-pointer
// panic or a connection error is proof the assertion passed. That is what keeps
// this a fast unit test while still pinning the exact regression.
func TestExecutionPlaneInboxServiceCanClaim(t *testing.T) {
	svc := newInboxService(nil)
	ctx := context.Background()
	ws, id := uuid.New(), uuid.New()

	// Every execution-plane entry point that guards on a claimer assertion.
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"ClaimPendingCompose", func() error { return svc.ClaimPendingCompose(ctx, ws, id) }},
		{"MarkPendingComposeSent", func() error { return svc.MarkPendingComposeSent(ctx, ws, id, "<m@x>") }},
		{"ReleasePendingCompose", func() error { return svc.ReleasePendingCompose(ctx, ws, id, "why") }},
		{"FailPendingCompose", func() error { return svc.FailPendingCompose(ctx, ws, id, "why") }},
		{"ClaimPendingReply", func() error { return svc.ClaimPendingReply(ctx, ws, id) }},
		{"MarkPendingReplySent", func() error { return svc.MarkPendingReplySent(ctx, ws, id, "<m@x>") }},
		{"ReleasePendingReply", func() error { return svc.ReleasePendingReply(ctx, ws, id, "why") }},
		{"FailPendingReply", func() error { return svc.FailPendingReply(ctx, ws, id, "why") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A nil pool means the call cannot complete; a panic here is the
			// store being reached, which is exactly what we are asserting.
			err := func() (err error) {
				defer func() {
					if r := recover(); r != nil {
						err = nil // reached the store: the assertion passed
					}
				}()
				return tc.call()
			}()
			if err != nil && errors.Is(err, inbox.ErrValidation) {
				t.Fatalf("%s returned a validation error (%v): the store is not wired, "+
					"so this send path is dead in production", tc.name, err)
			}
		})
	}
}
