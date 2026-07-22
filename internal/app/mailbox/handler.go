package mailbox

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
	"github.com/inroad/inroad/internal/platform/oauthstate"
)

// Handler exposes the mailbox domain over HTTP. Authentication is applied by
// the protected router group (see cmd/inroad), not here.
//
// jwtSecret signs/verifies the OAuth `state` parameter; appBaseURL is the
// frontend origin the public callback 302s back to. Both are only used by the
// Gmail OAuth surface (startGoogleOAuth / googleCallback).
type Handler struct {
	svc        *Service
	jwtSecret  []byte
	appBaseURL string
}

func NewHandler(svc *Service, jwtSecret []byte, appBaseURL string) *Handler {
	return &Handler{svc: svc, jwtSecret: jwtSecret, appBaseURL: appBaseURL}
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

// startGoogleOAuth (protected) begins the Gmail connect flow. It reads the
// workspace from the JWT, signs a 10-minute state binding the callback to that
// workspace, and returns the Google consent URL for the SPA to redirect to.
// access_type=offline + prompt=consent force a refresh token every time.
func (h *Handler) startGoogleOAuth(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	// State signing stays here (the handler holds jwtSecret); the oauth2 details
	// live behind the service seam.
	state := oauthstate.Sign(h.jwtSecret, wid.String(), time.Now(), 10*time.Minute)
	authURL, err := h.svc.GoogleAuthCodeURL(state)
	if err != nil {
		if errors.Is(err, ErrOAuthDisabled) {
			httpx.Error(w, http.StatusNotImplemented, "gmail oauth not configured")
			return
		}
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"auth_url": authURL})
}

// googleCallback (public) is the top-level browser navigation Google redirects
// to. It cannot rely on the JWT cookie (SameSite on a cross-site redirect), so
// it authenticates from the signed state and derives the workspace from it --
// never from a request param. It is a browser navigation, so it never returns a
// 5xx: every outcome 302s back to the SPA with connected=<email> or
// oauth_error=<reason>; server-side detail is logged, never leaked to the URL.
func (h *Handler) googleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	redirect := func(query string) {
		http.Redirect(w, r, h.appBaseURL+"/mailboxes?"+query, http.StatusFound)
	}
	if q.Get("error") != "" || q.Get("code") == "" {
		redirect("oauth_error=denied")
		return
	}
	wid, err := oauthstate.Verify(h.jwtSecret, q.Get("state"), time.Now())
	if err != nil {
		redirect("oauth_error=bad_state")
		return
	}
	wsID, err := uuid.Parse(wid)
	if err != nil {
		redirect("oauth_error=bad_state")
		return
	}
	m, err := h.svc.CompleteGoogleOAuth(r.Context(), q.Get("code"), wsID)
	if err != nil {
		switch {
		case errors.Is(err, ErrDuplicateMailbox):
			redirect("oauth_error=already_connected")
		case errors.Is(err, ErrOAuthDisabled):
			redirect("oauth_error=disabled")
		case errors.Is(err, ErrValidation):
			redirect("oauth_error=no_email")
		default:
			// Log the detail server-side; never surface internals to the browser.
			slog.Error("mailbox: gmail oauth callback failed", "err", err)
			redirect("oauth_error=exchange_failed")
		}
		return
	}
	redirect("connected=" + url.QueryEscape(m.Email))
}
