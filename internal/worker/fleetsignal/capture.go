// Package fleetsignal is the execution-plane half of per-worker provider signal
// collection: it observes what mailbox providers tell THIS worker, and flushes
// the accumulated deltas through coreapi.
//
// It captures by DECORATION rather than by editing each handler. Five separate
// code paths send mail (sequence:advance, warmup:tick, warmup:engage's reply,
// testsend:send, and the inbox package's manual reply/compose sends) and all
// five consume the same injected send seam, so one wrapper at the composition
// root observes every one of them — and a sixth added later is observed with no
// change here. Editing five handlers instead would put telemetry inside the
// delivery logic, where a future edit can drop it silently.
//
// Nothing in this package can fail an operation. Every method returns the inner
// result unchanged and records afterwards; a nil collector (signals disabled) is
// a straight pass-through.
//
// LAYERING: this reaches the database only through coreapi (see flush.go).
// internal/worker never imports platform/db, and a depguard rule enforces it.
package fleetsignal

import (
	"context"

	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/providersignal"
)

// Sender is the send seam every worker handler already consumes (it is
// structurally identical to sequence.Sender, warmup.Sender, testsend.Mailer and
// inbox.Mailer — each domain declares its own, which is what lets one decorator
// satisfy all of them).
type Sender interface {
	Send(ctx context.Context, tj mail.OutboundJob, msg mail.Message) (messageID string, err error)
}

type sender struct {
	inner     Sender
	collector *providersignal.Collector
}

// NewSender wraps inner so every send's provider answer is classified and
// counted against this worker. A nil collector returns a pass-through, so
// "signals disabled" is the same code path minus the map write.
func NewSender(inner Sender, collector *providersignal.Collector) Sender {
	return &sender{inner: inner, collector: collector}
}

// Send delegates, then records. The result — message id AND error — is returned
// untouched: classification reads the error, it never replaces or swallows it,
// so retry decisions downstream (mail.Retryable) see exactly what the transport
// produced.
func (s *sender) Send(ctx context.Context, tj mail.OutboundJob, msg mail.Message) (string, error) {
	messageID, err := s.inner.Send(ctx, tj, msg)
	observe(s.collector, providersignal.NormalizeProvider(tj.Provider), providersignal.OpSend, err)
	return messageID, err
}

type inboxReader struct {
	inner     mail.InboxReader
	collector *providersignal.Collector
}

// NewInboxReader wraps inner so every poll's provider answer is classified. A
// poll delivers no mail, but it AUTHENTICATES to the provider from this worker's
// egress IP on every tick, which is the same per-IP exposure a send has.
//
// This wraps the IMAP reader specifically — the seam the worker is handed at its
// composition root. The Gmail and Graph readers are constructed inside
// inbox.RegisterPerMessage and are not observed here; see flush.go's package doc
// for what that leaves unmeasured.
func NewInboxReader(inner mail.InboxReader, collector *providersignal.Collector) mail.InboxReader {
	return &inboxReader{inner: inner, collector: collector}
}

func (r *inboxReader) Fetch(ctx context.Context, cfg mail.IMAPConfig, sinceUID uint32, maxN int) ([]mail.InboundMessage, uint32, error) {
	msgs, uidValidity, err := r.inner.Fetch(ctx, cfg, sinceUID, maxN)
	observe(r.collector, providersignal.ProviderSMTP, providersignal.OpPoll, err)
	return msgs, uidValidity, err
}

func (r *inboxReader) CurrentState(ctx context.Context, cfg mail.IMAPConfig) (uint32, uint32, error) {
	uidValidity, uidNext, err := r.inner.CurrentState(ctx, cfg)
	observe(r.collector, providersignal.ProviderSMTP, providersignal.OpPoll, err)
	return uidValidity, uidNext, err
}

// observe is the single classification point. It exists so the nil-collector
// check and the Classify call are written once: three call sites repeating them
// is three places for a future edit to drop one.
func observe(c *providersignal.Collector, p providersignal.Provider, op providersignal.Operation, err error) {
	if c == nil {
		return
	}
	c.Observe(p, op, providersignal.Classify(err))
}
