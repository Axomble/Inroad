// Package sender is the execution-plane email send handler.
package sender

import (
	"context"
	"encoding/json"
	"time"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
)

// Sender sends one email over SMTP. Defined here (consumer side) so the handler
// depends on the behavior, not the concrete *mail.NetSender — which lets tests
// inject a fake and exercise the full pipeline without a live SMTP server.
type Sender interface {
	Send(cfg mail.SMTPConfig, msg mail.Message) (messageID string, err error)
}

// Handler returns an asynq handler for send:email tasks.
func Handler(core coreapi.Client, sender Sender, enq *queue.Client) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var p queue.SendEmailPayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return err
		}
		job, err := core.GetSendJob(ctx, p.SendID)
		if err != nil {
			return err
		}
		if job.Suppressed {
			return core.MarkSend(ctx, p.SendID, coreapi.SendResult{Status: "skipped"})
		}
		if job.SentToday >= job.EffectiveDailyCap {
			// Over today's cap: retry in the next daily window, leave status queued.
			return enq.EnqueueSendIn(p.SendID, 6*time.Hour)
		}

		subject := personalize(job.Subject, job.FirstName, job.ToEmail)
		bodyText := withUnsubText(personalize(job.BodyText, job.FirstName, job.ToEmail), job.UnsubURL)
		bodyHTML := ""
		if job.BodyHTML != "" {
			bodyHTML = withUnsubHTML(personalize(job.BodyHTML, job.FirstName, job.ToEmail), job.UnsubURL)
		}

		msgID, sendErr := sender.Send(
			mail.SMTPConfig{Host: job.SMTPHost, Port: job.SMTPPort, Username: job.SMTPUsername, Password: job.SMTPPassword, UseTLS: job.UseTLS},
			mail.Message{
				FromEmail: job.FromEmail, FromName: job.FromName, To: job.ToEmail,
				Subject: subject, BodyText: bodyText, BodyHTML: bodyHTML, ListUnsubscribe: job.UnsubURL,
			},
		)
		if sendErr != nil {
			return core.MarkSend(ctx, p.SendID, coreapi.SendResult{Status: "failed", Err: sendErr.Error()})
		}
		return core.MarkSend(ctx, p.SendID, coreapi.SendResult{Status: "sent", MessageID: msgID})
	}
}

func withUnsubText(body, url string) string {
	if url == "" {
		return body
	}
	return body + "\n\n---\nUnsubscribe: " + url
}

func withUnsubHTML(body, url string) string {
	if url == "" {
		return body
	}
	return body + `<hr><p style="font-size:12px;color:#888">` +
		`<a href="` + url + `">Unsubscribe</a></p>`
}
