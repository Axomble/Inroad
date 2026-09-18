package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/coreapi"
)

// There is no database anywhere in this file, for the reason remote_test.go
// states: the thing under test is a client that HAS no database access. The
// end-to-end proof — real Postgres, the real handler, a nil-pool worker, and a
// dropped response after a real commit — lives in
// internal/coreapi/inprocess/remoteoutcomes_integration_test.go.

// fakeOutcomes is the CONTROL plane's write side. It records what it was asked
// and answers what the test told it to.
//
// It carries a tiny claim state machine (claimed/sent) rather than a fixed
// answer, because the property this slice is about is a SEQUENCE: claim, lose
// the response, retry, and be told something different the second time. A fake
// that always answered the same thing could not express that.
type fakeOutcomes struct {
	calls []string
	// claimAnswers is consumed in order; the last value repeats once exhausted.
	// It is how a test says "the first claim wins, the second is told the row is
	// already sent" without this fake needing to model any SQL.
	claimAnswers []coreapi.ClaimOutcome
	claimIdx     int
	// rawOutcome, when non-empty, is written to the wire INSTEAD of the encoded
	// claim answer — the only way to exercise a control plane speaking a
	// vocabulary this worker does not know.
	advance coreapi.Advance
	defers  int
	err     error
}

func (f *fakeOutcomes) record(name string) { f.calls = append(f.calls, name) }

func (f *fakeOutcomes) nextClaim() coreapi.ClaimOutcome {
	if len(f.claimAnswers) == 0 {
		return coreapi.ClaimWon
	}
	i := min(f.claimIdx, len(f.claimAnswers)-1)
	f.claimIdx++
	return f.claimAnswers[i]
}

func (f *fakeOutcomes) ClaimStepSend(context.Context, coreapi.StepSendJob) (coreapi.ClaimOutcome, error) {
	f.record("ClaimStepSend")
	if f.err != nil {
		return coreapi.ClaimSkip, f.err
	}
	return f.nextClaim(), nil
}

func (f *fakeOutcomes) MarkStepDelivered(context.Context, coreapi.StepSendJob, string) error {
	f.record("MarkStepDelivered")
	return f.err
}

func (f *fakeOutcomes) AdvanceStepCursor(context.Context, coreapi.StepSendJob) (coreapi.Advance, error) {
	f.record("AdvanceStepCursor")
	return f.advance, f.err
}

func (f *fakeOutcomes) ReleaseStepSend(context.Context, coreapi.StepSendJob) error {
	f.record("ReleaseStepSend")
	return f.err
}

func (f *fakeOutcomes) FinalizeStepSend(context.Context, coreapi.StepSendJob, coreapi.StepResult) (coreapi.Advance, error) {
	f.record("FinalizeStepSend")
	return f.advance, f.err
}

func (f *fakeOutcomes) MarkStepStopped(context.Context, string, string, string) error {
	f.record("MarkStepStopped")
	return f.err
}

func (f *fakeOutcomes) DeferEnrollment(context.Context, string, string, time.Time) error {
	f.record("DeferEnrollment")
	return f.err
}

func (f *fakeOutcomes) IncrementEnrollmentCapDeferrals(context.Context, string, string) (int, error) {
	f.record("IncrementEnrollmentCapDeferrals")
	return f.defers, f.err
}

func (f *fakeOutcomes) ClaimWarmupSend(context.Context, coreapi.WarmupSendJob) (coreapi.ClaimOutcome, error) {
	f.record("ClaimWarmupSend")
	if f.err != nil {
		return coreapi.ClaimSkip, f.err
	}
	return f.nextClaim(), nil
}

func (f *fakeOutcomes) MarkWarmupSent(context.Context, coreapi.WarmupSendJob, string) error {
	f.record("MarkWarmupSent")
	return f.err
}

func (f *fakeOutcomes) ReleaseWarmupSend(context.Context, coreapi.WarmupSendJob) error {
	f.record("ReleaseWarmupSend")
	return f.err
}

func (f *fakeOutcomes) FailWarmupSend(context.Context, coreapi.WarmupSendJob, string) error {
	f.record("FailWarmupSend")
	return f.err
}

func (f *fakeOutcomes) MarkWarmupEngaged(context.Context, string, string, bool) error {
	f.record("MarkWarmupEngaged")
	return f.err
}

func (f *fakeOutcomes) MarkReplied(context.Context, string, string, string, string, float64) error {
	f.record("MarkReplied")
	return f.err
}

func (f *fakeOutcomes) RecordReplyClass(context.Context, string, string, string, string, float64) error {
	f.record("RecordReplyClass")
	return f.err
}

func (f *fakeOutcomes) MarkUnsubscribed(context.Context, string, string, string) error {
	f.record("MarkUnsubscribed")
	return f.err
}

func (f *fakeOutcomes) MarkBounced(context.Context, string, string, string, bool) error {
	f.record("MarkBounced")
	return f.err
}

func (f *fakeOutcomes) MarkWebhookDelivered(context.Context, string, string, int, int) error {
	f.record("MarkWebhookDelivered")
	return f.err
}

func (f *fakeOutcomes) MarkWebhookRetrying(context.Context, string, string, int, string, *int, time.Time) error {
	f.record("MarkWebhookRetrying")
	return f.err
}

func (f *fakeOutcomes) MarkWebhookFailed(context.Context, string, string, int, string, *int) error {
	f.record("MarkWebhookFailed")
	return f.err
}

// serveOutcomes stands the real handler up over a fake writer.
func serveOutcomes(t *testing.T, out *fakeOutcomes) (*Client, *httptest.Server) {
	t.Helper()
	h, err := NewHandler(Deps{
		Suppression: &fakeSuppression{}, Jobs: &fakeJobs{}, Outcomes: out, InboxSends: &fakeInboxSends{},
	}, testToken, quietLogger())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL, testToken, true, smtpSecret(""))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c, srv
}

// stepJob is a claimable step send: enough ids for every route, plus the
// DECRYPTED credential a worker really holds when it calls these methods.
func stepJob(ws uuid.UUID) coreapi.StepSendJob {
	return coreapi.StepSendJob{
		EnrollmentID: uuid.NewString(), WorkspaceID: ws.String(),
		CampaignID: uuid.NewString(), ContactID: uuid.NewString(),
		MailboxID: uuid.NewString(), SendID: uuid.NewString(),
		CurrentStep: 0, StepOrder: 1, ToEmail: "ada@example.test",
		Provider: "smtp", SMTPHost: "smtp.acme.test", SMTPPort: 587,
		AccessToken: []byte(outcomeSecretMarker), SMTPPassword: []byte(outcomeSecretMarker),
	}
}

func warmupJob(ws uuid.UUID) coreapi.WarmupSendJob {
	return coreapi.WarmupSendJob{
		WorkspaceID: ws.String(), FromMailbox: uuid.NewString(), ToMailbox: uuid.NewString(),
		ThreadID: uuid.NewString(), SendID: uuid.NewString(), Subject: "hi",
		Provider: "smtp", AccessToken: []byte(outcomeSecretMarker), SMTPPassword: []byte(outcomeSecretMarker),
	}
}

const outcomeSecretMarker = "SECRET-THAT-MUST-NOT-TRAVEL"

// ---------------------------------------------------------------------------
// The fault injector
// ---------------------------------------------------------------------------

// errResponseLost is what a worker sees when the control plane answered and the
// answer did not arrive. It is deliberately NOT a sentinel any production code
// branches on: the whole point is that a worker cannot tell this apart from a
// call that never happened, so nothing is allowed to.
var errResponseLost = errors.New("the response was lost in transit")

// droppedResponseTransport is a REAL http.RoundTripper that lets the request
// through, waits for the control plane to answer — which means its handler
// returned, which means everything it committed is committed — and then throws
// the answer away and reports a transport failure to the caller.
//
// That is the third outcome an in-process call does not have, and it is why this
// slice needed a test harness at all: the path where a worker claimed, sent, and
// never learned that the control plane recorded it is UNREACHABLE while the seam
// is a function call.
//
// It is a round tripper rather than a mock of the client because a mock would be
// asserting what this package was written to do. This asserts what happens when
// the network does something the code did not choose.
type droppedResponseTransport struct {
	base http.RoundTripper
	// drop decides per request path, so a test can lose exactly one call in a
	// sequence and let the rest through.
	drop func(path string) bool
	// dropped counts answers actually thrown away, so a test can prove the
	// injector fired rather than passing because nothing happened. Single
	// goroutine per test; no locking needed and none implied.
	dropped int
}

func (d *droppedResponseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := d.base.RoundTrip(req)
	if err != nil || !d.drop(req.URL.Path) {
		return resp, err
	}
	// Drain to EOF before discarding: the server goroutine is what writes this
	// body, so reading it to the end is how the test knows the handler ran to
	// completion rather than being cancelled halfway.
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	d.dropped++
	return nil, errResponseLost
}

// dropOutcomeResponses installs the injector on the client's OUTCOME budget —
// the only transport these methods use — and returns it so a test can assert it
// fired.
func dropOutcomeResponses(c *Client, drop func(path string) bool) *droppedResponseTransport {
	d := &droppedResponseTransport{base: c.outcomes.hc.Transport, drop: drop}
	c.outcomes.hc.Transport = d
	return d
}

// ---------------------------------------------------------------------------
// The tests
// ---------------------------------------------------------------------------

// THE headline for this slice, at unit level: the control plane took the claim,
// the answer was lost, and the worker is told it FAILED — not that it won.
//
// A client that returned (ClaimWon, nil) here would authorise a send on a claim
// it never learned it held; a client that returned (ClaimSkip, nil) would drop
// the send silently. The contract is the zero value AND an error.
func TestALostClaimResponseIsAFailureNotAWin(t *testing.T) {
	ws := uuid.New()
	out := &fakeOutcomes{claimAnswers: []coreapi.ClaimOutcome{coreapi.ClaimWon}}
	c, _ := serveOutcomes(t, out)
	injector := dropOutcomeResponses(c, func(p string) bool { return p == PathStepSendClaim })

	outcome, err := c.ClaimStepSend(context.Background(), stepJob(ws))
	if err == nil {
		t.Fatal("a lost claim response was reported as success")
	}
	if outcome != coreapi.ClaimSkip {
		t.Errorf("outcome = %v alongside the error, want the fail-closed ClaimSkip", outcome)
	}
	if injector.dropped != 1 {
		t.Fatalf("the injector dropped %d responses, want 1 — the test proved nothing", injector.dropped)
	}
	// And the control plane DID take the claim. This is the half that makes the
	// scenario real rather than a network error before anything happened.
	if len(out.calls) != 1 || out.calls[0] != "ClaimStepSend" {
		t.Fatalf("control plane saw %v, want exactly one ClaimStepSend", out.calls)
	}
}

// The retry, which is the whole design: the worker returns the error, asynq
// redelivers the task, and the SECOND claim reaches the control plane and is
// told what already happened. The transport relays that faithfully — it does not
// remember, cache or guess.
func TestTheRetryAfterALostClaimResponseAsksAgainAndIsToldTheTruth(t *testing.T) {
	ws := uuid.New()
	job := stepJob(ws)
	out := &fakeOutcomes{claimAnswers: []coreapi.ClaimOutcome{
		coreapi.ClaimWon,         // the attempt whose answer is lost
		coreapi.ClaimAlreadySent, // what the control plane knows on the retry
	}}
	c, _ := serveOutcomes(t, out)
	first := true
	dropOutcomeResponses(c, func(p string) bool {
		if p == PathStepSendClaim && first {
			first = false
			return true
		}
		return false
	})

	if _, err := c.ClaimStepSend(context.Background(), job); err == nil {
		t.Fatal("the first claim reported success despite a lost response")
	}
	outcome, err := c.ClaimStepSend(context.Background(), job)
	if err != nil {
		t.Fatalf("the retry's claim failed: %v", err)
	}
	if outcome != coreapi.ClaimAlreadySent {
		t.Fatalf("retry outcome = %v, want ClaimAlreadySent — the recover-forward state", outcome)
	}
	if len(out.calls) != 2 {
		t.Errorf("the control plane was asked %d times, want 2 (the client must not answer from memory)", len(out.calls))
	}
}

// The same for a delivery: the control plane committed status='sent' and the
// answer vanished. The worker must report a failure so the task retries — the
// retry's claim is what stops it delivering twice.
func TestALostDeliveryResponseIsReportedAsAFailure(t *testing.T) {
	ws := uuid.New()
	out := &fakeOutcomes{}
	c, _ := serveOutcomes(t, out)
	injector := dropOutcomeResponses(c, func(p string) bool { return p == PathStepSendDelivered })

	err := c.MarkStepDelivered(context.Background(), stepJob(ws), "<m@acme.test>")
	if err == nil {
		t.Fatal("a lost delivery response was reported as success; the retry would never happen")
	}
	if injector.dropped != 1 {
		t.Fatalf("the injector dropped %d responses, want 1", injector.dropped)
	}
	if len(out.calls) != 1 || out.calls[0] != "MarkStepDelivered" {
		t.Fatalf("control plane saw %v, want one MarkStepDelivered", out.calls)
	}
}

// Every mutating route fails the CALL on a lost response and returns its zero
// value. Nothing here invents a success, and nothing returns a half answer an
// "unknown" would be.
func TestEveryOutcomeFailsClosedWhenItsResponseIsLost(t *testing.T) {
	ws := uuid.New()
	ctx := context.Background()
	status := 500

	for _, tc := range []struct {
		name string
		path string
		call func(*Client) error
	}{
		{"ClaimStepSend", PathStepSendClaim, func(c *Client) error {
			o, err := c.ClaimStepSend(ctx, stepJob(ws))
			if err != nil && o != coreapi.ClaimSkip {
				t.Errorf("ClaimStepSend returned %v alongside its error, want the fail-closed ClaimSkip", o)
			}
			return err
		}},
		{"MarkStepDelivered", PathStepSendDelivered, func(c *Client) error {
			return c.MarkStepDelivered(ctx, stepJob(ws), "<m@x>")
		}},
		{"AdvanceStepCursor", PathStepSendAdvance, func(c *Client) error {
			adv, err := c.AdvanceStepCursor(ctx, stepJob(ws))
			if err != nil && adv != (coreapi.Advance{}) {
				t.Errorf("AdvanceStepCursor returned %+v alongside its error", adv)
			}
			return err
		}},
		{"ReleaseStepSend", PathStepSendRelease, func(c *Client) error {
			return c.ReleaseStepSend(ctx, stepJob(ws))
		}},
		{"FinalizeStepSend", PathStepSendFinalize, func(c *Client) error {
			adv, err := c.FinalizeStepSend(ctx, stepJob(ws), coreapi.StepResult{Status: "failed", Err: "550"})
			if err != nil && adv != (coreapi.Advance{}) {
				t.Errorf("FinalizeStepSend returned %+v alongside its error", adv)
			}
			return err
		}},
		{"MarkStepStopped", PathEnrollmentStop, func(c *Client) error {
			return c.MarkStepStopped(ctx, uuid.NewString(), ws.String(), "suppressed")
		}},
		{"DeferEnrollment", PathEnrollmentDefer, func(c *Client) error {
			return c.DeferEnrollment(ctx, uuid.NewString(), ws.String(), time.Now().Add(time.Hour))
		}},
		{"IncrementEnrollmentCapDeferrals", PathEnrollmentCapDeferral, func(c *Client) error {
			n, err := c.IncrementEnrollmentCapDeferrals(ctx, uuid.NewString(), ws.String())
			if err != nil && n != 0 {
				t.Errorf("IncrementEnrollmentCapDeferrals returned %d alongside its error", n)
			}
			return err
		}},
		{"ClaimWarmupSend", PathWarmupSendClaim, func(c *Client) error {
			o, err := c.ClaimWarmupSend(ctx, warmupJob(ws))
			if err != nil && o != coreapi.ClaimSkip {
				t.Errorf("ClaimWarmupSend returned %v alongside its error, want the fail-closed ClaimSkip", o)
			}
			return err
		}},
		{"MarkWarmupSent", PathWarmupSendSent, func(c *Client) error {
			return c.MarkWarmupSent(ctx, warmupJob(ws), "<w@x>")
		}},
		{"ReleaseWarmupSend", PathWarmupSendRelease, func(c *Client) error {
			return c.ReleaseWarmupSend(ctx, warmupJob(ws))
		}},
		{"FailWarmupSend", PathWarmupSendFail, func(c *Client) error {
			return c.FailWarmupSend(ctx, warmupJob(ws), "boom")
		}},
		{"MarkWarmupEngaged", PathWarmupEngaged, func(c *Client) error {
			return c.MarkWarmupEngaged(ctx, uuid.NewString(), ws.String(), true)
		}},
		{"MarkReplied", PathReplyReplied, func(c *Client) error {
			return c.MarkReplied(ctx, uuid.NewString(), ws.String(), "positive", "lexicon", 0.9)
		}},
		{"RecordReplyClass", PathReplyClass, func(c *Client) error {
			return c.RecordReplyClass(ctx, uuid.NewString(), ws.String(), "out_of_office", "header", 1)
		}},
		{"MarkUnsubscribed", PathReplyUnsubscribed, func(c *Client) error {
			return c.MarkUnsubscribed(ctx, uuid.NewString(), ws.String(), "ada@example.test")
		}},
		{"MarkBounced", PathReplyBounced, func(c *Client) error {
			return c.MarkBounced(ctx, uuid.NewString(), ws.String(), "ada@example.test", true)
		}},
		{"MarkWebhookDelivered", PathWebhookMarkDelivered, func(c *Client) error {
			return c.MarkWebhookDelivered(ctx, uuid.NewString(), ws.String(), 1, 200)
		}},
		{"MarkWebhookRetrying", PathWebhookMarkRetrying, func(c *Client) error {
			return c.MarkWebhookRetrying(ctx, uuid.NewString(), ws.String(), 1, "502", &status, time.Now())
		}},
		{"MarkWebhookFailed", PathWebhookMarkFailed, func(c *Client) error {
			return c.MarkWebhookFailed(ctx, uuid.NewString(), ws.String(), 5, "gave up", &status)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := &fakeOutcomes{}
			c, _ := serveOutcomes(t, out)
			injector := dropOutcomeResponses(c, func(p string) bool { return p == tc.path })

			// It works while the response arrives. Without this half the
			// assertion below would pass against a client that never worked.
			injector.drop = func(string) bool { return false }
			if err := tc.call(c); err != nil {
				t.Fatalf("%s failed with the response intact: %v", tc.name, err)
			}
			injector.drop = func(p string) bool { return p == tc.path }

			if err := tc.call(c); err == nil {
				t.Fatalf("%s reported success with its response lost", tc.name)
			}
			if injector.dropped != 1 {
				t.Errorf("the injector dropped %d responses, want 1", injector.dropped)
			}
			// The control plane was reached BOTH times: the write landed, which
			// is exactly what makes a false success dangerous.
			if len(out.calls) != 2 {
				t.Errorf("the control plane was reached %d times, want 2", len(out.calls))
			}
		})
	}
}

// The four-state claim protocol survives the wire in every state. This is the
// one enum in the codebase where a value silently becoming its neighbour is a
// double send or a stalled campaign, so all four are driven rather than the two
// a happy-path test would cover.
func TestEveryClaimStateCrossesTheWireUnchanged(t *testing.T) {
	for _, want := range []coreapi.ClaimOutcome{
		coreapi.ClaimSkip, coreapi.ClaimWon, coreapi.ClaimAlreadySent, coreapi.ClaimDeferred,
	} {
		out := &fakeOutcomes{claimAnswers: []coreapi.ClaimOutcome{want}}
		c, _ := serveOutcomes(t, out)

		got, err := c.ClaimStepSend(context.Background(), stepJob(uuid.New()))
		if err != nil {
			t.Fatalf("ClaimStepSend(%v): %v", want, err)
		}
		if got != want {
			t.Errorf("step claim crossed as %v, want %v", got, want)
		}
		gotWarmup, err := c.ClaimWarmupSend(context.Background(), warmupJob(uuid.New()))
		if err != nil {
			t.Fatalf("ClaimWarmupSend(%v): %v", want, err)
		}
		if gotWarmup != want {
			t.Errorf("warmup claim crossed as %v, want %v", gotWarmup, want)
		}
	}
}

// A claim outcome this worker does not recognise is REFUSED, not defaulted.
//
// There is a tempting default — ClaimSkip never double-sends — and taking it
// would turn a control plane and a worker that no longer agree about the
// protocol into a fleet that quietly stops sending, with no error anywhere.
func TestAnUnknownClaimOutcomeIsRefusedRatherThanDefaulted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"outcome":"probably_fine"}`))
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, testToken, true, smtpSecret(""))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	outcome, err := c.ClaimStepSend(context.Background(), stepJob(uuid.New()))
	if err == nil {
		t.Fatal("an unknown claim outcome was accepted")
	}
	if outcome != coreapi.ClaimSkip {
		t.Errorf("outcome = %v alongside the error, want the fail-closed ClaimSkip", outcome)
	}
	if !strings.Contains(err.Error(), "probably_fine") {
		t.Errorf("the error does not name the value that arrived: %v", err)
	}
}

// The credential the worker is HOLDING when it claims must not travel back.
//
// Slice 2 proved secrets do not cross control-plane→worker. This is the other
// direction and it is newer: the job a worker hands back carries its DECRYPTED
// SMTP password and OAuth token in memory, because the worker just dialed with
// them. The json:"-" tags are what keep them off the wire, and a tag is exactly
// the kind of thing a refactor removes without a build error.
func TestAnOutcomeRequestCarriesNoCredential(t *testing.T) {
	ws := uuid.New()
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"outcome":"won"}`))
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, testToken, true, smtpSecret(""))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx := context.Background()
	job, wjob := stepJob(ws), warmupJob(ws)
	// Errors are not the subject; what left this process is.
	_, _ = c.ClaimStepSend(ctx, job)
	_ = c.MarkStepDelivered(ctx, job, "<m@x>")
	_, _ = c.AdvanceStepCursor(ctx, job)
	_ = c.ReleaseStepSend(ctx, job)
	_, _ = c.FinalizeStepSend(ctx, job, coreapi.StepResult{Status: "failed"})
	_, _ = c.ClaimWarmupSend(ctx, wjob)
	_ = c.MarkWarmupSent(ctx, wjob, "<w@x>")
	_ = c.ReleaseWarmupSend(ctx, wjob)
	_ = c.FailWarmupSend(ctx, wjob, "boom")

	if len(bodies) != 9 {
		t.Fatalf("captured %d request bodies, want 9", len(bodies))
	}
	for i, body := range bodies {
		if bytes.Contains(body, []byte(outcomeSecretMarker)) {
			t.Errorf("request %d carried a credential: %s", i, body)
		}
		// base64 is what encoding/json does to a []byte, so check that form too.
		if bytes.Contains(body, []byte("U0VDUkVU")) {
			t.Errorf("request %d carried a base64 credential: %s", i, body)
		}
	}
	// Belt and braces on the positive half: the job itself DID travel, so the
	// assertion above is not passing because the body is empty.
	if !bytes.Contains(bodies[0], []byte(job.SendID)) {
		t.Errorf("the job did not cross the wire at all: %s", bodies[0])
	}
}

// Every ids-only outcome request carries IDS AND DECIDED VALUES ONLY — no
// filter, no pattern, no limit, no cursor. Asserted on the bytes for the reason
// slices 1 and 2 assert it: a request that could express "rows matching X" would
// make this seam a general query engine over the tenant database.
func TestEveryIdsOnlyOutcomeRequestCarriesIdsOnly(t *testing.T) {
	ws, id := uuid.New().String(), uuid.New().String()
	status := 502
	at := time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)
	until := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var fields map[string]any
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Errorf("request body %q: %v", raw, err)
		}
		bodies = append(bodies, fields)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, testToken, true, smtpSecret(""))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx := context.Background()
	_ = c.MarkStepStopped(ctx, id, ws, "suppressed")
	_ = c.DeferEnrollment(ctx, id, ws, until)
	_, _ = c.IncrementEnrollmentCapDeferrals(ctx, id, ws)
	_ = c.MarkWarmupEngaged(ctx, id, ws, true)
	_ = c.MarkReplied(ctx, id, ws, "positive", "lexicon", 0.75)
	_ = c.RecordReplyClass(ctx, id, ws, "out_of_office", "header", 1)
	_ = c.MarkUnsubscribed(ctx, id, ws, "ada@example.test")
	_ = c.MarkBounced(ctx, id, ws, "ada@example.test", true)
	_ = c.MarkWebhookDelivered(ctx, id, ws, 2, 200)
	_ = c.MarkWebhookRetrying(ctx, id, ws, 3, "502 bad gateway", &status, at)
	_ = c.MarkWebhookFailed(ctx, id, ws, 5, "gave up", &status)

	want := []map[string]any{
		{"workspace_id": ws, "enrollment_id": id, "reason": "suppressed"},
		{"workspace_id": ws, "enrollment_id": id, "until": "2026-05-06T07:08:09Z"},
		{"workspace_id": ws, "enrollment_id": id},
		{"workspace_id": ws, "receipt_id": id, "replied": true},
		{"workspace_id": ws, "enrollment_id": id, "class": "positive", "source": "lexicon", "confidence": 0.75},
		{"workspace_id": ws, "enrollment_id": id, "class": "out_of_office", "source": "header", "confidence": float64(1)},
		{"workspace_id": ws, "enrollment_id": id, "email": "ada@example.test"},
		{"workspace_id": ws, "enrollment_id": id, "email": "ada@example.test", "hard": true},
		{"workspace_id": ws, "delivery_id": id, "attempts": float64(2), "response_status": float64(200)},
		{"workspace_id": ws, "delivery_id": id, "attempts": float64(3), "last_error": "502 bad gateway",
			"response_status": float64(502), "next_attempt_at": "2026-04-05T06:07:08Z"},
		{"workspace_id": ws, "delivery_id": id, "attempts": float64(5), "last_error": "gave up", "response_status": float64(502)},
	}
	if len(bodies) != len(want) {
		t.Fatalf("captured %d request bodies, want %d", len(bodies), len(want))
	}
	for i := range want {
		if !mapsEqual(bodies[i], want[i]) {
			t.Errorf("request %d = %v, want %v", i, bodies[i], want[i])
		}
	}
}

func mapsEqual(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// A control-plane failure is a 500 that never becomes a success, and its error
// text is never relayed into a worker's log.
func TestAnOutcomeWriteFailureIsAnErrorAndRelaysNoText(t *testing.T) {
	out := &fakeOutcomes{err: errors.New(`pq: duplicate key value violates "sends_pkey"`)}
	c, _ := serveOutcomes(t, out)

	outcome, err := c.ClaimStepSend(context.Background(), stepJob(uuid.New()))
	if err == nil {
		t.Fatal("a writer failure was reported as success")
	}
	if outcome != coreapi.ClaimSkip {
		t.Errorf("outcome = %v alongside the error", outcome)
	}
	if strings.Contains(err.Error(), "sends_pkey") {
		t.Errorf("the control plane's error text reached the worker: %v", err)
	}
}

// The three inbox outcomes accept an EMPTY enrollment id — the matched send had
// none (a legacy direct-send) — and it must reach the control plane rather than
// becoming a 400. A 400 here is an error the poller cannot tell from a real
// failure, so it would return before SetInboxCursor and the mailbox would stop
// processing ALL inbound mail.
func TestAnAbsentEnrollmentIDIsPassedThroughRatherThanRefused(t *testing.T) {
	ws := uuid.New().String()
	ctx := context.Background()
	out := &fakeOutcomes{}
	c, _ := serveOutcomes(t, out)

	if err := c.MarkReplied(ctx, "", ws, "positive", "lexicon", 0.5); err != nil {
		t.Errorf("MarkReplied with no enrollment: %v", err)
	}
	if err := c.RecordReplyClass(ctx, "", ws, "out_of_office", "header", 1); err != nil {
		t.Errorf("RecordReplyClass with no enrollment: %v", err)
	}
	if err := c.MarkUnsubscribed(ctx, "", ws, "ada@example.test"); err != nil {
		t.Errorf("MarkUnsubscribed with no enrollment: %v", err)
	}
	if err := c.MarkBounced(ctx, "", ws, "ada@example.test", true); err != nil {
		t.Errorf("MarkBounced with no enrollment: %v", err)
	}
	if len(out.calls) != 4 {
		t.Errorf("the control plane was reached %d times, want 4", len(out.calls))
	}
	// A non-empty id that is NOT a uuid is still refused, on both sides — the
	// permission is for "absent", not for "anything".
	if err := c.MarkReplied(ctx, "not-a-uuid", ws, "positive", "lexicon", 0.5); err == nil {
		t.Error("a malformed enrollment id was accepted")
	}
	if len(out.calls) != 4 {
		t.Errorf("a malformed id reached the control plane: %v", out.calls)
	}
}

// A request whose envelope workspace disagrees with the workspace inside the job
// is refused before anything runs. No correct worker assembles one, so resolving
// it in favour of either side would be the transport deciding a tenancy question.
func TestAJobWhoseWorkspaceDisagreesWithTheEnvelopeIsRefused(t *testing.T) {
	out := &fakeOutcomes{}
	_, srv := serveOutcomes(t, out)

	job := stepJob(uuid.New())
	body, err := json.Marshal(stepJobRequest{WorkspaceID: uuid.NewString(), Job: job})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	code := postOutcome(t, srv, PathStepSendClaim, testToken, string(body))
	if code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", code)
	}
	if len(out.calls) != 0 {
		t.Errorf("the writer was reached %v despite a workspace mismatch", out.calls)
	}

	// The SAME workspace spelled differently is NOT a mismatch: uuid.Parse
	// accepts the braced form, and two spellings of one tenant are one tenant.
	ws := uuid.New()
	job.WorkspaceID = "{" + ws.String() + "}"
	body, err = json.Marshal(stepJobRequest{WorkspaceID: ws.String(), Job: job})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if code := postOutcome(t, srv, PathStepSendClaim, testToken, string(body)); code != http.StatusOK {
		t.Errorf("status = %d for two spellings of one workspace, want 200", code)
	}
}

// Every outcome route is behind the shared fleet token, and refuses an unknown
// field rather than ignoring it. Listed here as a table so a route added later
// is covered by being added to one list.
func TestEveryOutcomeRouteIsGuarded(t *testing.T) {
	out := &fakeOutcomes{}
	_, srv := serveOutcomes(t, out)

	for _, path := range outcomePaths() {
		t.Run(path+"/no token", func(t *testing.T) {
			if code := postOutcome(t, srv, path, "", `{}`); code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", code)
			}
		})
		t.Run(path+"/unknown field", func(t *testing.T) {
			body := `{"workspace_id":"` + uuid.NewString() + `","limit":100}`
			if code := postOutcome(t, srv, path, testToken, body); code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", code)
			}
		})
		t.Run(path+"/malformed workspace", func(t *testing.T) {
			if code := postOutcome(t, srv, path, testToken, `{"workspace_id":"nope"}`); code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", code)
			}
		})
	}
	if len(out.calls) != 0 {
		t.Errorf("the writer was reached %v; every request above should have been refused", out.calls)
	}
}

// outcomePaths is every route slice 3 mounts, in one place so the guard test
// above grows with the transport.
func outcomePaths() []string {
	return []string{
		PathStepSendClaim, PathStepSendDelivered, PathStepSendAdvance, PathStepSendRelease,
		PathStepSendFinalize, PathEnrollmentStop, PathEnrollmentDefer, PathEnrollmentCapDeferral,
		PathWarmupSendClaim, PathWarmupSendSent, PathWarmupSendRelease, PathWarmupSendFail,
		PathWarmupEngaged, PathReplyReplied, PathReplyClass, PathReplyUnsubscribed, PathReplyBounced,
		PathWebhookMarkDelivered, PathWebhookMarkRetrying, PathWebhookMarkFailed,
	}
}

func postOutcome(t *testing.T, srv *httptest.Server, path, token, body string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// A handler missing the outcome writer refuses to be built, like the two halves
// before it. #216's lesson: half a transport starts, registers its routes, and
// fails every call to the rest at the first send.
func TestAHandlerNeedsAnOutcomeWriter(t *testing.T) {
	if _, err := NewHandler(Deps{Suppression: &fakeSuppression{}, Jobs: &fakeJobs{}, InboxSends: &fakeInboxSends{}}, testToken, quietLogger()); err == nil {
		t.Error("a handler with no outcome writer was built")
	}
}

// A cancelled caller context cancels an outcome write rather than being ignored.
// The asynq ceiling above it is what makes that matter.
func TestTheCallerContextIsHonouredOnAnOutcomeWrite(t *testing.T) {
	c, _ := serveOutcomes(t, &fakeOutcomes{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.ClaimStepSend(ctx, stepJob(uuid.New())); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// An unreachable control plane fails every outcome, with the zero value. This is
// the "the call never happened" reading of the same uncertainty the injector
// tests cover from the other side.
func TestAnUnreachableControlPlaneFailsEveryOutcome(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	c, err := NewClient(srv.URL, testToken, true, smtpSecret(""))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	srv.Close()

	ws := uuid.New()
	if o, err := c.ClaimStepSend(context.Background(), stepJob(ws)); err == nil || o != coreapi.ClaimSkip {
		t.Errorf("ClaimStepSend = %v, %v; want (ClaimSkip, error)", o, err)
	}
	if err := c.MarkStepDelivered(context.Background(), stepJob(ws), "<m@x>"); err == nil {
		t.Error("MarkStepDelivered succeeded against a dead control plane")
	}
	if adv, err := c.AdvanceStepCursor(context.Background(), stepJob(ws)); err == nil || adv != (coreapi.Advance{}) {
		t.Errorf("AdvanceStepCursor = %+v, %v; want (zero, error)", adv, err)
	}
}

// The advance's two fields cross intact. next_due_at is what schedules the next
// step, so an instant that arrived wrong (or as the zero value) would put the
// whole sequence at the wrong time or immediately.
func TestTheAdvanceCrossesTheWireIntact(t *testing.T) {
	due := time.Date(2026, 7, 8, 9, 10, 11, 0, time.UTC)
	out := &fakeOutcomes{advance: coreapi.Advance{Completed: false, NextDueAt: due}}
	c, _ := serveOutcomes(t, out)

	adv, err := c.AdvanceStepCursor(context.Background(), stepJob(uuid.New()))
	if err != nil {
		t.Fatalf("AdvanceStepCursor: %v", err)
	}
	if adv.Completed {
		t.Error("Completed crossed as true")
	}
	// Equal, not ==: a time.Time carries a *Location, and the JSON round trip
	// gives it a different one that renders identically.
	if !adv.NextDueAt.Equal(due) {
		t.Errorf("NextDueAt = %v, want %v", adv.NextDueAt, due)
	}
}

// The cap-deferral counter's value crosses. It is the one non-idempotent method
// here, so a caller reading the WRONG number would misjudge a stuck loop.
func TestTheCapDeferralCountCrossesTheWire(t *testing.T) {
	out := &fakeOutcomes{defers: 31}
	c, _ := serveOutcomes(t, out)

	n, err := c.IncrementEnrollmentCapDeferrals(context.Background(), uuid.NewString(), uuid.NewString())
	if err != nil {
		t.Fatalf("IncrementEnrollmentCapDeferrals: %v", err)
	}
	if n != 31 {
		t.Errorf("deferrals = %d, want 31", n)
	}
}
