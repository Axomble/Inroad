package campaign

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/mail"
)

// Sentinel errors TestSend returns, mapped to HTTP status codes by the
// handler.
var (
	ErrStepNotFound        = errors.New("sequence step not found")
	ErrNoEligibleSender    = errors.New("no enabled sender with an active mailbox")
	ErrTestSendRateLimited = errors.New("too many test sends; try again shortly")
)

// testSendSubjectPrefix marks a test-send subject so the recipient (and any
// spam/compliance review) can tell it apart from a real campaign send at a
// glance.
const testSendSubjectPrefix = "[Test] "

// Fallback personalization vars used when the campaign's list has no contact
// yet, so an operator can preview a template before adding an audience.
const (
	testSendFallbackFirstName = "Alex"
	testSendFallbackCompany   = "Acme"
)

// testSendRateLimit / testSendRateLimitWindow bound test-send: 5 per
// workspace per minute. Reuses the SAME Redis-backed fixed-window limiter the
// auth throttles use (ratelimit.RedisLimiter, wired via RateLimiter below), so
// the cap holds across every API server instance rather than being per-process.
const (
	testSendRateLimit       = 5
	testSendRateLimitWindow = time.Minute
)

// SenderTransport is one resolved mailbox's from-identity plus its decrypted
// send credential (mail.OutboundJob, embedded). Credentials never reach this
// package as ciphertext: decrypting -- and, for an OAuth mailbox, refreshing --
// the secret happens only inside the SenderResolver's wiring.
type SenderTransport struct {
	FromEmail string
	FromName  string
	mail.OutboundJob
}

// SenderResolver resolves one mailbox's decrypted send transport for an ad
// hoc test-send, workspace-scoped. It is NOT a coreapi.Client method: that
// interface's ~40 methods are all shaped around an existing send/enrollment/
// warmup row, and none fit "no sends row exists yet, resolve this mailbox's
// transport directly". Consumed through this narrow, consumer-defined
// interface instead -- the same "avoid widening Client for one call site"
// trade documented on coreapi.BreakerResult -- and satisfied at wiring time in
// cmd/inroad by the coreapi in-process client (type assertion), so decrypting
// the credential (and refreshing an OAuth token) reuses the ONE existing
// implementation of that rather than a second copy (security invariants 8/9).
type SenderResolver interface {
	ResolveSenderTransport(ctx context.Context, ws, mailboxID uuid.UUID) (SenderTransport, error)
}

// Mailer sends one email through whichever transport the resolved
// SenderTransport selects. Satisfied by *mail.MultiSender in production;
// defined here (consumer side) so tests inject a fake instead of dialing a
// real server. Test-send calls this directly -- it never writes a sends row,
// never rewrites tracking links, and never sets a List-Unsubscribe header.
type Mailer interface {
	Send(ctx context.Context, tj mail.OutboundJob, msg mail.Message) (messageID string, err error)
}

// RateLimiter reports whether one more request under key is permitted within
// the current window, given a cap of limit requests per window. Satisfied by
// *ratelimit.RedisLimiter (the same limiter backing the auth throttles).
type RateLimiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error)
}

// TestSend renders one sequence step for a preview recipient and sends it
// immediately through the campaign's first enabled+active pool mailbox.
// Deliberately NOT the production send path: nothing is persisted to sends,
// tracking links are never rewritten, and no List-Unsubscribe header is set --
// a test-send is not subject to the real suppression/tracking machinery.
//
// Personalization uses the campaign list's first (earliest-added) contact's
// first_name/company; an empty list falls back to synthetic
// first_name=Alex, company=Acme so a template can be previewed before any
// audience exists.
func (s *Service) TestSend(ctx context.Context, ws, campaignID, stepID uuid.UUID, to string) error {
	if _, err := s.store.Get(ctx, ws, campaignID); err != nil {
		return ErrNotFound
	}
	if err := s.checkTestSendRateLimit(ctx, ws); err != nil {
		return err
	}
	steps, err := s.store.ListSteps(ctx, ws, campaignID)
	if err != nil {
		return err
	}
	step, ok := findStep(steps, stepID)
	if !ok {
		return ErrStepNotFound
	}
	sender, err := s.firstEligibleSender(ctx, ws, campaignID)
	if err != nil {
		return err
	}
	if s.sender == nil || s.mailer == nil {
		return fmt.Errorf("campaign: test-send is not configured")
	}
	transport, err := s.sender.ResolveSenderTransport(ctx, ws, sender.MailboxID)
	if err != nil {
		return fmt.Errorf("resolve sender transport: %w", err)
	}
	firstName, company := s.testSendVars(ctx, ws, campaignID)

	_, err = s.mailer.Send(ctx, transport.OutboundJob, mail.Message{
		FromEmail: transport.FromEmail, FromName: transport.FromName, To: to,
		Subject:  testSendSubjectPrefix + renderPreview(step.Subject, firstName, company),
		BodyText: renderPreview(step.BodyText, firstName, company),
		BodyHTML: renderPreview(step.BodyHtml, firstName, company),
		// ListUnsubscribe deliberately left empty: a test-send is never subject
		// to the real unsubscribe/suppression machinery.
	})
	return err
}

// checkTestSendRateLimit fails closed: a limiter error (e.g. an unreachable
// backing store) is treated as "not allowed" rather than silently lifting the
// cap. No limiter wired (nil) is treated as unlimited -- a deployment choice
// made once, at wiring time, in cmd/inroad, not an accidental bypass.
func (s *Service) checkTestSendRateLimit(ctx context.Context, ws uuid.UUID) error {
	if s.limiter == nil {
		return nil
	}
	allowed, err := s.limiter.Allow(ctx, "campaign:test-send:"+ws.String(), testSendRateLimit, testSendRateLimitWindow)
	if err != nil || !allowed {
		return ErrTestSendRateLimited
	}
	return nil
}

// firstEligibleSender returns the pool's first enabled sender whose mailbox
// is active, in the SAME order GetSenders reports the pool (ordered by
// mailbox email) -- "the first enabled+active pool mailbox" per the API
// contract.
func (s *Service) firstEligibleSender(ctx context.Context, ws, campaignID uuid.UUID) (Sender, error) {
	senders, err := s.pool(ctx, ws, campaignID)
	if err != nil {
		return Sender{}, err
	}
	for _, sd := range senders {
		if sd.Enabled && sd.Status == mailboxStatusActive {
			return sd, nil
		}
	}
	return Sender{}, ErrNoEligibleSender
}

// findStep locates a step by id among the campaign's steps.
func findStep(steps []gen.SequenceStep, stepID uuid.UUID) (gen.SequenceStep, bool) {
	for _, st := range steps {
		if st.ID == stepID {
			return st, true
		}
	}
	return gen.SequenceStep{}, false
}

// testSendVars resolves the preview personalization values: the campaign
// list's first contact, or the synthetic fallback when the list is empty (or
// the lookup fails -- a preview must never hard-fail the send over this).
func (s *Service) testSendVars(ctx context.Context, ws, campaignID uuid.UUID) (firstName, company string) {
	fn, co, found, err := s.store.FirstListContact(ctx, ws, campaignID)
	if err != nil || !found {
		return testSendFallbackFirstName, testSendFallbackCompany
	}
	return fn, co
}

// renderPreview does test-send's minimal template substitution:
// {{first_name}} and {{company}} only -- the two vars the API contract's
// fallback defines. It is deliberately NOT internal/worker/personalize's full
// renderer (custom fields, HTML escaping, a "there" fallback, leftover-
// placeholder warnings): that package lives in the execution plane and no
// app/* domain imports internal/worker/* today, so reusing it here would be a
// new, unreviewed layering precedent. If pixel-parity with production
// rendering becomes a requirement, promoting personalize to
// internal/platform/personalize is the follow-up.
func renderPreview(tmpl, firstName, company string) string {
	r := strings.NewReplacer("{{first_name}}", firstName, "{{company}}", company)
	return r.Replace(tmpl)
}
