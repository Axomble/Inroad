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
type InboxReplyJob struct {
	MailboxID string
	// Subject is the thread's raw subject, with NO "Re: " prefix — the worker
	// applies that itself, idempotently, so a thread whose subject already
	// carries one (a synthesized follow-up step) is never doubled to
	// "Re: Re: ".
	Subject string
	// ToEmail is the latest inbound message's From: address — the reply
	// recipient.
	ToEmail string
	// InReplyTo is the latest inbound message's Message-ID.
	InReplyTo string
	// References is the thread's message-id chain up to and including
	// InReplyTo, space-joined in chronological order (RFC 5322 References).
	References string
}

// RecordInboxReplyInput is one delivered manual reply to persist:
// the worker's report to RecordInboxReply after a successful send.
type RecordInboxReplyInput struct {
	WorkspaceID string
	ThreadID    string
	// MessageID is the provider-returned Message-ID of the delivered reply.
	MessageID string
	FromEmail string
	FromName  string
	ToEmail   string
	// Subject is the FULL subject actually sent (the idempotent "Re: "
	// prefix already applied), stored on the message row as sent.
	Subject  string
	BodyText string
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
type PendingInboxReply struct {
	ThreadID string
	BodyText string
	Job      InboxReplyJob
}

// PendingInboxCompose is a deferred composed email, resolved for delivery. It
// carries its own recipients and subject rather than deriving them from a
// thread, which is the whole difference from PendingInboxReply.
type PendingInboxCompose struct {
	MailboxID string
	ToEmails  []string
	CcEmails  []string
	BccEmails []string
	Subject   string
	BodyText  string
}
