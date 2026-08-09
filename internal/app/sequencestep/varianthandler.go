package sequencestep

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
)

const maxVariantBodyBytes = 1 << 20 // 1 MB — an email body, not a payload

// variantResponse is the wire shape of one alternative (StepVariant in
// api/openapi.yaml).
type variantResponse struct {
	ID       string `json:"id"`
	StepID   string `json:"step_id"`
	Label    string `json:"label"`
	Weight   int32  `json:"weight"`
	Subject  string `json:"subject"`
	BodyText string `json:"body_text"`
	BodyHTML string `json:"body_html"`
}

// variantRequest is field-for-field convertible to VariantInput, which is what
// lets the handlers convert rather than restate the mapping — one fewer place
// for a field to be forgotten when the shape grows.
type variantRequest struct {
	Label    string `json:"label"`
	Weight   int32  `json:"weight"`
	Subject  string `json:"subject"`
	BodyText string `json:"body_text"`
	BodyHTML string `json:"body_html"`
}

// baseWeightRequest sets the STEP's own share of the split. Its own endpoint
// rather than a field on the step update, because it is the one attribute of a
// step that changes what an in-flight A/B test measures — mixing it into the
// content PUT would make an unrelated body edit silently reweight the test.
type baseWeightRequest struct {
	Weight int32 `json:"weight"`
}

func (h *Handler) ListVariants(w http.ResponseWriter, r *http.Request) {
	ws, stepID, ok := workspaceAndStepID(w, r)
	if !ok {
		return
	}
	variants, err := h.svc.ListVariants(r.Context(), ws, stepID)
	if err != nil {
		writeVariantError(w, err, "could not load variants")
		return
	}
	httpx.JSON(w, http.StatusOK, variantsPayload(variants))
}

func (h *Handler) CreateVariant(w http.ResponseWriter, r *http.Request) {
	ws, stepID, ok := workspaceAndStepID(w, r)
	if !ok {
		return
	}
	var req variantRequest
	if !decodeVariantBody(w, r, &req) {
		return
	}
	variant, err := h.svc.CreateVariant(r.Context(), ws, stepID, VariantInput(req))
	if err != nil {
		writeVariantError(w, err, "could not create variant")
		return
	}
	httpx.JSON(w, http.StatusCreated, variantPayload(variant))
}

func (h *Handler) UpdateVariant(w http.ResponseWriter, r *http.Request) {
	ws, variantID, ok := workspaceAndVariantID(w, r)
	if !ok {
		return
	}
	var req variantRequest
	if !decodeVariantBody(w, r, &req) {
		return
	}
	variant, err := h.svc.UpdateVariant(r.Context(), ws, variantID, VariantInput(req))
	if err != nil {
		writeVariantError(w, err, "could not update variant")
		return
	}
	httpx.JSON(w, http.StatusOK, variantPayload(variant))
}

func (h *Handler) DeleteVariant(w http.ResponseWriter, r *http.Request) {
	ws, variantID, ok := workspaceAndVariantID(w, r)
	if !ok {
		return
	}
	if err := h.svc.DeleteVariant(r.Context(), ws, variantID); err != nil {
		writeVariantError(w, err, "could not delete variant")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) SetBaseWeight(w http.ResponseWriter, r *http.Request) {
	ws, stepID, ok := workspaceAndStepID(w, r)
	if !ok {
		return
	}
	var req baseWeightRequest
	if !decodeVariantBody(w, r, &req) {
		return
	}
	if err := h.svc.SetBaseWeight(r.Context(), ws, stepID, req.Weight); err != nil {
		writeVariantError(w, err, "could not set the base weight")
		return
	}
	variants, err := h.svc.ListVariants(r.Context(), ws, stepID)
	if err != nil {
		writeVariantError(w, err, "could not load variants")
		return
	}
	httpx.JSON(w, http.StatusOK, variantsPayload(variants))
}

func decodeVariantBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxVariantBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		httpx.Error(w, http.StatusBadRequest, "body must be a single JSON object with known fields")
		return false
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		httpx.Error(w, http.StatusBadRequest, "body must contain one JSON object")
		return false
	}
	return true
}

func workspaceAndStepID(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	return workspaceAndParam(w, r, "stepId")
}

func workspaceAndVariantID(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	return workspaceAndParam(w, r, "variantId")
}

func workspaceAndParam(w http.ResponseWriter, r *http.Request, param string) (uuid.UUID, uuid.UUID, bool) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	id, err := uuid.Parse(chi.URLParam(r, param))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, param+" must be a uuid")
		return uuid.Nil, uuid.Nil, false
	}
	return ws, id, true
}

// writeVariantError maps this feature's errors onto status codes.
//
// ErrVariantHasSends and ErrNoEligibleVariant are 409 rather than 400: the
// request is well-formed and the caller did nothing wrong — the workspace is in
// a state where the operation is not allowed, and the message says which state.
func writeVariantError(w http.ResponseWriter, err error, message string) {
	switch {
	case errors.Is(err, ErrVariantNotFound), errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrVariantLabel), errors.Is(err, ErrVariantWeight):
		httpx.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrLabelTaken):
		httpx.Error(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrTooManyVariants):
		httpx.Error(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrVariantHasSends):
		httpx.Error(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrNoEligibleVariant):
		httpx.Error(w, http.StatusConflict, err.Error())
	default:
		httpx.Error(w, http.StatusInternalServerError, message)
	}
}

func variantPayload(v Variant) variantResponse {
	return variantResponse{
		ID: v.ID.String(), StepID: v.StepID.String(), Label: v.Label, Weight: v.Weight,
		Subject: v.Subject, BodyText: v.BodyText, BodyHTML: v.BodyHTML,
	}
}

func variantsPayload(variants []Variant) []variantResponse {
	out := make([]variantResponse, 0, len(variants))
	for _, v := range variants {
		out = append(out, variantPayload(v))
	}
	return out
}
