package contact

import (
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
)

const maxUploadBytes = 10 << 20 // 10 MB

type Handler struct {
	svc       *Service
	jwtSecret []byte
}

func NewHandler(svc *Service, jwtSecret []byte) *Handler { return &Handler{svc: svc, jwtSecret: jwtSecret} }

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

func (h *Handler) listContacts(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	listID, err := uuid.Parse(r.URL.Query().Get("list"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "list query param required")
		return
	}
	limit, offset := httpx.LimitOffset(r, 50, 200)
	cs, err := h.svc.ListByList(r.Context(), ws, listID, int32(limit), int32(offset))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not list contacts")
		return
	}
	type contactResponse struct {
		ID        string `json:"id"`
		Email     string `json:"email"`
		FirstName string `json:"first_name"`
	}
	out := make([]contactResponse, 0, len(cs))
	for _, c := range cs {
		out = append(out, contactResponse{ID: c.ID.String(), Email: c.Email, FirstName: c.FirstName})
	}
	httpx.JSON(w, http.StatusOK, out)
}

