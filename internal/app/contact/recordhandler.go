package contact

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
)

// The record-page wire shapes (ContactDetail / ContactEngagement in
// api/openapi.yaml). Nullable fields are pointers so "absent" serialises as an
// explicit null; every field the schema marks required is present unconditionally.

type suppressionResponse struct {
	Reason         string    `json:"reason"`
	Email          string    `json:"email"`
	IsPrimaryEmail bool      `json:"is_primary_email"`
	SuppressedAt   time.Time `json:"suppressed_at"`
}

type companyResponse struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Domain string `json:"domain"`
}

type dealResponse struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	PipelineID   string     `json:"pipeline_id"`
	StageID      string     `json:"stage_id"`
	StageLabel   string     `json:"stage_label"`
	StageColor   string     `json:"stage_color"`
	StageIsWon   bool       `json:"stage_is_won"`
	StageIsLost  bool       `json:"stage_is_lost"`
	AmountMicros *int64     `json:"amount_micros"`
	Currency     string     `json:"currency"`
	CloseDate    *time.Time `json:"close_date"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type detailResponse struct {
	ID             string               `json:"id"`
	Email          string               `json:"email"`
	FirstName      string               `json:"first_name"`
	LastName       string               `json:"last_name"`
	JobTitle       string               `json:"job_title"`
	LinkedInURL    string               `json:"linkedin_url"`
	Suppression    *suppressionResponse `json:"suppression"`
	Company        *companyResponse     `json:"company"`
	Deals          []dealResponse       `json:"deals"`
	DealCount      int64                `json:"deal_count"`
	DealsTruncated bool                 `json:"deals_truncated"`
	CreatedAt      time.Time            `json:"created_at"`
	UpdatedAt      time.Time            `json:"updated_at"`
}

type enrollmentResponse struct {
	CampaignID      string     `json:"campaign_id"`
	CampaignName    string     `json:"campaign_name"`
	TrackingEnabled bool       `json:"tracking_enabled"`
	Status          string     `json:"status"`
	CurrentStep     int32      `json:"current_step"`
	StopReason      *string    `json:"stop_reason"`
	EnrolledAt      time.Time  `json:"enrolled_at"`
	LastSentAt      *time.Time `json:"last_sent_at"`
}

type engagementResponse struct {
	ContactID          string               `json:"contact_id"`
	EmailsSent         int64                `json:"emails_sent"`
	OpensIndicative    int64                `json:"opens_indicative"`
	Clicks             int64                `json:"clicks"`
	Replies            int64                `json:"replies"`
	Bounces            int64                `json:"bounces"`
	Unsubscribes       int64                `json:"unsubscribes"`
	OpenRate           float64              `json:"open_rate"`
	ClickRate          float64              `json:"click_rate"`
	CampaignsEnrolled  int64                `json:"campaigns_enrolled"`
	OpensMeasurable    bool                 `json:"opens_measurable"`
	LastActivityAt     *time.Time           `json:"last_activity_at"`
	Campaigns          []enrollmentResponse `json:"campaigns"`
	CampaignsTruncated bool                 `json:"campaigns_truncated"`
}

func (h *Handler) getContact(w http.ResponseWriter, r *http.Request) {
	ws, id, ok := workspaceAndID(w, r)
	if !ok {
		return
	}
	record, err := h.svc.Record(r.Context(), ws, id)
	if err != nil {
		writeRecordError(w, err, "could not load contact")
		return
	}
	httpx.JSON(w, http.StatusOK, detailPayload(record))
}

// companyLinkRequest is the PUT /contacts/{id}/company body. CompanyID is a
// pointer so an explicit null means "unlink" — a single-value PUT makes that
// unambiguous, which is exactly why this is not a PATCH on the contact (a PATCH
// would have to tell an ABSENT company_id apart from a null one, and getting
// that wrong silently either unlinks or refuses to unlink).
type companyLinkRequest struct {
	CompanyID *uuid.UUID `json:"company_id"`
}

func (h *Handler) putContactCompany(w http.ResponseWriter, r *http.Request) {
	ws, id, ok := workspaceAndID(w, r)
	if !ok {
		return
	}
	var req companyLinkRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "body must be {\"company_id\": \"<uuid>\"} or {\"company_id\": null}")
		return
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		httpx.Error(w, http.StatusBadRequest, "body must contain one JSON object")
		return
	}
	record, err := h.svc.SetCompany(r.Context(), ws, id, req.CompanyID)
	if err != nil {
		writeRecordError(w, err, "could not link contact to company")
		return
	}
	httpx.JSON(w, http.StatusOK, detailPayload(record))
}

func (h *Handler) getContactEngagement(w http.ResponseWriter, r *http.Request) {
	ws, id, ok := workspaceAndID(w, r)
	if !ok {
		return
	}
	engagement, err := h.svc.Engagement(r.Context(), ws, id)
	if err != nil {
		writeRecordError(w, err, "could not load contact engagement")
		return
	}
	httpx.JSON(w, http.StatusOK, engagementPayload(engagement))
}

// workspaceAndID resolves the caller's workspace from the verified token and the
// contact id from the path. The path id names the record; it never decides
// tenancy.
func workspaceAndID(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "id must be a uuid")
		return uuid.Nil, uuid.Nil, false
	}
	return ws, id, true
}

func writeRecordError(w http.ResponseWriter, err error, message string) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "contact not found")
	case errors.Is(err, ErrCompanyNotFound):
		httpx.Error(w, http.StatusNotFound, "company not found")
	default:
		httpx.Error(w, http.StatusInternalServerError, message)
	}
}

func detailPayload(record Record) detailResponse {
	out := detailResponse{
		ID: record.ID.String(), Email: record.Email, FirstName: record.FirstName,
		LastName: record.LastName, JobTitle: record.JobTitle, LinkedInURL: record.LinkedInURL,
		Deals:     make([]dealResponse, 0, len(record.Deals)),
		DealCount: record.DealCount, DealsTruncated: record.DealsTruncated,
		CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
	if record.Suppression != nil {
		out.Suppression = &suppressionResponse{
			Reason: record.Suppression.Reason, Email: record.Suppression.Email,
			IsPrimaryEmail: record.Suppression.IsPrimaryEmail, SuppressedAt: record.Suppression.SuppressedAt,
		}
	}
	if record.Company != nil {
		out.Company = &companyResponse{
			ID: record.Company.ID.String(), Name: record.Company.Name, Domain: record.Company.Domain,
		}
	}
	for _, d := range record.Deals {
		out.Deals = append(out.Deals, dealResponse{
			ID: d.ID.String(), Name: d.Name, PipelineID: d.PipelineID.String(), StageID: d.StageID.String(),
			StageLabel: d.StageLabel, StageColor: d.StageColor, StageIsWon: d.StageIsWon,
			StageIsLost: d.StageIsLost, AmountMicros: d.AmountMicros, Currency: d.Currency,
			CloseDate: d.CloseDate, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
		})
	}
	return out
}

func engagementPayload(e Engagement) engagementResponse {
	out := engagementResponse{
		ContactID: e.ContactID.String(), EmailsSent: e.EmailsSent,
		OpensIndicative: e.OpensIndicative, Clicks: e.Clicks, Replies: e.Replies,
		Bounces: e.Bounces, Unsubscribes: e.Unsubscribes, OpenRate: e.OpenRate,
		ClickRate: e.ClickRate, CampaignsEnrolled: e.CampaignsEnrolled,
		OpensMeasurable: e.OpensMeasurable,
		LastActivityAt:  e.LastActivityAt, CampaignsTruncated: e.CampaignsTruncated,
		Campaigns: make([]enrollmentResponse, 0, len(e.Campaigns)),
	}
	for _, c := range e.Campaigns {
		out.Campaigns = append(out.Campaigns, enrollmentResponse{
			CampaignID: c.CampaignID.String(), CampaignName: c.CampaignName,
			TrackingEnabled: c.TrackingEnabled, Status: c.Status,
			CurrentStep: c.CurrentStep, StopReason: c.StopReason, EnrolledAt: c.EnrolledAt,
			LastSentAt: c.LastSentAt,
		})
	}
	return out
}
