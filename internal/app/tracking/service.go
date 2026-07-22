package tracking

import (
	"context"
	"net/url"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/track"
)

// Event kinds recorded in tracking_events (mirrors gen.TrackingEventKind).
const (
	kindOpen  = "open"
	kindClick = "click"
)

// Service implements the tracking use cases: verifying tokens, resolving
// the event's tenant server-side, and recording the event. It depends on
// the Store interface, not the sqlc-backed struct.
type Service struct {
	secret []byte
	store  Store
}

// NewService builds a Service that verifies tokens with secret and records
// events via store.
func NewService(secret []byte, store Store) *Service {
	return &Service{secret: secret, store: store}
}

// RecordOpen verifies the open token and, if it (and the send it names) are
// valid, records an 'open' event with the given user agent. A malformed
// token and an unknown send are both silently no-ops: the pixel endpoint
// must never become an oracle a caller can use to probe which send ids
// exist, so both cases still serve the same pixel with no visible
// difference.
func (s *Service) RecordOpen(ctx context.Context, token, userAgent string) {
	sendIDStr, ok := track.ParseOpenToken(s.secret, token)
	if !ok {
		return
	}
	sendID, err := uuid.Parse(sendIDStr)
	if err != nil {
		return
	}
	workspaceID, campaignID, ok := s.store.ResolveSend(ctx, sendID)
	if !ok {
		return
	}
	_ = s.store.RecordEvent(ctx, workspaceID, campaignID, sendID, kindOpen, "", userAgent)
}

// RecordClick verifies the click token, rejects any redirect target that
// isn't http(s) (the signature only proves the token wasn't tampered with
// -- it does NOT prove the URL it names is safe to redirect to; a token
// minted for a javascript:/data: URL must still be blocked here), resolves
// the send's tenant server-side, and records the event. ok is false for a
// malformed/tampered token, an unsafe scheme, or an unknown send; callers
// must respond 404 with no redirect and no event in every one of those
// cases.
func (s *Service) RecordClick(ctx context.Context, token, userAgent string) (destURL string, ok bool) {
	sendIDStr, rawURL, ok := track.ParseClickToken(s.secret, token)
	if !ok {
		return "", false
	}
	sendID, err := uuid.Parse(sendIDStr)
	if err != nil {
		return "", false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", false
	}
	workspaceID, campaignID, ok := s.store.ResolveSend(ctx, sendID)
	if !ok {
		return "", false
	}
	_ = s.store.RecordEvent(ctx, workspaceID, campaignID, sendID, kindClick, rawURL, userAgent)
	return rawURL, true
}
