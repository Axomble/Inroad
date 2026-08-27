package tracking

import (
	"context"
	"log/slog"
	"net/netip"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/botfilter"
	"github.com/inroad/inroad/internal/platform/track"
)

// Hit is one inbound tracking request, reduced to the facts the service needs.
// The handler builds it; everything in it except the token is UNVERIFIED and is
// used only to classify, never to decide tenancy.
type Hit struct {
	// Token is the HMAC-signed open/click token. The ONLY trusted input.
	Token string
	// UserAgent is the raw request header.
	UserAgent string
	// IP is the resolved client address, or the zero Addr when it could not be
	// determined (no trusted proxy configured and an unparseable RemoteAddr).
	IP netip.Addr
	// At is when the request arrived.
	At time.Time
}

// Service implements the tracking use cases: verifying tokens, resolving
// the event's tenant server-side, classifying the hit as human or machine,
// and recording the event. It depends on the Store interface, not the
// sqlc-backed struct.
type Service struct {
	secret []byte
	store  Store
	log    *slog.Logger
}

// NewService builds a Service that verifies tokens with secret and records
// events via store. A nil logger is replaced with a discarding one, so the
// tracking path never nil-panics on a partially wired construction.
func NewService(secret []byte, store Store, log *slog.Logger) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Service{secret: secret, store: store, log: log}
}

// RecordOpen verifies the open token and, if it (and the send it names) are
// valid, classifies and records an 'open' event. A malformed token and an
// unknown send are both silently no-ops: the pixel endpoint must never become
// an oracle a caller can use to probe which send ids exist, so both cases still
// serve the same pixel with no visible difference.
func (s *Service) RecordOpen(ctx context.Context, hit Hit) {
	sendID, send, ok := s.resolve(ctx, func() (string, bool) {
		return track.ParseOpenToken(s.secret, hit.Token)
	})
	if !ok {
		return
	}
	s.record(ctx, hit, sendID, send, botfilter.KindOpen, "")
}

// RecordClick verifies the click token, rejects any redirect target that
// isn't http(s) (the signature only proves the token wasn't tampered with
// -- it does NOT prove the URL it names is safe to redirect to; a token
// minted for a javascript:/data: URL must still be blocked here), resolves
// the send's tenant server-side, and classifies and records the event. ok is
// false for a malformed/tampered token, an unsafe scheme, or an unknown send;
// callers must respond 404 with no redirect and no event in every one of those
// cases.
//
// A MACHINE verdict still redirects. A link scanner that got a 404 would report
// the link as broken, and the human whose mail it is protecting would never
// reach the page — the classification governs REPORTING, not delivery.
func (s *Service) RecordClick(ctx context.Context, hit Hit) (destURL string, ok bool) {
	var rawURL string
	sendID, send, ok := s.resolve(ctx, func() (string, bool) {
		id, u, parsed := track.ParseClickToken(s.secret, hit.Token)
		if !parsed {
			return "", false
		}
		target, err := url.Parse(u)
		if err != nil || (target.Scheme != "http" && target.Scheme != "https") {
			return "", false
		}
		rawURL = u
		return id, true
	})
	if !ok {
		return "", false
	}
	s.record(ctx, hit, sendID, send, botfilter.KindClick, rawURL)
	return rawURL, true
}

// resolve runs a token parser and turns its send id into a resolved send. It
// exists so the open and click paths cannot drift on the rule that matters
// most: the tenant comes from the SENDS ROW, never from the token or request.
func (s *Service) resolve(ctx context.Context, parse func() (string, bool)) (uuid.UUID, Send, bool) {
	sendIDStr, ok := parse()
	if !ok {
		return uuid.Nil, Send{}, false
	}
	sendID, err := uuid.Parse(sendIDStr)
	if err != nil {
		return uuid.Nil, Send{}, false
	}
	send, ok := s.store.ResolveSend(ctx, sendID)
	if !ok {
		return uuid.Nil, Send{}, false
	}
	return sendID, send, true
}

// record classifies the hit and stores it, machine verdicts included.
func (s *Service) record(ctx context.Context, hit Hit, sendID uuid.UUID, send Send, kind botfilter.Kind, rawURL string) {
	verdict, reason := s.classify(ctx, hit, sendID, send, kind)
	err := s.store.RecordEvent(ctx, Event{
		WorkspaceID: send.WorkspaceID,
		CampaignID:  send.CampaignID,
		SendID:      sendID,
		Kind:        kind,
		URL:         rawURL,
		UserAgent:   hit.UserAgent,
		Verdict:     verdict,
		Reason:      reason,
		ClientIP:    hit.IP,
	})
	if err != nil {
		// The response is already decided (a pixel, or a redirect), so there is
		// nothing to return this to and nothing useful to tell the caller. It
		// is logged rather than discarded so a broken tracking write is visible
		// as something other than a mysteriously flat open rate. No token, no
		// URL and no user agent: this endpoint's inputs are recipient data.
		s.log.ErrorContext(ctx, "record tracking event", "error", err, "kind", string(kind), "send_id", sendID)
	}
}

// classify gathers the classifier's inputs and returns its verdict.
//
// A failure to read prior events DEGRADES the classification rather than
// failing the hit: the UA, timing and IP rules still run, only the ordering and
// burst rules go quiet. Failing closed here would mark every event machine
// during a database blip and permanently zero a campaign's open rate; failing
// open at worst lets a scanner's click through, which the other signals mostly
// still catch.
func (s *Service) classify(ctx context.Context, hit Hit, sendID uuid.UUID, send Send, kind botfilter.Kind) (botfilter.Verdict, botfilter.Reason) {
	subnet, _ := botfilter.SubnetKey(hit.IP) // zero Prefix when the IP is unknown; PriorEvents skips the burst count
	prior, err := s.store.PriorEvents(ctx, sendID, subnet, hit.At.Add(-botfilter.BurstWindow))
	if err != nil {
		s.log.WarnContext(ctx, "read prior tracking events; classifying on the remaining signals",
			"error", err, "kind", string(kind), "send_id", sendID)
	}
	return botfilter.Classify(botfilter.Hit{
		Kind:      kind,
		UserAgent: hit.UserAgent,
		IP:        hit.IP,
		At:        hit.At,
		SentAt:    send.SentAt,
	}, prior)
}
