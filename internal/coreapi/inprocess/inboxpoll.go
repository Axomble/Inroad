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
	// gmail resolves a refreshed short-lived access token and resumes from the
	// opaque inbox_cursor (historyId), leaving the IMAP UID cursor columns zero;
	// smtp unseals the stored IMAP password and resumes from the UID cursor.
	if m.Provider == "gmail" {
		at, err := c.gmailAccessToken(ctx, id, ws, m.SecretCiphertext)
		if err != nil {
			return coreapi.InboxPollJob{}, err
		}
		return coreapi.InboxPollJob{
			Provider: "gmail", AccessToken: []byte(at), Cursor: m.InboxCursor,
		}, nil
	}
	password, err := c.sealer.Open(m.SecretCiphertext)
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

// MarkReplied halts the enrollment on an inbound reply. A no-op when
// enrollmentID is "" (the matched send has no enrollment — the legacy
// direct-send path has nothing to stop).
func (c client) MarkReplied(ctx context.Context, enrollmentID, workspaceID string) error {
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
	return c.enroll.MarkStepStopped(ctx, ws, eid, enrollment.StopReplied)
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
