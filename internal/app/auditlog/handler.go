package auditlog

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/httpx"
)

// Handler serves the audit viewer.
type Handler struct{ svc *Service }

// NewHandler builds a Handler over svc.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes is mounted at /api/v1/audit-events in the SESSION-ONLY group:
//
//	GET /   list, newest first (filters + cursor, see parseListInput)
//
// Owner/admin only. RequireRole is the whole gate and is sufficient on its own:
// a machine principal (api key, OAuth grant) carries an empty role and can
// never pass it, and the session-only mount additionally refuses their tokens
// at the door. The log reveals who did what, from which address — the same
// class of information as member and api-key management, which carry the same
// gate. There is no scope for it on purpose: a scope would make it grantable.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireRole("admin"))
	r.Get("/", h.list)
	return r
}

type eventResponse struct {
	ID          string            `json:"id"`
	Action      string            `json:"action"`
	ActorType   string            `json:"actor_type"`
	ActorID     *string           `json:"actor_id"`
	ActorUserID *string           `json:"actor_user_id"`
	ActorEmail  *string           `json:"actor_email"`
	TargetType  *string           `json:"target_type"`
	TargetID    *string           `json:"target_id"`
	IP          *string           `json:"ip"`
	UserAgent   *string           `json:"user_agent"`
	Metadata    map[string]string `json:"metadata"`
	CreatedAt   string            `json:"created_at"`
}

type listResponse struct {
	Events     []eventResponse `json:"events"`
	NextCursor *string         `json:"next_cursor"`
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	in, err := parseListInput(r)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	page, err := h.svc.List(r.Context(), ws, in)
	switch {
	case errors.Is(err, ErrInvalidFilter), errors.Is(err, ErrBadCursor):
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	case err != nil:
		slog.ErrorContext(r.Context(), "audit list failed", "workspace_id", ws.String(), "err", err)
		httpx.Error(w, http.StatusInternalServerError, "could not list audit events")
		return
	}
	out := listResponse{Events: make([]eventResponse, 0, len(page.Events))}
	for _, row := range page.Events {
		ev, err := toResponse(row)
		if err != nil {
			slog.ErrorContext(r.Context(), "audit row undecodable", "id", row.ID.String(), "err", err)
			httpx.Error(w, http.StatusInternalServerError, "could not list audit events")
			return
		}
		out.Events = append(out.Events, ev)
	}
	if page.NextCursor != "" {
		out.NextCursor = &page.NextCursor
	}
	httpx.JSON(w, http.StatusOK, out)
}

// parseListInput reads the query string. Every malformed value is a 400 that
// names the parameter: a typo'd filter silently ignored would return MORE
// events than asked for, which in an audit viewer reads as evidence.
func parseListInput(r *http.Request) (ListInput, error) {
	q := r.URL.Query()
	in := ListInput{
		Filter: Filter{
			ActionPrefix: q.Get("action"),
			ActorType:    q.Get("actor_type"),
			ActorID:      q.Get("actor_id"),
			TargetType:   q.Get("target_type"),
			TargetID:     q.Get("target_id"),
		},
		Cursor: q.Get("cursor"),
	}
	if raw := q.Get("actor_user_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return ListInput{}, errors.New("actor_user_id must be a uuid")
		}
		in.Filter.ActorUserID = &id
	}
	var err error
	if in.Filter.Since, err = timeParam(q.Get("since"), "since"); err != nil {
		return ListInput{}, err
	}
	if in.Filter.Until, err = timeParam(q.Get("until"), "until"); err != nil {
		return ListInput{}, err
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return ListInput{}, errors.New("limit must be a positive integer")
		}
		in.Limit = n
	}
	return in, nil
}

func timeParam(raw, name string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, fmt.Errorf("%s must be an RFC3339 timestamp", name)
	}
	return &t, nil
}

func toResponse(row gen.ListAuditEventsRow) (eventResponse, error) {
	md := map[string]string{}
	if len(row.Metadata) > 0 {
		if err := json.Unmarshal(row.Metadata, &md); err != nil {
			return eventResponse{}, fmt.Errorf("decode metadata: %w", err)
		}
	}
	out := eventResponse{
		ID:         row.ID.String(),
		Action:     row.Action,
		ActorType:  row.ActorType,
		ActorID:    nonEmpty(row.ActorID),
		ActorEmail: row.ActorEmail,
		TargetType: nonEmpty(row.TargetType),
		TargetID:   nonEmpty(row.TargetID),
		UserAgent:  nonEmpty(row.UserAgent),
		Metadata:   md,
		CreatedAt:  row.CreatedAt.Time.UTC().Format(time.RFC3339),
	}
	if row.ActorUserID.Valid {
		s := uuid.UUID(row.ActorUserID.Bytes).String()
		out.ActorUserID = &s
	}
	if row.Ip != nil {
		s := row.Ip.String()
		out.IP = &s
	}
	return out, nil
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
