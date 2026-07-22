package inprocess

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/unsub"
)

// GetSendJob joins the send/campaign/contact/mailbox rows, decrypts the SMTP
// password, checks suppression, and computes today's ramp-aware send cap.
// workspaceID is pinned in the SQL WHERE so a task from a different tenant
// (or a corrupted payload) yields a not-found error rather than reading
// another workspace's row.
func (c client) GetSendJob(ctx context.Context, sendID, workspaceID string) (coreapi.SendJob, error) {
	id, err := uuid.Parse(sendID)
	if err != nil {
		return coreapi.SendJob{}, err
	}
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return coreapi.SendJob{}, err
	}
	b, err := c.q.GetSendBundle(ctx, gen.GetSendBundleParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return coreapi.SendJob{}, err
	}
	// Belt-and-braces: the SQL pin already guarantees this, but if a future
	// migration ever relaxes the WHERE clause this assertion still fails
	// closed instead of leaking another tenant's row.
	if b.WorkspaceID != ws {
		return coreapi.SendJob{}, coreapi.ErrCrossTenant
	}
	password, err := c.sealer.Open(b.SecretCiphertext)
	if err != nil {
		return coreapi.SendJob{}, err
	}
	suppressed, err := c.q.IsSuppressed(ctx, gen.IsSuppressedParams{
		WorkspaceID: b.WorkspaceID,
		Lower:       b.ToEmail,
	})
	if err != nil {
		return coreapi.SendJob{}, err
	}
	sentToday, err := c.q.CountSentToday(ctx, b.MailboxID)
	if err != nil {
		return coreapi.SendJob{}, err
	}
	ageDays := int(time.Since(b.MailboxCreatedAt.Time).Hours() / 24)
	cap := effectiveCap(int(b.DailyCap), int(b.RampStartCap), int(b.RampDays), b.RampEnabled, ageDays)
	token := unsub.MakeToken(c.jwtSecret, b.WorkspaceID.String(), b.ToEmail)

	return coreapi.SendJob{
		SendID:            sendID,
		WorkspaceID:       b.WorkspaceID.String(),
		Attempts:          int(b.Attempts),
		Suppressed:        suppressed,
		EffectiveDailyCap: cap,
		SentToday:         int(sentToday),
		ToEmail:           b.ToEmail,
		FirstName:         b.FirstName,
		Subject:           b.Subject,
		BodyText:          b.BodyText,
		BodyHTML:          b.BodyHtml,
		TrackingEnabled:   b.TrackingEnabled,
		UnsubURL:          c.publicURL + "/u/" + token,
		FromEmail:         b.FromEmail,
		FromName:          b.FromName,
		SMTPHost:          b.SmtpHost,
		SMTPPort:          int(b.SmtpPort),
		SMTPUsername:      b.SmtpUsername,
		SMTPPassword:      password,
		UseTLS:            b.UseTls,
	}, nil
}

// IncrementSendAttempts bumps the counter and returns the new value.
// workspaceID is pinned so a cross-tenant task can't skew another
// workspace's counter.
func (c client) IncrementSendAttempts(ctx context.Context, sendID, workspaceID string) (int, error) {
	id, err := uuid.Parse(sendID)
	if err != nil {
		return 0, err
	}
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return 0, err
	}
	n, err := c.q.IncrementSendAttempts(ctx, gen.IncrementSendAttemptsParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// MarkSend records the outcome of a send attempt. workspaceID is pinned
// alongside sendID so a stray/spoofed task can't clobber a row in another
// tenant.
func (c client) MarkSend(ctx context.Context, sendID, workspaceID string, res coreapi.SendResult) error {
	id, err := uuid.Parse(sendID)
	if err != nil {
		return err
	}
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		return err
	}
	return c.q.SetSendResult(ctx, gen.SetSendResultParams{
		ID:          id,
		Status:      res.Status,
		MessageID:   res.MessageID,
		Error:       res.Err,
		WorkspaceID: ws,
	})
}

// ListStuckQueuedSends returns (send id, workspace id) pairs stuck in
// 'queued' longer than the reconcile window. Consumed by the periodic
// sweeper.
func (c client) ListStuckQueuedSends(ctx context.Context) ([]coreapi.StuckSend, error) {
	rows, err := c.q.ListStuckQueuedSends(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]coreapi.StuckSend, len(rows))
	for i, r := range rows {
		out[i] = coreapi.StuckSend{SendID: r.ID.String(), WorkspaceID: r.WorkspaceID.String()}
	}
	return out, nil
}
