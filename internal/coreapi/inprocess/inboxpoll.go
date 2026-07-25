package inprocess

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/app/enrollment"
	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// enrollmentRefID maps the nullable enrollment_id column returned by
// GetSendByMessageID to a coreapi.SendRef.EnrollmentID: "" when the matched
// send has no enrollment (the legacy direct-send path), the UUID string
// otherwise.
func enrollmentRefID(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return uuid.UUID(id.Bytes).String()
}

// ListActiveMailboxes returns (id, workspace id) pairs for every mailbox
// eligible for inbox polling. Consumed by the periodic poll-queue enqueuer.
func (c client) ListActiveMailboxes(ctx context.Context) ([]coreapi.MailboxRef, error) {
	rows, err := c.q.ListActiveMailboxes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]coreapi.MailboxRef, len(rows))
	for i, r := range rows {
		out[i] = coreapi.MailboxRef{ID: r.ID.String(), WorkspaceID: r.WorkspaceID.String()}
	}
	return out, nil
}

// GetInboxPollJob loads everything the inbox poller needs to open one
// mailbox's IMAP connection and resume from its stored cursor: connection
// details, decrypted credential, and (LastSeenUID, UIDValidity). workspaceID
// is pinned in the SQL WHERE (defense in depth on the unguessable mailbox
// UUID).
func (c client) GetInboxPollJob(ctx context.Context, mailboxID, workspaceID string) (coreapi.InboxPollJob, error) {
	id, err := uuid.Parse(mailboxID)
	if err != nil {
		return coreapi.InboxPollJob{}, err
	}
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return coreapi.InboxPollJob{}, err
	}
	m, err := c.q.GetMailbox(ctx, gen.GetMailboxParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return coreapi.InboxPollJob{}, err
	}
	// Belt-and-braces: the SQL pin already guarantees this, but if a future
	// migration ever relaxes the WHERE clause this assertion still fails
	// closed instead of leaking another tenant's row.
	if m.WorkspaceID != ws {
		return coreapi.InboxPollJob{}, coreapi.ErrCrossTenant
	}
	// Transport dispatch on the mailbox provider (parallel to GetStepSendJob):
	// the API providers (gmail, m365) resolve a refreshed short-lived access token
	// and resume from the opaque inbox_cursor (Gmail historyId / Graph delta-link),
	// leaving the IMAP UID cursor columns zero; smtp unseals the stored IMAP
	// password and resumes from the UID cursor.
	if m.Provider == "gmail" || m.Provider == "m365" {
		at, err := c.oauthAccessToken(ctx, m.Provider, id, ws, m.SecretCiphertext, c.oauthConfigFor(m.Provider))
		if err != nil {
			return coreapi.InboxPollJob{}, err
		}
		return coreapi.InboxPollJob{
			Provider: m.Provider, AccessToken: []byte(at), Cursor: m.InboxCursor,
		}, nil
	}
	sealer, err := c.keyring.SealerFor(ctx, ws)
	if err != nil {
		return coreapi.InboxPollJob{}, err
	}
	password, err := sealer.Open(m.SecretCiphertext)
	if err != nil {
		return coreapi.InboxPollJob{}, err
	}
	return coreapi.InboxPollJob{
		Provider: "smtp",
		Host:     m.ImapHost, Port: int(m.ImapPort), Username: m.ImapUsername, Password: password,
		UseTLS: m.UseTls, LastSeenUID: uint32(m.InboxLastSeenUid), UIDValidity: uint32(m.InboxUidValidity),
	}, nil
}

// SetInboxCursor persists the IMAP poll cursor after a poll pass, workspace-pinned.
func (c client) SetInboxCursor(ctx context.Context, mailboxID, workspaceID string, lastSeenUID, uidValidity uint32) error {
	id, err := uuid.Parse(mailboxID)
	if err != nil {
		return err
	}
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return err
	}
	return c.q.SetInboxCursor(ctx, gen.SetInboxCursorParams{
		ID: id, WorkspaceID: ws,
		InboxLastSeenUid: int64(lastSeenUID), InboxUidValidity: int64(uidValidity),
	})
}

// SetInboxCursorString persists the opaque provider cursor (Gmail historyId)
// after a poll pass, workspace-pinned. The IMAP UID cursor columns are left
// untouched so the two transports never clobber each other's cursor.
func (c client) SetInboxCursorString(ctx context.Context, mailboxID, workspaceID, cursor string) error {
	id, err := uuid.Parse(mailboxID)
	if err != nil {
		return err
	}
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return err
	}
	return c.q.SetInboxCursorString(ctx, gen.SetInboxCursorStringParams{
		ID: id, WorkspaceID: ws, InboxCursor: cursor,
	})
}

// FindSendByMessageID matches an inbound reply/bounce's Message-ID back to the
// send that caused it, workspace-scoped. Returns ErrNoMatch when nothing
// matches (unknown Message-ID — e.g. a reply to a message this workspace
// never sent).
func (c client) FindSendByMessageID(ctx context.Context, workspaceID, messageID string) (coreapi.SendRef, error) {
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return coreapi.SendRef{}, err
	}
	row, err := c.q.GetSendByMessageID(ctx, gen.GetSendByMessageIDParams{WorkspaceID: ws, MessageID: messageID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return coreapi.SendRef{}, coreapi.ErrNoMatch
		}
		return coreapi.SendRef{}, err
	}
	return coreapi.SendRef{
		SendID:       row.ID.String(),
		EnrollmentID: enrollmentRefID(row.EnrollmentID),
		ContactEmail: row.ToEmail,
	}, nil
}

// MarkReplied halts the enrollment on an inbound reply and tags it with the
// classified reply. A no-op when enrollmentID is "" (the matched send has no
// enrollment — the legacy direct-send path has nothing to stop or tag).
func (c client) MarkReplied(ctx context.Context, enrollmentID, workspaceID, replyClass, replySource string, confidence float64) error {
	if enrollmentID == "" {
		return nil
	}
	eid, err := uuid.Parse(enrollmentID)
	if err != nil {
		return err
	}
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return err
	}
	if err := c.enroll.MarkStepStopped(ctx, ws, eid, enrollment.StopReplied); err != nil {
		return err
	}
	return c.recordReplyClass(ctx, eid, ws, replyClass, replySource, confidence)
}

// RecordReplyClass tags the enrollment with a classified reply without touching
// its status — for automated replies (auto_reply/out_of_office) that must not
// halt the sequence. A no-op when enrollmentID is "" (nothing to tag).
func (c client) RecordReplyClass(ctx context.Context, enrollmentID, workspaceID, class, source string, confidence float64) error {
	if enrollmentID == "" {
		return nil
	}
	eid, err := uuid.Parse(enrollmentID)
	if err != nil {
		return err
	}
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return err
	}
	return c.recordReplyClass(ctx, eid, ws, class, source, confidence)
}

// MarkUnsubscribed suppresses the address and, when the reply belongs to an
// enrollment, halts it (reason unsubscribed) and tags it class=unsubscribe.
// The suppression insert is the SAME workspace-scoped, idempotent
// (ON CONFLICT DO NOTHING) one MarkBounced uses — only the reason literal
// differs ("unsubscribe"). Suppression happens EVEN WHEN enrollmentID is ""
// (a reply-unsubscribe to a legacy direct-send must still suppress the address
// — compliance).
func (c client) MarkUnsubscribed(ctx context.Context, enrollmentID, workspaceID, email string) error {
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return err
	}
	// Compliance first: the opt-out suppression is the load-bearing write, so it
	// runs BEFORE the enrollment stop/tag — a failure there can never skip it.
	// The insert is idempotent (ON CONFLICT DO NOTHING), so ordering it first is
	// safe even when the stop/tag below also runs.
	if err := c.q.AddSuppression(ctx, gen.AddSuppressionParams{WorkspaceID: ws, Email: email, Reason: "unsubscribe"}); err != nil {
		return err
	}
	if enrollmentID == "" {
		return nil
	}
	eid, err := uuid.Parse(enrollmentID)
	if err != nil {
		return err
	}
	if err := c.enroll.MarkStepStopped(ctx, ws, eid, enrollment.StopUnsubscribed); err != nil {
		return err
	}
	return c.recordReplyClass(ctx, eid, ws, "unsubscribe", "", 0)
}

// recordReplyClass persists the classified reply (class/source/confidence +
// replied_at=now()) on the enrollment, workspace-pinned. Shared by MarkReplied,
// RecordReplyClass and MarkUnsubscribed. Confidence narrows to the real column's
// float32.
//
// An empty class/source is stored as SQL NULL (untagged), NOT "": the 000014
// CHECK pins reply_class to NULL or one of the 7 classes, so writing "" would
// be rejected — the interim untagged MarkReplied(...,"","",0) on the enrolled
// path relies on this to stop-without-tag safely until Task 4 wires real classes.
func (c client) recordReplyClass(ctx context.Context, eid, ws uuid.UUID, class, source string, confidence float64) error {
	conf := float32(confidence)
	return c.q.SetEnrollmentReplyClass(ctx, gen.SetEnrollmentReplyClassParams{
		ID: eid, WorkspaceID: ws,
		ReplyClass: nilIfEmpty(class), ReplySource: nilIfEmpty(source), ReplyConfidence: &conf,
	})
}

// nilIfEmpty maps an empty string to a nil *string (SQL NULL) and any non-empty
// string to a pointer to it. Used so an untagged reply writes NULL reply_class/
// reply_source rather than "" (which the 000014 reply_class CHECK rejects).
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// MarkBounced records a hard bounce: halts the enrollment (if any) and
// suppresses the address so no future step or campaign emails it again. Soft
// bounces are handled by the caller (logged, no action) — MarkBounced is only
// called with hard=true; the flag is kept on the signature so that stays an
// explicit, visible decision at the call site rather than an implicit one.
func (c client) MarkBounced(ctx context.Context, enrollmentID, workspaceID, email string, hard bool) error {
	if !hard {
		return nil
	}
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return err
	}
	if enrollmentID != "" {
		eid, err := uuid.Parse(enrollmentID)
		if err != nil {
			return err
		}
		if err := c.enroll.MarkStepStopped(ctx, ws, eid, enrollment.StopBounced); err != nil {
			return err
		}
	}
	// Reason literal is "bounce", not "bounced" — the suppression CHECK
	// constraint allows 'unsubscribe','bounce','manual' (distinct from the
	// enrollment stop_reason vocabulary used just above).
	return c.q.AddSuppression(ctx, gen.AddSuppressionParams{WorkspaceID: ws, Email: email, Reason: "bounce"})
}
