package inbox

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
)

// composeDraftResponse is the wire shape of one autosaved draft.
type composeDraftResponse struct {
	ID        string   `json:"id"`
	MailboxID *string  `json:"mailbox_id"`
	ToEmails  []string `json:"to_emails"`
	CcEmails  []string `json:"cc_emails"`
	BccEmails []string `json:"bcc_emails"`
	Subject   string   `json:"subject"`
	BodyText  string   `json:"body_text"`
	UpdatedAt string   `json:"updated_at"`
}

func toComposeDraftResponse(d ComposeDraft) composeDraftResponse {
	return composeDraftResponse{
		ID:        d.ID.String(),
		MailboxID: uuidString(d.MailboxID),
		// Non-nil slices so an empty recipient list marshals `[]`, not `null`.
		ToEmails:  orEmpty(d.ToEmails),
		CcEmails:  orEmpty(d.CcEmails),
		BccEmails: orEmpty(d.BccEmails),
		Subject:   d.Subject,
		BodyText:  d.BodyText,
		UpdatedAt: d.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func orEmpty(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// composeDraftListResponse is GET /inbox/drafts.
type composeDraftListResponse struct {
	Drafts []composeDraftResponse `json:"drafts"`
}

// saveComposeDraftRequest is the body for PUT /inbox/drafts/{draftId}.
//
// The id is in the PATH, minted by the client when the composer opens, so
// autosave is a plain idempotent PUT — the composer never has to track whether
// it has saved before.
type saveComposeDraftRequest struct {
	MailboxID string   `json:"mailbox_id"`
	ToEmails  []string `json:"to_emails"`
	CcEmails  []string `json:"cc_emails"`
	BccEmails []string `json:"bcc_emails"`
	Subject   string   `json:"subject"`
	BodyText  string   `json:"body_text"`
}

// pendingComposeResponse is the wire shape of one queued composed email.
type pendingComposeResponse struct {
	ID           string   `json:"id"`
	MailboxID    string   `json:"mailbox_id"`
	MailboxEmail string   `json:"mailbox_email"`
	ToEmails     []string `json:"to_emails"`
	CcEmails     []string `json:"cc_emails"`
	BccEmails    []string `json:"bcc_emails"`
	Subject      string   `json:"subject"`
	BodyText     string   `json:"body_text"`
	Status       string   `json:"status"`
	SendAfter    string   `json:"send_after"`
	LastError    string   `json:"last_error"`
	Cancellable  bool     `json:"cancellable"`
	CreatedAt    string   `json:"created_at"`
}

func toPendingComposeResponse(c PendingCompose) pendingComposeResponse {
	return pendingComposeResponse{
		ID:           c.ID.String(),
		MailboxID:    c.MailboxID.String(),
		MailboxEmail: c.MailboxEmail,
		ToEmails:     orEmpty(c.ToEmails),
		CcEmails:     orEmpty(c.CcEmails),
		BccEmails:    orEmpty(c.BccEmails),
		Subject:      c.Subject,
		BodyText:     c.BodyText,
		Status:       c.Status,
		SendAfter:    c.SendAfter.UTC().Format(time.RFC3339),
		LastError:    c.LastError,
		Cancellable:  c.Cancellable(),
		CreatedAt:    c.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// pendingComposeListResponse is GET /inbox/composes.
type pendingComposeListResponse struct {
	Items []pendingComposeResponse `json:"items"`
}

// sendComposeRequest is the body for POST /inbox/composes.
type sendComposeRequest struct {
	MailboxID string   `json:"mailbox_id"`
	ToEmails  []string `json:"to_emails"`
	CcEmails  []string `json:"cc_emails"`
	BccEmails []string `json:"bcc_emails"`
	Subject   string   `json:"subject"`
	BodyText  string   `json:"body_text"`
	SendAt    string   `json:"send_at"`
	// DraftID, when set, is the autosaved draft this send came from — deleted on
	// success so the composer's draft list does not keep a copy of mail already
	// on its way.
	DraftID string `json:"draft_id"`
}

// saveComposeDraft handles PUT /inbox/drafts/{draftId}.
func (h *Handler) saveComposeDraft(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	userID := callerUserID(r)
	if userID == nil {
		// A draft belongs to a person; a machine principal has no one to own it.
		httpx.Error(w, http.StatusForbidden, "drafts require a signed-in user")
		return
	}
	draftID, err := uuid.Parse(chi.URLParam(r, "draftId"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid draft id")
		return
	}
	var req saveComposeDraftRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	in := SaveComposeDraftInput{
		ID: draftID, WorkspaceID: wid, UserID: *userID,
		ToEmails: req.ToEmails, CcEmails: req.CcEmails, BccEmails: req.BccEmails,
		Subject: req.Subject, BodyText: req.BodyText,
	}
	if req.MailboxID != "" {
		mailboxID, err := uuid.Parse(req.MailboxID)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "mailbox_id must be a UUID")
			return
		}
		in.MailboxID = &mailboxID
	}

	saved, err := h.svc.SaveComposeDraft(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toComposeDraftResponse(saved))
}

// listComposeDrafts handles GET /inbox/drafts — this caller's own drafts only.
func (h *Handler) listComposeDrafts(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	userID := callerUserID(r)
	if userID == nil {
		// An empty list, not a 403: a machine principal simply has no drafts,
		// and the endpoint is a read.
		httpx.JSON(w, http.StatusOK, composeDraftListResponse{Drafts: []composeDraftResponse{}})
		return
	}
	drafts, err := h.svc.ListComposeDrafts(r.Context(), wid, *userID)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := composeDraftListResponse{Drafts: make([]composeDraftResponse, 0, len(drafts))}
	for _, d := range drafts {
		out.Drafts = append(out.Drafts, toComposeDraftResponse(d))
	}
	httpx.JSON(w, http.StatusOK, out)
}

// deleteComposeDraft handles DELETE /inbox/drafts/{draftId}.
func (h *Handler) deleteComposeDraft(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	userID := callerUserID(r)
	if userID == nil {
		httpx.Error(w, http.StatusForbidden, "drafts require a signed-in user")
		return
	}
	draftID, err := uuid.Parse(chi.URLParam(r, "draftId"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid draft id")
		return
	}
	if err := h.svc.DeleteComposeDraft(r.Context(), wid, *userID, draftID); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// sendCompose handles POST /inbox/composes — queue a new email.
func (h *Handler) sendCompose(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	var req sendComposeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	mailboxID, err := uuid.Parse(req.MailboxID)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "mailbox_id must be a UUID")
		return
	}
	var sendAt *time.Time
	if req.SendAt != "" {
		at, err := time.Parse(time.RFC3339, req.SendAt)
		if err != nil {
			httpx.Error(w, http.StatusBadRequest, "send_at must be an RFC3339 timestamp")
			return
		}
		sendAt = &at
	}

	queued, err := h.svc.ScheduleCompose(r.Context(), wid, CreatePendingComposeInput{
		MailboxID: mailboxID,
		ToEmails:  req.ToEmails, CcEmails: req.CcEmails, BccEmails: req.BccEmails,
		Subject: req.Subject, BodyText: req.BodyText,
		CreatedBy: callerUserID(r),
	}, sendAt)
	if err != nil {
		writeErr(w, err)
		return
	}

	// The draft is discarded only AFTER the send is safely queued, and a failure
	// to discard it is logged-and-ignored rather than failing the request: the
	// mail is on its way, and a leftover draft is a tidiness problem the operator
	// can fix, not a reason to report the send as failed.
	if req.DraftID != "" {
		if draftID, parseErr := uuid.Parse(req.DraftID); parseErr == nil {
			if userID := callerUserID(r); userID != nil {
				_ = h.svc.DeleteComposeDraft(r.Context(), wid, *userID, draftID)
			}
		}
	}

	httpx.JSON(w, http.StatusCreated, toPendingComposeResponse(queued))
}

// listPendingComposes handles GET /inbox/composes.
func (h *Handler) listPendingComposes(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	items, err := h.svc.ListPendingComposes(r.Context(), wid, 0)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := pendingComposeListResponse{Items: make([]pendingComposeResponse, 0, len(items))}
	for _, c := range items {
		out.Items = append(out.Items, toPendingComposeResponse(c))
	}
	httpx.JSON(w, http.StatusOK, out)
}

// cancelPendingCompose handles DELETE /inbox/composes/{pendingId}.
func (h *Handler) cancelPendingCompose(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "pendingId"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.svc.CancelPendingCompose(r.Context(), wid, id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
