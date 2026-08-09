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

// maxFieldBodyBytes bounds a definition or value payload. Values are capped
// individually too (maxValueBytes); this stops a caller sending ten thousand of
// them in one request.
const maxFieldBodyBytes = 1 << 18 // 256 KB

// fieldDefResponse is the wire shape of one definition (CustomFieldDef in
// api/openapi.yaml). Options is always present — an empty array for a
// non-select — so a client never has to distinguish null from absent.
type fieldDefResponse struct {
	ID         string     `json:"id"`
	Key        string     `json:"key"`
	Label      string     `json:"label"`
	Type       string     `json:"type"`
	Options    []string   `json:"options"`
	CreatedAt  time.Time  `json:"created_at"`
	Archived   bool       `json:"archived"`
	ArchivedAt *time.Time `json:"archived_at"`
}

// fieldValueResponse pairs a stored value with the definition describing it.
// Def is null for a value whose key has no live definition — an archived field,
// or one written before definitions existed. The client renders those read-only
// rather than hiding them, because they are real data still being sent.
type fieldValueResponse struct {
	Key   string            `json:"key"`
	Value string            `json:"value"`
	Def   *fieldDefResponse `json:"def"`
}

type createFieldRequest struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Type    string   `json:"type"`
	Options []string `json:"options"`
}

// updateFieldRequest carries the two mutable attributes. Key and type are
// absent by design, not omitted by oversight: both are load-bearing on data
// already written, so changing them is archive-and-recreate, not an edit.
type updateFieldRequest struct {
	Label   string   `json:"label"`
	Options []string `json:"options"`
}

type setFieldValuesRequest struct {
	// Values is the contact's COMPLETE custom field set as the form rendered
	// it. A live key omitted here is cleared; see Service.SetContactFields for
	// why that is a replace rather than a merge.
	Values map[string]string `json:"values"`
}

func (h *Handler) listFieldDefs(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	defs, err := h.svc.ListFieldDefs(r.Context(), ws)
	if err != nil {
		writeFieldError(w, err, "could not load custom fields")
		return
	}
	out := make([]fieldDefResponse, 0, len(defs))
	for _, d := range defs {
		out = append(out, fieldDefPayload(d))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) createFieldDef(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	var req createFieldRequest
	if !decodeFieldBody(w, r, &req) {
		return
	}
	def, err := h.svc.CreateFieldDef(r.Context(), ws, FieldDefInput{
		Key: req.Key, Label: req.Label, Type: FieldType(req.Type), Options: req.Options,
	})
	if err != nil {
		writeFieldError(w, err, "could not create custom field")
		return
	}
	httpx.JSON(w, http.StatusCreated, fieldDefPayload(def))
}

func (h *Handler) updateFieldDef(w http.ResponseWriter, r *http.Request) {
	ws, id, ok := workspaceAndFieldID(w, r)
	if !ok {
		return
	}
	var req updateFieldRequest
	if !decodeFieldBody(w, r, &req) {
		return
	}
	def, err := h.svc.UpdateFieldDef(r.Context(), ws, id, req.Label, req.Options)
	if err != nil {
		writeFieldError(w, err, "could not update custom field")
		return
	}
	httpx.JSON(w, http.StatusOK, fieldDefPayload(def))
}

// archiveFieldDef retires a definition. It returns 200 with the archived row
// rather than 204, because the response is what tells the client the field is
// now archived-but-present — a 204 would read as "deleted" and invite a UI that
// drops it from the list, hiding the values contacts still carry under it.
func (h *Handler) archiveFieldDef(w http.ResponseWriter, r *http.Request) {
	ws, id, ok := workspaceAndFieldID(w, r)
	if !ok {
		return
	}
	def, err := h.svc.ArchiveFieldDef(r.Context(), ws, id)
	if err != nil {
		writeFieldError(w, err, "could not archive custom field")
		return
	}
	httpx.JSON(w, http.StatusOK, fieldDefPayload(def))
}

func (h *Handler) getContactFields(w http.ResponseWriter, r *http.Request) {
	ws, id, ok := workspaceAndID(w, r)
	if !ok {
		return
	}
	values, err := h.svc.ContactFields(r.Context(), ws, id)
	if err != nil {
		writeFieldError(w, err, "could not load contact custom fields")
		return
	}
	httpx.JSON(w, http.StatusOK, fieldValuesPayload(values))
}

func (h *Handler) putContactFields(w http.ResponseWriter, r *http.Request) {
	ws, id, ok := workspaceAndID(w, r)
	if !ok {
		return
	}
	var req setFieldValuesRequest
	if !decodeFieldBody(w, r, &req) {
		return
	}
	values, err := h.svc.SetContactFields(r.Context(), ws, id, req.Values)
	if err != nil {
		writeFieldError(w, err, "could not save contact custom fields")
		return
	}
	httpx.JSON(w, http.StatusOK, fieldValuesPayload(values))
}

// decodeFieldBody reads exactly one JSON object, rejecting unknown members so a
// misspelled field name is an error rather than a silently ignored setting.
func decodeFieldBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxFieldBodyBytes))
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

func workspaceAndFieldID(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	id, err := uuid.Parse(chi.URLParam(r, "fieldID"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "fieldID must be a uuid")
		return uuid.Nil, uuid.Nil, false
	}
	return ws, id, true
}

// writeFieldError maps this feature's errors onto status codes. An
// InvalidFieldError carries its own reason to the caller because it names
// something the caller can fix (a bad key, a value the type rejects); the
// sentinels get fixed copy. Anything unrecognised is a 500 with a generic
// message — never the raw error, which could carry storage detail.
func writeFieldError(w http.ResponseWriter, err error, message string) {
	var invalid *InvalidFieldError
	switch {
	case errors.As(err, &invalid):
		httpx.Error(w, http.StatusBadRequest, invalid.Error())
	case errors.Is(err, ErrFieldNotFound):
		httpx.Error(w, http.StatusNotFound, "custom field not found")
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "contact not found")
	case errors.Is(err, ErrFieldKeyTaken):
		httpx.Error(w, http.StatusConflict, "a custom field with this key already exists (archived fields keep their key)")
	case errors.Is(err, ErrFieldArchived):
		httpx.Error(w, http.StatusConflict, "this custom field is archived and cannot be edited")
	case errors.Is(err, ErrTooManyFields):
		httpx.Error(w, http.StatusConflict, "this workspace already has the maximum number of custom fields")
	default:
		httpx.Error(w, http.StatusInternalServerError, message)
	}
}

func fieldDefPayload(d FieldDef) fieldDefResponse {
	options := d.Options
	if options == nil {
		options = []string{}
	}
	return fieldDefResponse{
		ID: d.ID.String(), Key: d.Key, Label: d.Label, Type: string(d.Type),
		Options: options, CreatedAt: d.CreatedAt,
		Archived: !d.Live(), ArchivedAt: d.ArchivedAt,
	}
}

func fieldValuesPayload(values []FieldValue) []fieldValueResponse {
	out := make([]fieldValueResponse, 0, len(values))
	for _, v := range values {
		row := fieldValueResponse{Key: v.Key, Value: v.Value}
		if v.Def != nil {
			def := fieldDefPayload(*v.Def)
			row.Def = &def
		}
		out = append(out, row)
	}
	return out
}
