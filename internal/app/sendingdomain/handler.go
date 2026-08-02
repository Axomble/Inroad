package sendingdomain

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
)

// Handler exposes the sending-domain surface over HTTP. Authentication is
// applied by the protected router group (see cmd/inroad), not here.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// spfStatus / dmarcStatus / dkimStatus mirror the frozen API schemas. dkim is
// advisory: found=false means "none of the probed selectors matched", NOT that
// the domain is unsigned, and it never contributes to state.
type spfStatus struct {
	Found  bool   `json:"found"`
	Record string `json:"record"`
}

type dmarcStatus struct {
	Found bool `json:"found"`
	// Policy is the p= tag: "none" | "quarantine" | "reject", or "" when the
	// record is absent or its policy unreadable. "none" is monitoring only.
	Policy string `json:"policy"`
}

type dkimStatus struct {
	Found    bool   `json:"found"`
	Selector string `json:"selector"`
}

// sendingDomainResponse is the wire shape (components/schemas/SendingDomain).
// CheckedAt is a pointer so a never-checked domain serializes as null rather
// than as a zero timestamp that would read as "checked in year 1".
type sendingDomainResponse struct {
	Domain       string      `json:"domain"`
	State        string      `json:"state"`
	SPF          spfStatus   `json:"spf"`
	DMARC        dmarcStatus `json:"dmarc"`
	DKIM         dkimStatus  `json:"dkim"`
	MailboxCount int64       `json:"mailbox_count"`
	CheckedAt    *string     `json:"checked_at"`
}

func toResponse(d Domain) sendingDomainResponse {
	out := sendingDomainResponse{
		Domain:       d.Domain,
		State:        string(d.State),
		SPF:          spfStatus{Found: d.SPFFound, Record: d.SPFRecord},
		DMARC:        dmarcStatus{Found: d.DMARCFound, Policy: d.DMARCPolicy},
		DKIM:         dkimStatus{Found: d.DKIMFound, Selector: d.DKIMSelector},
		MailboxCount: d.MailboxCount,
	}
	if d.CheckedAt != nil {
		ts := d.CheckedAt.UTC().Format(time.RFC3339)
		out.CheckedAt = &ts
	}
	return out
}

func writeErr(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNotFound) {
		httpx.Error(w, http.StatusNotFound, "no mailbox on that domain")
		return
	}
	httpx.Error(w, http.StatusInternalServerError, "internal error")
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	domains, err := h.svc.List(r.Context(), wid)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]sendingDomainResponse, 0, len(domains))
	for _, d := range domains {
		out = append(out, toResponse(d))
	}
	httpx.JSON(w, http.StatusOK, out)
}

// check re-runs the lookups for one domain. The {domain} path parameter is
// caller-controlled, so it is resolved against the workspace's own mailboxes
// first (404) and only then handed to the resolver.
func (h *Handler) check(w http.ResponseWriter, r *http.Request) {
	wid, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	d, err := h.svc.Check(r.Context(), wid, chi.URLParam(r, "domain"))
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toResponse(d))
}
