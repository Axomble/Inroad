package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/cadence"
	"github.com/inroad/inroad/internal/platform/credbroker"
)

// There is no database, no pool and no migration anywhere in this file, for the
// reason remote_test.go states: the thing under test is a client that HAS no
// database access. The end-to-end proof against real Postgres, through the real
// handler, with a NIL POOL on the worker side, lives in
// internal/coreapi/inprocess/remotejobs_integration_test.go.

// fakeJobs is the CONTROL plane's side: it records what it was asked and
// returns fixed answers.
type fakeJobs struct {
	calls     int
	gotArgs   []string
	err       error
	step      coreapi.StepSendJob
	poll      coreapi.InboxPollJob
	warmup    coreapi.WarmupSendJob
	engage    coreapi.WarmupEngageJob
	delivery  coreapi.WebhookDeliveryJob
	content   coreapi.TestSendContent
	transport coreapi.SenderTransport
	send      coreapi.SendRef
}

func (f *fakeJobs) record(args ...string) { f.calls++; f.gotArgs = args }

func (f *fakeJobs) GetStepSendJob(_ context.Context, enrollmentID, workspaceID string) (coreapi.StepSendJob, error) {
	f.record(enrollmentID, workspaceID)
	return f.step, f.err
}

func (f *fakeJobs) GetInboxPollJob(_ context.Context, mailboxID, workspaceID string) (coreapi.InboxPollJob, error) {
	f.record(mailboxID, workspaceID)
	return f.poll, f.err
}

func (f *fakeJobs) GetWarmupSendJob(_ context.Context, mailboxID, workspaceID string) (coreapi.WarmupSendJob, error) {
	f.record(mailboxID, workspaceID)
	return f.warmup, f.err
}

func (f *fakeJobs) GetWarmupEngageJob(_ context.Context, receiptID, workspaceID string) (coreapi.WarmupEngageJob, error) {
	f.record(receiptID, workspaceID)
	return f.engage, f.err
}

func (f *fakeJobs) GetWebhookDeliveryJob(_ context.Context, deliveryID, workspaceID string) (coreapi.WebhookDeliveryJob, error) {
	f.record(deliveryID, workspaceID)
	return f.delivery, f.err
}

func (f *fakeJobs) GetTestSendContent(_ context.Context, workspaceID, campaignID, stepID string) (coreapi.TestSendContent, error) {
	f.record(workspaceID, campaignID, stepID)
	return f.content, f.err
}

func (f *fakeJobs) ResolveSenderTransport(_ context.Context, workspaceID, mailboxID string) (coreapi.SenderTransport, error) {
	f.record(workspaceID, mailboxID)
	return f.transport, f.err
}

func (f *fakeJobs) FindSendByMessageID(_ context.Context, workspaceID, messageID string) (coreapi.SendRef, error) {
	f.record(workspaceID, messageID)
	return f.send, f.err
}

// fakeOpener is the credential broker's side: the channel that DOES hand out a
// plaintext secret, which the coreapi wire deliberately does not.
type fakeOpener struct {
	calls        int
	gotWS        uuid.UUID
	gotMailbox   uuid.UUID
	gotEndpoint  uuid.UUID
	provider     string
	accessToken  []byte
	smtpPassword []byte
	secret       []byte
	err          error
}

func (f *fakeOpener) OpenMailbox(_ context.Context, ref credbroker.MailboxRef) (credbroker.MailboxSecret, error) {
	f.calls++
	f.gotWS, f.gotMailbox = ref.WorkspaceID, ref.MailboxID
	if f.err != nil {
		return credbroker.MailboxSecret{}, f.err
	}
	return credbroker.MailboxSecret{
		Provider: f.provider, AccessToken: f.accessToken, SMTPPassword: f.smtpPassword,
	}, nil
}

func (f *fakeOpener) OpenWebhookEndpointSecret(_ context.Context, ws, endpoint uuid.UUID, _ []byte) ([]byte, error) {
	f.calls++
	f.gotWS, f.gotEndpoint = ws, endpoint
	if f.err != nil {
		return nil, f.err
	}
	return f.secret, nil
}

// serveJobs stands the real handler up over a fake reader and returns a client
// wired to a fake broker — the two channels a fleet worker actually has.
func serveJobs(t *testing.T, jobs *fakeJobs, opener *fakeOpener) (*Client, *httptest.Server) {
	t.Helper()
	h, err := NewHandler(Deps{Suppression: &fakeSuppression{}, Jobs: jobs, Outcomes: &fakeOutcomes{}, InboxSends: &fakeInboxSends{}}, testToken, quietLogger())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL, testToken, true, opener)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c, srv
}

// smtpSecret is a broker that answers for an smtp mailbox.
func smtpSecret(password string) *fakeOpener {
	return &fakeOpener{provider: "smtp", smtpPassword: []byte(password)}
}

// THE headline for this slice: a full step send job crosses the wire intact —
// every gate flag, the schedule, the personalization vars, the threading
// headers — and its credential arrives from the broker rather than the job.
func TestAStepSendJobRoundTripsAndItsCredentialComesFromTheBroker(t *testing.T) {
	ws, enrollment, mailbox := uuid.New(), uuid.New(), uuid.New()
	jobs := &fakeJobs{step: coreapi.StepSendJob{
		EnrollmentID: enrollment.String(), WorkspaceID: ws.String(),
		CampaignID: uuid.New().String(), ContactID: uuid.New().String(),
		MailboxID: mailbox.String(), SendID: uuid.New().String(), VariantID: uuid.New().String(),
		CurrentStep: 2, StepOrder: 3, NextDelaySeconds: 259200, LastStep: true,
		Suppressed: false, CampaignLimited: false, NewLeadLimited: false, HealthPaused: false,
		NotDueUntil:       time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC),
		EffectiveDailyCap: 50, SentToday: 12, MinIntervalSeconds: 180,
		ToEmail: "ada@example.test",
		Vars: coreapi.ContactVars{
			FirstName: "Ada", LastName: "Lovelace", Email: "ada@example.test",
			Company: "Analytical Engines", Custom: map[string]string{"role": "ops"},
		},
		Subject: "Re: engines", ThreadSubject: "engines",
		BodyText: "hello", BodyHTML: "<p>hello</p>",
		UnsubURL: "https://app.example/u/tok", InReplyTo: "<a@b>", References: "<a@b> <c@d>",
		TrackingEnabled: true,
		Schedule:        cadence.DefaultSchedule("America/New_York"),
		FromEmail:       "grace@acme.test", FromName: "Grace",
		Provider: "smtp", SMTPHost: "smtp.acme.test", SMTPPort: 587,
		SMTPUsername: "grace@acme.test", AllowPlaintext: false,
		// What the CONTROL plane's own build would have carried. It must not
		// reach the worker through this transport.
		AccessToken: []byte("control-plane-token"), SMTPPassword: []byte("control-plane-password"),
	}}
	opener := smtpSecret("brokered-password")
	c, _ := serveJobs(t, jobs, opener)

	got, err := c.GetStepSendJob(context.Background(), enrollment.String(), ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob: %v", err)
	}

	// Everything that is not a credential survived the round trip byte for byte.
	want := jobs.step
	want.AccessToken, want.SMTPPassword = nil, []byte("brokered-password")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("job mismatch\n got: %+v\nwant: %+v", got, want)
	}
	// The secret is the BROKER's, not the control plane's job build.
	if string(got.SMTPPassword) != "brokered-password" {
		t.Errorf("SMTPPassword = %q, want the brokered value", got.SMTPPassword)
	}
	if opener.calls != 1 {
		t.Errorf("the broker was asked %d times, want 1", opener.calls)
	}
	if opener.gotWS != ws || opener.gotMailbox != mailbox {
		t.Errorf("broker asked about (%v, %v), want (%v, %v)", opener.gotWS, opener.gotMailbox, ws, mailbox)
	}
	// And the control plane saw the ids it was sent.
	if len(jobs.gotArgs) != 2 || jobs.gotArgs[0] != enrollment.String() || jobs.gotArgs[1] != ws.String() {
		t.Errorf("control plane saw %v, want [%s %s]", jobs.gotArgs, enrollment, ws)
	}
}

// The property the whole design rests on, asserted on the BYTES: no job
// response body contains a credential, whatever the control plane's own build
// put on the struct. A hand-written mirror would give this by construction; the
// tagged seam type gives it by tag, so it is worth proving on the wire.
func TestSecretJobFieldsNeverCrossTheWire(t *testing.T) {
	const marker = "SECRET-THAT-MUST-NOT-TRAVEL"
	secret := []byte(marker)

	jobs := &fakeJobs{
		step: coreapi.StepSendJob{
			MailboxID: uuid.New().String(), Provider: "smtp",
			AccessToken: secret, SMTPPassword: secret,
		},
		poll: coreapi.InboxPollJob{Provider: "smtp", AccessToken: secret, Password: secret},
		warmup: coreapi.WarmupSendJob{
			FromMailbox: uuid.New().String(), Provider: "smtp",
			AccessToken: secret, SMTPPassword: secret,
		},
		engage: coreapi.WarmupEngageJob{
			RecipientMailbox: uuid.New().String(), Provider: "smtp",
			AccessToken: secret, SMTPPassword: secret, DoReply: true,
			ReplySend: coreapi.WarmupSendJob{AccessToken: secret, SMTPPassword: secret},
		},
		delivery: coreapi.WebhookDeliveryJob{
			EndpointID: uuid.New().String(), Secret: secret,
			// Payload is NOT a secret and must travel: it is the body the
			// receiver is about to be handed.
			Payload: []byte("the-delivery-payload"),
		},
		transport: coreapi.SenderTransport{Provider: "smtp", AccessToken: secret, SMTPPassword: secret},
	}

	// Capture every response body the handler writes.
	h, err := NewHandler(Deps{Suppression: &fakeSuppression{}, Jobs: jobs, Outcomes: &fakeOutcomes{}, InboxSends: &fakeInboxSends{}}, testToken, quietLogger())
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		body := rec.Body.Bytes()
		bodies = append(bodies, body)
		for k, vs := range rec.Header() {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, testToken, true, smtpSecret("brokered"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx, ws, id := context.Background(), uuid.New().String(), uuid.New().String()
	// Errors are not the subject here — only what the handler put on the wire.
	_, _ = c.GetStepSendJob(ctx, id, ws)
	_, _ = c.GetInboxPollJob(ctx, id, ws)
	_, _ = c.GetWarmupSendJob(ctx, id, ws)
	_, _ = c.GetWarmupEngageJob(ctx, id, ws)
	_, _ = c.GetWebhookDeliveryJob(ctx, id, ws)
	_, _ = c.ResolveSenderTransport(ctx, ws, id)

	if len(bodies) != 6 {
		t.Fatalf("captured %d response bodies, want 6", len(bodies))
	}
	for i, body := range bodies {
		if bytes.Contains(body, []byte(marker)) {
			t.Errorf("response %d carried a credential: %s", i, body)
		}
		// base64 is what encoding/json does to a []byte, so check that form too
		// — a secret that travelled would travel encoded, not as raw text.
		if strings.Contains(string(body), "U0VDUkVU") { // the marker, as encoding/json would render a []byte
			t.Errorf("response %d carried a base64 credential: %s", i, body)
		}
	}
	// Belt and braces on the positive half: the non-secret []byte DID travel, so
	// the test above is not passing because nothing is encoded at all.
	if !strings.Contains(string(bodies[4]), "dGhlLWRlbGl2ZXJ5LXBheWxvYWQ=") { // the payload, base64 as encoding/json renders it
		t.Errorf("the webhook payload did not cross the wire: %s", bodies[4])
	}
}

// Every request carries IDS ONLY — no filter, no pattern, no limit, no cursor.
// Asserted on the bytes for the same reason slice 1 asserts it: a request that
// could express "rows matching X" would make this seam a general query engine.
func TestEveryJobRequestCarriesIdsOnly(t *testing.T) {
	ws, id, campaign := uuid.New().String(), uuid.New().String(), uuid.New().String()

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
	// The credential fill is irrelevant here and its failures are ignored; the
	// request bodies are the subject.
	_, _ = c.GetStepSendJob(ctx, id, ws)
	_, _ = c.GetInboxPollJob(ctx, id, ws)
	_, _ = c.GetWarmupSendJob(ctx, id, ws)
	_, _ = c.GetWarmupEngageJob(ctx, id, ws)
	_, _ = c.GetWebhookDeliveryJob(ctx, id, ws)
	_, _ = c.GetTestSendContent(ctx, ws, campaign, id)
	_, _ = c.ResolveSenderTransport(ctx, ws, id)
	_, _ = c.FindSendByMessageID(ctx, ws, "<a@b.test>")

	want := []map[string]any{
		{"workspace_id": ws, "enrollment_id": id},
		{"workspace_id": ws, "mailbox_id": id},
		{"workspace_id": ws, "mailbox_id": id},
		{"workspace_id": ws, "receipt_id": id},
		{"workspace_id": ws, "delivery_id": id},
		{"workspace_id": ws, "campaign_id": campaign, "step_id": id},
		{"workspace_id": ws, "mailbox_id": id},
		{"workspace_id": ws, "message_id": "<a@b.test>"},
	}
	if len(bodies) != len(want) {
		t.Fatalf("captured %d request bodies, want %d", len(bodies), len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(bodies[i], want[i]) {
			t.Errorf("request %d = %v, want %v", i, bodies[i], want[i])
		}
	}
}

// A job with no sender resolved — every skip, stop and deferral branch — asks
// the broker for nothing. Opening a credential for a send that will not happen
// is the exact waste GetStepSendJob's own ordering avoids in process; the wire
// must not reintroduce it.
func TestAJobWithNoResolvedSenderBrokersNoCredential(t *testing.T) {
	for _, tc := range []struct {
		name string
		jobs *fakeJobs
		call func(*Client) error
	}{
		{
			"step send: skipped enrollment",
			&fakeJobs{step: coreapi.StepSendJob{Skip: true}},
			func(c *Client) error {
				_, err := c.GetStepSendJob(context.Background(), uuid.New().String(), uuid.New().String())
				return err
			},
		},
		{
			"step send: campaign paused",
			&fakeJobs{step: coreapi.StepSendJob{CampaignPaused: true}},
			func(c *Client) error {
				_, err := c.GetStepSendJob(context.Background(), uuid.New().String(), uuid.New().String())
				return err
			},
		},
		{
			"step send: mailbox removed",
			&fakeJobs{step: coreapi.StepSendJob{MailboxRemoved: true}},
			func(c *Client) error {
				_, err := c.GetStepSendJob(context.Background(), uuid.New().String(), uuid.New().String())
				return err
			},
		},
		{
			"warmup send: nothing to do",
			&fakeJobs{warmup: coreapi.WarmupSendJob{Skip: true}},
			func(c *Client) error {
				_, err := c.GetWarmupSendJob(context.Background(), uuid.New().String(), uuid.New().String())
				return err
			},
		},
		{
			"test send content: no credential on this route at all",
			&fakeJobs{content: coreapi.TestSendContent{Subject: "hi"}},
			func(c *Client) error {
				_, err := c.GetTestSendContent(context.Background(),
					uuid.New().String(), uuid.New().String(), uuid.New().String())
				return err
			},
		},
		{
			"send by message id: no credential on this route at all",
			&fakeJobs{send: coreapi.SendRef{SendID: uuid.New().String()}},
			func(c *Client) error {
				_, err := c.FindSendByMessageID(context.Background(), uuid.New().String(), "<a@b>")
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opener := smtpSecret("never-needed")
			c, _ := serveJobs(t, tc.jobs, opener)
			if err := tc.call(c); err != nil {
				t.Fatalf("call: %v", err)
			}
			if opener.calls != 0 {
				t.Errorf("the broker was asked %d times for a job with no sender, want 0", opener.calls)
			}
		})
	}
}

// The provider check, mirrored from inprocess.openMailboxSecret: a broker
// answering about a different provider than the job describes means the two are
// not looking at the same mailbox. Dialing SMTP with an access token is not a
// failure mode worth discovering at the provider.
func TestABrokerAnsweringForTheWrongProviderIsRefused(t *testing.T) {
	jobs := &fakeJobs{step: coreapi.StepSendJob{
		MailboxID: uuid.New().String(), Provider: "gmail",
	}}
	// The job says gmail; the broker answers smtp.
	c, _ := serveJobs(t, jobs, smtpSecret("wrong-shape"))

	job, err := c.GetStepSendJob(context.Background(), uuid.New().String(), uuid.New().String())
	if err == nil {
		t.Fatal("a provider mismatch was accepted, want an error")
	}
	if job.SMTPPassword != nil || job.AccessToken != nil {
		t.Error("a refused job still carried a credential")
	}
	if job.MailboxID != "" {
		t.Error("a refused job was returned partially built; the contract is the zero value")
	}
}

// Fail closed on the credential half too: a job that fetched fine but whose
// credential could not be opened is NOT returned. A job with an empty password
// would dial without authenticating, which is worse than no job.
func TestABrokerFailureDiscardsTheWholeJob(t *testing.T) {
	jobs := &fakeJobs{step: coreapi.StepSendJob{
		MailboxID: uuid.New().String(), Provider: "smtp", ToEmail: "ada@example.test",
	}}
	c, _ := serveJobs(t, jobs, &fakeOpener{err: errors.New("broker is down")})

	job, err := c.GetStepSendJob(context.Background(), uuid.New().String(), uuid.New().String())
	if err == nil {
		t.Fatal("GetStepSendJob succeeded with a dead broker, want an error")
	}
	if job.ToEmail != "" || job.MailboxID != "" {
		t.Errorf("a partially built job was returned: %+v", job)
	}
}

// The warmup engage job's two credential copies must be the SAME slices. The
// engage worker defers a zeroize of the OUTER pair only, so an unaliased inner
// copy would survive the wipe with the plaintext intact — which is exactly what
// a naive re-fill across a wire would produce.
func TestTheEngageJobsReplyCredentialAliasesTheOuterOne(t *testing.T) {
	jobs := &fakeJobs{engage: coreapi.WarmupEngageJob{
		RecipientMailbox: uuid.New().String(), Provider: "smtp", DoReply: true,
		ReplySend: coreapi.WarmupSendJob{SendID: uuid.New().String()},
	}}
	c, _ := serveJobs(t, jobs, smtpSecret("recipient-password"))

	job, err := c.GetWarmupEngageJob(context.Background(), uuid.New().String(), uuid.New().String())
	if err != nil {
		t.Fatalf("GetWarmupEngageJob: %v", err)
	}
	if string(job.ReplySend.SMTPPassword) != "recipient-password" {
		t.Fatalf("the reply send carries %q, want the recipient's password", job.ReplySend.SMTPPassword)
	}
	// Wipe the outer copy the way the worker does, and assert the inner one went
	// with it. Comparing pointers would also work; wiping is the actual property.
	for i := range job.SMTPPassword {
		job.SMTPPassword[i] = 0
	}
	if bytes.Contains(job.ReplySend.SMTPPassword, []byte("recipient-password")) {
		t.Errorf("the reply credential survived the worker's zeroize: %q", job.ReplySend.SMTPPassword)
	}
	// Same for the access token half, so a gmail engagement is covered too.
	if len(job.ReplySend.AccessToken) != len(job.AccessToken) {
		t.Errorf("reply access token length %d, outer %d; the two are not the same slice",
			len(job.ReplySend.AccessToken), len(job.AccessToken))
	}
}

// An engage job that names no recipient is REFUSED, not returned uncredentialled.
// It is the one route where an empty credential subject cannot mean "no
// credential needed": every engagement dials the recipient's own mailbox, even a
// passive mark-read, so a job without one would dial unauthenticated.
func TestAnEngageJobWithNoRecipientIsRefused(t *testing.T) {
	jobs := &fakeJobs{engage: coreapi.WarmupEngageJob{
		Provider: "smtp", DoMarkRead: true, // RecipientMailbox deliberately empty
	}}
	opener := smtpSecret("never-reached")
	c, _ := serveJobs(t, jobs, opener)

	job, err := c.GetWarmupEngageJob(context.Background(), uuid.New().String(), uuid.New().String())
	if err == nil {
		t.Fatal("an engage job with no recipient was accepted, want an error")
	}
	if !reflect.ValueOf(job).IsZero() {
		t.Errorf("a refused engage job was returned: %+v", job)
	}
	if opener.calls != 0 {
		t.Errorf("the broker was asked %d times for a job naming no mailbox, want 0", opener.calls)
	}
}

// A passive engagement (mark-read only, no reply) still needs a credential —
// the IMAP modify uses it — and it is brokered for the recipient named on the
// job rather than inferred from a ReplySend that is not there.
func TestAPassiveEngageJobStillBrokersTheRecipientCredential(t *testing.T) {
	recipient := uuid.New()
	jobs := &fakeJobs{engage: coreapi.WarmupEngageJob{
		RecipientMailbox: recipient.String(), Provider: "smtp", DoMarkRead: true, DoReply: false,
	}}
	opener := smtpSecret("recipient-password")
	c, _ := serveJobs(t, jobs, opener)

	job, err := c.GetWarmupEngageJob(context.Background(), uuid.New().String(), uuid.New().String())
	if err != nil {
		t.Fatalf("GetWarmupEngageJob: %v", err)
	}
	if string(job.SMTPPassword) != "recipient-password" {
		t.Errorf("SMTPPassword = %q, want the recipient's", job.SMTPPassword)
	}
	if opener.gotMailbox != recipient {
		t.Errorf("broker asked about %v, want the receipt's recipient %v", opener.gotMailbox, recipient)
	}
	if job.ReplySend.SMTPPassword != nil {
		t.Error("a non-replying engagement carried a reply credential")
	}
}

// The inbox poll job's secret lands on Password, not SMTPPassword: a mailbox
// has ONE stored secret and this job type names it for the protocol it is used
// on. Getting this wrong would leave the poller dialing IMAP with no password.
func TestTheInboxPollJobsSecretLandsOnPassword(t *testing.T) {
	jobs := &fakeJobs{poll: coreapi.InboxPollJob{
		Provider: "smtp", Host: "imap.acme.test", Port: 993,
		Username: "grace@acme.test", Email: "grace@acme.test",
		LastSeenUID: 4242, UIDValidity: 7,
	}}
	c, _ := serveJobs(t, jobs, smtpSecret("imap-password"))

	job, err := c.GetInboxPollJob(context.Background(), uuid.New().String(), uuid.New().String())
	if err != nil {
		t.Fatalf("GetInboxPollJob: %v", err)
	}
	if string(job.Password) != "imap-password" {
		t.Errorf("Password = %q, want the brokered secret", job.Password)
	}
	if job.AccessToken != nil {
		t.Errorf("an smtp mailbox got an access token: %q", job.AccessToken)
	}
	// And the cursor survived: resuming from the wrong UID re-reads or skips mail.
	if job.LastSeenUID != 4242 || job.UIDValidity != 7 {
		t.Errorf("cursor = (%d, %d), want (4242, 7)", job.LastSeenUID, job.UIDValidity)
	}
}

// An API-provider mailbox gets an access token and no password, and its opaque
// cursor survives.
func TestAnAPIProviderPollJobCarriesTheTokenAndCursor(t *testing.T) {
	jobs := &fakeJobs{poll: coreapi.InboxPollJob{
		Provider: "gmail", Cursor: "history-id-991", Email: "grace@acme.test",
	}}
	c, _ := serveJobs(t, jobs, &fakeOpener{provider: "gmail", accessToken: []byte("ya29.token")})

	job, err := c.GetInboxPollJob(context.Background(), uuid.New().String(), uuid.New().String())
	if err != nil {
		t.Fatalf("GetInboxPollJob: %v", err)
	}
	if string(job.AccessToken) != "ya29.token" {
		t.Errorf("AccessToken = %q, want the brokered token", job.AccessToken)
	}
	if job.Password != nil {
		t.Errorf("an api-provider mailbox got a password: %q", job.Password)
	}
	if job.Cursor != "history-id-991" {
		t.Errorf("Cursor = %q, want the stored one", job.Cursor)
	}
}

// The webhook secret is brokered by the endpoint id the JOB names, not by
// anything the caller supplied — the caller names a delivery and does not know
// its endpoint.
func TestTheWebhookSecretIsBrokeredByTheJobsEndpointID(t *testing.T) {
	ws, endpoint := uuid.New(), uuid.New()
	jobs := &fakeJobs{delivery: coreapi.WebhookDeliveryJob{
		DeliveryID: uuid.New().String(), EndpointID: endpoint.String(), WorkspaceID: ws.String(),
		EventType: "reply.received", Payload: []byte(`{"a":1}`),
		URL: "https://hooks.example/inroad", Attempts: 1, Status: "pending", EndpointActive: true,
	}}
	opener := &fakeOpener{secret: []byte("hmac-key")}
	c, _ := serveJobs(t, jobs, opener)

	job, err := c.GetWebhookDeliveryJob(context.Background(), uuid.New().String(), ws.String())
	if err != nil {
		t.Fatalf("GetWebhookDeliveryJob: %v", err)
	}
	if string(job.Secret) != "hmac-key" {
		t.Errorf("Secret = %q, want the brokered one", job.Secret)
	}
	if opener.gotEndpoint != endpoint || opener.gotWS != ws {
		t.Errorf("broker asked about (%v, %v), want (%v, %v)", opener.gotWS, opener.gotEndpoint, ws, endpoint)
	}
	if string(job.Payload) != `{"a":1}` {
		t.Errorf("Payload = %q, want the stored body byte-identical", job.Payload)
	}
}

// ErrNoMatch is the ordinary answer for nearly every message the poller sees,
// and internal/worker/inbox branches on it in three places. A generic error
// would abort the poll before SetInboxCursor and stop the mailbox processing
// inbound mail at all.
func TestNoMatchingSendCrossesAsErrNoMatch(t *testing.T) {
	jobs := &fakeJobs{err: coreapi.ErrNoMatch}
	c, _ := serveJobs(t, jobs, smtpSecret(""))

	ref, err := c.FindSendByMessageID(context.Background(), uuid.New().String(), "<stranger@elsewhere.test>")
	if !errors.Is(err, coreapi.ErrNoMatch) {
		t.Fatalf("err = %v, want coreapi.ErrNoMatch", err)
	}
	if ref != (coreapi.SendRef{}) {
		t.Errorf("a no-match returned %+v, want the zero value", ref)
	}
}

// A vanished row crosses as pgx.ErrNoRows, because internal/worker/webhook
// drops the task on it rather than retrying to exhaustion.
func TestAVanishedRowCrossesAsErrNoRows(t *testing.T) {
	jobs := &fakeJobs{err: pgx.ErrNoRows}
	c, _ := serveJobs(t, jobs, smtpSecret(""))

	if _, err := c.GetWebhookDeliveryJob(context.Background(), uuid.New().String(), uuid.New().String()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("err = %v, want pgx.ErrNoRows", err)
	}
}

// The other direction, and the one that would be silent: a 404 that names no
// code — an older control plane that does not serve the route, answering
// net/http's own plain-text "404 page not found" — must NOT become ErrNoRows.
// It would make a worker discard every webhook delivery as "already gone".
func TestAnUnrecognised404IsNotMistakenForAVanishedRow(t *testing.T) {
	// A server with no routes at all: exactly what an un-upgraded control plane
	// running slice 1 would be.
	srv := httptest.NewServer(http.NewServeMux())
	defer srv.Close()

	c, err := NewClient(srv.URL, testToken, true, smtpSecret(""))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = c.GetWebhookDeliveryJob(context.Background(), uuid.New().String(), uuid.New().String())
	if err == nil {
		t.Fatal("an unserved route answered successfully")
	}
	if errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("a plain 404 was read as a vanished row: %v", err)
	}
	if errors.Is(err, coreapi.ErrNoMatch) {
		t.Errorf("a plain 404 was read as a no-match: %v", err)
	}
}

// Fail closed for every job read: an unreachable control plane is an error and
// a ZERO job, never a half-built one. A job with an empty body or an empty gate
// flag is worse than no job — it would send the wrong mail rather than none.
func TestAnUnreachableControlPlaneFailsEveryJobRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	c, err := NewClient(srv.URL, testToken, true, smtpSecret("unused"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	srv.Close() // nothing is listening from here on

	ctx, ws, id := context.Background(), uuid.New().String(), uuid.New().String()
	for _, tc := range []struct {
		name string
		call func() (any, error)
	}{
		{"GetStepSendJob", func() (any, error) { return c.GetStepSendJob(ctx, id, ws) }},
		{"GetInboxPollJob", func() (any, error) { return c.GetInboxPollJob(ctx, id, ws) }},
		{"GetWarmupSendJob", func() (any, error) { return c.GetWarmupSendJob(ctx, id, ws) }},
		{"GetWarmupEngageJob", func() (any, error) { return c.GetWarmupEngageJob(ctx, id, ws) }},
		{"GetWebhookDeliveryJob", func() (any, error) { return c.GetWebhookDeliveryJob(ctx, id, ws) }},
		{"GetTestSendContent", func() (any, error) { return c.GetTestSendContent(ctx, ws, id, id) }},
		{"ResolveSenderTransport", func() (any, error) { return c.ResolveSenderTransport(ctx, ws, id) }},
		{"FindSendByMessageID", func() (any, error) { return c.FindSendByMessageID(ctx, ws, "<a@b>") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.call()
			if err != nil {
				if !reflect.ValueOf(got).IsZero() {
					t.Errorf("%s returned %+v alongside its error, want the zero value", tc.name, got)
				}
				return
			}
			t.Fatalf("%s succeeded against a dead control plane", tc.name)
		})
	}
}

// The control plane's own error text is never relayed into a worker's log: an
// upstream string can carry a pg message or request detail.
func TestAJobReadFailureDoesNotRelayTheControlPlanesErrorText(t *testing.T) {
	jobs := &fakeJobs{err: errors.New("pq: relation \"sequence_enrollments\" does not exist")}
	c, _ := serveJobs(t, jobs, smtpSecret(""))

	_, err := c.GetStepSendJob(context.Background(), uuid.New().String(), uuid.New().String())
	if err == nil {
		t.Fatal("a reader failure was reported as success")
	}
	if strings.Contains(err.Error(), "sequence_enrollments") {
		t.Errorf("the control plane's error text reached the worker: %v", err)
	}
}

// A malformed id never reaches the reader: rejected on both sides, because a
// handler may not trust its client and a client should not spend a round trip.
func TestAMalformedIDNeverReachesTheJobReader(t *testing.T) {
	jobs := &fakeJobs{}
	c, srv := serveJobs(t, jobs, smtpSecret(""))

	if _, err := c.GetStepSendJob(context.Background(), "not-a-uuid", uuid.New().String()); err == nil {
		t.Fatal("a malformed enrollment id was accepted")
	}
	if _, err := c.GetStepSendJob(context.Background(), uuid.New().String(), "not-a-uuid"); err == nil {
		t.Fatal("a malformed workspace id was accepted")
	}
	if jobs.calls != 0 {
		t.Fatalf("the reader was reached %d times for a malformed id, want 0", jobs.calls)
	}

	// Handler side, bypassing the client's own check.
	for _, body := range []string{
		`{"workspace_id":"nope","enrollment_id":"` + uuid.New().String() + `"}`,
		`{"workspace_id":"` + uuid.New().String() + `","enrollment_id":"nope"}`,
	} {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
			srv.URL+PathStepSendJob, strings.NewReader(body))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+testToken)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("status = %d for %s, want 400", resp.StatusCode, body)
		}
	}
	if jobs.calls != 0 {
		t.Errorf("the reader was reached %d times for a malformed id, want 0", jobs.calls)
	}
}

// An unknown field is refused rather than ignored, on every job route. This is
// what stops a route growing an accidental second parameter: a worker that sent
// a "limit" must get a 400, not a silently narrower answer.
func TestAnUnknownFieldIsRefusedOnEveryJobRoute(t *testing.T) {
	jobs := &fakeJobs{}
	_, srv := serveJobs(t, jobs, smtpSecret(""))

	ws, id := uuid.New().String(), uuid.New().String()
	for _, path := range []string{
		PathStepSendJob, PathInboxPollJob, PathWarmupSendJob, PathWarmupEngageJob,
		PathWebhookDeliveryJob, PathTestSendContent, PathSenderTransport, PathSendByMessageID,
	} {
		body := `{"workspace_id":"` + ws + `","mailbox_id":"` + id + `","limit":100}`
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+testToken)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", path, resp.StatusCode)
		}
	}
	if jobs.calls != 0 {
		t.Errorf("the reader was reached %d times, want 0", jobs.calls)
	}
}

// Every job route is behind the shared fleet token, not just the one slice 1
// added. A route that forgot the wrapper would serve a tenant's send content to
// anyone who could reach the listener.
func TestEveryJobRouteRequiresTheFleetToken(t *testing.T) {
	jobs := &fakeJobs{}
	_, srv := serveJobs(t, jobs, smtpSecret(""))

	for _, path := range []string{
		PathSuppressionCheck, PathStepSendJob, PathInboxPollJob, PathWarmupSendJob,
		PathWarmupEngageJob, PathWebhookDeliveryJob, PathTestSendContent,
		PathSenderTransport, PathSendByMessageID,
	} {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+path,
			strings.NewReader(`{"workspace_id":"`+uuid.New().String()+`"}`))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		// No Authorization header at all.
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", path, resp.StatusCode)
		}
	}
	if jobs.calls != 0 {
		t.Errorf("the reader was reached %d times with no token, want 0", jobs.calls)
	}
}

// A handler missing any half refuses to be built. Part of a transport would
// start, register its routes, and fail every call to the rest at the first send.
func TestAHandlerNeedsBothHalvesWired(t *testing.T) {
	if _, err := NewHandler(Deps{Jobs: &fakeJobs{}, Outcomes: &fakeOutcomes{}, InboxSends: &fakeInboxSends{}}, testToken, quietLogger()); err == nil {
		t.Error("a handler with no suppression reader was built")
	}
	if _, err := NewHandler(Deps{Suppression: &fakeSuppression{}, Outcomes: &fakeOutcomes{}, InboxSends: &fakeInboxSends{}}, testToken, quietLogger()); err == nil {
		t.Error("a handler with no job reader was built")
	}
	if _, err := NewHandler(Deps{Suppression: &fakeSuppression{}, Jobs: &fakeJobs{}, Outcomes: &fakeOutcomes{}}, testToken, quietLogger()); err == nil {
		t.Error("a handler with no inbox send writer was built")
	}
}

// A client with no credential broker refuses to be built. It could fetch jobs
// and then have nothing to send them with — a failure that would otherwise
// surface at a mailbox dial rather than at startup.
func TestAClientNeedsACredentialBroker(t *testing.T) {
	if _, err := NewClient("https://control.example", testToken, false, nil); !errors.Is(err, ErrNoCredentialSource) {
		t.Errorf("err = %v, want ErrNoCredentialSource", err)
	}
}

// A cancelled caller context cancels a job read rather than being ignored.
func TestTheCallerContextIsHonouredOnAJobRead(t *testing.T) {
	c, _ := serveJobs(t, &fakeJobs{}, smtpSecret(""))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.GetStepSendJob(ctx, uuid.New().String(), uuid.New().String()); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
