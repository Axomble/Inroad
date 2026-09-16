package credbroker

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// maxRequestBytes caps a broker request body. Both shapes are two UUIDs.
const maxRequestBytes = 4 << 10

// NewHandler returns the CONTROL plane's credential-broker handler: the server
// side of HTTPOpener. It turns "open this named subject" into a call on the
// keyring-backed Opener the control plane already holds.
//
// It is meant for a DEDICATED listener, never the public API router — the same
// rule the Prometheus listener follows (internal/platform/httpx.MetricsMux),
// and for a sharper reason: this endpoint returns plaintext credentials. The
// composition root opens that listener only when an operator sets both an
// address and a token, so an installation that has not opted in does not serve
// this route at all.
//
// Authentication is the fleet channel's shared bearer token (RequireToken),
// which this listener now serves more than one transport under — see
// channel.go.
func NewHandler(o Opener, token string, logger *slog.Logger) (http.Handler, error) {
	if o == nil {
		return nil, errors.New("credbroker: handler needs an opener")
	}
	if len(token) < MinTokenLen {
		return nil, ErrWeakToken
	}
	if logger == nil {
		logger = slog.Default()
	}
	h := &handler{opener: o, logger: logger}
	authed := RequireToken(token, logger)
	mux := http.NewServeMux()
	mux.Handle("POST "+PathMailbox, authed(http.HandlerFunc(h.openMailbox)))
	mux.Handle("POST "+PathWebhookEndpoint, authed(http.HandlerFunc(h.openWebhookEndpoint)))
	return mux, nil
}

type handler struct {
	opener Opener
	logger *slog.Logger
}

func (h *handler) openMailbox(w http.ResponseWriter, r *http.Request) {
	var in mailboxRequest
	if !decode(w, r, &in) {
		return
	}
	ws, mailbox, ok := parsePair(w, in.WorkspaceID, in.MailboxID)
	if !ok {
		return
	}
	// Provider and Sealed are left zero on purpose: the opener re-reads the row
	// itself, workspace-pinned. See MailboxRef.
	sec, err := h.opener.OpenMailbox(r.Context(), MailboxRef{WorkspaceID: ws, MailboxID: mailbox})
	if err != nil {
		h.fail(w, "mailbox", err, "workspace_id", ws, "mailbox_id", mailbox)
		return
	}
	// See the mirror conversion in HTTPOpener.OpenMailbox for why this is a
	// conversion rather than a field-by-field copy.
	respond(w, http.StatusOK, mailboxResponse(sec))
}

func (h *handler) openWebhookEndpoint(w http.ResponseWriter, r *http.Request) {
	var in webhookEndpointRequest
	if !decode(w, r, &in) {
		return
	}
	ws, endpoint, ok := parsePair(w, in.WorkspaceID, in.EndpointID)
	if !ok {
		return
	}
	secret, err := h.opener.OpenWebhookEndpointSecret(r.Context(), ws, endpoint, nil)
	if err != nil {
		h.fail(w, "webhook endpoint", err, "workspace_id", ws, "endpoint_id", endpoint)
		return
	}
	respond(w, http.StatusOK, webhookEndpointResponse{Secret: secret})
}

// fail maps an opener error to a status and logs it. A missing row is 404 and
// everything else is 500; the RESPONSE carries a fixed string either way, so
// the endpoint is not a probe oracle for which ids exist in which workspace.
// The log keeps the real error, because that side is the operator's.
func (h *handler) fail(w http.ResponseWriter, subject string, err error, logArgs ...any) {
	if errors.Is(err, pgx.ErrNoRows) {
		respond(w, http.StatusNotFound, errorResponse{Error: "not found"})
		return
	}
	h.logger.Error("credential broker: open failed", append(logArgs, "subject", subject, "err", err)...)
	respond(w, http.StatusInternalServerError, errorResponse{Error: "could not open credential"})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		respond(w, http.StatusBadRequest, errorResponse{Error: "invalid request"})
		return false
	}
	return true
}

// parsePair parses the workspace id and the subject id. A malformed id is a 400
// and never reaches a query.
func parsePair(w http.ResponseWriter, workspaceID, subjectID string) (uuid.UUID, uuid.UUID, bool) {
	ws, err := uuid.Parse(workspaceID)
	if err != nil {
		respond(w, http.StatusBadRequest, errorResponse{Error: "invalid workspace_id"})
		return uuid.Nil, uuid.Nil, false
	}
	subject, err := uuid.Parse(subjectID)
	if err != nil {
		respond(w, http.StatusBadRequest, errorResponse{Error: "invalid id"})
		return uuid.Nil, uuid.Nil, false
	}
	return ws, subject, true
}

func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	// A credential response must not be cached by anything between the two
	// planes, however unlikely an intermediary is on this listener.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
