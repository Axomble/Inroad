package mailbox

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
)

// Handler exposes the mailbox domain over HTTP.
type Handler struct {
	svc       *Service
	jwtSecret []byte
}

func NewHandler(svc *Service, jwtSecret []byte) *Handler {
	return &Handler{svc: svc, jwtSecret: jwtSecret}
}

// connectRequest is the wire shape for POST /. It maps 1:1 onto ConnectInput.
type connectRequest struct {
	Email        string `json:"email"`
	DisplayName  string `json:"display_name"`
	SMTPHost     string `json:"smtp_host"`
	SMTPPort     int    `json:"smtp_port"`
	SMTPUsername string `json:"smtp_username"`
	IMAPHost     string `json:"imap_host"`
	IMAPPort     int    `json:"imap_port"`
	IMAPUsername string `json:"imap_username"`
	Secret       string `json:"secret"`
	UseTLS       bool   `json:"use_tls"`
}

// mailboxResponse is the wire shape returned for a mailbox. It deliberately
// omits SecretCiphertext -- encrypted or not, the secret never leaves this
// service in an HTTP response.
type mailboxResponse struct {
	ID                 string `json:"id"`
	Email              string `json:"email"`
	DisplayName        string `json:"display_name"`
	Provider           string `json:"provider"`
	SMTPHost           string `json:"smtp_host"`
	SMTPPort           int32  `json:"smtp_port"`
	SMTPUsername       string `json:"smtp_username"`
	IMAPHost           string `json:"imap_host"`
	IMAPPort           int32  `json:"imap_port"`
	IMAPUsername       string `json:"imap_username"`
	UseTLS             bool   `json:"use_tls"`
	DailyCap           int32  `json:"daily_cap"`
	MinIntervalSeconds int32  `json:"min_interval_seconds"`
	RampEnabled        bool   `json:"ramp_enabled"`
	RampStartCap       int32  `json:"ramp_start_cap"`
	RampDays           int32  `json:"ramp_days"`
	Status             string `json:"status"`
	LastError          string `json:"last_error"`
	CreatedAt          string `json:"created_at"`
}

func toResponse(m MailboxSafe) mailboxResponse {
	return mailboxResponse{
		ID:                 m.ID.String(),
		Email:              m.Email,
		DisplayName:        m.DisplayName,
		Provider:           m.Provider,
		SMTPHost:           m.SmtpHost,
		SMTPPort:           m.SmtpPort,
		SMTPUsername:       m.SmtpUsername,
		IMAPHost:           m.ImapHost,
		IMAPPort:           m.ImapPort,
		IMAPUsername:       m.ImapUsername,
		UseTLS:             m.UseTls,
		DailyCap:           m.DailyCap,
		MinIntervalSeconds: m.MinIntervalSeconds,
		RampEnabled:        m.RampEnabled,
		RampStartCap:       m.RampStartCap,
		RampDays:           m.RampDays,
		Status:             m.Status,
		LastError:          m.LastError,
		CreatedAt:          m.CreatedAt.Time.Format(time.RFC3339),
	}
}

// writeErr maps domain errors to HTTP status codes.
func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrDuplicateMailbox):
		httpx.Error(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrConnectionTestFailed):
		httpx.Error(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, ErrValidation):
		httpx.Error(w, http.StatusBadRequest, err.Error())
	default:
		httpx.Error(w, http.StatusInternalServerError, "internal error")
	}
}

func (h *Handler) connect(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	var req connectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	m, err := h.svc.ConnectSMTP(r.Context(), wid, ConnectInput{
		Email:        req.Email,
		DisplayName:  req.DisplayName,
		SMTPHost:     req.SMTPHost,
		SMTPPort:     req.SMTPPort,
		SMTPUsername: req.SMTPUsername,
		IMAPHost:     req.IMAPHost,
		IMAPPort:     req.IMAPPort,
		IMAPUsername: req.IMAPUsername,
		Secret:       req.Secret,
		UseTLS:       req.UseTLS,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toResponse(m))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	mailboxes, err := h.svc.List(r.Context(), wid)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]mailboxResponse, 0, len(mailboxes))
	for _, m := range mailboxes {
		out = append(out, toResponse(m))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid id")
		return
	}
	m, err := h.svc.Get(r.Context(), wid, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toResponse(m))
}

func (h *Handler) pause(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid id")
		return
	}
	m, err := h.svc.Pause(r.Context(), wid, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toResponse(m))
}

func (h *Handler) resume(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid id")
		return
	}
	m, err := h.svc.Resume(r.Context(), wid, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toResponse(m))
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.svc.Delete(r.Context(), wid, id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
