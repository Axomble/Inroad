package coreapi

import "errors"

// ErrInboxNoInbound is returned by GetInboxReplyJob when the thread has no
// inbound message to reply to. The API layer (internal/app/inbox.Service.
// Reply) already rejects this before enqueuing, so this is a defense-in-depth
// backstop the worker treats as permanent (log + drop, never retry) rather
// than a transient send failure.
var ErrInboxNoInbound = errors.New("coreapi: thread has no inbound message")

// InboxReplyJob is everything internal/worker/inbox's reply-send handler
// needs to build and send one manual reply: the thread's sending mailbox,
// its (unprefixed) subject, the recipient (the latest inbound message's
// From: address), and the threading headers.
//
// It is NOT a coreapi.Client method: resolving it is consumed through the
// narrow, consumer-defined worker/inbox.ReplyCore interface (the same
// "avoid widening Client's ~40-method surface for one call site" trade as
// SenderTransport/TestSendContent), satisfied by the in-process client via
// type assertion at the composition root.
//
// It carries snake_case json tags for the reason the package doc gives: the
// remote transport encodes THIS type rather than a parallel wire struct, so a
// field added here cannot arrive zero-valued at a worker through a mirror
// somebody forgot to update. There is no credential on it.
type InboxReplyJob struct {
	MailboxID string `json:"mailbox_id"`
	// Subject is the thread's raw subject, with NO "Re: " prefix — the worker
	// applies that itself, idempotently, so a thread whose subject already
	// carries one (a synthesized follow-up step) is never doubled to
	// "Re: Re: ".
	Subject string `json:"subject"`
	// ToEmail is the latest inbound message's From: address — the reply
	// recipient.
	ToEmail string `json:"to_email"`
	// InReplyTo is the latest inbound message's Message-ID.
	InReplyTo string `json:"in_reply_to"`
	// References is the thread's message-id chain up to and including
	// InReplyTo, space-joined in chronological order (RFC 5322 References).
	References string `json:"references"`
}

// RecordInboxReplyInput is one delivered manual reply to persist:
// the worker's report to RecordInboxReply after a successful send.
//
// BodyText is the operator's own correspondence, and it travels on the remote
// transport in the REQUEST direction because the thread's history is written
// from it. That is a deliberate exception to "ids in, values out", and it is the
// same body the control plane already stored on the inbox_pending_replies row
// and handed the worker to send — so the wire reveals nothing the caller was not
// already holding. It is never logged on either side.
type RecordInboxReplyInput struct {
	WorkspaceID string `json:"workspace_id"`
	ThreadID    string `json:"thread_id"`
	// MessageID is the provider-returned Message-ID of the delivered reply.
	MessageID string `json:"message_id"`
	FromEmail string `json:"from_email"`
	FromName  string `json:"from_name"`
	ToEmail   string `json:"to_email"`
	// Subject is the FULL subject actually sent (the idempotent "Re: "
	// prefix already applied), stored on the message row as sent.
	Subject  string `json:"subject"`
	BodyText string `json:"body_text"`
}

// ErrInboxPendingNotClaimable is returned by ClaimPendingInboxReply when the row
// cannot be claimed. It covers every reason at once — cancelled by the operator,
// already sent, still waiting for send_after, or held by another worker's live
// lease — because the worker's response to all of them is identical: stop, and
// do not retry. Distinguishing them would only invite a caller to treat one as
// retryable, which is exactly the mistake that double-sends mail.
var ErrInboxPendingNotClaimable = errors.New("coreapi: pending reply is not claimable")

// PendingInboxReply is a deferred manual reply, resolved for delivery: the
// stored body plus everything InboxReplyJob carries.
//
// The body comes from the ROW, never from the task payload — the operator may
// have cancelled between scheduling and now, and only the row knows.
//
// On the remote transport the claim and the body cross TOGETHER, in one
// response, which is what makes a lost response to this call different from
// every other claim on the seam: the worker loses the lease AND the content at
// once. See internal/coreapi/remote/inboxsends.go for what the retry then does.
type PendingInboxReply struct {
	ThreadID string        `json:"thread_id"`
	BodyText string        `json:"body_text"`
	Job      InboxReplyJob `json:"job"`
}

// PendingInboxCompose is a deferred composed email, resolved for delivery. It
// carries its own recipients and subject rather than deriving them from a
// thread, which is the whole difference from PendingInboxReply.
type PendingInboxCompose struct {
	MailboxID string   `json:"mailbox_id"`
	ToEmails  []string `json:"to_emails"`
	CcEmails  []string `json:"cc_emails"`
	BccEmails []string `json:"bcc_emails"`
	Subject   string   `json:"subject"`
	BodyText  string   `json:"body_text"`
}
