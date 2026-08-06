package campaign

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Sentinel errors TestSend returns, mapped to HTTP status codes by the
// handler.
var (
	ErrStepNotFound        = errors.New("sequence step not found")
	ErrNoEligibleSender    = errors.New("no enabled sender with an active mailbox")
	ErrTestSendRateLimited = errors.New("too many test sends; try again shortly")
	// ErrRecipientSuppressed is returned when the requested `to` address is on
	// the workspace's suppression list (unsubscribed or bounced): a test-send
	// must never bypass the same suppression check a real send honors
	// (docs/security.md).
	ErrRecipientSuppressed = errors.New("recipient is on the workspace's suppression list")
)

// SuppressionChecker reports whether an address is on the workspace's
// suppression list. Suppression is owned by the suppression app domain;
// app/* packages never import each other (CLAUDE.md), so this narrow,
// consumer-defined interface is satisfied at wiring time in cmd/inroad by
// suppression.Store -- the same shape as DomainAuthReader.
type SuppressionChecker interface {
	// IsSuppressed reports whether email is on ws's suppression list
	// (case-insensitive lookup, backed by the (workspace_id, lower(email))
	// index).
	IsSuppressed(ctx context.Context, ws uuid.UUID, email string) (bool, error)
}

// testSendRateLimit / testSendRateLimitWindow bound test-send: 5 per
// workspace per minute. Reuses the SAME Redis-backed fixed-window limiter the
// auth throttles use (ratelimit.RedisLimiter, wired via RateLimiter below), so
// the cap holds across every API server instance rather than being per-process.
const (
	testSendRateLimit       = 5
	testSendRateLimitWindow = time.Minute
)

// RateLimiter reports whether one more request under key is permitted within
// the current window, given a cap of limit requests per window. Satisfied by
// *ratelimit.RedisLimiter (the same limiter backing the auth throttles).
type RateLimiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error)
}

// TestSendEnqueuer schedules a testsend:send task. Defined here (consumer
// side) with primitive args -- like Enqueuer -- so the domain doesn't depend
// on platform/queue's payload type. Satisfied by *queue.Client.
//
// ARCHITECTURE: decrypting a mailbox credential and dialing a provider must
// happen ONLY in the execution plane (docs/security.md invariant 1 — cmd/inroad
// never opens a Sealer or a provider connection). TestSend therefore does
// every synchronous validation here (campaign/step ownership, rate limit,
// eligible-sender existence) and then ENQUEUES the actual render+send as a
// testsend:send task; internal/worker/testsend resolves the transport
// (through the SAME coreapi credential path every real send uses) and sends.
type TestSendEnqueuer interface {
	EnqueueTestSend(campaignID, stepID, mailboxID, to, workspaceID string) error
}

// TestSend validates a test-send request (campaign/step ownership, rate
// limit, the recipient is not suppressed, an eligible sender exists) and
// enqueues the render+send as a testsend:send task. Nothing is decrypted or
// dialed here -- see TestSendEnqueuer's doc comment. The worker handler
// renders the step for the campaign list's first contact (or the synthetic
// first_name=Alex, company=Acme fallback when the list is empty), prefixes
// the subject with "[Test] ", and sends through the resolved mailbox: nothing
// is persisted to sends, tracking links are never rewritten, and no
// List-Unsubscribe header is set.
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
	if _, ok := findStep(steps, stepID); !ok {
		return ErrStepNotFound
	}
	if err := s.checkRecipientNotSuppressed(ctx, ws, to); err != nil {
		return err
	}
	sender, err := s.firstEligibleSender(ctx, ws, campaignID)
	if err != nil {
		return err
	}
	if s.testSendEnq == nil {
		return errors.New("campaign: test-send is not configured")
	}
	return s.testSendEnq.EnqueueTestSend(campaignID.String(), stepID.String(), sender.MailboxID.String(), to, ws.String())
}

// checkRecipientNotSuppressed rejects a test-send to an address the
// workspace has already unsubscribed or bounced: a test-send must never
// bypass the same suppression list a real send honors (docs/security.md). A
// checker error is propagated (fails closed, like checkTestSendRateLimit) --
// it is never swallowed into "not suppressed". No checker wired (nil) is
// unwired -- a deployment/test choice, like domainAuth/limiter -- but
// cmd/inroad always wires one in production, and internal/worker/testsend
// re-checks independently before dialing as defense in depth against the
// race between this check and an incoming unsubscribe.
func (s *Service) checkRecipientNotSuppressed(ctx context.Context, ws uuid.UUID, to string) error {
	if s.suppression == nil {
		return nil
	}
	suppressed, err := s.suppression.IsSuppressed(ctx, ws, to)
	if err != nil {
		return err
	}
	if suppressed {
		return ErrRecipientSuppressed
	}
	return nil
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
	senders, err := s.loadSenderPool(ctx, ws, campaignID)
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
