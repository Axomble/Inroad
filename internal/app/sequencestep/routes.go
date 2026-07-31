package sequencestep

import (
	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Register mounts the step sub-routes onto an already-authenticated campaign
// router (paths are relative to /campaigns). Kept as Register rather than a
// standalone Routes() so the steps live under the same {id} campaign scope and
// inherit the campaign router's auth middleware — chi does not allow two
// routers mounted at the same prefix.
func (h *Handler) Register(r chi.Router) {
	// Steps are part of a campaign, so they share the campaign scopes: listing needs
	// campaigns:read, structural edits campaigns:write. Transparent to session
	// principals (they hold every scope).
	read := auth.RequireScope(auth.ScopeCampaignsRead)
	write := auth.RequireScope(auth.ScopeCampaignsWrite)
	r.With(read).Get("/{id}/steps", h.List)
	r.With(write).Post("/{id}/steps", h.Create)
	// Static /reorder is registered alongside the /{stepId} wildcard; chi
	// prefers the literal segment, so POST .../steps/reorder never resolves to
	// the {stepId} routes (which are PUT/DELETE anyway).
	r.With(write).Post("/{id}/steps/reorder", h.Reorder)
	r.With(write).Put("/{id}/steps/{stepId}", h.Update)
	r.With(write).Delete("/{id}/steps/{stepId}", h.Delete)
}

type stepResponse struct {
	ID           string `json:"id"`
	StepOrder    int32  `json:"step_order"`
	DelaySeconds int32  `json:"delay_seconds"`
	Subject      string `json:"subject"`
	BodyText     string `json:"body_text"`
	BodyHTML     string `json:"body_html"`
}

func toResponse(st gen.SequenceStep) stepResponse {
	return stepResponse{
		ID: st.ID.String(), StepOrder: st.StepOrder, DelaySeconds: st.DelaySeconds,
		Subject: st.Subject, BodyText: st.BodyText, BodyHTML: st.BodyHtml,
	}
}
