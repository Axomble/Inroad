package remote

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/credbroker"
)

// maxRequestBytes caps a request body. The one shape is a uuid and an address.
const maxRequestBytes = 4 << 10

// SuppressionReader is the control plane's side of the one method this slice
// carries. It is defined HERE, at the consumer, and is deliberately the exact
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
func NewHandler(r SuppressionReader, token string, logger *slog.Logger) (http.Handler, error) {
	if r == nil {
		return nil, errors.New("coreapi remote: handler needs a suppression reader")
	}
	if len(token) < credbroker.MinTokenLen {
		return nil, credbroker.ErrWeakToken
	}
	if logger == nil {
		logger = slog.Default()
	}
	h := &handler{suppression: r, logger: logger}
	authed := credbroker.RequireToken(token, logger)
	mux := http.NewServeMux()
	mux.Handle("POST "+PathSuppressionCheck, authed(http.HandlerFunc(h.checkSuppression)))
	return mux, nil
}

type handler struct {
	suppression SuppressionReader
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
