package remote

import (
	"time"

	"github.com/inroad/inroad/internal/coreapi"
)

// The wire contract between a worker (Client) and the control plane (Handler).
// Both sides are in this package on purpose, exactly as credbroker does it: one
// file defines the shapes, so the two transports cannot drift into disagreeing
// about a field name.

const (
	// PathPrefix is where cmd/inroad mounts NewHandler on the fleet listener,
	// beside credbroker.PathPrefix. Keeping every coreapi route under one
	// prefix is what lets the composition root mount this package as a unit
	// without naming its individual routes.
	PathPrefix = "/internal/fleet/coreapi/"
	// PathSuppressionCheck answers one suppression question (slice 1).
	PathSuppressionCheck = PathPrefix + "suppression/check"

	// The per-message job READS (slice 2). Every one of them is
	// side-effect-free from the worker's point of view: it names one subject by
	// id and receives the work to do about it. Nothing here claims, marks,
	// finalizes, advances or fails anything — those carry the idempotency risk
	// and are a slice of their own.
	PathStepSendJob        = PathPrefix + "step-send/job"
	PathInboxPollJob       = PathPrefix + "inbox-poll/job"
	PathWarmupSendJob      = PathPrefix + "warmup-send/job"
	PathWarmupEngageJob    = PathPrefix + "warmup-engage/job"
	PathWebhookDeliveryJob = PathPrefix + "webhook-delivery/job"
	PathTestSendContent    = PathPrefix + "test-send/content"
	PathSenderTransport    = PathPrefix + "sender-transport"
	PathSendByMessageID    = PathPrefix + "send/by-message-id"

	// The CLAIM AND OUTCOME routes (slice 3). Everything that claims, marks,
	// finalizes, advances, stops, defers or fails — the writes slices 1 and 2
	// deliberately kept out, because over a network a call has a third outcome
	// the in-process seam does not: it happened, the control plane committed,
	// and the RESPONSE was lost.
	//
	// What makes that safe is not this transport. It is the claim: a worker that
	// claimed, sent, and lost the response to MarkStepDelivered retries the whole
	// asynq task, re-claims, and is told ClaimAlreadySent — so it advances the
	// cursor instead of delivering again. Every route below is idempotent by KEY
	// (the deterministic send id, the enrollment id, the receipt id), never by
	// attempt, so a repeat is a no-op returning the same answer. See outcomes.go
	// for the per-method audit.
	PathStepSendClaim         = PathPrefix + "step-send/claim"
	PathStepSendDelivered     = PathPrefix + "step-send/delivered"
	PathStepSendAdvance       = PathPrefix + "step-send/advance"
	PathStepSendRelease       = PathPrefix + "step-send/release"
	PathStepSendFinalize      = PathPrefix + "step-send/finalize"
	PathEnrollmentStop        = PathPrefix + "enrollment/stop"
	PathEnrollmentDefer       = PathPrefix + "enrollment/defer"
	PathEnrollmentCapDeferral = PathPrefix + "enrollment/cap-deferral"
	PathWarmupSendClaim       = PathPrefix + "warmup-send/claim"
	PathWarmupSendSent        = PathPrefix + "warmup-send/sent"
	PathWarmupSendRelease     = PathPrefix + "warmup-send/release"
	PathWarmupSendFail        = PathPrefix + "warmup-send/fail"
	PathWarmupEngaged         = PathPrefix + "warmup-engage/engaged"
	PathReplyReplied          = PathPrefix + "reply/replied"
	PathReplyClass            = PathPrefix + "reply/class"
	PathReplyUnsubscribed     = PathPrefix + "reply/unsubscribed"
	PathReplyBounced          = PathPrefix + "reply/bounced"
	PathWebhookMarkDelivered  = PathPrefix + "webhook-delivery/delivered"
	PathWebhookMarkRetrying   = PathPrefix + "webhook-delivery/retrying"
	PathWebhookMarkFailed     = PathPrefix + "webhook-delivery/failed"

	// The MANUAL MAIL routes (slice 3b): the reply/compose protocol for mail a
	// HUMAN wrote and pressed send on. Slice 3 deliberately left this alone
	// because ClaimPendingInboxReply is a claim-and-READ hybrid — it takes the
	// lease and returns the BODY in one call — so moving its outcome half without
	// its read half would have reproduced exactly the split slice 3 argued
	// against for the step claim. It moves whole, or not at all.
	//
	// The double-send bar is HIGHER here than on a sequence step, not equal: a
	// duplicate step is one extra marketing touch, a duplicate manual reply is
	// the operator's own words arriving twice in a customer's thread. What holds
	// it is the ROW — a status-guarded 'scheduled' -> 'sending' UPDATE with a
	// lease, which is also the operator's undo handle — never this transport. See
	// inboxsends.go for the per-method audit of what a lost response costs.
	//
	// The first three are the LEGACY drain (worker/inbox.ReplySendHandler),
	// deleted with it in the release after this one.
	PathInboxReplyJob     = PathPrefix + "inbox-reply/job"
	PathInboxReplyClaim   = PathPrefix + "inbox-reply/claim"
	PathInboxReplyRelease = PathPrefix + "inbox-reply/release"
	// PathInboxReplyRecord is shared by the drain and the deferred path: both
	// record the delivered message onto the thread after the provider ACK.
	PathInboxReplyRecord = PathPrefix + "inbox-reply/record"

	PathInboxPendingReplyClaim   = PathPrefix + "inbox-pending-reply/claim"
	PathInboxPendingReplySent    = PathPrefix + "inbox-pending-reply/sent"
	PathInboxPendingReplyRelease = PathPrefix + "inbox-pending-reply/release"
	PathInboxPendingReplyFail    = PathPrefix + "inbox-pending-reply/fail"

	PathInboxPendingComposeClaim   = PathPrefix + "inbox-pending-compose/claim"
	PathInboxPendingComposeSent    = PathPrefix + "inbox-pending-compose/sent"
	PathInboxPendingComposeRelease = PathPrefix + "inbox-pending-compose/release"
	PathInboxPendingComposeFail    = PathPrefix + "inbox-pending-compose/fail"
)

// The claim outcome, on the wire.
//
// It is a STRING, not the Go enum's integer. coreapi.ClaimOutcome is an iota
// whose zero value is ClaimSkip, so encoding the int would make the enum's
// DECLARATION ORDER part of a network contract: inserting a constant would
// silently re-point every deployed worker's reading of every other value, and
// the failure would be a send decision, not a decode error.
//
// An unrecognised string is an ERROR on both sides rather than a default. There
// is a tempting default — ClaimSkip, which never double-sends — and taking it
// would hide a control plane and a worker that no longer agree about the
// protocol behind a worker that quietly stops sending. A refusal fails the CALL,
// which asynq retries and an operator can see.
const (
	claimOutcomeSkip        = "skip"
	claimOutcomeWon         = "won"
	claimOutcomeAlreadySent = "already_sent"
	claimOutcomeDeferred    = "deferred"
)

// Error codes. A code names a SENTINEL the in-process path returns and a
// consumer BRANCHES on — it is not a description of what went wrong, and there
// is deliberately no code for "the query failed".
//
// Two sentinels qualify today, and both are load-bearing in the execution
// plane. Without them the wire would turn an ordinary answer into a task
// failure: nearly every inbound message the poller sees matches no send, and a
// webhook delivery deleted after its task was queued must be dropped rather
// than retried forever.
//
// A 404 carrying NEITHER code — an older control plane that does not serve the
// route, so net/http's own "404 page not found" — maps to no sentinel at all
// and stays a plain error. That direction matters more than it looks: mapping
// an unrecognised 404 to ErrNoRows would make a worker silently discard every
// webhook delivery against a control plane that had not been upgraded.
// Two more sentinels join them with slice 3b, on a DIFFERENT status, and the
// status is the decision worth recording. 404 means "the named row is gone"; the
// manual-mail pair mean "the row is there and its STATE forbids this", which is
// 409 Conflict. Keeping them off 404 is not tidiness: a stray 404 from a
// mis-routed request or an intermediary must never be readable as "the operator
// cancelled this reply", because that reading makes a worker drop a human's mail
// and report success. On 409 it cannot be, whatever the body says.
const (
	codeNotFound = "not_found" // → pgx.ErrNoRows
	codeNoMatch  = "no_match"  // → coreapi.ErrNoMatch

	// codeNotClaimable → coreapi.ErrInboxPendingNotClaimable: cancelled, already
	// sent, not yet due, or held by another worker's live lease. All four mean
	// "stop and do not retry", which is why the in-process seam gives them one
	// error and this gives them one code.
	codeNotClaimable = "not_claimable"
	// codeNoInbound → coreapi.ErrInboxNoInbound: the thread has no inbound
	// message to reply to. PERMANENT — both reply handlers log it and fail the
	// row rather than retrying a reply that can never be built.
	codeNoInbound = "no_inbound"
)

// suppressionRequest asks whether ONE named address is suppressed in ONE named
// workspace. It carries a workspace id and a single address — deliberately no
// filter, no pattern, no limit and no cursor.
//
// That restriction is the same one credbroker's ids-only request enforces, for
// the same reason: a request that could express "rows matching X" would make
// this seam a general query engine over the tenant database, which is precisely
// the capability the plane split exists to take away. Every route added in a
// later slice has to answer to that rule too.
//
// The address itself is not an id, and that is worth stating rather than
// glossing. The subject of a suppression record IS an email address, and the
// worker is about to send to this one — it already holds it, from the job it
// was handed. So the request reveals nothing to the control plane and the
// ANSWER reveals one bit about an address the caller named. What it is not is
// enumerable: there is no way to list the suppression table, page it, or match
// a prefix, so learning the list means already knowing every address in it.
type suppressionRequest struct {
	WorkspaceID string `json:"workspace_id"`
	Email       string `json:"email"`
}

// suppressionResponse is the one bit of the answer. A dedicated struct rather
// than a bare boolean so the route can gain a field (a reason, an as-of time)
// without every deployed worker needing to change on the same day.
type suppressionResponse struct {
	Suppressed bool `json:"suppressed"`
}

// errorResponse is the only body an error ever returns. The message is a FIXED
// string chosen by the handler, never an upstream or database error text: a
// seam that echoed "no rows in result set" or a pg error would be a probe
// oracle, and one that echoed a query plan would be worse.
//
// Code is likewise a fixed value from the closed set above, present only on the
// two answers a caller branches on. It is omitted everywhere else rather than
// defaulted, so "no code" is unambiguous.
type errorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

// The job-read request shapes. Every one carries IDS ONLY — no filter, no
// pattern, no limit, no cursor — for the reason suppressionRequest states: a
// request that could express "rows matching X" would make this seam a general
// query engine over the tenant database, which is the capability the plane
// split exists to take away.
//
// The workspace is on every one of them because there is no session here to
// derive it from, and the handler pins with it exactly as the in-process path
// pins. See checkSuppression's doc for why that ADDS a pin rather than
// replacing one.

// enrollmentRequest names one enrollment whose next due step is wanted.
type enrollmentRequest struct {
	WorkspaceID  string `json:"workspace_id"`
	EnrollmentID string `json:"enrollment_id"`
}

// mailboxRequest names one mailbox. Shared by the three routes whose subject is
// a mailbox (inbox poll, warmup send, sender transport) rather than split into
// three identical structs — one shape, one place for the field names to be
// right.
type mailboxRequest struct {
	WorkspaceID string `json:"workspace_id"`
	MailboxID   string `json:"mailbox_id"`
}

// receiptRequest names one warmup receipt to engage with.
type receiptRequest struct {
	WorkspaceID string `json:"workspace_id"`
	ReceiptID   string `json:"receipt_id"`
}

// deliveryRequest names one webhook delivery.
type deliveryRequest struct {
	WorkspaceID string `json:"workspace_id"`
	DeliveryID  string `json:"delivery_id"`
}

// testSendContentRequest names one step of one campaign. Both ids travel
// because the in-process loader checks the step actually belongs to the
// campaign (coreapi.ErrCrossTenant) and the remote path must ask the same
// question, not a weaker one.
type testSendContentRequest struct {
	WorkspaceID string `json:"workspace_id"`
	CampaignID  string `json:"campaign_id"`
	StepID      string `json:"step_id"`
}

// messageIDRequest names one inbound RFC 5322 Message-ID to match back to a
// send.
//
// MessageID is the one field on this wire that is not an id we minted: it comes
// off unauthenticated inbound mail. It is passed through UNVALIDATED for the
// same parity reason suppressionRequest.Email is — whatever string the worker
// would have handed the local query, the control plane hands to the same query
// — and the request body cap is what bounds it.
type messageIDRequest struct {
	WorkspaceID string `json:"workspace_id"`
	MessageID   string `json:"message_id"`
}

// The job-read response shapes.
//
// Each wraps the coreapi job type itself rather than mirroring its fields.
// A hand-written mirror would fail in the worst available direction: a field
// added to a job and forgotten in the mirror arrives ZERO-VALUED, which for one
// of StepSendJob's gate flags is a send that should not have gone out. One
// definition cannot drift from itself. See the package doc on coreapi for the
// whole argument, including why the credential fields are `json:"-"` there and
// are filled from the credential broker on this side.
//
// Each is a struct around one field rather than the bare job, for the reason
// suppressionResponse is: a route can gain a sibling field without every
// deployed worker needing to change on the same day.

type stepSendJobResponse struct {
	Job coreapi.StepSendJob `json:"job"`
}

type inboxPollJobResponse struct {
	Job coreapi.InboxPollJob `json:"job"`
}

type warmupSendJobResponse struct {
	Job coreapi.WarmupSendJob `json:"job"`
}

type warmupEngageJobResponse struct {
	Job coreapi.WarmupEngageJob `json:"job"`
}

type webhookDeliveryJobResponse struct {
	Job coreapi.WebhookDeliveryJob `json:"job"`
}

type testSendContentResponse struct {
	Content coreapi.TestSendContent `json:"content"`
}

type senderTransportResponse struct {
	Transport coreapi.SenderTransport `json:"transport"`
}

type sendRefResponse struct {
	Send coreapi.SendRef `json:"send"`
}

// The CLAIM AND OUTCOME shapes.
//
// # Why the whole job travels back
//
// ClaimStepSend / MarkStepDelivered / AdvanceStepCursor / ReleaseStepSend /
// FinalizeStepSend all take the coreapi.StepSendJob the worker was handed, and
// so these requests carry it whole rather than the subset each method reads.
// A subset would be a hand-written mirror, and it would fail exactly the way the
// response mirrors would (see the job-response block above): the day a new gate
// field is added to the job and ClaimStepSend starts reading it, a forgotten
// mirror field arrives ZERO-VALUED. For NotDueUntil that is not a decode error,
// it is a send into a stated out-of-office absence. One definition cannot drift
// from itself.
//
// This is what makes maxJobRequestBytes necessary: a step job carries the
// campaign's subject and HTML body, so a request here is as large as a job
// RESPONSE, and the 64 KiB cap the ids-only routes use would refuse a claim for
// any campaign with a real HTML email in it.
//
// # Why the workspace is on the envelope as well as inside the job
//
// Every other route on this transport names its workspace at the top level, and
// the handler pins with it. Keeping that true here means the pin is visible on
// the wire rather than buried in one field of a two-thousand-byte struct, and it
// gives the handler a workspace to parse, refuse and log BEFORE anything runs.
// A request whose envelope and job disagree is a 400: the two can only differ if
// the caller assembled them from different places, which is not a state a correct
// worker reaches.

// stepJobRequest names one claimed-or-claimable step send. Shared by the claim,
// the release and the cursor advance — the three that need the job and nothing
// else.
type stepJobRequest struct {
	WorkspaceID string              `json:"workspace_id"`
	Job         coreapi.StepSendJob `json:"job"`
}

// stepDeliveredRequest records a delivery: the job plus the Message-ID the
// provider assigned.
type stepDeliveredRequest struct {
	WorkspaceID string              `json:"workspace_id"`
	Job         coreapi.StepSendJob `json:"job"`
	MessageID   string              `json:"message_id"`
}

// stepFinalizeRequest finalizes a step to a NON-'sent' terminal state and
// advances the cursor in one transaction (the fail-forward path).
type stepFinalizeRequest struct {
	WorkspaceID string              `json:"workspace_id"`
	Job         coreapi.StepSendJob `json:"job"`
	Result      coreapi.StepResult  `json:"result"`
}

// enrollmentStopRequest halts one enrollment. Reason is one of the enrollment
// stop reasons; it is validated by the enrollment state machine on the control
// plane, exactly as it is in process, rather than a second time here.
type enrollmentStopRequest struct {
	WorkspaceID  string `json:"workspace_id"`
	EnrollmentID string `json:"enrollment_id"`
	Reason       string `json:"reason"`
}

// enrollmentDeferRequest pushes an active enrollment's next_due_at out.
type enrollmentDeferRequest struct {
	WorkspaceID  string    `json:"workspace_id"`
	EnrollmentID string    `json:"enrollment_id"`
	Until        time.Time `json:"until"`
}

// warmupJobRequest names one claimed-or-claimable warmup send. Shared by the
// claim and the release, like stepJobRequest.
type warmupJobRequest struct {
	WorkspaceID string                `json:"workspace_id"`
	Job         coreapi.WarmupSendJob `json:"job"`
}

// warmupSentRequest finalizes a warmup send to 'sent'.
type warmupSentRequest struct {
	WorkspaceID string                `json:"workspace_id"`
	Job         coreapi.WarmupSendJob `json:"job"`
	MessageID   string                `json:"message_id"`
}

// warmupFailRequest finalizes a warmup send to 'failed' after a PERMANENT
// failure. Error is a diagnostic the control plane stores on the row; it is a
// send error the worker observed, never relayed anywhere a tenant reads.
type warmupFailRequest struct {
	WorkspaceID string                `json:"workspace_id"`
	Job         coreapi.WarmupSendJob `json:"job"`
	Error       string                `json:"error"`
}

// warmupEngagedRequest flips one receipt's engaged guard.
type warmupEngagedRequest struct {
	WorkspaceID string `json:"workspace_id"`
	ReceiptID   string `json:"receipt_id"`
	Replied     bool   `json:"replied"`
}

// replyClassRequest tags one enrollment with a classified reply. Shared by the
// two routes whose signature it is — MarkReplied (which also STOPS the
// enrollment) and RecordReplyClass (which deliberately does not) — because the
// difference between them is which route was called, not what was sent.
//
// EnrollmentID is "" for a matched send with no enrollment (the legacy
// direct-send path), and the empty string must survive the wire: in process it
// makes both methods a no-op, so validating it here would turn the ordinary
// answer into a 400 the poller cannot distinguish from a real failure.
type replyClassRequest struct {
	WorkspaceID  string  `json:"workspace_id"`
	EnrollmentID string  `json:"enrollment_id"`
	Class        string  `json:"class"`
	Source       string  `json:"source"`
	Confidence   float64 `json:"confidence"`
}

// unsubscribeRequest suppresses one address and, when an enrollment matched,
// stops it. Email is a contact's address and is here for the reason
// suppressionRequest.Email is: the worker already holds it, off the send row it
// just matched, so the request reveals nothing the caller did not have.
type unsubscribeRequest struct {
	WorkspaceID  string `json:"workspace_id"`
	EnrollmentID string `json:"enrollment_id"`
	Email        string `json:"email"`
}

// bounceRequest records a hard bounce. Hard travels EXPLICITLY rather than being
// assumed true, because the in-process method no-ops on false and the two
// transports must agree about that.
type bounceRequest struct {
	WorkspaceID  string `json:"workspace_id"`
	EnrollmentID string `json:"enrollment_id"`
	Email        string `json:"email"`
	Hard         bool   `json:"hard"`
}

// The three webhook delivery outcomes are three shapes rather than one with
// optional fields. A shared shape would mean the 'delivered' route silently
// accepting and ignoring a next_attempt_at — which is precisely the "a caller
// adds a parameter this endpoint does not honour and believes the answer"
// failure DisallowUnknownFields exists to prevent.

type webhookDeliveredRequest struct {
	WorkspaceID    string `json:"workspace_id"`
	DeliveryID     string `json:"delivery_id"`
	Attempts       int    `json:"attempts"`
	ResponseStatus int    `json:"response_status"`
}

type webhookRetryingRequest struct {
	WorkspaceID string `json:"workspace_id"`
	DeliveryID  string `json:"delivery_id"`
	Attempts    int    `json:"attempts"`
	LastError   string `json:"last_error"`
	// ResponseStatus is nil for a transport-level failure — no HTTP response
	// arrived at all — which is a different fact from a 0 status and is stored
	// as SQL NULL. A non-pointer here would flatten the two.
	ResponseStatus *int      `json:"response_status"`
	NextAttemptAt  time.Time `json:"next_attempt_at"`
}

type webhookFailedRequest struct {
	WorkspaceID    string `json:"workspace_id"`
	DeliveryID     string `json:"delivery_id"`
	Attempts       int    `json:"attempts"`
	LastError      string `json:"last_error"`
	ResponseStatus *int   `json:"response_status"`
}

// claimOutcomeResponse is the four-state claim protocol's answer. A struct
// rather than a bare string for the reason suppressionResponse is one: the
// route can gain a sibling field without every deployed worker changing on the
// same day.
type claimOutcomeResponse struct {
	Outcome string `json:"outcome"`
}

// advanceResponse wraps the coreapi type itself rather than mirroring its two
// fields, same rule as the job responses.
type advanceResponse struct {
	Advance coreapi.Advance `json:"advance"`
}

// capDeferralResponse carries the cap-deferral counter's new value.
type capDeferralResponse struct {
	Deferrals int `json:"deferrals"`
}

// The MANUAL MAIL shapes (slice 3b).
//
// # Ids in, values out — and the one direction that carries correspondence
//
// Eleven of the twelve routes take IDS ONLY, exactly like every other route on
// this transport. The twelfth, the record, carries the delivered reply's BODY
// back, and that is a deliberate exception rather than an oversight: the thread's
// history is written from it, and the worker is by then holding that exact text
// because the control plane handed it over on the claim. The wire therefore
// reveals nothing the caller was not already given, and nothing here can express
// "rows matching X" — there is no filter, no pattern, no limit and no cursor on
// any of the twelve.
//
// The body travelling at all is what makes these routes different from the rest
// of the seam, and it is unavoidable: a worker cannot send a reply it has not
// been given the text of. What follows from it is a rule rather than a
// mitigation — no route here logs a body, a subject or a recipient on either
// side, and inboxsendhandler.go's log arguments are ids only.

// threadRequest names one inbox thread whose reply job is wanted.
type threadRequest struct {
	WorkspaceID string `json:"workspace_id"`
	ThreadID    string `json:"thread_id"`
}

// replyTaskRequest names one LEGACY inbox:reply_send task's claim key.
//
// TaskID is not a uuid and must not be validated as one: it is the asynq task id
// minted at enqueue time ("inboxreply:<thread>:<unix-second>"), it is the claim's
// key rather than a row id, and refusing an unfamiliar shape would strand exactly
// the in-flight tasks the drain exists to finish.
type replyTaskRequest struct {
	WorkspaceID string `json:"workspace_id"`
	TaskID      string `json:"task_id"`
}

// recordReplyRequest carries one delivered reply back to be written onto the
// thread.
//
// It wraps coreapi.RecordInboxReplyInput itself rather than mirroring its eight
// fields, the same rule the job requests follow: a field added to the input and
// forgotten in a hand-written mirror would arrive ZERO-VALUED, and for BodyText
// that is a thread whose history records an empty message the customer actually
// received.
//
// The workspace is on the envelope AS WELL as inside the input, for the reason
// stepJobRequest states: the pin is then visible on the wire rather than buried
// in one field, and the handler has a workspace to parse and refuse BEFORE
// anything runs. A disagreement is a 400.
type recordReplyRequest struct {
	WorkspaceID string                        `json:"workspace_id"`
	Reply       coreapi.RecordInboxReplyInput `json:"reply"`
}

// pendingRequest names one deferred row — a reply or a composed email. One shape
// for both, because the difference between them is which route was called.
type pendingRequest struct {
	WorkspaceID string `json:"workspace_id"`
	PendingID   string `json:"pending_id"`
}

// pendingSentRequest completes one claimed deferred row with the Message-ID the
// provider assigned.
type pendingSentRequest struct {
	WorkspaceID string `json:"workspace_id"`
	PendingID   string `json:"pending_id"`
	MessageID   string `json:"message_id"`
}

// pendingReasonRequest releases or fails one claimed deferred row.
//
// Reason is a STABLE CLIENT-SAFE TOKEN chosen by internal/worker/inbox, never a
// provider's error text, and that property is the caller's to keep rather than
// this transport's to enforce: the row's last_error is served to any inbox:read
// caller, so the worker already refuses to put an upstream string there (see
// worker/inbox.releaseAndReturn). It travels verbatim for the parity reason every
// free-text value on this transport does.
type pendingReasonRequest struct {
	WorkspaceID string `json:"workspace_id"`
	PendingID   string `json:"pending_id"`
	Reason      string `json:"reason"`
}

// The manual-mail response shapes. Each wraps the coreapi type itself rather
// than mirroring its fields, same rule and same reason as the job responses.

type inboxReplyJobResponse struct {
	Job coreapi.InboxReplyJob `json:"job"`
}

type pendingInboxReplyResponse struct {
	Pending coreapi.PendingInboxReply `json:"pending"`
}

type pendingInboxComposeResponse struct {
	Compose coreapi.PendingInboxCompose `json:"compose"`
}

// claimedResponse is the legacy drain claim's answer: claimed=false means a
// prior attempt at this exact task already reached the dial, and the caller must
// SKIP rather than send again.
//
// A struct rather than a bare boolean for the reason suppressionResponse is one,
// and the field is named rather than reused from that type because the two
// answers mean opposite things and a shared shape would invite reading one as
// the other.
type claimedResponse struct {
	Claimed bool `json:"claimed"`
}

// ackResponse is what a route that returns nothing but "this committed" writes.
// It is deliberately EMPTY rather than {"ok":true}: the status code already says
// the call succeeded, and a boolean nobody reads is a boolean somebody will
// eventually branch on. The body exists at all so the client has valid JSON to
// decode — an empty body would be io.EOF, which is indistinguishable from a
// truncated response.
type ackResponse struct{}
