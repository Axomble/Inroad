package contact

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
)

const maxUploadBytes = 10 << 20 // 10 MB

// Handler exposes the contact domain over HTTP. Authentication is applied by
// the protected router group (see cmd/inroad), not here.
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func (h *Handler) importCSV(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	listID, err := uuid.Parse(r.URL.Query().Get("list"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "list query param required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	file, _, err := r.FormFile("file")
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "missing 'file' upload")
		return
	}
	defer file.Close()

	res, err := h.svc.ImportCSV(r.Context(), ws, listID, file)
	if errors.Is(err, ErrListNotFound) {
		httpx.Error(w, http.StatusNotFound, "list not found")
		return
	}
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

// contactResponse is the wire shape of one contact. Kept minimal by
// construction — the search columns (last name, company) are matched against
// but not returned, matching the committed Contact schema.
type contactResponse struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	FirstName string `json:"first_name"`
}

// pageResponse is the ContactPage schema. The cursors are pointers so an
// absent neighbour serialises as an explicit null rather than an empty string —
// the client distinguishes "no next page" from "a cursor I failed to read".
type pageResponse struct {
	Items         []contactResponse `json:"items"`
	NextCursor    *string           `json:"next_cursor"`
	PrevCursor    *string           `json:"prev_cursor"`
	Total         int64             `json:"total"`
	TotalIsCapped bool              `json:"total_is_capped"`
}

func (h *Handler) listContacts(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	req, err := parseSearchRequest(r)
	if err != nil {
		httpx.Error(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	page, err := h.svc.Search(r.Context(), ws, req)
	switch {
	case errors.Is(err, ErrListNotFound):
		httpx.Error(w, http.StatusNotFound, "list not found")
		return
	case IsCursorError(err):
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	case IsValidationError(err):
		httpx.Error(w, http.StatusUnprocessableEntity, err.Error())
		return
	case err != nil:
		httpx.Error(w, http.StatusInternalServerError, "could not list contacts")
		return
	}

	items := make([]contactResponse, 0, len(page.Items))
	for _, c := range page.Items {
		items = append(items, contactResponse{ID: c.ID.String(), Email: c.Email, FirstName: c.FirstName})
	}
	httpx.JSON(w, http.StatusOK, pageResponse{
		Items:         items,
		NextCursor:    page.NextCursor,
		PrevCursor:    page.PrevCursor,
		Total:         page.Total,
		TotalIsCapped: page.TotalIsCapped,
	})
}

// parseSearchRequest reads the query string into a SearchRequest. It only
// reports shape errors (a value that is not a uuid or not an integer); range
// and vocabulary checks belong to the service, which owns the rules.
func parseSearchRequest(r *http.Request) (SearchRequest, error) {
	qs := r.URL.Query()
	req := SearchRequest{Q: qs.Get("q"), Sort: qs.Get("sort"), Cursor: qs.Get("cursor")}
	if raw := qs.Get("list"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return SearchRequest{}, errors.New("list must be a uuid")
		}
		req.ListID = &id
	}
	if raw := qs.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return SearchRequest{}, errors.New("limit must be an integer")
		}
		req.Limit = &n
	}
	return req, nil
}
