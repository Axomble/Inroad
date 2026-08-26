package deadletter

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/httpx"
)

// Handler exposes dead-letter triage over HTTP. Authentication is applied by the
// protected router group (see cmd/inroad), not here; the workspace always comes
// from the authenticated principal, never from the request.
type Handler struct{ svc *Service }

// NewHandler builds the HTTP surface over the service.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// deadLetterResponse is the wire shape of one dead letter.
//
// workspace_id is omitted: the caller is already scoped to it and echoing a
// tenant id back invites clients to start sending one (same rule as
// replylabel's labelResponse).
//
// payload IS included. It is the whole point of the record — an operator
// triaging a dropped send needs to see which enrollment or mailbox it named —
// and it is the tenant's own data being returned to that tenant. It carries no
// credential: task payloads in this codebase are ids plus, for a manual reply,
// the operator's own text; secrets are resolved by the worker through the
// keyring at execution time and never travel in a payload (docs/security.md
// invariant 1). It is emitted as raw JSON rather than a string so a client can
// read its fields without a second parse.
type deadLetterResponse struct {
	ID           string          `json:"id"`
	TaskType     string          `json:"task_type"`
	Payload      json.RawMessage `json:"payload"`
	LastError    string          `json:"last_error"`
	AttemptCount int32           `json:"attempt_count"`
	Status       string          `json:"status"`
	CreatedAt    string          `json:"created_at"`
	ReplayedAt   *string         `json:"replayed_at"`
}

func toResponse(d gen.TaskDeadLetter) deadLetterResponse {
	out := deadLetterResponse{
		ID:           d.ID.String(),
		TaskType:     d.TaskType,
		Payload:      json.RawMessage(d.Payload),
		LastError:    d.LastError,
		AttemptCount: d.AttemptCount,
		Status:       d.Status,
		CreatedAt:    d.CreatedAt.Time.UTC().Format(time.RFC3339),
	}
	if d.ReplayedAt.Valid {
		replayed := d.ReplayedAt.Time.UTC().Format(time.RFC3339)
		out.ReplayedAt = &replayed
	}
	return out
}

// toResponses always returns a non-nil slice so an empty page serialises as []
// rather than null — a client mapping over the result must not have to guard.
func toResponses(rows []gen.TaskDeadLetter) []deadLetterResponse {
	out := make([]deadLetterResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, toResponse(row))
	}
	return out
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	rows, err := h.svc.List(r.Context(), ws, ListParams{
		Status: r.URL.Query().Get("status"),
		Limit:  intQuery(r, "limit"),
		Offset: intQuery(r, "offset"),
	})
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"dead_letters": toResponses(rows)})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	row, err := h.svc.Get(r.Context(), ws, id)
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toResponse(row))
}

// replay re-enqueues one dead letter. It takes no request body at all: the
// payload to re-run is the one that was captured, and accepting a
// caller-supplied one would turn this endpoint into an arbitrary task-injection
// primitive.
func (h *Handler) replay(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	row, err := h.svc.Replay(r.Context(), ws, id)
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toResponse(row))
}

func (h *Handler) discard(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := pathID(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := h.svc.Discard(r.Context(), ws, id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// pathID parses the {id} path parameter. An unparseable id is reported as
// not-found rather than a 400: to this API a malformed id and an id that does
// not exist are the same thing, and it keeps the two indistinguishable to a
// caller probing for other tenants' ids.
func pathID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return uuid.Nil, ErrNotFound
	}
	return id, nil
}

// intQuery reads an optional non-negative integer query parameter. An absent or
// unparseable value yields 0, which the service then clamps to its own default
// — paging is a convenience, so a typo'd limit returns the first page rather
// than an error.
func intQuery(r *http.Request, key string) int32 {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return 0
	}
	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || n < 0 {
		return 0
	}
	return int32(n)
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "dead letter not found")
	case errors.Is(err, ErrNotPending):
		httpx.Error(w, http.StatusConflict, "dead letter has already been replayed or discarded")
	case errors.Is(err, ErrMalformedPayload):
		// 422, not 500: the row is unreplayable because of what it CONTAINS,
		// which is a permanent property of that row. A client must not retry
		// it, and an operator needs to know the task cannot be re-run rather
		// than seeing a generic server error and trying again.
		httpx.Error(w, http.StatusUnprocessableEntity, "dead letter payload cannot be replayed")
	case errors.Is(err, ErrValidation):
		httpx.Error(w, http.StatusUnprocessableEntity, err.Error())
	default:
		httpx.Error(w, http.StatusInternalServerError, "dead letter request failed")
	}
}
