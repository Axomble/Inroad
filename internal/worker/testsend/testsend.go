// Package testsend is the execution-plane handler for testsend:send tasks:
// render one sequence step for a preview recipient through the SAME
// personalize renderer production sends use, then deliver it through the
// resolved mailbox's transport. Nothing here writes a sends row, rewrites
// tracking links, or sets a List-Unsubscribe header -- a test-send is not
// subject to the real suppression/tracking machinery.
//
// This is the execution-plane half of POST /campaigns/{id}/test-send: the API
// (internal/app/campaign) does every synchronous validation (campaign/step
// ownership, rate limit, an eligible sender exists) and enqueues; decrypting
// the mailbox credential and dialing the provider happen ONLY here
// (docs/security.md invariant 1 — the control plane never does either).
package testsend

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hibiken/asynq"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/worker/personalize"
)

// testSendSubjectPrefix marks a test-send subject so the recipient (and any
// spam/compliance review) can tell it apart from a real campaign send at a
// glance.
const testSendSubjectPrefix = "[Test] "

// Core is the narrow coreapi capability this handler needs: load one
// test-send's raw step content + preview vars, and resolve the sending
// mailbox's decrypted transport. Defined here (consumer side) -- the same
// "avoid widening coreapi.Client's ~40-method surface for one call site"
// trade as worker/deliverability.Breaker and worker/maintenance.Cleaner --
// and satisfied by the in-process client via type assertion at the
// composition root (internal/worker/handlers.go).
type Core interface {
	// GetTestSendContent loads the step's raw content and the preview
	// personalization vars (or the synthetic fallback), workspace-pinned.
	GetTestSendContent(ctx context.Context, workspaceID, campaignID, stepID string) (coreapi.TestSendContent, error)
	// ResolveSenderTransport decrypts the resolved mailbox's send credential
	// (refreshing an OAuth token for gmail/m365), workspace-pinned.
	ResolveSenderTransport(ctx context.Context, workspaceID, mailboxID string) (coreapi.SenderTransport, error)
}

// Mailer sends one email through whichever transport the resolved
// SenderTransport selects. Satisfied by *mail.MultiSender in production;
// defined here so tests inject a fake instead of dialing a real server.
type Mailer interface {
	Send(ctx context.Context, tj mail.OutboundJob, msg mail.Message) (messageID string, err error)
}

// Handler returns an asynq handler for testsend:send tasks.
func Handler(core Core, sender Mailer) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var p queue.TestSendPayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return err
		}

		content, err := core.GetTestSendContent(ctx, p.WorkspaceID, p.CampaignID, p.StepID)
		if err != nil {
			return fmt.Errorf("load test-send content: %w", err)
		}
		transport, err := core.ResolveSenderTransport(ctx, p.WorkspaceID, p.MailboxID)
		if err != nil {
			return fmt.Errorf("resolve sender transport: %w", err)
		}
		// Zeroize the decrypted secret(s) before returning, mirroring every other
		// send handler (internal/worker/sender, internal/worker/sequence): the
		// primary long-lived buffer this handler allocated should not linger past
		// it in memory.
		defer zeroize(transport.SMTPPassword)
		defer zeroize(transport.AccessToken)

		// {{first_name}}/{{company}} substitution through the SAME renderer
		// production sends use: HTML-escaped in the HTML body, raw in text/subject
		// (personalize.Text vs personalize.HTML — see personalize.go).
		vars := personalize.Vars{FirstName: content.FirstName, Company: content.Company}
		_, err = sender.Send(ctx,
			mail.OutboundJob{
				Provider: transport.Provider, Host: transport.SMTPHost, Port: transport.SMTPPort,
				Username: transport.SMTPUsername, Password: string(transport.SMTPPassword),
				AllowPlaintext: transport.AllowPlaintext, AccessToken: string(transport.AccessToken),
			},
			mail.Message{
				FromEmail: transport.FromEmail, FromName: transport.FromName, To: p.To,
				Subject:  testSendSubjectPrefix + personalize.Text(content.Subject, vars),
				BodyText: personalize.Text(content.BodyText, vars),
				BodyHTML: personalize.HTML(content.BodyHTML, vars),
				// ListUnsubscribe / InReplyTo / References deliberately left empty:
				// a test-send is never subject to the real unsubscribe/suppression/
				// threading machinery, and nothing is persisted to sends.
			},
		)
		return err
	}
}

// zeroize overwrites b in place. Go strings are immutable so this only works
// on the []byte form, mirroring sender.zeroize/sequence's equivalent.
func zeroize(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
