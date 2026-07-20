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
func (c client) GetSendJob(ctx context.Context, sendID string) (coreapi.SendJob, error) {
	id, err := uuid.Parse(sendID)
	if err != nil {
		return coreapi.SendJob{}, err
	}
	b, err := c.q.GetSendBundle(ctx, id)
	if err != nil {
		return coreapi.SendJob{}, err
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
		Suppressed:        suppressed,
		EffectiveDailyCap: cap,
		SentToday:         int(sentToday),
		ToEmail:           b.ToEmail,
		FirstName:         b.FirstName,
		Subject:           b.Subject,
		BodyText:          b.BodyText,
		BodyHTML:          b.BodyHtml,
		UnsubURL:          c.publicURL + "/u/" + token,
		FromEmail:         b.FromEmail,
		FromName:          b.FromName,
		SMTPHost:          b.SmtpHost,
		SMTPPort:          int(b.SmtpPort),
		SMTPUsername:      b.SmtpUsername,
		SMTPPassword:      string(password),
		UseTLS:            b.UseTls,
	}, nil
}

// MarkSend records the outcome of a send attempt.
func (c client) MarkSend(ctx context.Context, sendID string, res coreapi.SendResult) error {
	id, err := uuid.Parse(sendID)
	if err != nil {
		return err
	}
	return c.q.SetSendResult(ctx, gen.SetSendResultParams{
		ID:        id,
		Status:    res.Status,
		MessageID: res.MessageID,
		Error:     res.Err,
	})
}

// ListStuckQueuedSends returns send ids stuck in 'queued' longer than the
// reconcile window. Consumed by the periodic sweeper.
func (c client) ListStuckQueuedSends(ctx context.Context) ([]string, error) {
	ids, err := c.q.ListStuckQueuedSends(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out, nil
}
