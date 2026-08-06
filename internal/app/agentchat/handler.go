package agentchat

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
)

type Handler struct{ service *Service }

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/threads", h.listThreads)
	r.Get("/approvals", h.listPendingActions)
	r.Get("/approvals/{actionID}", h.getPendingAction)
	r.Post("/approvals/{actionID}/decision", h.decidePendingAction)
	r.Post("/threads", h.createThread)
	r.Get("/threads/{id}", h.getThread)
	r.Patch("/threads/{id}", h.renameThread)
	r.Delete("/threads/{id}", h.deleteThread)
	r.Post("/threads/{id}/messages", h.sendMessage)
	r.Get("/threads/{id}/queue", h.listQueue)
	r.Delete("/threads/{id}/queue/{messageID}", h.deleteQueued)
	r.Post("/threads/{id}/stop", h.stop)
	r.Get("/threads/{id}/stream", h.stream)
	return r
}

func actorFromRequest(w http.ResponseWriter, r *http.Request) (Actor, bool) {
	p, ok := auth.UserFromContext(r.Context())
	if !ok || p.Kind != auth.KindSession {
		httpx.Error(w, http.StatusUnauthorized, "unauthorized")
		return Actor{}, false
	}
	workspaceID, err := uuid.Parse(p.WorkspaceID)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "bad workspace")
		return Actor{}, false
	}
	userID, err := uuid.Parse(p.UserID)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "bad user")
		return Actor{}, false
	}
	return Actor{WorkspaceID: workspaceID, UserID: userID, Role: p.Role}, true
}

func threadID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid thread id")
		return uuid.Nil, false
	}
	return id, true
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) error {
	return decodeBodyLimit(w, r, dst, 64<<10)
}

func decodeBodyLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple json values")
		}
		return err
	}
	return nil
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrThreadNotFound), errors.Is(err, ErrQueueEmpty), errors.Is(err, ErrActionNotFound):
		httpx.Error(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrValidation):
		httpx.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrNoActiveRun):
		httpx.Error(w, http.StatusConflict, "thread has no stoppable run")
	case errors.Is(err, ErrRunActive):
		httpx.Error(w, http.StatusConflict, "thread already has an active run")
	case errors.Is(err, ErrActionDecided):
		httpx.Error(w, http.StatusConflict, "approval has already been decided")
	case errors.Is(err, ErrStreamLimit):
		httpx.Error(w, http.StatusTooManyRequests, "too many open streams for this user")
	default:
		httpx.Error(w, http.StatusInternalServerError, "internal error")
	}
}

func pendingActionID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "actionID"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid action id")
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) listPendingActions(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFromRequest(w, r)
	if !ok {
		return
	}
	limit, err := queryInt32(r, "limit", defaultThreadLimit)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid limit")
		return
	}
	actions, err := h.service.ListPendingActions(r.Context(), actor, r.URL.Query().Get("status"), limit)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string][]PendingActionDTO{"actions": actions})
}

func (h *Handler) getPendingAction(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFromRequest(w, r)
	if !ok {
		return
	}
	id, ok := pendingActionID(w, r)
	if !ok {
		return
	}
	action, err := h.service.GetPendingAction(r.Context(), actor, id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, action)
}

func (h *Handler) decidePendingAction(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFromRequest(w, r)
	if !ok {
		return
	}
	id, ok := pendingActionID(w, r)
	if !ok {
		return
	}
	var body struct {
		Decision        string          `json:"decision"`
		EditedArguments json.RawMessage `json:"edited_arguments"`
		Reason          string          `json:"reason"`
	}
	// Edited bulk-import arguments can legitimately exceed the small request
	// limit used by chat metadata, while still staying bounded.
	if err := decodeBodyLimit(w, r, &body, 2<<20); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	action, err := h.service.DecidePendingAction(r.Context(), actor, id, ApprovalDecision{
		Decision: body.Decision, EditedArguments: body.EditedArguments, Reason: body.Reason,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, action)
}

func (h *Handler) createThread(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFromRequest(w, r)
	if !ok {
		return
	}
	thread, err := h.service.CreateThread(r.Context(), actor)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, thread)
}

func (h *Handler) listThreads(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFromRequest(w, r)
	if !ok {
		return
	}
	offset, err := queryInt32(r, "offset", 0)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid offset")
		return
	}
	limit, err := queryInt32(r, "limit", defaultThreadLimit)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid limit")
		return
	}
	threads, err := h.service.ListThreads(r.Context(), actor, offset, limit)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string][]ThreadDTO{"threads": threads})
}

func (h *Handler) getThread(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFromRequest(w, r)
	if !ok {
		return
	}
	id, ok := threadID(w, r)
	if !ok {
		return
	}
	thread, err := h.service.GetThread(r.Context(), actor, id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, thread)
}

func (h *Handler) renameThread(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFromRequest(w, r)
	if !ok {
		return
	}
	id, ok := threadID(w, r)
	if !ok {
		return
	}
	var body struct {
		Title string `json:"title"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	thread, err := h.service.RenameThread(r.Context(), actor, id, body.Title)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, thread)
}

func (h *Handler) deleteThread(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFromRequest(w, r)
	if !ok {
		return
	}
	id, ok := threadID(w, r)
	if !ok {
		return
	}
	if err := h.service.DeleteThread(r.Context(), actor, id); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func queryInt32(r *http.Request, key string, fallback int32) (int32, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 32)
	return int32(value), err
}

func (h *Handler) sendMessage(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFromRequest(w, r)
	if !ok {
		return
	}
	id, ok := threadID(w, r)
	if !ok {
		return
	}
	var body struct {
		Text            string           `json:"text"`
		Model           string           `json:"model"`
		BrowsingContext *BrowsingContext `json:"browsing_context"`
	}
	if err := decodeBody(w, r, &body); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	result, err := h.service.Send(r.Context(), actor, id, SendInput{Text: body.Text, Model: body.Model, BrowsingContext: body.BrowsingContext})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, result)
}

func (h *Handler) listQueue(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFromRequest(w, r)
	if !ok {
		return
	}
	id, ok := threadID(w, r)
	if !ok {
		return
	}
	queued, err := h.service.ListQueue(r.Context(), actor, id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string][]QueuedMessageDTO{"queued": queued})
}

func (h *Handler) deleteQueued(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFromRequest(w, r)
	if !ok {
		return
	}
	id, ok := threadID(w, r)
	if !ok {
		return
	}
	messageID, err := uuid.Parse(chi.URLParam(r, "messageID"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid message id")
		return
	}
	if err := h.service.DeleteQueued(r.Context(), actor, id, messageID); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) stop(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFromRequest(w, r)
	if !ok {
		return
	}
	id, ok := threadID(w, r)
	if !ok {
		return
	}
	if err := h.service.Stop(r.Context(), actor, id); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) stream(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorFromRequest(w, r)
	if !ok {
		return
	}
	id, ok := threadID(w, r)
	if !ok {
		return
	}
	after, err := streamOffset(r)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid event offset")
		return
	}
	frames, err := h.service.Attach(r.Context(), actor, id, after)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpx.Error(w, http.StatusInternalServerError, "streaming unavailable")
		return
	}
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache, no-transform")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	// An agent can think for a long time between chunks, and an idle
	// connection is what proxies reap first (nginx defaults to 60s). A comment
	// line is a no-op for every SSE client and keeps the hop alive.
	heartbeat := time.NewTicker(streamHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case frame, open := <-frames:
			if !open {
				return
			}
			if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", frame.Seq, frame.Data); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func streamOffset(r *http.Request) (int64, error) {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("after_seq")
	}
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, errors.New("invalid offset")
	}
	return value, nil
}
