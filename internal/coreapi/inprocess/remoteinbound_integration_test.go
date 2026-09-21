//go:build integration

package inprocess

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/coreapi/remote"
	"github.com/inroad/inroad/internal/platform/credbroker"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/mail"
)

// Slice 4 end to end: the INBOUND-MAIL and WORKER-INFRASTRUCTURE routes, driven
// by the client a fleet host actually runs, against real Postgres, through the
// real handler.
//
// # What is different about this file
//
// The three files beside it (remotejobs, remoteoutcomes, remoteinboxsends) each
// build a HYBRID: an in-process client with a NIL POOL and one remote source
// installed, because until slice 4 that is what cmd/worker built. This one uses
// a bare *remote.Client, because that is what cmd/worker now builds for
// role=send — there is no in-process client and no pool to be nil.
//
// That makes the assertion stronger rather than merely different. A nil pool is
// a value that would panic if anything touched it; a *remote.Client has no pool
// FIELD, so "this client cannot reach the database" is a property of the type.
// Every value asserted below is one only Postgres could have produced.

// remoteInboundCore builds the EXECUTION plane's coreapi client exactly the way
// cmd/worker/main.go now does for a role=send fleet host: the transport IS the
// client, and the credential broker beside it on the same listener.
//
// The return type is coreapi.Client rather than *remote.Client on purpose — the
// worker holds it through the seam, and a test that held the concrete type
// could pass on a method the worker could never reach.
func remoteInboundCore(t *testing.T, baseURL string) coreapi.Client {
	t.Helper()
	opener, err := credbroker.NewHTTPOpener(baseURL, remoteTestToken, true) // httptest speaks http
	if err != nil {
		t.Fatalf("credbroker.NewHTTPOpener: %v", err)
	}
	rc, err := remote.NewClient(baseURL, remoteTestToken, true, opener)
	if err != nil {
		t.Fatalf("remote.NewClient: %v", err)
	}
	return rc
}

// THE headline for slice 4: a worker with no database connection at all moves
// an inbox cursor, registers itself, gets a mailbox routed to it, reports what
// providers told it, and records a dead letter — and every one of those lands
// in Postgres.
//
// Read as one sequence rather than five tests because that is the sequence a
// fleet host performs on boot and on every poll, and because the routing
// assertion DEPENDS on the heartbeat: a worker that has not registered is not
// live, and AssignMailboxWorker correctly refuses to pin a mailbox to it.
func TestAPoollessWorkerDrivesTheInboundAndFleetRoutesAgainstPostgres(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)
	cp := New(pool, itKeyring(t, q), nil, "", mail.GoogleOAuth{}, mail.MicrosoftOAuth{}, nil, nil)
	srv := fleetListener(t, q, itKeyring(t, q), cp)
	c := remoteInboundCore(t, srv.URL)

	t.Run("the IMAP cursor advances", func(t *testing.T) {
		if err := c.SetInboxCursor(ctx, fx.mailboxID.String(), fx.ws.String(), 4242, 99); err != nil {
			t.Fatalf("SetInboxCursor: %v", err)
		}
		mb, err := q.GetMailbox(ctx, gen.GetMailboxParams{ID: fx.mailboxID, WorkspaceID: fx.ws})
		if err != nil {
			t.Fatalf("GetMailbox: %v", err)
		}
		if mb.InboxLastSeenUid != 4242 || mb.InboxUidValidity != 99 {
			t.Errorf("cursor = (%d, %d), want (4242, 99) — the worker's write did not reach Postgres",
				mb.InboxLastSeenUid, mb.InboxUidValidity)
		}
	})

	t.Run("the provider cursor advances", func(t *testing.T) {
		if err := c.SetInboxCursorString(ctx, fx.mailboxID.String(), fx.ws.String(), "history:990011"); err != nil {
			t.Fatalf("SetInboxCursorString: %v", err)
		}
		mb, err := q.GetMailbox(ctx, gen.GetMailboxParams{ID: fx.mailboxID, WorkspaceID: fx.ws})
		if err != nil {
			t.Fatalf("GetMailbox: %v", err)
		}
		if mb.InboxCursor != "history:990011" {
			t.Errorf("cursor = %q, want history:990011", mb.InboxCursor)
		}
		// And the UID cursor is UNTOUCHED, which is the whole reason these are
		// two routes rather than one: an API-provider mailbox must not have its
		// IMAP columns overwritten on every poll.
		if mb.InboxLastSeenUid != 4242 {
			t.Errorf("the opaque cursor write clobbered the UID cursor (%d)", mb.InboxLastSeenUid)
		}
	})

	workerID := "fleet-host-" + uuid.NewString()

	t.Run("the worker registers and is routed to", func(t *testing.T) {
		if err := c.UpsertWorkerHeartbeat(ctx, workerID, "203.0.113.7", "ipv4"); err != nil {
			t.Fatalf("UpsertWorkerHeartbeat: %v", err)
		}
		queue, err := c.AssignMailboxWorker(ctx, fx.mailboxID.String(), fx.ws.String())
		if err != nil {
			t.Fatalf("AssignMailboxWorker: %v", err)
		}
		// The queue name is derived SERVER-SIDE from the registry and the
		// assignment (docs/security.md invariant 23). Nothing on the wire could
		// have asked for this value, so getting it back proves the control
		// plane made the decision.
		if queue != "w:"+workerID {
			t.Fatalf("queue = %q, want w:%s — the placement did not reach Postgres", queue, workerID)
		}
		stored, err := q.GetLiveMailboxWorkerAssignment(ctx, gen.GetLiveMailboxWorkerAssignmentParams{
			MailboxID: fx.mailboxID, WorkspaceID: fx.ws, LiveSince: liveSinceNow(),
		})
		if err != nil {
			t.Fatalf("GetLiveMailboxWorkerAssignment: %v", err)
		}
		if stored != workerID {
			t.Errorf("stored assignment = %q, want %q", stored, workerID)
		}
	})

	t.Run("provider signals are recorded", func(t *testing.T) {
		now := time.Now().UTC()
		err := c.(coreapi.ProviderSignalClient).RecordWorkerProviderSignals(ctx, coreapi.WorkerProviderSignals{
			WorkerID: workerID, WindowStart: now.Add(-5 * time.Minute), WindowEnd: now,
			Counts: []coreapi.WorkerProviderSignalCount{
				{Provider: "smtp", Operation: "send", Reason: "rate_limited", Events: 7},
			},
		})
		if err != nil {
			t.Fatalf("RecordWorkerProviderSignals: %v", err)
		}
		// Read back through the workspace rollup, which joins via the assignment
		// made above — so this also proves the two fleet writes agree about
		// which worker they are talking about.
		rows, err := q.RollupWorkspaceFleetProviderSignals(ctx, gen.RollupWorkspaceFleetProviderSignalsParams{
			WorkspaceID: fx.ws, SignalsSince: pgtype.Timestamptz{Time: now.Add(-time.Hour), Valid: true},
		})
		if err != nil {
			t.Fatalf("RollupWorkspaceFleetProviderSignals: %v", err)
		}
		var found bool
		for _, r := range rows {
			if r.WorkerID == workerID {
				found = true
			}
		}
		if !found {
			t.Errorf("no provider signal recorded for %s; the worker's flush did not reach Postgres", workerID)
		}
	})

	t.Run("a dead letter is captured", func(t *testing.T) {
		enrollment := uuid.NewString()
		payload := []byte(`{"enrollment_id":"` + enrollment + `","attempt":3}`)
		err := c.(coreapi.DeadLetterClient).RecordDeadLetter(ctx, coreapi.DeadLetterInput{
			WorkspaceID: fx.ws.String(), TaskType: "sequence:advance", Payload: payload,
			LastError: "provider refused", AttemptCount: 25, Queue: "send",
		})
		if err != nil {
			t.Fatalf("RecordDeadLetter: %v", err)
		}
		rows, err := q.ListTaskDeadLetters(ctx, gen.ListTaskDeadLettersParams{
			WorkspaceID: fx.ws, Status: "", PageLimit: 50,
		})
		if err != nil {
			t.Fatalf("ListTaskDeadLetters: %v", err)
		}
		var found bool
		for _, r := range rows {
			// Compared as JSON, not as bytes, and that is a fact about the
			// COLUMN rather than a weakening of the assertion. task_dead_letters
			// .payload is jsonb, so Postgres reparses and re-renders it — key
			// order is normalised and a space appears after each colon — on the
			// in-process path exactly as much as on this one. What the transport
			// has to preserve is the VALUE, and comparing bytes here would
			// assert a property the storage never had.
			var got, want map[string]any
			if err := json.Unmarshal(r.Payload, &got); err != nil {
				continue
			}
			if err := json.Unmarshal(payload, &want); err != nil {
				t.Fatalf("the test's own payload is not JSON: %v", err)
			}
			if reflect.DeepEqual(got, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("the dead letter did not reach Postgres with its payload intact (%d rows)", len(rows))
		}
	})

	t.Run("a reply label resolves", func(t *testing.T) {
		if _, err := q.CreateReplyLabel(ctx, gen.CreateReplyLabelParams{
			WorkspaceID: fx.ws, Key: "interested", Label: "Interested", Color: "#22c55e",
			StopsEnrollment: true, CapturesDeal: true,
		}); err != nil {
			t.Fatalf("CreateReplyLabel: %v", err)
		}
		label, ok, err := c.(coreapi.ReplyLabelClient).ResolveReplyLabel(ctx, fx.ws.String(), "interested")
		if err != nil {
			t.Fatalf("ResolveReplyLabel: %v", err)
		}
		if !ok || !label.StopsEnrollment || !label.CapturesDeal {
			t.Errorf("label = %+v ok = %v, want the stored flags", label, ok)
		}

		// ok=false is the ORDINARY answer for a key no label claims, and it must
		// be a nil error: a 404 here would fail the poll and the poller would
		// never reach SetInboxCursor.
		_, ok, err = c.(coreapi.ReplyLabelClient).ResolveReplyLabel(ctx, fx.ws.String(), "no-such-key")
		if err != nil {
			t.Fatalf("ResolveReplyLabel for an unknown key = %v, want nil error", err)
		}
		if ok {
			t.Error("an unknown reply label key resolved")
		}

		// And workspace-pinned: the same key in another tenant resolves to
		// nothing, because the label belongs to the workspace that created it.
		_, ok, err = c.(coreapi.ReplyLabelClient).ResolveReplyLabel(ctx, fx.foreignWS.String(), "interested")
		if err != nil {
			t.Fatalf("ResolveReplyLabel in the foreign workspace: %v", err)
		}
		if ok {
			t.Error("a label resolved in a workspace that never created it")
		}
	})

	t.Run("the two no-match lookups answer without a fixture", func(t *testing.T) {
		// Both are the ORDINARY answer for nearly every inbound message, and
		// both must be a nil error with a false — the poller branches on the
		// bool and would otherwise fail the whole poll on ordinary mail.
		ref, ok, err := c.(coreapi.WarmupSendLookupClient).FindWarmupSendByMessageID(
			ctx, fx.ws.String(), fx.mailboxID.String(), "<not-a-warmup-"+uuid.NewString()+"@x.test>")
		if err != nil {
			t.Fatalf("FindWarmupSendByMessageID: %v", err)
		}
		if ok || ref.WarmupSendID != "" {
			t.Errorf("ref = %+v ok = %v, want no match", ref, ok)
		}

		matched, err := c.(coreapi.WarmupEvidenceClient).RecordWarmupHardBounce(
			ctx, fx.ws.String(), "<not-a-warmup-"+uuid.NewString()+"@x.test>", fx.mailboxID.String())
		if err != nil {
			t.Fatalf("RecordWarmupHardBounce: %v", err)
		}
		if matched {
			t.Error("a DSN for no warmup send reported a match; a real campaign bounce would be swallowed")
		}
	})

	t.Run("a mailbox with no warmup participant is not due", func(t *testing.T) {
		due, sendNow, err := c.NextWarmupDue(ctx, fx.mailboxID.String(), fx.ws.String())
		if err != nil {
			t.Fatalf("NextWarmupDue: %v", err)
		}
		if sendNow || !due.IsZero() {
			t.Errorf("due = %v sendNow = %v, want the zero time and false for a mailbox that is not warming", due, sendNow)
		}
	})
}

// The tenant pin, across the wire, on the route whose write is easiest to get
// wrong: a cursor.
//
// A foreign workspace id names a real mailbox that belongs to someone else. The
// in-process path pins workspace_id in the UPDATE, so it matches zero rows —
// and the wire must not weaken that into "the handler trusted the envelope".
// The call SUCCEEDS (zero rows updated is not an error, in process or here) and
// the owning workspace's cursor is untouched, which is the assertion that
// matters.
func TestAForeignWorkspaceMovesNoInboxCursorOverTheWire(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)
	cp := New(pool, itKeyring(t, q), nil, "", mail.GoogleOAuth{}, mail.MicrosoftOAuth{}, nil, nil)
	srv := fleetListener(t, q, itKeyring(t, q), cp)
	c := remoteInboundCore(t, srv.URL)

	if err := c.SetInboxCursor(ctx, fx.mailboxID.String(), fx.ws.String(), 1000, 5); err != nil {
		t.Fatalf("SetInboxCursor (owner): %v", err)
	}
	// The foreign tenant names the same mailbox and a much higher UID. If the
	// pin were dropped, the owner would resume from 9999 and silently skip every
	// message in between — replies and bounces included.
	if err := c.SetInboxCursor(ctx, fx.mailboxID.String(), fx.foreignWS.String(), 9999, 5); err != nil {
		t.Fatalf("SetInboxCursor (foreign) = %v, want the same no-op the in-process path performs", err)
	}
	mb, err := q.GetMailbox(ctx, gen.GetMailboxParams{ID: fx.mailboxID, WorkspaceID: fx.ws})
	if err != nil {
		t.Fatalf("GetMailbox: %v", err)
	}
	if mb.InboxLastSeenUid != 1000 {
		t.Errorf("a foreign workspace moved the cursor to %d; the owner would skip every message up to it", mb.InboxLastSeenUid)
	}
}

// Fail closed, for the slice-4 set: a worker that cannot reach the control
// plane gets an error from every one of them, never a zero value that looks
// like an answer.
//
// It proves the calls WORK first and then kills the listener, so a failure is
// attributable to the control plane being gone rather than to a malformed
// request — the same shape as
// TestEveryJobReadFailsClosedWhenTheControlPlaneDies.
func TestTheSlice4RoutesFailClosedWhenTheControlPlaneDies(t *testing.T) {
	ctx := context.Background()
	pool, q := claimConnect(t)
	fx := seedForClaim(t, ctx, q)
	cp := New(pool, itKeyring(t, q), nil, "", mail.GoogleOAuth{}, mail.MicrosoftOAuth{}, nil, nil)
	srv := fleetListener(t, q, itKeyring(t, q), cp)
	c := remoteInboundCore(t, srv.URL)

	if err := c.SetInboxCursor(ctx, fx.mailboxID.String(), fx.ws.String(), 1, 1); err != nil {
		t.Fatalf("SetInboxCursor while the control plane is up: %v", err)
	}
	srv.Close()

	if err := c.SetInboxCursor(ctx, fx.mailboxID.String(), fx.ws.String(), 2, 1); err == nil {
		t.Error("SetInboxCursor succeeded against a dead control plane")
	}
	if err := c.SetInboxCursorString(ctx, fx.mailboxID.String(), fx.ws.String(), "x"); err == nil {
		t.Error("SetInboxCursorString succeeded against a dead control plane")
	}
	if err := c.UpsertWorkerHeartbeat(ctx, "gone", "", "hostname"); err == nil {
		t.Error("UpsertWorkerHeartbeat succeeded against a dead control plane")
	}
	// The one whose fail-closed direction is a routing decision: an empty queue
	// alongside a nil error would silently un-pin a mailbox from the IP it has
	// been building provider trust on.
	queue, err := c.AssignMailboxWorker(ctx, fx.mailboxID.String(), fx.ws.String())
	if err == nil {
		t.Error("AssignMailboxWorker succeeded against a dead control plane")
	}
	if queue != "" {
		t.Errorf("queue = %q on a failed call, want empty", queue)
	}
	// And it does NOT fall back to reading the pool it does not have: the error
	// is a transport error, not a database one.
	if errors.Is(err, coreapi.ErrNoEligibleWorker) {
		t.Error("an unreachable control plane was reported as ErrNoEligibleWorker")
	}
}
