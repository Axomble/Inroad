package remote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/coreapi"
)

// Slice 3b: the manual reply/compose protocol. There is no database anywhere in
// this file, for the reason remote_test.go states — the thing under test is a
// client that HAS no database access. The end-to-end proof (real Postgres, the
// real handler, a nil-pool worker, and a response dropped after a real commit)
// lives in internal/coreapi/inprocess/remoteinboxsends_integration_test.go.

// fakeInboxSends is the CONTROL plane's side. It records what it was asked and
// answers what the test told it to.
//
// Like fakeOutcomes it carries a tiny claim state machine rather than a fixed
// answer, because the property this slice is about is a SEQUENCE: claim, lose the
// response, retry, and be told the row is no longer claimable.
type fakeInboxSends struct {
	calls []string
	// gotWS/gotID record the last ids the control plane was pinned with.
	gotWS, gotID string
	gotReason    string
	gotMessageID string
	gotRecord    coreapi.RecordInboxReplyInput

	job      coreapi.InboxReplyJob
	pending  coreapi.PendingInboxReply
	compose  coreapi.PendingInboxCompose
	claimed  bool
	claims   int
	claimErr error
	err      error
}

func (f *fakeInboxSends) record(name string) { f.calls = append(f.calls, name) }

func (f *fakeInboxSends) GetInboxReplyJob(_ context.Context, threadID, workspaceID string) (coreapi.InboxReplyJob, error) {
	f.record("GetInboxReplyJob")
	f.gotWS, f.gotID = workspaceID, threadID
	if f.err != nil {
		return coreapi.InboxReplyJob{}, f.err
	}
	return f.job, nil
}

func (f *fakeInboxSends) RecordInboxReply(_ context.Context, in coreapi.RecordInboxReplyInput) error {
	f.record("RecordInboxReply")
	f.gotRecord = in
	return f.err
}

func (f *fakeInboxSends) ClaimInboxReply(_ context.Context, workspaceID, taskID string) (bool, error) {
	f.record("ClaimInboxReply")
	f.gotWS, f.gotID = workspaceID, taskID
	if f.err != nil {
		return false, f.err
	}
	return f.claimed, nil
}

func (f *fakeInboxSends) ReleaseInboxReply(_ context.Context, workspaceID, taskID string) error {
	f.record("ReleaseInboxReply")
	f.gotWS, f.gotID = workspaceID, taskID
	return f.err
}

func (f *fakeInboxSends) ClaimPendingInboxReply(_ context.Context, workspaceID, pendingID string) (coreapi.PendingInboxReply, error) {
	f.record("ClaimPendingInboxReply")
	f.gotWS, f.gotID = workspaceID, pendingID
	f.claims++
	if f.claimErr != nil {
		return coreapi.PendingInboxReply{}, f.claimErr
	}
	if f.err != nil {
		return coreapi.PendingInboxReply{}, f.err
	}
	return f.pending, nil
}

func (f *fakeInboxSends) MarkPendingInboxReplySent(_ context.Context, workspaceID, pendingID, messageID string) error {
	f.record("MarkPendingInboxReplySent")
	f.gotWS, f.gotID, f.gotMessageID = workspaceID, pendingID, messageID
	return f.err
}

func (f *fakeInboxSends) ReleasePendingInboxReply(_ context.Context, workspaceID, pendingID, reason string) error {
	f.record("ReleasePendingInboxReply")
	f.gotWS, f.gotID, f.gotReason = workspaceID, pendingID, reason
	return f.err
}

func (f *fakeInboxSends) FailPendingInboxReply(_ context.Context, workspaceID, pendingID, reason string) error {
	f.record("FailPendingInboxReply")
	f.gotWS, f.gotID, f.gotReason = workspaceID, pendingID, reason
	return f.err
}

func (f *fakeInboxSends) ClaimPendingInboxCompose(_ context.Context, workspaceID, pendingID string) (coreapi.PendingInboxCompose, error) {
	f.record("ClaimPendingInboxCompose")
	f.gotWS, f.gotID = workspaceID, pendingID
	f.claims++
	if f.claimErr != nil {
		return coreapi.PendingInboxCompose{}, f.claimErr
	}
	if f.err != nil {
		return coreapi.PendingInboxCompose{}, f.err
	}
	return f.compose, nil
}

func (f *fakeInboxSends) MarkPendingInboxComposeSent(_ context.Context, workspaceID, pendingID, messageID string) error {
	f.record("MarkPendingInboxComposeSent")
	f.gotWS, f.gotID, f.gotMessageID = workspaceID, pendingID, messageID
	return f.err
}

func (f *fakeInboxSends) ReleasePendingInboxCompose(_ context.Context, workspaceID, pendingID, reason string) error {
	f.record("ReleasePendingInboxCompose")
	f.gotWS, f.gotID, f.gotReason = workspaceID, pendingID, reason
	return f.err
}

func (f *fakeInboxSends) FailPendingInboxCompose(_ context.Context, workspaceID, pendingID, reason string) error {
	f.record("FailPendingInboxCompose")
	f.gotWS, f.gotID, f.gotReason = workspaceID, pendingID, reason
	return f.err
}

// serveInboxSends stands the real handler up over a fakeInboxSends and returns a
// client pointed at it.
func serveInboxSends(t *testing.T, f *fakeInboxSends) (*Client, *httptest.Server) {
	t.Helper()
	h, err := NewHandler(Deps{
		Suppression: &fakeSuppression{}, Jobs: &fakeJobs{}, Outcomes: &fakeOutcomes{}, InboxSends: f,
		Inbound: &fakeInbound{}, Fleet: &fakeFleet{},
	}, testToken, quietLogger())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := newTestClient(t, srv.URL, testToken)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c, srv
}

// THE headline: the reply a human wrote crosses intact, in both directions,
// through the real handler, from a client with no database.
//
// The BODY is the assertion that matters. A worker that received an empty one
// would dial the provider and deliver a blank message to a customer — worse than
// not sending at all, and not something a zero value would make obvious.
func TestThePendingReplyClaimCarriesTheBodyAndTheThreadingHeaders(t *testing.T) {
	f := &fakeInboxSends{pending: coreapi.PendingInboxReply{
		ThreadID: uuid.NewString(),
		BodyText: "Happy to help — here are our numbers.\n\n— Ada",
		Job: coreapi.InboxReplyJob{
			MailboxID:  uuid.NewString(),
			Subject:    "Question about pricing",
			ToEmail:    "lead@x.test",
			InReplyTo:  "<inbound@sender.test>",
			References: "<root@sender.test> <inbound@sender.test>",
		},
	}}
	c, _ := serveInboxSends(t, f)

	ws, pendingID := uuid.NewString(), uuid.NewString()
	got, err := c.ClaimPendingInboxReply(context.Background(), ws, pendingID)
	if err != nil {
		t.Fatalf("ClaimPendingInboxReply: %v", err)
	}
	if diff := got; diff != f.pending {
		t.Errorf("the claimed reply changed across the wire:\ngot  %+v\nwant %+v", got, f.pending)
	}
	if f.gotWS != ws || f.gotID != pendingID {
		t.Errorf("control plane pinned %s/%s, want %s/%s", f.gotWS, f.gotID, ws, pendingID)
	}
	if f.claims != 1 {
		t.Errorf("the control plane was asked to claim %d times, want 1", f.claims)
	}
}

// Every one of the twelve crosses, each asserted on a value the transport could
// not have invented.
func TestEveryManualSendCallCrossesTheWire(t *testing.T) {
	ws, id := uuid.NewString(), uuid.NewString()

	t.Run("GetInboxReplyJob", func(t *testing.T) {
		f := &fakeInboxSends{job: coreapi.InboxReplyJob{
			MailboxID: uuid.NewString(), Subject: "Re-entry", ToEmail: "lead@x.test",
			InReplyTo: "<a@b>", References: "<a@b>",
		}}
		c, _ := serveInboxSends(t, f)
		got, err := c.GetInboxReplyJob(context.Background(), id, ws)
		if err != nil {
			t.Fatalf("over the wire: %v", err)
		}
		if got != f.job {
			t.Errorf("job = %+v, want %+v", got, f.job)
		}
	})

	t.Run("RecordInboxReply", func(t *testing.T) {
		f := &fakeInboxSends{}
		c, _ := serveInboxSends(t, f)
		in := coreapi.RecordInboxReplyInput{
			WorkspaceID: ws, ThreadID: id, MessageID: "<sent@acme.test>",
			FromEmail: "me@acme.test", FromName: "Ada", ToEmail: "lead@x.test",
			Subject: "Re: pricing", BodyText: "here are our numbers",
		}
		if err := c.RecordInboxReply(context.Background(), in); err != nil {
			t.Fatalf("over the wire: %v", err)
		}
		if f.gotRecord != in {
			t.Errorf("the control plane saw %+v, want %+v", f.gotRecord, in)
		}
	})

	t.Run("ClaimInboxReply", func(t *testing.T) {
		for _, claimed := range []bool{true, false} {
			f := &fakeInboxSends{claimed: claimed}
			c, _ := serveInboxSends(t, f)
			// The drain claim's key is an asynq task id, not a uuid, and it must
			// travel unvalidated: rejecting it here would strand every legacy task.
			got, err := c.ClaimInboxReply(context.Background(), ws, "inboxreply:abc:1700000000")
			if err != nil {
				t.Fatalf("over the wire: %v", err)
			}
			if got != claimed {
				t.Errorf("claimed = %v, want %v", got, claimed)
			}
			if f.gotID != "inboxreply:abc:1700000000" {
				t.Errorf("task id = %q, want the caller's own", f.gotID)
			}
		}
	})

	t.Run("ReleaseInboxReply", func(t *testing.T) {
		f := &fakeInboxSends{}
		c, _ := serveInboxSends(t, f)
		if err := c.ReleaseInboxReply(context.Background(), ws, "inboxreply:abc:1700000000"); err != nil {
			t.Fatalf("over the wire: %v", err)
		}
		if f.gotID != "inboxreply:abc:1700000000" {
			t.Errorf("task id = %q, want the caller's own", f.gotID)
		}
	})

	t.Run("MarkPendingInboxReplySent", func(t *testing.T) {
		f := &fakeInboxSends{}
		c, _ := serveInboxSends(t, f)
		if err := c.MarkPendingInboxReplySent(context.Background(), ws, id, "<out@acme.test>"); err != nil {
			t.Fatalf("over the wire: %v", err)
		}
		if f.gotMessageID != "<out@acme.test>" {
			t.Errorf("message id = %q, want the provider's own", f.gotMessageID)
		}
	})

	t.Run("ReleasePendingInboxReply", func(t *testing.T) {
		f := &fakeInboxSends{}
		c, _ := serveInboxSends(t, f)
		if err := c.ReleasePendingInboxReply(context.Background(), ws, id, "the mail provider rejected the message"); err != nil {
			t.Fatalf("over the wire: %v", err)
		}
		if f.gotReason != "the mail provider rejected the message" {
			t.Errorf("reason = %q, want the stable token verbatim", f.gotReason)
		}
	})

	t.Run("FailPendingInboxReply", func(t *testing.T) {
		f := &fakeInboxSends{}
		c, _ := serveInboxSends(t, f)
		if err := c.FailPendingInboxReply(context.Background(), ws, id, "the recipient has unsubscribed or bounced"); err != nil {
			t.Fatalf("over the wire: %v", err)
		}
		if f.gotReason != "the recipient has unsubscribed or bounced" {
			t.Errorf("reason = %q, want the stable token verbatim", f.gotReason)
		}
	})

	t.Run("ClaimPendingInboxCompose", func(t *testing.T) {
		f := &fakeInboxSends{compose: coreapi.PendingInboxCompose{
			MailboxID: uuid.NewString(),
			ToEmails:  []string{"a@x.test", "b@x.test"},
			CcEmails:  []string{"c@x.test"},
			BccEmails: []string{"d@x.test"},
			Subject:   "Intro",
			BodyText:  "Hello from Acme",
		}}
		c, _ := serveInboxSends(t, f)
		got, err := c.ClaimPendingInboxCompose(context.Background(), ws, id)
		if err != nil {
			t.Fatalf("over the wire: %v", err)
		}
		if !reflect.DeepEqual(got, f.compose) {
			t.Errorf("compose = %+v, want %+v", got, f.compose)
		}
	})

	t.Run("MarkPendingInboxComposeSent", func(t *testing.T) {
		f := &fakeInboxSends{}
		c, _ := serveInboxSends(t, f)
		if err := c.MarkPendingInboxComposeSent(context.Background(), ws, id, "<out@acme.test>"); err != nil {
			t.Fatalf("over the wire: %v", err)
		}
		if f.gotMessageID != "<out@acme.test>" {
			t.Errorf("message id = %q, want the provider's own", f.gotMessageID)
		}
	})

	t.Run("ReleasePendingInboxCompose", func(t *testing.T) {
		f := &fakeInboxSends{}
		c, _ := serveInboxSends(t, f)
		if err := c.ReleasePendingInboxCompose(context.Background(), ws, id, "reason"); err != nil {
			t.Fatalf("over the wire: %v", err)
		}
		if f.gotReason != "reason" {
			t.Errorf("reason = %q, want it verbatim", f.gotReason)
		}
	})

	t.Run("FailPendingInboxCompose", func(t *testing.T) {
		f := &fakeInboxSends{}
		c, _ := serveInboxSends(t, f)
		if err := c.FailPendingInboxCompose(context.Background(), ws, id, "reason"); err != nil {
			t.Fatalf("over the wire: %v", err)
		}
		if f.gotReason != "reason" {
			t.Errorf("reason = %q, want it verbatim", f.gotReason)
		}
	})
}

// THE sentinel that decides what a worker does with an undone reply.
//
// coreapi.ErrInboxPendingNotClaimable means "stop, and do not retry" — the
// operator cancelled, it is already sent, it is not yet due, or another worker
// holds the lease. A worker that received a generic error instead would retry an
// undone reply until asynq gave up and then write it into task_dead_letters.
func TestAnUnclaimableRowCrossesAsItsOwnSentinel(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*Client) error
	}{
		{"reply", func(c *Client) error {
			_, err := c.ClaimPendingInboxReply(context.Background(), uuid.NewString(), uuid.NewString())
			return err
		}},
		{"compose", func(c *Client) error {
			_, err := c.ClaimPendingInboxCompose(context.Background(), uuid.NewString(), uuid.NewString())
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeInboxSends{claimErr: coreapi.ErrInboxPendingNotClaimable}
			c, _ := serveInboxSends(t, f)
			if err := tc.call(c); !errors.Is(err, coreapi.ErrInboxPendingNotClaimable) {
				t.Fatalf("err = %v, want coreapi.ErrInboxPendingNotClaimable", err)
			}
		})
	}
}

// The other branched-on sentinel: a thread with nothing to reply to. Both reply
// handlers treat it as PERMANENT (log, fail the row, never retry), so flattening
// it into a generic error would retry a reply that can never be built.
func TestAThreadWithNoInboundMessageCrossesAsItsOwnSentinel(t *testing.T) {
	t.Run("GetInboxReplyJob", func(t *testing.T) {
		f := &fakeInboxSends{err: coreapi.ErrInboxNoInbound}
		c, _ := serveInboxSends(t, f)
		if _, err := c.GetInboxReplyJob(context.Background(), uuid.NewString(), uuid.NewString()); !errors.Is(err, coreapi.ErrInboxNoInbound) {
			t.Fatalf("err = %v, want coreapi.ErrInboxNoInbound", err)
		}
	})
	t.Run("ClaimPendingInboxReply", func(t *testing.T) {
		// The claim resolves the thread too, so it can answer with this as well —
		// and when it does, the row was already moved to 'sending'.
		f := &fakeInboxSends{claimErr: coreapi.ErrInboxNoInbound}
		c, _ := serveInboxSends(t, f)
		if _, err := c.ClaimPendingInboxReply(context.Background(), uuid.NewString(), uuid.NewString()); !errors.Is(err, coreapi.ErrInboxNoInbound) {
			t.Fatalf("err = %v, want coreapi.ErrInboxNoInbound", err)
		}
	})
}

// A 409 carrying NO recognised code is NOT read as either sentinel. The direction
// matters more than it looks: reading an unknown conflict as "not claimable"
// would make a worker treat an intermediary's response as the operator's undo and
// silently drop a reply the operator is watching for.
func TestAnUnrecognisedConflictIsNotMistakenForAnUndo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, "409 conflict")
	}))
	defer srv.Close()

	c, err := newTestClient(t, srv.URL, testToken)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = c.ClaimPendingInboxReply(context.Background(), uuid.NewString(), uuid.NewString())
	if err == nil {
		t.Fatal("an unexplained 409 was accepted as an answer")
	}
	if errors.Is(err, coreapi.ErrInboxPendingNotClaimable) || errors.Is(err, coreapi.ErrInboxNoInbound) {
		t.Errorf("an unexplained 409 was mapped to a sentinel: %v", err)
	}
}

// The wire contract on the BYTES: a claim names one row in one workspace and
// carries nothing else. A request that could express "rows matching X" would make
// this seam a query engine over a tenant's correspondence, which is precisely the
// capability the plane split exists to take away.
func TestAClaimRequestCarriesIdsOnly(t *testing.T) {
	ws, id := uuid.NewString(), uuid.NewString()
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pendingInboxReplyResponse{})
	}))
	defer srv.Close()

	c, err := newTestClient(t, srv.URL, testToken)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := c.ClaimPendingInboxReply(context.Background(), ws, id); err != nil {
		t.Fatalf("ClaimPendingInboxReply: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("unmarshal %q: %v", body, err)
	}
	if len(fields) != 2 || fields["workspace_id"] != ws || fields["pending_id"] != id {
		t.Errorf("request body = %s, want exactly workspace_id + pending_id", body)
	}
}

// FAIL CLOSED. The control plane is gone and every call must return an error AND
// a zero value — never a claim nobody took, and never an empty reply job a worker
// would dial with.
func TestEveryManualSendCallFailsClosedWhenTheControlPlaneDies(t *testing.T) {
	f := &fakeInboxSends{}
	c, srv := serveInboxSends(t, f)
	ctx := context.Background()
	ws, id := uuid.NewString(), uuid.NewString()

	calls := []struct {
		name string
		call func() (any, error)
	}{
		{"GetInboxReplyJob", func() (any, error) { return c.GetInboxReplyJob(ctx, id, ws) }},
		{"RecordInboxReply", func() (any, error) {
			return nil, c.RecordInboxReply(ctx, coreapi.RecordInboxReplyInput{WorkspaceID: ws, ThreadID: id})
		}},
		{"ClaimInboxReply", func() (any, error) { return c.ClaimInboxReply(ctx, ws, "task") }},
		{"ReleaseInboxReply", func() (any, error) { return nil, c.ReleaseInboxReply(ctx, ws, "task") }},
		{"ClaimPendingInboxReply", func() (any, error) { return c.ClaimPendingInboxReply(ctx, ws, id) }},
		{"MarkPendingInboxReplySent", func() (any, error) { return nil, c.MarkPendingInboxReplySent(ctx, ws, id, "<m@x>") }},
		{"ReleasePendingInboxReply", func() (any, error) { return nil, c.ReleasePendingInboxReply(ctx, ws, id, "why") }},
		{"FailPendingInboxReply", func() (any, error) { return nil, c.FailPendingInboxReply(ctx, ws, id, "why") }},
		{"ClaimPendingInboxCompose", func() (any, error) { return c.ClaimPendingInboxCompose(ctx, ws, id) }},
		{"MarkPendingInboxComposeSent", func() (any, error) { return nil, c.MarkPendingInboxComposeSent(ctx, ws, id, "<m@x>") }},
		{"ReleasePendingInboxCompose", func() (any, error) { return nil, c.ReleasePendingInboxCompose(ctx, ws, id, "why") }},
		{"FailPendingInboxCompose", func() (any, error) { return nil, c.FailPendingInboxCompose(ctx, ws, id, "why") }},
	}
	// Every one works while the control plane is UP. Without this half the loop
	// below would pass against a client that never worked at all.
	for _, tc := range calls {
		if _, err := tc.call(); err != nil {
			t.Fatalf("%s failed while the control plane was UP: %v", tc.name, err)
		}
	}
	if len(f.calls) != len(calls) {
		t.Fatalf("the control plane saw %d calls, want %d — %v", len(f.calls), len(calls), f.calls)
	}

	srv.Close()

	for _, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.call()
			if err == nil {
				t.Fatalf("%s answered with the control plane down", tc.name)
			}
			if got != nil && !reflect.ValueOf(got).IsZero() {
				t.Errorf("%s returned %+v alongside its error, want the zero value", tc.name, got)
			}
		})
	}
}

// A malformed id never reaches the control plane: rejected at the boundary on
// both sides, because a handler may not trust its client.
func TestAMalformedPendingIDNeverReachesTheControlPlane(t *testing.T) {
	f := &fakeInboxSends{}
	c, srv := serveInboxSends(t, f)

	if _, err := c.ClaimPendingInboxReply(context.Background(), uuid.NewString(), "not-a-uuid"); err == nil {
		t.Fatal("the client accepted a malformed pending id")
	}
	if len(f.calls) != 0 {
		t.Fatalf("the control plane was reached %v for a malformed id", f.calls)
	}

	// Handler side, bypassing the client's own check.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		srv.URL+PathInboxPendingReplyClaim,
		strings.NewReader(`{"workspace_id":"`+uuid.NewString()+`","pending_id":"nope"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if len(f.calls) != 0 {
		t.Errorf("the control plane was reached %v despite a malformed id", f.calls)
	}
}

// An unknown field is refused rather than ignored, on every route of this
// protocol: it is what stops a caller quietly adding a parameter the endpoint
// does not honour and believing the answer it gets back.
func TestAnUnknownFieldIsRefusedOnTheManualSendRoutes(t *testing.T) {
	f := &fakeInboxSends{}
	_, srv := serveInboxSends(t, f)

	for _, path := range []string{
		PathInboxReplyJob, PathInboxReplyRecord, PathInboxReplyClaim, PathInboxReplyRelease,
		PathInboxPendingReplyClaim, PathInboxPendingReplySent, PathInboxPendingReplyRelease,
		PathInboxPendingReplyFail, PathInboxPendingComposeClaim, PathInboxPendingComposeSent,
		PathInboxPendingComposeRelease, PathInboxPendingComposeFail,
	} {
		t.Run(path, func(t *testing.T) {
			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+path,
				strings.NewReader(`{"workspace_id":"`+uuid.NewString()+`","limit":100}`))
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			req.Header.Set("Authorization", "Bearer "+testToken)
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", resp.StatusCode)
			}
		})
	}
	if len(f.calls) != 0 {
		t.Errorf("the control plane was reached %v for requests with unknown fields", f.calls)
	}
}

// The record route carries the workspace twice — on the envelope and inside the
// reply — and a disagreement is a 400 before anything runs. The two can only
// differ if the caller assembled them from different places, which is not a state
// a correct worker reaches.
func TestARecordWhoseEnvelopeAndReplyDisagreeIsRefused(t *testing.T) {
	f := &fakeInboxSends{}
	c, _ := serveInboxSends(t, f)

	err := c.RecordInboxReply(context.Background(), coreapi.RecordInboxReplyInput{
		WorkspaceID: uuid.NewString(), ThreadID: uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("a well-formed record failed: %v", err)
	}
	// The client builds the envelope FROM the input, so the only way to make them
	// disagree is on the wire.
	_, srv := serveInboxSends(t, f)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+PathInboxReplyRecord,
		strings.NewReader(`{"workspace_id":"`+uuid.NewString()+`","reply":{"workspace_id":"`+uuid.NewString()+
			`","thread_id":"`+uuid.NewString()+`","message_id":"","from_email":"","from_name":"","to_email":"","subject":"","body_text":""}}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an envelope that disagrees with the reply", resp.StatusCode)
	}
}

// The control plane's own error text is never relayed. A seam that echoed a pg
// message would be a probe oracle, and on this protocol the text could quote the
// row it failed on.
func TestAControlPlaneFailureDoesNotRelayItsText(t *testing.T) {
	f := &fakeInboxSends{err: errors.New("pq: duplicate key value violates unique constraint")}
	c, _ := serveInboxSends(t, f)

	err := c.MarkPendingInboxReplySent(context.Background(), uuid.NewString(), uuid.NewString(), "<m@x>")
	if err == nil {
		t.Fatal("a control-plane failure was reported as success")
	}
	if strings.Contains(err.Error(), "duplicate key") {
		t.Errorf("the control plane's error text reached the worker: %v", err)
	}
}
