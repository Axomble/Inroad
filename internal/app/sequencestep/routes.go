package sequencestep

import (
	"github.com/go-chi/chi/v5"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Register mounts the step sub-routes onto an already-authenticated campaign
// router (paths are relative to /campaigns). Kept as Register rather than a
// standalone Routes() so the steps live under the same {id} campaign scope and
// inherit the campaign router's auth middleware — chi does not allow two
// routers mounted at the same prefix.
func (h *Handler) Register(r chi.Router) {
	r.Get("/{id}/steps", h.List)
	r.Post("/{id}/steps", h.Create)
	r.Put("/{id}/steps/{stepId}", h.Update)
	r.Delete("/{id}/steps/{stepId}", h.Delete)
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
