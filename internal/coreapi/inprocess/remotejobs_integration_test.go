//go:build integration

package inprocess

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/app/suppression"
	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/coreapi/remote"
	"github.com/inroad/inroad/internal/platform/credbroker"
	"github.com/inroad/inroad/internal/platform/crypto"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/mail"
)

// Slice 2 of the coreapi remote transport, end to end: the per-message job
// READS, against real Postgres, through the real handler, from a worker-side
// client that HAS NO DATABASE.
//
// The nil pool is the assertion, exactly as it is in
// remotesuppression_integration_test.go. localJobs would dereference it on the
// first query, so any answer this client gives demonstrably came off the wire.
// Every expectation below is a value only Postgres and the keyring could have
// produced — a decrypted password, a seeded subject, a stored cursor — rather
// than a zero value a broken transport would also return.

// fleetListener stands up BOTH fleet transports on one server under one token,
// the way cmd/inroad's newFleetHandler does. Both are needed: the job routes
// answer with everything except a credential, and the worker gets that from the
// credential broker beside them.
//
// It takes the control-plane coreapi.Client and makes the same two type
// assertions cmd/inroad makes, so a signature that drifts fails here the way it
// would fail at startup.
func fleetListener(t *testing.T, q *gen.Queries, keyring *crypto.Keyring, cp coreapi.Client) *httptest.Server {
	t.Helper()
	broker, err := credbroker.NewHandler(
		NewCredentialOpener(q, keyring, mail.GoogleOAuth{}, mail.MicrosoftOAuth{}),
		remoteTestToken, remoteQuiet())
	if err != nil {
		t.Fatalf("credbroker.NewHandler: %v", err)
	}
	core, err := remote.NewHandler(remote.Deps{
		Suppression: suppression.NewStore(q),
		Jobs:        jobReader(t, cp),
		Outcomes:    outcomeWriter(t, cp),
		InboxSends:  inboxSendWriter(t, cp),
	}, remoteTestToken, remoteQuiet())
	if err != nil {
		t.Fatalf("remote.NewHandler: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle(credbroker.PathPrefix, broker)
	mux.Handle(remote.PathPrefix, core)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// jobReader casts a control-plane client to the fleet job-reading surface, the
// same assertion cmd/inroad makes.
func jobReader(t *testing.T, c coreapi.Client) remote.JobReader {
	t.Helper()
	r, ok := c.(remote.JobReader)
	if !ok {
		t.Fatalf("the in-process client (%T) does not satisfy remote.JobReader", c)
	}
	return r
}

// outcomeWriter is the same assertion for the claim and outcome half.
func outcomeWriter(t *testing.T, c coreapi.Client) remote.OutcomeWriter {
	t.Helper()
	o, ok := c.(remote.OutcomeWriter)
	if !ok {
		t.Fatalf("the in-process client (%T) does not satisfy remote.OutcomeWriter", c)
	}
	return o
}

// inboxSendWriter is the same assertion for the manual reply/compose half.
func inboxSendWriter(t *testing.T, c coreapi.Client) remote.InboxSendWriter {
	t.Helper()
	s, ok := c.(remote.InboxSendWriter)
	if !ok {
		t.Fatalf("the in-process client (%T) does not satisfy remote.InboxSendWriter", c)
	}
	return s
}

// remoteJobCore builds the EXECUTION plane's coreapi client the way
// cmd/worker/main.go does for a role=send fleet host: NO POOL, NO KEYRING, a
// credential broker, and the remote coreapi transport installed over both
// source seams.
func remoteJobCore(t *testing.T, baseURL string) JobSource {
	t.Helper()
	opener, err := credbroker.NewHTTPOpener(baseURL, remoteTestToken, true) // httptest speaks http
	if err != nil {
		t.Fatalf("credbroker.NewHTTPOpener: %v", err)
	}
	rc, err := remote.NewClient(baseURL, remoteTestToken, true, opener)
	if err != nil {
		t.Fatalf("remote.NewClient: %v", err)
	}
	c := New(nil, nil, nil, "", mail.GoogleOAuth{}, mail.MicrosoftOAuth{}, nil, nil,
		WithCredentialBroker(opener), WithRemoteSuppression(rc), WithRemoteJobs(rc))
	jobs, ok := c.(JobSource)
	if !ok {
		t.Fatalf("the remote-sourced client (%T) does not satisfy JobSource", c)
	}
	return jobs
}

// THE headline for the send path: a worker with no database builds a complete
// step send job — content, gates, schedule, routing — and holds the DECRYPTED
// mailbox password, which came from the credential broker rather than from this
// job's own response.
func TestARemoteStepSendJobIsCompleteAndCredentialled(t *testing.T) {
	ctx, f := setupPool(t)
	enrollmentID := f.enroll(t, ctx)
	srv := fleetListener(t, f.q, itKeyring(t, f.q), f.core)
	worker := remoteJobCore(t, srv.URL)

	got, err := worker.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob over the wire: %v", err)
	}

	// The control plane's own answer, for the same enrollment, built from the
	// pool it holds. The two must agree on everything.
	want, err := f.core.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
	if err != nil {
		t.Fatalf("GetStepSendJob in process: %v", err)
	}
	// cmp with an explicit time comparer, NOT reflect.DeepEqual, and the reason
	// is worth keeping: DeepEqual compares a time.Time's loc POINTER, and under
	// TZ=UTC time.Local and time.UTC are two different *Location values that
	// both render as "UTC". So a pgx-sourced instant and that same instant
	// after a JSON round trip compared unequal while printing identically —
	// green on any developer machine outside UTC, red in CI, and the %+v dump
	// of two 2000-character structs showed a reader nothing.
	if diff := cmp.Diff(want, got, cmp.Comparer(func(a, b time.Time) bool { return a.Equal(b) })); diff != "" {
		t.Errorf("the wire and the pool disagree about the same job (-pool +wire):\n%s", diff)
	}

	// Spot-checks that a zero-valued or half-built job would fail, stated
	// explicitly so a DeepEqual of two identically-broken jobs cannot pass.
	if got.Skip || got.MailboxID != f.mailboxA.String() {
		t.Errorf("job skipped or resolved the wrong sender: skip=%v mailbox=%s want %s",
			got.Skip, got.MailboxID, f.mailboxA)
	}
	if got.Subject != "Hi" || got.BodyText != "Hello" {
		t.Errorf("content = %q/%q, want the seeded Hi/Hello", got.Subject, got.BodyText)
	}
	if got.SMTPHost != "smtp.acme.test" || got.SMTPPort != 587 {
		t.Errorf("transport = %s:%d, want smtp.acme.test:587", got.SMTPHost, got.SMTPPort)
	}
	if string(got.SMTPPassword) != "smtp-app-password" {
		t.Errorf("SMTPPassword = %q, want the brokered plaintext", got.SMTPPassword)
	}
	if got.EffectiveDailyCap != 100 {
		t.Errorf("EffectiveDailyCap = %d, want the mailbox's stored 100", got.EffectiveDailyCap)
	}
	// The compiled schedule survived: a worker that lost it would schedule the
	// next step outside the campaign's window.
	if len(got.Schedule.Windows) == 0 {
		t.Error("the campaign schedule did not cross the wire")
	}
}

// Every one of the eight reads crosses, each asserted on a value only the
// database could have produced.
func TestEveryJobReadCrossesTheWireWithNoPool(t *testing.T) {
	ctx, f := setupPool(t)
	enrollmentID := f.enroll(t, ctx)
	stepID := seedStep(t, ctx, f.pool, f.ws, f.campaignID, 3,
		"Preview subject", "Preview text", "<p>Preview</p>")
	deliveryID, endpointID := seedWebhookDelivery(t, ctx, f)
	messageID := seedSentMessage(t, ctx, f)
	srv := fleetListener(t, f.q, itKeyring(t, f.q), f.core)
	worker := remoteJobCore(t, srv.URL)

	t.Run("GetStepSendJob", func(t *testing.T) {
		job, err := worker.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
		if err != nil {
			t.Fatalf("over the wire: %v", err)
		}
		if job.CampaignID != f.campaignID.String() {
			t.Errorf("campaign = %s, want %s", job.CampaignID, f.campaignID)
		}
	})

	t.Run("GetInboxPollJob", func(t *testing.T) {
		job, err := worker.GetInboxPollJob(ctx, f.mailboxA.String(), f.ws.String())
		if err != nil {
			t.Fatalf("over the wire: %v", err)
		}
		if job.Provider != "smtp" || job.Host != "imap.acme.test" || job.Port != 993 {
			t.Errorf("poll transport = %s %s:%d, want smtp imap.acme.test:993", job.Provider, job.Host, job.Port)
		}
		// The credential lands on Password for a poll, and it is the real one.
		if string(job.Password) != "smtp-app-password" {
			t.Errorf("Password = %q, want the brokered plaintext", job.Password)
		}
		if job.Email == "" {
			t.Error("the polled mailbox's own address did not cross; identity extraction needs it")
		}
	})

	t.Run("GetWarmupSendJob", func(t *testing.T) {
		// mailboxA is not a warmup participant, so the control plane's real
		// answer is Skip. That IS the assertion here: a nil-pool client cannot
		// produce it locally, and a broken transport would error instead.
		job, err := worker.GetWarmupSendJob(ctx, f.mailboxA.String(), f.ws.String())
		if err != nil {
			t.Fatalf("over the wire: %v", err)
		}
		if !job.Skip {
			t.Errorf("job = %+v, want Skip for a non-participant mailbox", job)
		}
	})

	t.Run("GetWarmupEngageJob", func(t *testing.T) {
		// No such receipt: the control plane answers pgx.ErrNoRows and the wire
		// reproduces the sentinel rather than flattening it to a generic error.
		_, err := worker.GetWarmupEngageJob(ctx, uuid.NewString(), f.ws.String())
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("err = %v, want pgx.ErrNoRows for a vanished receipt", err)
		}
	})

	t.Run("GetWebhookDeliveryJob", func(t *testing.T) {
		job, err := worker.GetWebhookDeliveryJob(ctx, deliveryID.String(), f.ws.String())
		if err != nil {
			t.Fatalf("over the wire: %v", err)
		}
		if job.URL != "https://hooks.example.test/inroad" || job.EventType != "reply.received" {
			t.Errorf("delivery = %s %s, want the seeded endpoint and event", job.URL, job.EventType)
		}
		if job.EndpointID != endpointID.String() {
			t.Errorf("endpoint = %s, want %s", job.EndpointID, endpointID)
		}
		// Byte-identical to what the CONTROL plane reads, which is what the
		// signature is computed over. Not compared against the literal that was
		// inserted: the column is jsonb, so Postgres returns its own normalised
		// spacing — in process too. What this asserts is that the wire did not
		// change it further.
		local, err := jobReader(t, f.core).GetWebhookDeliveryJob(ctx, deliveryID.String(), f.ws.String())
		if err != nil {
			t.Fatalf("in process: %v", err)
		}
		if string(job.Payload) != string(local.Payload) {
			t.Errorf("payload over the wire = %s, in process = %s", job.Payload, local.Payload)
		}
		if len(job.Payload) == 0 {
			t.Error("the delivery payload did not cross the wire at all")
		}
		// The signing secret is the endpoint's real one, opened by the broker.
		if string(job.Secret) != "webhook-signing-secret" {
			t.Errorf("Secret = %q, want the brokered plaintext", job.Secret)
		}
	})

	t.Run("GetTestSendContent", func(t *testing.T) {
		content, err := worker.GetTestSendContent(ctx, f.ws.String(), f.campaignID.String(), stepID.String())
		if err != nil {
			t.Fatalf("over the wire: %v", err)
		}
		if content.Subject != "Preview subject" || content.BodyHTML != "<p>Preview</p>" {
			t.Errorf("content = %+v, want the seeded step", content)
		}
		// The empty-list fallback is a CONTROL-plane policy, and it travelled.
		if content.FirstName != testSendFallbackFirstName || content.Company != testSendFallbackCompany {
			t.Errorf("preview vars = %s/%s, want the synthetic fallback", content.FirstName, content.Company)
		}
	})

	t.Run("ResolveSenderTransport", func(t *testing.T) {
		tr, err := worker.ResolveSenderTransport(ctx, f.ws.String(), f.mailboxA.String())
		if err != nil {
			t.Fatalf("over the wire: %v", err)
		}
		if tr.Provider != "smtp" || tr.SMTPHost != "smtp.acme.test" || tr.SMTPPort != 587 {
			t.Errorf("transport = %s %s:%d, want smtp smtp.acme.test:587", tr.Provider, tr.SMTPHost, tr.SMTPPort)
		}
		if string(tr.SMTPPassword) != "smtp-app-password" {
			t.Errorf("SMTPPassword = %q, want the brokered plaintext", tr.SMTPPassword)
		}
		if !strings.HasSuffix(tr.FromEmail, "@acme.test") {
			t.Errorf("FromEmail = %q, want the mailbox's stored address", tr.FromEmail)
		}
	})

	t.Run("FindSendByMessageID", func(t *testing.T) {
		ref, err := worker.FindSendByMessageID(ctx, f.ws.String(), messageID)
		if err != nil {
			t.Fatalf("over the wire: %v", err)
		}
		if ref.MessageID != messageID || ref.CampaignID != f.campaignID.String() {
			t.Errorf("send ref = %+v, want the seeded send", ref)
		}
	})
}

// The warmup pair, against a REAL warmup fixture rather than the Skip/not-found
// answers above: a participant with a partner produces a full send job, and a
// recorded receipt produces a full engage job — each carrying the right
// mailbox's decrypted credential.
func TestTheWarmupJobReadsCrossTheWireWithNoPool(t *testing.T) {
	ctx, f := setupWarmup(t)
	srv := fleetListener(t, f.q, itKeyring(t, f.q), f.core)
	worker := remoteJobCore(t, srv.URL)

	send, err := worker.GetWarmupSendJob(ctx, f.a.String(), f.ws1.String())
	if err != nil {
		t.Fatalf("GetWarmupSendJob over the wire: %v", err)
	}
	if send.Skip {
		t.Fatalf("job = %+v, want a real warmup send for a participant with a partner", send)
	}
	if send.FromMailbox != f.a.String() || send.ToMailbox != f.b.String() {
		t.Errorf("pair = %s -> %s, want %s -> %s", send.FromMailbox, send.ToMailbox, f.a, f.b)
	}
	if send.Subject == "" || send.BodyText == "" {
		t.Error("the warmup content library's copy did not cross the wire")
	}
	// The signed receipt token DOES cross — it is a header the message carries,
	// minted from a secret the worker does not hold and could not mint one with.
	if send.Token == "" {
		t.Error("the X-Inroad-Warmup receipt token did not cross the wire")
	}
	if string(send.SMTPPassword) != "smtp-app-password" {
		t.Errorf("SMTPPassword = %q, want the sender's brokered plaintext", send.SMTPPassword)
	}

	// Now the engage half, over a receipt the control plane recorded.
	sendID, recipient := makeWarmupSend(t, ctx, f)
	plan, err := f.core.RecordWarmupReceipt(ctx, coreapi.WarmupReceiptInput{
		WorkspaceID: f.ws1.String(), WarmupSendID: sendID, RecipientMailbox: recipient,
		Placement: placementSpam, SourceFolder: "Junk", MessageID: "<orig@acme.test>",
	})
	if err != nil {
		t.Fatalf("RecordWarmupReceipt: %v", err)
	}

	engage, err := worker.GetWarmupEngageJob(ctx, plan.ReceiptID, f.ws1.String())
	if err != nil {
		t.Fatalf("GetWarmupEngageJob over the wire: %v", err)
	}
	if engage.RecipientMailbox != recipient {
		t.Errorf("RecipientMailbox = %s, want the receipt's recipient %s", engage.RecipientMailbox, recipient)
	}
	if engage.SourceFolder != "Junk" || engage.MessageID != "<orig@acme.test>" {
		t.Errorf("locator = %q/%q, want Junk/<orig@acme.test>", engage.SourceFolder, engage.MessageID)
	}
	if engage.IMAPHost != "imap.acme.test" || engage.IMAPPort != 993 {
		t.Errorf("IMAP transport = %s:%d, want imap.acme.test:993", engage.IMAPHost, engage.IMAPPort)
	}
	if !engage.DoRescue || !engage.DoMarkRead {
		t.Errorf("engage job = %+v, want rescue+markread on a spam placement", engage)
	}
	if string(engage.SMTPPassword) != "smtp-app-password" {
		t.Errorf("SMTPPassword = %q, want the recipient's brokered plaintext", engage.SMTPPassword)
	}
	// When the plan replies, the reply's credential must be the SAME SLICE: the
	// engage worker's single deferred zeroize wipes only the outer copy.
	if engage.DoReply {
		for i := range engage.SMTPPassword {
			engage.SMTPPassword[i] = 0
		}
		if strings.Contains(string(engage.ReplySend.SMTPPassword), "smtp-app-password") {
			t.Error("the reply credential survived the worker's zeroize; the wire un-aliased it")
		}
	}
}

// FAIL CLOSED, proven where it matters: the data IS in Postgres and the worker
// cannot reach the control plane. Every read must return an error AND a zero
// value. A job with an empty body or an empty gate flag is worse than no job —
// it would send the wrong mail rather than none.
func TestEveryJobReadFailsClosedWhenTheControlPlaneDies(t *testing.T) {
	ctx, f := setupPool(t)
	enrollmentID := f.enroll(t, ctx)
	stepID := seedStep(t, ctx, f.pool, f.ws, f.campaignID, 3, "Preview", "Preview", "")
	deliveryID, _ := seedWebhookDelivery(t, ctx, f)
	messageID := seedSentMessage(t, ctx, f)
	srv := fleetListener(t, f.q, itKeyring(t, f.q), f.core)
	worker := remoteJobCore(t, srv.URL)

	reads := []struct {
		name string
		call func() (any, error)
	}{
		{"GetStepSendJob", func() (any, error) {
			return worker.GetStepSendJob(ctx, enrollmentID.String(), f.ws.String())
		}},
		{"GetInboxPollJob", func() (any, error) {
			return worker.GetInboxPollJob(ctx, f.mailboxA.String(), f.ws.String())
		}},
		{"GetWarmupSendJob", func() (any, error) {
			return worker.GetWarmupSendJob(ctx, f.mailboxA.String(), f.ws.String())
		}},
		{"GetWebhookDeliveryJob", func() (any, error) {
			return worker.GetWebhookDeliveryJob(ctx, deliveryID.String(), f.ws.String())
		}},
		{"GetTestSendContent", func() (any, error) {
			return worker.GetTestSendContent(ctx, f.ws.String(), f.campaignID.String(), stepID.String())
		}},
		{"ResolveSenderTransport", func() (any, error) {
			return worker.ResolveSenderTransport(ctx, f.ws.String(), f.mailboxA.String())
		}},
		{"FindSendByMessageID", func() (any, error) {
			return worker.FindSendByMessageID(ctx, f.ws.String(), messageID)
		}},
	}

	// Every one of them works while the control plane is up. Without this half
	// the test below would pass against a client that never worked at all.
	for _, r := range reads {
		if _, err := r.call(); err != nil {
			t.Fatalf("%s failed while the control plane was UP: %v", r.name, err)
		}
	}

	srv.Close()

	for _, r := range reads {
		t.Run(r.name, func(t *testing.T) {
			got, err := r.call()
			if err == nil {
				t.Fatalf("%s answered with the control plane down; a worker must refuse, not guess", r.name)
			}
			if !reflect.ValueOf(got).IsZero() {
				t.Errorf("%s returned %+v alongside its error, want the zero value", r.name, got)
			}
		})
	}
}

// Workspace-pinned across the wire: the control plane applies the same
// workspace_id filter the in-process path applies, so another tenant's ids
// resolve to nothing rather than to that tenant's data (docs/security.md
// invariant 4).
func TestJobReadsArePinnedToTheRequestedWorkspace(t *testing.T) {
	ctx, f := setupPool(t)
	enrollmentID := f.enroll(t, ctx)
	stepID := seedStep(t, ctx, f.pool, f.ws, f.campaignID, 3, "Preview", "Preview", "")
	deliveryID, _ := seedWebhookDelivery(t, ctx, f)
	messageID := seedSentMessage(t, ctx, f)
	srv := fleetListener(t, f.q, itKeyring(t, f.q), f.core)
	worker := remoteJobCore(t, srv.URL)

	foreign := f.foreignWS.String()
	for _, tc := range []struct {
		name string
		call func() (any, error)
	}{
		{"GetStepSendJob", func() (any, error) {
			return worker.GetStepSendJob(ctx, enrollmentID.String(), foreign)
		}},
		{"GetInboxPollJob", func() (any, error) {
			return worker.GetInboxPollJob(ctx, f.mailboxA.String(), foreign)
		}},
		{"GetWebhookDeliveryJob", func() (any, error) {
			return worker.GetWebhookDeliveryJob(ctx, deliveryID.String(), foreign)
		}},
		{"GetTestSendContent", func() (any, error) {
			return worker.GetTestSendContent(ctx, foreign, f.campaignID.String(), stepID.String())
		}},
		{"ResolveSenderTransport", func() (any, error) {
			return worker.ResolveSenderTransport(ctx, foreign, f.mailboxA.String())
		}},
		{"FindSendByMessageID", func() (any, error) {
			return worker.FindSendByMessageID(ctx, foreign, messageID)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.call()
			if err == nil {
				t.Fatalf("%s answered %+v for a foreign workspace", tc.name, got)
			}
			if !reflect.ValueOf(got).IsZero() {
				t.Errorf("%s leaked %+v alongside its refusal", tc.name, got)
			}
		})
	}
}

// A no-match on the inbound-reply lookup is the ORDINARY answer for nearly
// every message the poller sees, and it must arrive as coreapi.ErrNoMatch. A
// generic error would abort the poll before SetInboxCursor and stop the mailbox
// processing inbound mail at all.
func TestAnUnmatchedMessageIDCrossesAsErrNoMatch(t *testing.T) {
	ctx, f := setupPool(t)
	srv := fleetListener(t, f.q, itKeyring(t, f.q), f.core)
	worker := remoteJobCore(t, srv.URL)

	ref, err := worker.FindSendByMessageID(ctx, f.ws.String(), "<never-sent-"+uuid.NewString()+"@elsewhere.test>")
	if !errors.Is(err, coreapi.ErrNoMatch) {
		t.Fatalf("err = %v, want coreapi.ErrNoMatch", err)
	}
	if ref != (coreapi.SendRef{}) {
		t.Errorf("a no-match returned %+v, want the zero value", ref)
	}
}

// seedWebhookDelivery inserts one active endpoint with a real sealed signing
// secret plus one pending delivery against it.
func seedWebhookDelivery(t *testing.T, ctx context.Context, f poolFixture) (deliveryID, endpointID uuid.UUID) {
	t.Helper()
	sealer, err := itKeyring(t, f.q).SealerFor(ctx, f.ws)
	if err != nil {
		t.Fatalf("sealer for workspace: %v", err)
	}
	ct, err := sealer.Seal([]byte("webhook-signing-secret"))
	if err != nil {
		t.Fatalf("seal webhook secret: %v", err)
	}
	ep, err := f.q.CreateWebhookEndpoint(ctx, gen.CreateWebhookEndpointParams{
		WorkspaceID: f.ws, Url: "https://hooks.example.test/inroad", Description: "IT",
		SecretCiphertext: []byte(ct), EventTypes: []string{"reply.received"}, Active: true,
	})
	if err != nil {
		t.Fatalf("webhook endpoint: %v", err)
	}
	d, err := f.q.CreateWebhookDelivery(ctx, gen.CreateWebhookDeliveryParams{
		ID: uuid.New(), EndpointID: ep.ID, WorkspaceID: f.ws,
		EventType: "reply.received", Payload: []byte(`{"hello":"world"}`),
	})
	if err != nil {
		t.Fatalf("webhook delivery: %v", err)
	}
	return d.ID, ep.ID
}

// seedSentMessage inserts one delivered send carrying a Message-ID, the row
// FindSendByMessageID matches an inbound reply back to.
func seedSentMessage(t *testing.T, ctx context.Context, f poolFixture) string {
	t.Helper()
	c, err := f.q.UpsertContact(ctx, gen.UpsertContactParams{
		WorkspaceID: f.ws, Email: "replier-" + uuid.NewString() + "@x.test", FirstName: "R",
	})
	if err != nil {
		t.Fatalf("contact: %v", err)
	}
	messageID := "<sent-" + uuid.NewString() + "@acme.test>"
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO sends (workspace_id, campaign_id, contact_id, mailbox_id, to_email, status, message_id, sent_at)
		 VALUES ($1,$2,$3,$4,$5,'sent',$6, now())`,
		f.ws, f.campaignID, c.ID, f.mailboxA, "replier@x.test", messageID); err != nil {
		t.Fatalf("send row: %v", err)
	}
	return messageID
}
