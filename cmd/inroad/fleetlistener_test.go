package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/coreapi/remote"
	"github.com/inroad/inroad/internal/platform/credbroker"
)

const fleetTestToken = "0123456789abcdef0123456789abcdef" // credbroker.MinTokenLen

type fakeOpener struct{ calls int }

func (f *fakeOpener) OpenMailbox(context.Context, credbroker.MailboxRef) (credbroker.MailboxSecret, error) {
	f.calls++
	return credbroker.MailboxSecret{Provider: "smtp", SMTPPassword: []byte("hunter2")}, nil
}

func (f *fakeOpener) OpenWebhookEndpointSecret(context.Context, uuid.UUID, uuid.UUID, []byte) ([]byte, error) {
	f.calls++
	return []byte("secret"), nil
}

type fakeSuppressionReader struct{ calls int }

func (f *fakeSuppressionReader) IsSuppressed(context.Context, uuid.UUID, string) (bool, error) {
	f.calls++
	return true, nil
}

// fakeJobReader is the control plane's job-build half. It answers zero values:
// this file is about which routes are mounted, behind which token, on which
// listener — what the builds return is internal/coreapi/remote's subject.
type fakeJobReader struct{ calls int }

func (f *fakeJobReader) GetStepSendJob(context.Context, string, string) (coreapi.StepSendJob, error) {
	f.calls++
	return coreapi.StepSendJob{}, nil
}

func (f *fakeJobReader) GetInboxPollJob(context.Context, string, string) (coreapi.InboxPollJob, error) {
	f.calls++
	return coreapi.InboxPollJob{}, nil
}

func (f *fakeJobReader) GetWarmupSendJob(context.Context, string, string) (coreapi.WarmupSendJob, error) {
	f.calls++
	return coreapi.WarmupSendJob{}, nil
}

func (f *fakeJobReader) GetWarmupEngageJob(context.Context, string, string) (coreapi.WarmupEngageJob, error) {
	f.calls++
	return coreapi.WarmupEngageJob{}, nil
}

func (f *fakeJobReader) GetWebhookDeliveryJob(context.Context, string, string) (coreapi.WebhookDeliveryJob, error) {
	f.calls++
	return coreapi.WebhookDeliveryJob{}, nil
}

func (f *fakeJobReader) GetTestSendContent(context.Context, string, string, string) (coreapi.TestSendContent, error) {
	f.calls++
	return coreapi.TestSendContent{}, nil
}

func (f *fakeJobReader) ResolveSenderTransport(context.Context, string, string) (coreapi.SenderTransport, error) {
	f.calls++
	return coreapi.SenderTransport{}, nil
}

func (f *fakeJobReader) FindSendByMessageID(context.Context, string, string) (coreapi.SendRef, error) {
	f.calls++
	return coreapi.SendRef{}, nil
}

func (f *fakeJobReader) NextWarmupDue(context.Context, string, string) (time.Time, bool, error) {
	f.calls++
	return time.Time{}, false, nil
}

// fakeInboundWriter is the control plane's inbound-mail half (slice 4). Like
// the readers above it answers zero values: this file is about which routes are
// mounted, behind which token, on which listener.
type fakeInboundWriter struct{ calls int }

func (f *fakeInboundWriter) SetInboxCursor(context.Context, string, string, uint32, uint32) error {
	f.calls++
	return nil
}

func (f *fakeInboundWriter) SetInboxCursorString(context.Context, string, string, string) error {
	f.calls++
	return nil
}

func (f *fakeInboundWriter) StoreInboundMessage(context.Context, coreapi.InboxMessageInput) error {
	f.calls++
	return nil
}

func (f *fakeInboundWriter) CaptureCRMReply(context.Context, coreapi.CRMReplyInput) error {
	f.calls++
	return nil
}

func (f *fakeInboundWriter) IngestComplaint(context.Context, coreapi.ComplaintInput) error {
	f.calls++
	return nil
}

func (f *fakeInboundWriter) ResolveReplyLabel(context.Context, string, string) (coreapi.ReplyLabel, bool, error) {
	f.calls++
	return coreapi.ReplyLabel{}, false, nil
}

func (f *fakeInboundWriter) RecordWarmupReceipt(context.Context, coreapi.WarmupReceiptInput) (coreapi.WarmupEngagePlan, error) {
	f.calls++
	return coreapi.WarmupEngagePlan{}, nil
}

func (f *fakeInboundWriter) FindWarmupSendByMessageID(context.Context, string, string, string) (coreapi.WarmupSendRef, bool, error) {
	f.calls++
	return coreapi.WarmupSendRef{}, false, nil
}

func (f *fakeInboundWriter) RecordWarmupTokenFailure(context.Context, string, string, string, string) error {
	f.calls++
	return nil
}

func (f *fakeInboundWriter) RecordWarmupHardBounce(context.Context, string, string, string) (bool, error) {
	f.calls++
	return false, nil
}

// fakeFleetWriter is the control plane's worker-infrastructure half (slice 4).
type fakeFleetWriter struct{ calls int }

func (f *fakeFleetWriter) UpsertWorkerHeartbeat(context.Context, string, string, string) error {
	f.calls++
	return nil
}

func (f *fakeFleetWriter) RecordWorkerProviderSignals(context.Context, coreapi.WorkerProviderSignals) error {
	f.calls++
	return nil
}

func (f *fakeFleetWriter) AssignMailboxWorker(context.Context, string, string) (string, error) {
	f.calls++
	return "", nil
}

func (f *fakeFleetWriter) RecordDeadLetter(context.Context, coreapi.DeadLetterInput) error {
	f.calls++
	return nil
}

// fakeOutcomeWriter is the control plane's claim/outcome half. Like
// fakeJobReader it answers zero values: this file is about which routes are
// mounted, behind which token, on which listener — what the writes DO is
// internal/coreapi/remote's and internal/coreapi/inprocess's subject.
type fakeOutcomeWriter struct{ calls int }

func (f *fakeOutcomeWriter) ClaimStepSend(context.Context, coreapi.StepSendJob) (coreapi.ClaimOutcome, error) {
	f.calls++
	return coreapi.ClaimSkip, nil
}

func (f *fakeOutcomeWriter) MarkStepDelivered(context.Context, coreapi.StepSendJob, string) error {
	f.calls++
	return nil
}

func (f *fakeOutcomeWriter) AdvanceStepCursor(context.Context, coreapi.StepSendJob) (coreapi.Advance, error) {
	f.calls++
	return coreapi.Advance{}, nil
}

func (f *fakeOutcomeWriter) ReleaseStepSend(context.Context, coreapi.StepSendJob) error {
	f.calls++
	return nil
}

func (f *fakeOutcomeWriter) FinalizeStepSend(context.Context, coreapi.StepSendJob, coreapi.StepResult) (coreapi.Advance, error) {
	f.calls++
	return coreapi.Advance{}, nil
}

func (f *fakeOutcomeWriter) MarkStepStopped(context.Context, string, string, string) error {
	f.calls++
	return nil
}

func (f *fakeOutcomeWriter) DeferEnrollment(context.Context, string, string, time.Time) error {
	f.calls++
	return nil
}

func (f *fakeOutcomeWriter) IncrementEnrollmentCapDeferrals(context.Context, string, string) (int, error) {
	f.calls++
	return 0, nil
}

func (f *fakeOutcomeWriter) ClaimWarmupSend(context.Context, coreapi.WarmupSendJob) (coreapi.ClaimOutcome, error) {
	f.calls++
	return coreapi.ClaimSkip, nil
}

func (f *fakeOutcomeWriter) MarkWarmupSent(context.Context, coreapi.WarmupSendJob, string) error {
	f.calls++
	return nil
}

func (f *fakeOutcomeWriter) ReleaseWarmupSend(context.Context, coreapi.WarmupSendJob) error {
	f.calls++
	return nil
}

func (f *fakeOutcomeWriter) FailWarmupSend(context.Context, coreapi.WarmupSendJob, string) error {
	f.calls++
	return nil
}

func (f *fakeOutcomeWriter) MarkWarmupEngaged(context.Context, string, string, bool) error {
	f.calls++
	return nil
}

func (f *fakeOutcomeWriter) MarkReplied(context.Context, string, string, string, string, float64) error {
	f.calls++
	return nil
}

func (f *fakeOutcomeWriter) RecordReplyClass(context.Context, string, string, string, string, float64) error {
	f.calls++
	return nil
}

func (f *fakeOutcomeWriter) MarkUnsubscribed(context.Context, string, string, string) error {
	f.calls++
	return nil
}

func (f *fakeOutcomeWriter) MarkBounced(context.Context, string, string, string, bool) error {
	f.calls++
	return nil
}

func (f *fakeOutcomeWriter) MarkWebhookDelivered(context.Context, string, string, int, int) error {
	f.calls++
	return nil
}

func (f *fakeOutcomeWriter) MarkWebhookRetrying(context.Context, string, string, int, string, *int, time.Time) error {
	f.calls++
	return nil
}

func (f *fakeOutcomeWriter) MarkWebhookFailed(context.Context, string, string, int, string, *int) error {
	f.calls++
	return nil
}

// fakeInboxSendWriter is the control plane's manual reply/compose half. Like the
// two above it answers zero values: this file is about which routes are mounted,
// behind which token, on which listener.
type fakeInboxSendWriter struct{ calls int }

func (f *fakeInboxSendWriter) GetInboxReplyJob(context.Context, string, string) (coreapi.InboxReplyJob, error) {
	f.calls++
	return coreapi.InboxReplyJob{}, nil
}

func (f *fakeInboxSendWriter) RecordInboxReply(context.Context, coreapi.RecordInboxReplyInput) error {
	f.calls++
	return nil
}

func (f *fakeInboxSendWriter) ClaimInboxReply(context.Context, string, string) (bool, error) {
	f.calls++
	return true, nil
}

func (f *fakeInboxSendWriter) ReleaseInboxReply(context.Context, string, string) error {
	f.calls++
	return nil
}

func (f *fakeInboxSendWriter) ClaimPendingInboxReply(context.Context, string, string) (coreapi.PendingInboxReply, error) {
	f.calls++
	return coreapi.PendingInboxReply{}, nil
}

func (f *fakeInboxSendWriter) MarkPendingInboxReplySent(context.Context, string, string, string) error {
	f.calls++
	return nil
}

func (f *fakeInboxSendWriter) ReleasePendingInboxReply(context.Context, string, string, string) error {
	f.calls++
	return nil
}

func (f *fakeInboxSendWriter) FailPendingInboxReply(context.Context, string, string, string) error {
	f.calls++
	return nil
}

func (f *fakeInboxSendWriter) ClaimPendingInboxCompose(context.Context, string, string) (coreapi.PendingInboxCompose, error) {
	f.calls++
	return coreapi.PendingInboxCompose{}, nil
}

func (f *fakeInboxSendWriter) MarkPendingInboxComposeSent(context.Context, string, string, string) error {
	f.calls++
	return nil
}

func (f *fakeInboxSendWriter) ReleasePendingInboxCompose(context.Context, string, string, string) error {
	f.calls++
	return nil
}

func (f *fakeInboxSendWriter) FailPendingInboxCompose(context.Context, string, string, string) error {
	f.calls++
	return nil
}

// fleetRoutes is every route the fleet listener is supposed to mount, with a
// body each accepts. Listing them in ONE place is what makes the "behind the
// token" and "not on the public router" tests grow with the transport instead
// of quietly covering whichever routes existed when they were written.
func fleetRoutes() []struct{ name, path, body string } {
	ws, id := uuid.New().String(), uuid.New().String()
	return []struct{ name, path, body string }{
		{"credentials: mailbox", credbroker.PathMailbox, `{"workspace_id":"` + ws + `","mailbox_id":"` + id + `"}`},
		{"credentials: webhook endpoint", credbroker.PathWebhookEndpoint, `{"workspace_id":"` + ws + `","endpoint_id":"` + id + `"}`},
		{"coreapi: suppression", remote.PathSuppressionCheck, `{"workspace_id":"` + ws + `","email":"ada@example.test"}`},
		{"coreapi: step send job", remote.PathStepSendJob, `{"workspace_id":"` + ws + `","enrollment_id":"` + id + `"}`},
		{"coreapi: inbox poll job", remote.PathInboxPollJob, `{"workspace_id":"` + ws + `","mailbox_id":"` + id + `"}`},
		{"coreapi: warmup send job", remote.PathWarmupSendJob, `{"workspace_id":"` + ws + `","mailbox_id":"` + id + `"}`},
		{"coreapi: warmup engage job", remote.PathWarmupEngageJob, `{"workspace_id":"` + ws + `","receipt_id":"` + id + `"}`},
		{"coreapi: webhook delivery job", remote.PathWebhookDeliveryJob, `{"workspace_id":"` + ws + `","delivery_id":"` + id + `"}`},
		{"coreapi: test send content", remote.PathTestSendContent, `{"workspace_id":"` + ws + `","campaign_id":"` + id + `","step_id":"` + id + `"}`},
		{"coreapi: sender transport", remote.PathSenderTransport, `{"workspace_id":"` + ws + `","mailbox_id":"` + id + `"}`},
		{"coreapi: send by message id", remote.PathSendByMessageID, `{"workspace_id":"` + ws + `","message_id":"<a@b.test>"}`},

		// The claim and outcome routes (slice 3). The five job-carrying ones send
		// a minimal job whose workspace matches the envelope — the handler refuses
		// a mismatch before it reaches the writer, so a body that omitted it would
		// make this a 400 test rather than a mounting test.
		{"coreapi: step send claim", remote.PathStepSendClaim, `{"workspace_id":"` + ws + `","job":{"workspace_id":"` + ws + `"}}`},
		{"coreapi: step send delivered", remote.PathStepSendDelivered, `{"workspace_id":"` + ws + `","job":{"workspace_id":"` + ws + `"},"message_id":"<a@b.test>"}`},
		{"coreapi: step cursor advance", remote.PathStepSendAdvance, `{"workspace_id":"` + ws + `","job":{"workspace_id":"` + ws + `"}}`},
		{"coreapi: step send release", remote.PathStepSendRelease, `{"workspace_id":"` + ws + `","job":{"workspace_id":"` + ws + `"}}`},
		{"coreapi: step send finalize", remote.PathStepSendFinalize, `{"workspace_id":"` + ws + `","job":{"workspace_id":"` + ws + `"},"result":{"status":"failed"}}`},
		{"coreapi: enrollment stop", remote.PathEnrollmentStop, `{"workspace_id":"` + ws + `","enrollment_id":"` + id + `","reason":"suppressed"}`},
		{"coreapi: enrollment defer", remote.PathEnrollmentDefer, `{"workspace_id":"` + ws + `","enrollment_id":"` + id + `","until":"2026-01-01T00:00:00Z"}`},
		{"coreapi: enrollment cap deferral", remote.PathEnrollmentCapDeferral, `{"workspace_id":"` + ws + `","enrollment_id":"` + id + `"}`},
		{"coreapi: warmup send claim", remote.PathWarmupSendClaim, `{"workspace_id":"` + ws + `","job":{"workspace_id":"` + ws + `"}}`},
		{"coreapi: warmup send sent", remote.PathWarmupSendSent, `{"workspace_id":"` + ws + `","job":{"workspace_id":"` + ws + `"},"message_id":"<a@b.test>"}`},
		{"coreapi: warmup send release", remote.PathWarmupSendRelease, `{"workspace_id":"` + ws + `","job":{"workspace_id":"` + ws + `"}}`},
		{"coreapi: warmup send fail", remote.PathWarmupSendFail, `{"workspace_id":"` + ws + `","job":{"workspace_id":"` + ws + `"},"error":"boom"}`},
		{"coreapi: warmup engaged", remote.PathWarmupEngaged, `{"workspace_id":"` + ws + `","receipt_id":"` + id + `","replied":true}`},
		{"coreapi: reply replied", remote.PathReplyReplied, `{"workspace_id":"` + ws + `","enrollment_id":"` + id + `","class":"positive","source":"lexicon","confidence":0.9}`},
		{"coreapi: reply class", remote.PathReplyClass, `{"workspace_id":"` + ws + `","enrollment_id":"` + id + `","class":"out_of_office","source":"header","confidence":1}`},
		{"coreapi: reply unsubscribed", remote.PathReplyUnsubscribed, `{"workspace_id":"` + ws + `","enrollment_id":"` + id + `","email":"ada@example.test"}`},
		{"coreapi: reply bounced", remote.PathReplyBounced, `{"workspace_id":"` + ws + `","enrollment_id":"` + id + `","email":"ada@example.test","hard":true}`},
		{"coreapi: webhook delivered", remote.PathWebhookMarkDelivered, `{"workspace_id":"` + ws + `","delivery_id":"` + id + `","attempts":1,"response_status":200}`},
		{"coreapi: webhook retrying", remote.PathWebhookMarkRetrying, `{"workspace_id":"` + ws + `","delivery_id":"` + id + `","attempts":1,"last_error":"502","response_status":502,"next_attempt_at":"2026-01-01T00:00:00Z"}`},
		{"coreapi: webhook failed", remote.PathWebhookMarkFailed, `{"workspace_id":"` + ws + `","delivery_id":"` + id + `","attempts":5,"last_error":"gave up","response_status":null}`},

		// The manual reply/compose routes (slice 3b). The record route carries the
		// workspace twice — envelope and reply — because the handler refuses a
		// mismatch before it reaches the writer.
		{"coreapi: inbox reply job", remote.PathInboxReplyJob, `{"workspace_id":"` + ws + `","thread_id":"` + id + `"}`},
		{"coreapi: inbox reply record", remote.PathInboxReplyRecord, `{"workspace_id":"` + ws + `","reply":{"workspace_id":"` + ws + `","thread_id":"` + id + `","message_id":"<a@b.test>","from_email":"me@acme.test","from_name":"Acme","to_email":"lead@x.test","subject":"Re: hi","body_text":"hello"}}`},
		{"coreapi: inbox reply claim", remote.PathInboxReplyClaim, `{"workspace_id":"` + ws + `","task_id":"inboxreply:x:1700000000"}`},
		{"coreapi: inbox reply release", remote.PathInboxReplyRelease, `{"workspace_id":"` + ws + `","task_id":"inboxreply:x:1700000000"}`},
		{"coreapi: pending reply claim", remote.PathInboxPendingReplyClaim, `{"workspace_id":"` + ws + `","pending_id":"` + id + `"}`},
		{"coreapi: pending reply sent", remote.PathInboxPendingReplySent, `{"workspace_id":"` + ws + `","pending_id":"` + id + `","message_id":"<a@b.test>"}`},
		{"coreapi: pending reply release", remote.PathInboxPendingReplyRelease, `{"workspace_id":"` + ws + `","pending_id":"` + id + `","reason":"transient"}`},
		{"coreapi: pending reply fail", remote.PathInboxPendingReplyFail, `{"workspace_id":"` + ws + `","pending_id":"` + id + `","reason":"permanent"}`},
		{"coreapi: pending compose claim", remote.PathInboxPendingComposeClaim, `{"workspace_id":"` + ws + `","pending_id":"` + id + `"}`},
		{"coreapi: pending compose sent", remote.PathInboxPendingComposeSent, `{"workspace_id":"` + ws + `","pending_id":"` + id + `","message_id":"<a@b.test>"}`},
		{"coreapi: pending compose release", remote.PathInboxPendingComposeRelease, `{"workspace_id":"` + ws + `","pending_id":"` + id + `","reason":"transient"}`},
		{"coreapi: pending compose fail", remote.PathInboxPendingComposeFail, `{"workspace_id":"` + ws + `","pending_id":"` + id + `","reason":"permanent"}`},

		// The inbound-mail routes and the last job read (slice 4). The four
		// input-carrying ones repeat the workspace inside the input, because the
		// handler refuses a mismatch before it reaches the writer — a body that
		// omitted it would make this a 400 test rather than a mounting test.
		{"coreapi: inbox cursor uid", remote.PathInboxCursorUID, `{"workspace_id":"` + ws + `","mailbox_id":"` + id + `","last_seen_uid":42,"uid_validity":7}`},
		{"coreapi: inbox cursor string", remote.PathInboxCursorString, `{"workspace_id":"` + ws + `","mailbox_id":"` + id + `","cursor":"history:1"}`},
		{"coreapi: inbound message", remote.PathInboxMessageStore, `{"workspace_id":"` + ws + `","message":{"WorkspaceID":"` + ws + `","MailboxID":"` + id + `"}}`},
		{"coreapi: crm reply capture", remote.PathCRMReplyCapture, `{"workspace_id":"` + ws + `","reply":{"WorkspaceID":"` + ws + `","SendID":"` + id + `"}}`},
		{"coreapi: complaint ingest", remote.PathComplaintIngest, `{"workspace_id":"` + ws + `","complaint":{"WorkspaceID":"` + ws + `","SendID":"` + id + `"}}`},
		{"coreapi: reply label resolve", remote.PathReplyLabelResolve, `{"workspace_id":"` + ws + `","key":"positive"}`},
		{"coreapi: warmup receipt", remote.PathWarmupReceipt, `{"workspace_id":"` + ws + `","receipt":{"WorkspaceID":"` + ws + `","WarmupSendID":"` + id + `"}}`},
		{"coreapi: warmup send by message id", remote.PathWarmupSendByMessageID, `{"workspace_id":"` + ws + `","to_mailbox_id":"` + id + `","message_id":"<a@b.test>"}`},
		{"coreapi: warmup token failure", remote.PathWarmupTokenFailure, `{"workspace_id":"` + ws + `","recipient_mailbox":"` + id + `","fingerprint":"ab12","reason_code":"bad_signature"}`},
		{"coreapi: warmup hard bounce", remote.PathWarmupHardBounce, `{"workspace_id":"` + ws + `","message_id":"<a@b.test>","observer_mailbox":"` + id + `"}`},
		{"coreapi: warmup next due", remote.PathWarmupNextDue, `{"workspace_id":"` + ws + `","mailbox_id":"` + id + `"}`},

		// The worker-infrastructure routes (slice 4). Three of the four carry no
		// workspace, which is the honest shape rather than an omission — see
		// remote.FleetWriter.
		{"coreapi: worker heartbeat", remote.PathWorkerHeartbeat, `{"worker_id":"box-1","egress_ip":"203.0.113.7","id_family":"ipv4"}`},
		{"coreapi: worker provider signals", remote.PathWorkerProviderSignals, `{"signals":{"WorkerID":"box-1","Counts":[]}}`},
		{"coreapi: assign mailbox worker", remote.PathMailboxWorkerAssign, `{"workspace_id":"` + ws + `","mailbox_id":"` + id + `"}`},
		{"coreapi: dead letter record", remote.PathDeadLetterRecord, `{"dead_letter":{"WorkspaceID":"` + ws + `","TaskType":"sequence:advance"}}`},
	}
}

func postFleet(t *testing.T, h http.Handler, path, token, body string) int {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code
}

// The sharing decision, made executable: ONE listener carries both fleet
// transports and ONE token authenticates both. A worker holds a single
// credential and an operator firewalls a single address.
func TestTheFleetListenerServesBothTransportsUnderOneToken(t *testing.T) {
	opener, reader, jobs := &fakeOpener{}, &fakeSuppressionReader{}, &fakeJobReader{}
	outcomes, sends := &fakeOutcomeWriter{}, &fakeInboxSendWriter{}
	inbound, workers := &fakeInboundWriter{}, &fakeFleetWriter{}
	h, err := newFleetHandler(
		fleetDeps{
			credentials: opener, suppression: reader, jobs: jobs, outcomes: outcomes,
			inboxSends: sends, inbound: inbound, fleet: workers,
		},
		fleetTestToken, discardLogger())
	if err != nil {
		t.Fatalf("newFleetHandler: %v", err)
	}

	for _, route := range fleetRoutes() {
		if code := postFleet(t, h, route.path, fleetTestToken, route.body); code != http.StatusOK {
			t.Errorf("%s (%s): status %d, want 200", route.name, route.path, code)
		}
	}
	if opener.calls != 2 {
		t.Errorf("opener reached %d times, want 2", opener.calls)
	}
	if reader.calls != 1 {
		t.Errorf("suppression reader reached %d times, want 1", reader.calls)
	}
	// Nine since slice 4 added NextWarmupDue: it is a per-message READ about one
	// named mailbox, so it sits with the job reads rather than with that slice's
	// inbound-mail group.
	if jobs.calls != 9 {
		t.Errorf("job reader reached %d times, want 9", jobs.calls)
	}
	if outcomes.calls != 20 {
		t.Errorf("outcome writer reached %d times, want 20", outcomes.calls)
	}
	if sends.calls != 12 {
		t.Errorf("inbox send writer reached %d times, want 12", sends.calls)
	}
	if inbound.calls != 10 {
		t.Errorf("inbound writer reached %d times, want 10", inbound.calls)
	}
	if workers.calls != 4 {
		t.Errorf("fleet writer reached %d times, want 4", workers.calls)
	}
}

// One token means one rotation — and one refusal. A wrong token is rejected on
// BOTH transports and reaches neither dependency.
func TestTheFleetListenerRejectsAWrongTokenOnBothTransports(t *testing.T) {
	opener, reader, jobs := &fakeOpener{}, &fakeSuppressionReader{}, &fakeJobReader{}
	outcomes, sends := &fakeOutcomeWriter{}, &fakeInboxSendWriter{}
	inbound, workers := &fakeInboundWriter{}, &fakeFleetWriter{}
	h, err := newFleetHandler(
		fleetDeps{
			credentials: opener, suppression: reader, jobs: jobs, outcomes: outcomes,
			inboxSends: sends, inbound: inbound, fleet: workers,
		},
		fleetTestToken, discardLogger())
	if err != nil {
		t.Fatalf("newFleetHandler: %v", err)
	}

	for _, tc := range fleetRoutes() {
		t.Run(tc.name+"/wrong token", func(t *testing.T) {
			if code := postFleet(t, h, tc.path, strings.Repeat("z", credbroker.MinTokenLen), tc.body); code != http.StatusUnauthorized {
				t.Errorf("status %d, want 401", code)
			}
		})
		t.Run(tc.name+"/no token", func(t *testing.T) {
			if code := postFleet(t, h, tc.path, "", tc.body); code != http.StatusUnauthorized {
				t.Errorf("status %d, want 401", code)
			}
		})
	}
	if opener.calls != 0 || reader.calls != 0 || jobs.calls != 0 || outcomes.calls != 0 ||
		sends.calls != 0 || inbound.calls != 0 || workers.calls != 0 {
		t.Errorf("dependencies were reached (%d opener, %d reader, %d jobs, %d outcomes, %d sends, %d inbound, %d fleet) despite rejected tokens",
			opener.calls, reader.calls, jobs.calls, outcomes.calls, sends.calls, inbound.calls, workers.calls)
	}
}

// The fleet listener serves the two mounted prefixes and nothing else: it is
// not a second copy of the API, and a path nobody mounted is a 404 rather than
// whatever a catch-all would do.
func TestTheFleetListenerServesNothingElse(t *testing.T) {
	h, err := newFleetHandler(
		fleetDeps{
			credentials: &fakeOpener{}, suppression: &fakeSuppressionReader{}, jobs: &fakeJobReader{},
			outcomes: &fakeOutcomeWriter{}, inboxSends: &fakeInboxSendWriter{},
			inbound: &fakeInboundWriter{}, fleet: &fakeFleetWriter{},
		},
		fleetTestToken, discardLogger())
	if err != nil {
		t.Fatalf("newFleetHandler: %v", err)
	}
	for _, path := range []string{"/", "/api/v1/campaigns", "/internal/fleet/", "/healthz"} {
		if code := postFleet(t, h, path, fleetTestToken, "{}"); code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", path, code)
		}
	}
}

// A listener missing ANY transport refuses to be built. Half a fleet channel
// would start, serve the half it has, and fail every call to the other at the
// first send — which is #216's lesson in a different shape.
func TestTheFleetListenerRefusesAMissingTransport(t *testing.T) {
	// Built from a COMPLETE set with one field cleared, rather than seven
	// literals each listing six dependencies. The earlier shape was already
	// drifting: a case that omitted two fields would still pass while proving
	// only that one of them was checked, and slice 4 added two more chances to
	// get that wrong.
	full := func() fleetDeps {
		return fleetDeps{
			credentials: &fakeOpener{}, suppression: &fakeSuppressionReader{}, jobs: &fakeJobReader{},
			outcomes: &fakeOutcomeWriter{}, inboxSends: &fakeInboxSendWriter{},
			inbound: &fakeInboundWriter{}, fleet: &fakeFleetWriter{},
		}
	}
	if _, err := newFleetHandler(full(), fleetTestToken, discardLogger()); err != nil {
		t.Fatalf("newFleetHandler with every transport: %v", err)
	}
	for _, tc := range []struct {
		name  string
		clear func(*fleetDeps)
	}{
		{"no credentials", func(d *fleetDeps) { d.credentials = nil }},
		{"no suppression", func(d *fleetDeps) { d.suppression = nil }},
		{"no jobs", func(d *fleetDeps) { d.jobs = nil }},
		{"no outcomes", func(d *fleetDeps) { d.outcomes = nil }},
		{"no inbox sends", func(d *fleetDeps) { d.inboxSends = nil }},
		{"no inbound", func(d *fleetDeps) { d.inbound = nil }},
		{"no fleet", func(d *fleetDeps) { d.fleet = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := full()
			tc.clear(&deps)
			if _, err := newFleetHandler(deps, fleetTestToken, discardLogger()); err == nil {
				t.Error("newFleetHandler accepted a half-wired listener, want an error")
			}
		})
	}
}

// And the other half of that rule: the PUBLIC API router serves no fleet route
// at all. These endpoints hand a machine credential holder decrypted secrets
// and a tenant's compliance state; they must never sit behind the public
// listener, where an operator's firewall is not what protects them.
func TestThePublicAPIRouterServesNoFleetRoute(t *testing.T) {
	r := buildRouter(discardLogger(), nil, nil, nil)
	for _, route := range fleetRoutes() {
		if code := postFleet(t, r, route.path, fleetTestToken, route.body); code != http.StatusNotFound {
			t.Errorf("the public router answered %s with %d, want 404", route.path, code)
		}
	}
}

// A weak token is refused when the listener is BUILT, naming the variable,
// rather than at the first request as a 401 nobody is watching for.
func TestTheFleetListenerRefusesAWeakToken(t *testing.T) {
	_, err := newFleetHandler(
		fleetDeps{
			credentials: &fakeOpener{}, suppression: &fakeSuppressionReader{}, jobs: &fakeJobReader{},
			outcomes: &fakeOutcomeWriter{}, inboxSends: &fakeInboxSendWriter{},
			inbound: &fakeInboundWriter{}, fleet: &fakeFleetWriter{},
		},
		strings.Repeat("a", credbroker.MinTokenLen-1), discardLogger())
	if err == nil {
		t.Fatal("newFleetHandler accepted a short token, want an error")
	}
}
