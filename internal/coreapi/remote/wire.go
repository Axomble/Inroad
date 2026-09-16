package remote

import "github.com/inroad/inroad/internal/coreapi"

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
const (
	codeNotFound = "not_found" // → pgx.ErrNoRows
	codeNoMatch  = "no_match"  // → coreapi.ErrNoMatch
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
