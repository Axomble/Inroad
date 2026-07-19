package contact

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/app/list"
	"github.com/inroad/inroad/internal/platform/httpx"
)

const maxUploadBytes = 10 << 20 // 10 MB

type Handler struct {
	svc       *Service
	jwtSecret []byte
}

func NewHandler(svc *Service, jwtSecret []byte) *Handler { return &Handler{svc: svc, jwtSecret: jwtSecret} }

func (h *Handler) importCSV(w http.ResponseWriter, r *http.Request) {
	ws, ok := wsID(w, r)
	if !ok {
		return
	}
	listID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "bad list id")
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
	if errors.Is(err, list.ErrNotFound) {
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
	ws, ok := wsID(w, r)
	if !ok {
		return
	}
	listID, err := uuid.Parse(r.URL.Query().Get("list"))
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "list query param required")
		return
	}
	limit := clamp(atoiDefault(r.URL.Query().Get("limit"), 50), 1, 200)
	offset := max0(atoiDefault(r.URL.Query().Get("offset"), 0))
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

func wsID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	claims, ok := auth.UserFromContext(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthorized")
		return uuid.Nil, false
	}
	id, err := uuid.Parse(claims.WorkspaceID)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "bad workspace")
		return uuid.Nil, false
	}
	return id, true
}

func atoiDefault(s string, d int) int {
	if s == "" {
		return d
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return d
	}
	return n
}
func clamp(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}
func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
