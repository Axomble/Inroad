// Package sequence is the execution-plane multi-step sequencing engine: the
// sequence:advance handler sends a contact's next due step and schedules the
// following one (lazy chain), and the enrollment sweeper reconciles anything
// left behind.
package sequence

import (
	"context"
	"encoding/json"
	"time"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/worker/personalize"
)

// Sender sends one email over SMTP (same contract as the direct sender).
// Defined here so tests inject a fake and exercise the pipeline without a live
// server.
type Sender interface {
	Send(cfg mail.SMTPConfig, msg mail.Message) (messageID string, err error)
}

// Enqueuer schedules the next advance. Satisfied by *queue.Client.
type Enqueuer interface {
	EnqueueAdvanceAt(enrollmentID, workspaceID string, t time.Time) error
	EnqueueAdvanceIn(enrollmentID, workspaceID string, d time.Duration) error
}

// capBackoff is how long to wait before retrying an enrollment blocked by the
// mailbox's daily cap. Matches the direct sender's 6h re-enqueue.
const capBackoff = 6 * time.Hour

// stopReasonSuppressed mirrors enrollment.StopSuppressed. Duplicated as a
// string constant here because the worker reaches the enrollment domain only
// through coreapi (app/* isolation); the value must stay identical.
const stopReasonSuppressed = "suppressed"

// AdvanceHandler returns an asynq handler for sequence:advance tasks. It owns
// the whole step lifecycle: fetch the due step, personalize, build a threaded
// MIME message, send over SMTP, record the result + advance the cursor, and
// (lazy chain) schedule the next step — or stop.
func AdvanceHandler(core coreapi.Client, sender Sender, enq Enqueuer) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var p queue.AdvancePayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return err
		}
		job, err := core.GetStepSendJob(ctx, p.EnrollmentID, p.WorkspaceID)
		if err != nil {
			return err
		}
		defer zeroize(job.SMTPPassword)

		// Enrollment no longer active (stopped/completed) or no next step.
		if job.Skip {
			return nil
		}
		if job.Suppressed {
			return core.MarkStepStopped(ctx, p.EnrollmentID, p.WorkspaceID, stopReasonSuppressed)
		}
		if job.SentToday >= job.EffectiveDailyCap {
			// Over today's cap: retry this enrollment later; leave it active and
			// its cursor unchanged so the same step is re-attempted.
			return enq.EnqueueAdvanceIn(p.EnrollmentID, p.WorkspaceID, capBackoff)
		}

		vars := personalize.Vars{
			FirstName: job.Vars.FirstName, LastName: job.Vars.LastName,
			Email: job.Vars.Email, Company: job.Vars.Company, Custom: job.Vars.Custom,
		}
		subject := personalize.Text(job.Subject, vars)
		bodyText := withUnsubText(personalize.Text(job.BodyText, vars), job.UnsubURL)
		bodyHTML := ""
		if job.BodyHTML != "" {
			bodyHTML = withUnsubHTML(personalize.HTML(job.BodyHTML, vars), job.UnsubURL)
		}

		msgID, sendErr := sender.Send(
			mail.SMTPConfig{Host: job.SMTPHost, Port: job.SMTPPort, Username: job.SMTPUsername, Password: string(job.SMTPPassword), UseTLS: job.UseTLS},
			mail.Message{
				FromEmail: job.FromEmail, FromName: job.FromName, To: job.ToEmail,
				Subject: subject, BodyText: bodyText, BodyHTML: bodyHTML,
				ListUnsubscribe: job.UnsubURL, InReplyTo: job.InReplyTo, References: job.References,
			},
		)
		res := coreapi.StepResult{Status: "sent", MessageID: msgID}
		if sendErr != nil {
			// Record the failure and advance the cursor so a single failing step
			// doesn't wedge the enrollment forever. NOTE: this "fail-forward"
			// choice is inferred (spec §4.2 not available) — a retry-on-transient
			// policy may be preferred; revisit.
			res = coreapi.StepResult{Status: "failed", Err: sendErr.Error()}
		}
		adv, err := core.MarkStepSent(ctx, p.EnrollmentID, p.WorkspaceID, res)
		if err != nil {
			return err
		}
		if !adv.Completed {
			return enq.EnqueueAdvanceAt(p.EnrollmentID, p.WorkspaceID, adv.NextDueAt)
		}
		return nil
	}
}

// zeroize overwrites the decrypted SMTP password in place after use.
func zeroize(b []byte) {
	for i := range b {
		b[i] = 0
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
