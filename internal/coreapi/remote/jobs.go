package remote

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/credbroker"
)

// The per-message job READS, slice 2 of the plane split.
//
// # Two calls, one credential channel
//
// Every method here answers in at most two round trips: one to this transport
// for the job, and — only when the job needs a credential — one to the
// CREDENTIAL BROKER for that credential. The job responses carry no secret at
// all (the fields are `json:"-"` on the coreapi types; see that package's doc).
//
// The alternative was to inline the credential in the job response, which is
// one round trip and mirrors the in-process shape exactly. It was rejected:
//
//   - credbroker (#207) exists FOR brokering credentials to a keyless worker
//     and has a reviewed ids-in/values-out contract. A second channel handing
//     out plaintext secrets would be a second set of audit properties to keep
//     in step, and two such channels drift.
//   - It is already there. A worker may read coreapi remotely only in
//     role=send (cmd/worker's resolveCoreAPIMode), and role=send may not start
//     without a broker (resolveCredentialMode's ErrSendRoleNeedsBroker). So
//     brokering costs no new machinery on the only host that can do this.
//   - It keeps secrets out of the big body. A step-send response is kilobytes
//     of subject and HTML that is buffered, decoded and possibly retained; a
//     broker response is a few hundred bytes whose entire purpose is the
//     secret. Fewer copies of a plaintext credential in fewer places.
//   - Omission becomes structural. With no secret field on the wire type, no
//     later change to a job can silently start emitting one — the standard
//     docs/security.md invariant 2 sets for API DTOs.
//
// What it costs: one extra small request per credentialed job build, on a
// pooled connection to a host on the operator's own network, inside a two
// minute send budget. The control plane's own job build still opens the
// credential and this transport then does not carry it — that open is an
// AES-GCM decrypt of a cached DEK in the process that holds the key anyway.
//
// # What brokering does NOT restore
//
// Nothing here makes the worker's `zeroize` reach every copy of a secret, and
// nothing ever did on a fleet host: since #207 the plaintext already arrives in
// a credential-broker HTTP response, through its read buffer and its base64
// decode, neither of which a later wipe of the resulting []byte touches. Moving
// the job read remote does not change that, and the reason it does not is
// precisely that the secret does not travel on this wire. See credbroker's
// package doc for what brokering does and does not buy.

// GetStepSendJob loads the enrollment's next due step. The control plane runs
// the whole build — including the suppression gate and the cap/pause/limit
// decisions — and the worker receives a decided job.
//
// FAIL CLOSED. Any failure returns the zero job alongside the error, and the
// worker treats a non-nil error as "do not send" (internal/worker/sequence).
// A partially-built job is never returned: the credential is filled in only
// after a successful fetch, and a broker failure discards the job rather than
// yielding one with an empty password that would dial without authenticating.
func (c *Client) GetStepSendJob(ctx context.Context, enrollmentID, workspaceID string) (coreapi.StepSendJob, error) {
	if err := parseIDs(workspaceID, enrollmentID); err != nil {
		return coreapi.StepSendJob{}, err
	}
	// MailboxID is set exactly when a sender was resolved, which is exactly when
	// the in-process build opened a credential: every skip, stop and deferral
	// branch returns before a sender exists. So the credential subject is read
	// off the job rather than re-derived from the gate flags.
	out, at, pw, err := credentialledJob(ctx, c, PathStepSendJob, workspaceID,
		enrollmentRequest{WorkspaceID: workspaceID, EnrollmentID: enrollmentID},
		func(r stepSendJobResponse) (string, string) { return r.Job.MailboxID, r.Job.Provider })
	if err != nil {
		return coreapi.StepSendJob{}, err
	}
	job := out.Job
	job.AccessToken, job.SMTPPassword = at, pw
	return job, nil
}

// GetInboxPollJob loads one mailbox's poll transport and stored cursor. The
// mailbox always has a credential, so this always brokers one.
func (c *Client) GetInboxPollJob(ctx context.Context, mailboxID, workspaceID string) (coreapi.InboxPollJob, error) {
	if err := parseIDs(workspaceID, mailboxID); err != nil {
		return coreapi.InboxPollJob{}, err
	}
	out, at, pw, err := credentialledJob(ctx, c, PathInboxPollJob, workspaceID,
		mailboxRequest{WorkspaceID: workspaceID, MailboxID: mailboxID},
		func(r inboxPollJobResponse) (string, string) { return mailboxID, r.Job.Provider })
	if err != nil {
		return coreapi.InboxPollJob{}, err
	}
	job := out.Job
	// The IMAP password lands on Password, not SMTPPassword: a mailbox has one
	// stored secret and this job type names it for the protocol it is used on.
	job.AccessToken, job.Password = at, pw
	return job, nil
}

// GetWarmupSendJob picks the next warmup action for a warming mailbox.
func (c *Client) GetWarmupSendJob(ctx context.Context, mailboxID, workspaceID string) (coreapi.WarmupSendJob, error) {
	if err := parseIDs(workspaceID, mailboxID); err != nil {
		return coreapi.WarmupSendJob{}, err
	}
	// Every Skip branch returns before a partner is selected, so FromMailbox is
	// empty on exactly the jobs that carry no transport — the same reading as
	// StepSendJob's MailboxID, and for the same reason: the credential subject
	// and the presence of a credential are one fact, not two.
	out, at, pw, err := credentialledJob(ctx, c, PathWarmupSendJob, workspaceID,
		mailboxRequest{WorkspaceID: workspaceID, MailboxID: mailboxID},
		func(r warmupSendJobResponse) (string, string) { return r.Job.FromMailbox, r.Job.Provider })
	if err != nil {
		return coreapi.WarmupSendJob{}, err
	}
	job := out.Job
	job.AccessToken, job.SMTPPassword = at, pw
	return job, nil
}

// GetWarmupEngageJob loads what the engage worker needs to act on one received
// warmup message, over the RECIPIENT's own transport.
func (c *Client) GetWarmupEngageJob(ctx context.Context, receiptID, workspaceID string) (coreapi.WarmupEngageJob, error) {
	if err := parseIDs(workspaceID, receiptID); err != nil {
		return coreapi.WarmupEngageJob{}, err
	}
	out, at, pw, err := credentialledJob(ctx, c, PathWarmupEngageJob, workspaceID,
		receiptRequest{WorkspaceID: workspaceID, ReceiptID: receiptID},
		func(r warmupEngageJobResponse) (string, string) { return r.Job.RecipientMailbox, r.Job.Provider })
	if err != nil {
		return coreapi.WarmupEngageJob{}, err
	}
	job := out.Job
	// This is the one route where an empty credential subject cannot mean "no
	// credential needed". Every engage job has a transport — the in-process
	// build opens one before it branches on anything, because even a passive
	// mark-read dials IMAP — so an empty RecipientMailbox means the control
	// plane did not say whose mailbox this is, and credentialledJob's skip
	// branch would hand back a job that dials unauthenticated. Refuse instead.
	if job.RecipientMailbox == "" {
		return coreapi.WarmupEngageJob{}, fmt.Errorf(
			"coreapi remote: %s: the engage job names no recipient mailbox, so its credential cannot be opened", PathWarmupEngageJob)
	}
	job.AccessToken, job.SMTPPassword = at, pw
	// The SAME slices on the nested reply, not copies. internal/worker/warmup's
	// EngageHandler defers a zeroize of job.AccessToken and job.SMTPPassword
	// only — it never touches ReplySend's — so an unaliased inner copy would
	// survive the wipe with the plaintext intact. The in-process build aliases
	// them for exactly this reason; the wire must not quietly un-alias them.
	if job.DoReply {
		job.ReplySend.AccessToken, job.ReplySend.SMTPPassword = at, pw
	}
	return job, nil
}

// GetWebhookDeliveryJob loads one delivery plus its endpoint's signing secret.
//
// A delivery deleted after its task was queued comes back as pgx.ErrNoRows, the
// same sentinel the in-process path returns, because the worker drops the task
// on it rather than retrying forever. See notFound.
func (c *Client) GetWebhookDeliveryJob(ctx context.Context, deliveryID, workspaceID string) (coreapi.WebhookDeliveryJob, error) {
	if err := parseIDs(workspaceID, deliveryID); err != nil {
		return coreapi.WebhookDeliveryJob{}, err
	}
	var out webhookDeliveryJobResponse
	if err := c.post(ctx, c.jobs, PathWebhookDeliveryJob, deliveryRequest{
		WorkspaceID: workspaceID, DeliveryID: deliveryID,
	}, &out); err != nil {
		return coreapi.WebhookDeliveryJob{}, err
	}
	job := out.Job
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return coreapi.WebhookDeliveryJob{}, fmt.Errorf("coreapi remote: workspace id: %w", err)
	}
	// The endpoint id comes off the JOB rather than the request, because the
	// delivery names its endpoint and the caller does not know it. It is an id
	// the control plane just produced, so a malformed one is a fault worth
	// failing on rather than working around.
	endpoint, err := uuid.Parse(job.EndpointID)
	if err != nil {
		return coreapi.WebhookDeliveryJob{}, fmt.Errorf("coreapi remote: endpoint id: %w", err)
	}
	// nil sealed: the broker re-reads the row itself, workspace-pinned. A
	// caller that could name the ciphertext would make it a decryption oracle.
	secret, err := c.creds.OpenWebhookEndpointSecret(ctx, ws, endpoint, nil)
	if err != nil {
		return coreapi.WebhookDeliveryJob{}, err
	}
	job.Secret = secret
	return job, nil
}

// GetTestSendContent loads one test-send's raw step content and preview vars.
// No credential: this route carries content only, and the test-send worker
// resolves its transport separately through ResolveSenderTransport.
func (c *Client) GetTestSendContent(ctx context.Context, workspaceID, campaignID, stepID string) (coreapi.TestSendContent, error) {
	if err := parseIDs(workspaceID, campaignID, stepID); err != nil {
		return coreapi.TestSendContent{}, err
	}
	var out testSendContentResponse
	if err := c.post(ctx, c.jobs, PathTestSendContent, testSendContentRequest{
		WorkspaceID: workspaceID, CampaignID: campaignID, StepID: stepID,
	}, &out); err != nil {
		return coreapi.TestSendContent{}, err
	}
	return out.Content, nil
}

// ResolveSenderTransport resolves one mailbox's send identity and connection
// settings, and brokers its credential.
func (c *Client) ResolveSenderTransport(ctx context.Context, workspaceID, mailboxID string) (coreapi.SenderTransport, error) {
	if err := parseIDs(workspaceID, mailboxID); err != nil {
		return coreapi.SenderTransport{}, err
	}
	var out senderTransportResponse
	if err := c.post(ctx, c.jobs, PathSenderTransport, mailboxRequest{
		WorkspaceID: workspaceID, MailboxID: mailboxID,
	}, &out); err != nil {
		return coreapi.SenderTransport{}, err
	}
	t := out.Transport
	at, pw, err := c.openMailbox(ctx, workspaceID, mailboxID, t.Provider)
	if err != nil {
		return coreapi.SenderTransport{}, err
	}
	t.AccessToken, t.SMTPPassword = at, pw
	return t, nil
}

// FindSendByMessageID matches an inbound reply or bounce back to the send that
// caused it.
//
// No match is coreapi.ErrNoMatch, not a generic failure, and that distinction
// is load-bearing: it is the ORDINARY answer for nearly every message the
// poller sees, and internal/worker/inbox branches on it in three places. A
// generic error there would abort the poll before SetInboxCursor and stop the
// mailbox processing inbound mail at all.
//
// The workspace id is parsed here but the Message-ID is not validated: it is
// whatever the inbound message carried, and the local query would simply match
// nothing. Validating on one transport and not the other is the one difference
// a transport must never introduce.
func (c *Client) FindSendByMessageID(ctx context.Context, workspaceID, messageID string) (coreapi.SendRef, error) {
	if err := parseIDs(workspaceID); err != nil {
		return coreapi.SendRef{}, err
	}
	var out sendRefResponse
	if err := c.post(ctx, c.jobs, PathSendByMessageID, messageIDRequest{
		WorkspaceID: workspaceID, MessageID: messageID,
	}, &out); err != nil {
		return coreapi.SendRef{}, err
	}
	return out.Send, nil
}

// credentialledJob fetches one job and brokers the credential it needs.
//
// It exists so the parts that are the SAME on every credentialled route — post,
// decode, the "does this job need a credential at all" branch, and the provider
// check — live in one place. "The provider check runs on every route" is then
// structural rather than four copies that have to stay in step, which is the
// property worth having in a credential path.
//
// subject is the one thing that genuinely differs per route: it names the
// mailbox the credential belongs to, and where that name came from. Some routes
// read it off the CALLER's argument (an inbox poll is for the mailbox the
// caller named); others read it off the JOB, because the control plane resolved
// which mailbox a send goes from. An empty mailbox id means "this job carries no
// transport" — a skip, a stop, a deferral — and nothing is brokered for it.
//
// It RETURNS the secrets rather than installing them, because where they go
// differs too: an inbox poll's lands on Password, a send job's on SMTPPassword,
// an engage job's in two aliased places at once. A callback for that would hide
// the one line per route most worth reading.
//
// FAIL CLOSED: any error returns the ZERO response, never a partially built job
// — a job whose credential did not arrive would dial without authenticating.
func credentialledJob[Req, Resp any](
	ctx context.Context, c *Client, path, workspaceID string, req Req,
	subject func(Resp) (mailboxID, provider string),
) (resp Resp, accessToken, password []byte, err error) {
	var zero Resp
	if err := c.post(ctx, c.jobs, path, req, &resp); err != nil {
		return zero, nil, nil, err
	}
	mailboxID, provider := subject(resp)
	if mailboxID == "" {
		return resp, nil, nil, nil
	}
	at, pw, err := c.openMailbox(ctx, workspaceID, mailboxID, provider)
	if err != nil {
		return zero, nil, nil, err
	}
	return resp, at, pw, nil
}

// openMailbox brokers one mailbox's decrypted credential and checks the answer
// is about the mailbox we asked about.
//
// The provider check mirrors inprocess.openMailboxSecret's, deliberately and
// not by accident: the broker re-reads the row itself, so a provider it reports
// that disagrees with the job the control plane just built means the two are
// not looking at the same mailbox (or it changed under us mid-job). Dialing
// SMTP with an access token, or Gmail with a password, is not a failure mode
// worth discovering at the provider.
func (c *Client) openMailbox(ctx context.Context, workspaceID, mailboxID, provider string) (accessToken, password []byte, err error) {
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return nil, nil, fmt.Errorf("coreapi remote: workspace id: %w", err)
	}
	mailbox, err := uuid.Parse(mailboxID)
	if err != nil {
		return nil, nil, fmt.Errorf("coreapi remote: mailbox id: %w", err)
	}
	// Provider and Sealed are left zero on purpose: the remote opener ignores
	// them anyway (a worker must never name the ciphertext that gets opened),
	// and passing a cached value would only invite a local opener to trust it.
	sec, err := c.creds.OpenMailbox(ctx, credbroker.MailboxRef{WorkspaceID: ws, MailboxID: mailbox})
	if err != nil {
		return nil, nil, err
	}
	if sec.Provider != provider {
		return nil, nil, fmt.Errorf("coreapi remote: credential opener answered for provider %q, mailbox %s is %q",
			sec.Provider, mailbox, provider)
	}
	return sec.AccessToken, sec.SMTPPassword, nil
}

// parseIDs rejects a malformed uuid before it costs a round trip, and before it
// reaches the control plane at all. The handler parses every one of them again
// — a server may not trust its client — so this is about parity and waste, not
// about safety: the in-process implementations parse their ids first too, so a
// malformed id must be an error on both transports rather than an error on one
// and a query on the other.
func parseIDs(ids ...string) error {
	for _, id := range ids {
		if _, err := uuid.Parse(id); err != nil {
			return fmt.Errorf("coreapi remote: %w", err)
		}
	}
	return nil
}
