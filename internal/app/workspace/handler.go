package workspace

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
)

type Handler struct {
	svc       *Service
	jwtSecret []byte
}

func NewHandler(svc *Service, jwtSecret []byte) *Handler {
	return &Handler{svc: svc, jwtSecret: jwtSecret}
}

type registerRequest struct {
	WorkspaceName string `json:"workspace_name"`
	Email         string `json:"email"`
	Password      string `json:"password"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type tokenResponse struct {
	Token       string `json:"token"`
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.WorkspaceName == "" || req.Email == "" || len(req.Password) < 8 {
		httpx.Error(w, http.StatusBadRequest, "workspace_name, email, and 8+ char password required")
		return
	}
	res, err := h.svc.Register(r.Context(), RegisterInput{
		WorkspaceName: req.WorkspaceName, Email: req.Email, Password: req.Password,
	})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not register")
		return
	}
	h.issue(w, res.UserID, res.WorkspaceID)
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	uid, wid, err := h.svc.Authenticate(r.Context(), req.Email, req.Password)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	h.issue(w, uid, wid)
}

func (h *Handler) issue(w http.ResponseWriter, userID, workspaceID string) {
	tok, err := auth.IssueToken(h.jwtSecret, userID, workspaceID, 24*time.Hour)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not issue token")
		return
	}
	httpx.JSON(w, http.StatusOK, tokenResponse{Token: tok, WorkspaceID: workspaceID, UserID: userID})
}
