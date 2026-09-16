package remote

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/credbroker"
)

// maxRequestBytes caps a request body. Every shape is a small number of uuids
// plus, on two routes, one free-text subject: a contact's address, and an
// inbound Message-ID.
//
// 64 KiB rather than the few hundred bytes those actually need, because of what
// refusing one would do. The Message-ID comes off unauthenticated inbound mail,
// so its length is chosen by whoever sent the message; in-process an absurd one
// simply matches nothing, but a 400 here would be an error the poller cannot
// distinguish from a real failure, so it would return before SetInboxCursor and
// the mailbox would stop processing ALL inbound mail — campaign replies and
// bounces included. The cap still bounds the body hard; it just sits far above
// anything a real header carries.
const maxRequestBytes = 64 << 10

// SuppressionReader is the control plane's side of the one method slice 1
// carried. It is defined HERE, at the consumer, and is deliberately the exact
// signature internal/app/suppression.Store already has, so cmd/inroad wires the
// store it already builds straight in with no adapter — one implementation of
// "is this address suppressed", shared by the HTTP API, the in-process coreapi
// path and this transport.
//
// It takes a parsed uuid.UUID, not a string: parsing happens once, at the HTTP
// boundary, and a value that reaches this interface has already been validated.
type SuppressionReader interface {
	IsSuppressed(ctx context.Context, workspaceID uuid.UUID, email string) (bool, error)
}

// JobReader is the control plane's side of the per-message job reads. Like
// SuppressionReader it is defined HERE, at the consumer, and its methods are
// the EXACT signatures the in-process client already has — so cmd/inroad
// satisfies it by type assertion on the client it already built, with no
// adapter and no second implementation of a job build.
//
// It is one interface rather than eight because there is one decision behind
// it ("this control plane serves job reads to the fleet") and one implementor.
// Splitting it would be eight things to wire and eight things to forget.
//
// Every method takes ids as STRINGS, which is what the coreapi seam speaks and
// what the in-process implementations parse themselves. The handler parses them
// first anyway, so a malformed id never reaches a query — but it hands the
// original string on, so the two transports feed the implementation identical
// input.
type JobReader interface {
	GetStepSendJob(ctx context.Context, enrollmentID, workspaceID string) (coreapi.StepSendJob, error)
	GetInboxPollJob(ctx context.Context, mailboxID, workspaceID string) (coreapi.InboxPollJob, error)
	GetWarmupSendJob(ctx context.Context, mailboxID, workspaceID string) (coreapi.WarmupSendJob, error)
	GetWarmupEngageJob(ctx context.Context, receiptID, workspaceID string) (coreapi.WarmupEngageJob, error)
	GetWebhookDeliveryJob(ctx context.Context, deliveryID, workspaceID string) (coreapi.WebhookDeliveryJob, error)
	GetTestSendContent(ctx context.Context, workspaceID, campaignID, stepID string) (coreapi.TestSendContent, error)
	ResolveSenderTransport(ctx context.Context, workspaceID, mailboxID string) (coreapi.SenderTransport, error)
	FindSendByMessageID(ctx context.Context, workspaceID, messageID string) (coreapi.SendRef, error)
}

// Deps is what the control-plane handler serves. A struct rather than a
// parameter list so a later slice adds a field instead of a fourth positional
// argument, and so cmd/inroad's fleetDeps maps onto it one-to-one.
//
// Both are REQUIRED. A handler serving half the transport would start, register
// its routes, and fail every call to the other half at the first send — and the
// operator who enabled the flag would have no signal until then.
type Deps struct {
	// Suppression answers one suppression question (slice 1).
	Suppression SuppressionReader
	// Jobs answers the per-message job reads (slice 2).
	Jobs JobReader
}

// NewHandler returns the CONTROL plane's coreapi transport handler: the server
// side of Client.
//
// It is meant for the FLEET LISTENER, never the public API router — the same
// rule credbroker.NewHandler and the Prometheus listener follow. The
// composition root opens that listener only when an operator sets an address
// and a token, so an installation that has not opted in serves none of these
// routes at all and a self-hosted deployment is untouched.
//
// Authentication is credbroker.RequireToken, the fleet channel's shared bearer
// token, by decision rather than convenience: the same principal (a role=send
// worker) needs both this transport and the credential broker, so two tokens
// would partition nothing while doubling what an operator has to rotate.
func NewHandler(d Deps, token string, logger *slog.Logger) (http.Handler, error) {
	if d.Suppression == nil {
		return nil, errors.New("coreapi remote: handler needs a suppression reader")
	}
	if d.Jobs == nil {
		return nil, errors.New("coreapi remote: handler needs a job reader")
	}
	if len(token) < credbroker.MinTokenLen {
		return nil, credbroker.ErrWeakToken
	}
	if logger == nil {
		logger = slog.Default()
	}
	h := &handler{suppression: d.Suppression, jobs: d.Jobs, logger: logger}
	authed := credbroker.RequireToken(token, logger)
	mux := http.NewServeMux()
	for path, fn := range map[string]http.HandlerFunc{
		PathSuppressionCheck:   h.checkSuppression,
		PathStepSendJob:        h.stepSendJob,
		PathInboxPollJob:       h.inboxPollJob,
		PathWarmupSendJob:      h.warmupSendJob,
		PathWarmupEngageJob:    h.warmupEngageJob,
		PathWebhookDeliveryJob: h.webhookDeliveryJob,
		PathTestSendContent:    h.testSendContent,
		PathSenderTransport:    h.senderTransport,
		PathSendByMessageID:    h.sendByMessageID,
	} {
		mux.Handle("POST "+path, authed(fn))
	}
	return mux, nil
}

type handler struct {
	suppression SuppressionReader
	jobs        JobReader
	logger      *slog.Logger
}

// checkSuppression answers one suppression question, workspace-pinned.
//
// The pin comes from the request because there is no session here to derive it
// from — the caller is a machine holding the fleet token, not a user holding a
// JWT. That does not weaken docs/security.md invariant 4, and the distinction
// is worth being precise about: the workspace on the wire selects WHICH
// workspace's list is consulted, and the reader then applies the same
// workspace_id SQL filter the in-process path applies, so the answer is still
// confined to one tenant's rows. What a caller can do by naming a different
// workspace is ask a question about that workspace — which is exactly what the
// fleet token already authorises, since one worker legitimately serves every
// tenant's sends. The wire ADDS a pin; it does not replace one.
func (h *handler) checkSuppression(w http.ResponseWriter, r *http.Request) {
	var in suppressionRequest
	if !decode(w, r, &in) {
		return
	}
	ws, err := uuid.Parse(in.WorkspaceID)
	if err != nil {
		respond(w, http.StatusBadRequest, errorResponse{Error: "invalid workspace_id"})
		return
	}
	suppressed, err := h.suppression.IsSuppressed(r.Context(), ws, in.Email)
	if err != nil {
		// The log keeps the real error, because that side is the operator's.
		// The RESPONSE carries a fixed string, so the endpoint is not a probe
		// oracle and cannot relay a database message to a fleet host. The
		// ADDRESS is not logged either: it is a tenant's contact.
		h.logger.Error("coreapi remote: suppression check failed", "workspace_id", ws, "err", err)
		respond(w, http.StatusInternalServerError, errorResponse{Error: "could not check suppression"})
		return
	}
	respond(w, http.StatusOK, suppressionResponse{Suppressed: suppressed})
}

// decode reads the request body under a size cap and refuses unknown fields. An
// unknown field is a 400 rather than an ignored value on purpose: it is what
// stops a caller quietly adding a parameter this endpoint does not honour and
// believing the narrower answer it gets back.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		respond(w, http.StatusBadRequest, errorResponse{Error: "invalid request"})
		return false
	}
	return true
}

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	// A suppression answer is a tenant's compliance state. Nothing between the
	// two planes may cache it, however unlikely an intermediary is here — and a
	// cached answer is precisely the stale negative this slice refuses to have.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
