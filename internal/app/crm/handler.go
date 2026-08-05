package crm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
)

type Handler struct{ svc *Service }

func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

type companyRequest struct {
	Name                string     `json:"name"`
	Domain              string     `json:"domain"`
	OwnerUserID         *uuid.UUID `json:"owner_user_id"`
	AnnualRevenueMicros *int64     `json:"annual_revenue_micros"`
	Currency            string     `json:"currency"`
}

type pipelineRequest struct {
	Name string `json:"name"`
}

type stageRequest struct {
	Label    string `json:"label"`
	Color    string `json:"color"`
	Position int32  `json:"position"`
	IsWon    bool   `json:"is_won"`
	IsLost   bool   `json:"is_lost"`
}

type dealRequest struct {
	PipelineID       uuid.UUID  `json:"pipeline_id"`
	StageID          uuid.UUID  `json:"stage_id"`
	CompanyID        *uuid.UUID `json:"company_id"`
	PrimaryContactID *uuid.UUID `json:"primary_contact_id"`
	OwnerUserID      *uuid.UUID `json:"owner_user_id"`
	Name             string     `json:"name"`
	AmountMicros     *int64     `json:"amount_micros"`
	Currency         string     `json:"currency"`
	CloseDate        *time.Time `json:"close_date"`
}

type noteRequest struct {
	Title      string    `json:"title"`
	Body       string    `json:"body"`
	TargetType string    `json:"target_type"`
	TargetID   uuid.UUID `json:"target_id"`
}
type taskRequest struct {
	Title          string     `json:"title"`
	Body           string     `json:"body"`
	DueAt          *time.Time `json:"due_at"`
	Status         string     `json:"status"`
	AssigneeUserID *uuid.UUID `json:"assignee_user_id"`
	TargetType     string     `json:"target_type"`
	TargetID       uuid.UUID  `json:"target_id"`
}
type contactEmailRequest struct {
	Email string `json:"email"`
}
type moveDealRequest struct {
	StageID      uuid.UUID  `json:"stage_id"`
	BeforeDealID *uuid.UUID `json:"before_deal_id"`
	AfterDealID  *uuid.UUID `json:"after_deal_id"`
}
type settingsRequest struct {
	AutoCapturePolicy string `json:"auto_capture_policy"`
}

func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Group(func(read chi.Router) {
		read.Use(auth.RequireScope(auth.ScopeCRMRead))
		read.Get("/companies", h.listCompanies)
		read.Get("/companies/{id}", h.getCompany)
		read.Get("/pipelines", h.listPipelines)
		read.Get("/pipelines/{id}", h.getPipeline)
		read.Get("/deals", h.listDeals)
		read.Get("/board", h.getBoard)
		read.Get("/deals/{id}", h.getDeal)
		read.Get("/deals/{id}/threads", h.listDealThreads)
		read.Get("/events", h.listEvents)
		read.Get("/settings", h.getSettings)
		read.Get("/notes", h.listNotes)
		read.Get("/tasks", h.listTasks)
		read.Get("/contacts/{id}/emails", h.listContactEmails)
	})
	r.Group(func(write chi.Router) {
		write.Use(auth.RequireScope(auth.ScopeCRMWrite))
		write.Post("/companies", h.createCompany)
		write.Put("/companies/{id}", h.updateCompany)
		write.Delete("/companies/{id}", h.deleteCompany)
		write.Post("/pipelines", h.createPipeline)
		write.Put("/pipelines/{id}", h.updatePipeline)
		write.Delete("/pipelines/{id}", h.deletePipeline)
		write.Post("/pipelines/{id}/stages", h.createStage)
		write.Put("/pipelines/{id}/stages/{stageID}", h.updateStage)
		write.Delete("/pipelines/{id}/stages/{stageID}", h.deleteStage)
		write.Post("/deals", h.createDeal)
		write.Put("/deals/{id}", h.updateDeal)
		write.Post("/deals/{id}/move", h.moveDeal)
		write.Delete("/deals/{id}", h.deleteDeal)
		write.Post("/notes", h.createNote)
		write.Put("/notes/{id}", h.updateNote)
		write.Delete("/notes/{id}", h.deleteNote)
		write.Post("/tasks", h.createTask)
		write.Put("/tasks/{id}", h.updateTask)
		write.Delete("/tasks/{id}", h.deleteTask)
		write.Put("/settings", h.updateSettings)
		// A contact's alias set decides contacts.email, which IS the recipient
		// on the send path (queries/stepsend.sql). Writing it therefore
		// requires the contact-writing capability as well: a key holding only
		// crm:write must not be able to redirect a live campaign's mail or
		// rotate a contact off a suppressed address.
		write.Group(func(contacts chi.Router) {
			contacts.Use(auth.RequireScope(auth.ScopeContactsWrite))
			contacts.Post("/contacts/{id}/emails", h.addContactEmail)
			contacts.Put("/contacts/{id}/emails/{emailID}/primary", h.setPrimaryContactEmail)
		})
	})
	return r
}

func (h *Handler) listCompanies(w http.ResponseWriter, r *http.Request) {
	page, err := queryPage(r)
	if err != nil {
		writeError(w, err)
		return
	}
	h.list(w, r, func(ws uuid.UUID) (any, error) { return h.svc.ListCompanies(r.Context(), ws, page) })
}
func (h *Handler) getCompany(w http.ResponseWriter, r *http.Request) {
	h.get(w, r, func(ws, id uuid.UUID) (any, error) { return h.svc.GetCompany(r.Context(), ws, id) })
}
func (h *Handler) createCompany(w http.ResponseWriter, r *http.Request) {
	var req companyRequest
	if !decode(w, r, &req) {
		return
	}
	h.create(w, r, func(ws uuid.UUID, actor Actor) (any, error) {
		return h.svc.CreateCompanyWithActor(r.Context(), ws, CompanyInput(req), actor)
	})
}
func (h *Handler) updateCompany(w http.ResponseWriter, r *http.Request) {
	var req companyRequest
	if !decode(w, r, &req) {
		return
	}
	h.update(w, r, func(ws, id uuid.UUID, actor Actor) (any, error) {
		return h.svc.UpdateCompanyWithActor(r.Context(), ws, id, CompanyInput(req), actor)
	})
}
func (h *Handler) deleteCompany(w http.ResponseWriter, r *http.Request) {
	h.remove(w, r, h.svc.DeleteCompany)
}

func (h *Handler) listPipelines(w http.ResponseWriter, r *http.Request) {
	h.list(w, r, func(ws uuid.UUID) (any, error) {
		items, err := h.svc.ListPipelines(r.Context(), ws)
		return map[string]any{"items": items}, err
	})
}
func (h *Handler) getPipeline(w http.ResponseWriter, r *http.Request) {
	h.get(w, r, func(ws, id uuid.UUID) (any, error) { return h.svc.GetPipeline(r.Context(), ws, id) })
}
func (h *Handler) createPipeline(w http.ResponseWriter, r *http.Request) {
	var req pipelineRequest
	if !decode(w, r, &req) {
		return
	}
	h.create(w, r, func(ws uuid.UUID, _ Actor) (any, error) {
		return h.svc.CreatePipeline(r.Context(), ws, PipelineInput(req))
	})
}
func (h *Handler) updatePipeline(w http.ResponseWriter, r *http.Request) {
	var req pipelineRequest
	if !decode(w, r, &req) {
		return
	}
	h.update(w, r, func(ws, id uuid.UUID, _ Actor) (any, error) {
		return h.svc.UpdatePipeline(r.Context(), ws, id, PipelineInput(req))
	})
}
func (h *Handler) deletePipeline(w http.ResponseWriter, r *http.Request) {
	h.remove(w, r, h.svc.DeletePipeline)
}

func (h *Handler) createStage(w http.ResponseWriter, r *http.Request) {
	var req stageRequest
	if !decode(w, r, &req) {
		return
	}
	h.create(w, r, func(ws uuid.UUID, _ Actor) (any, error) {
		id, err := parsePath(r, "id")
		if err != nil {
			return nil, err
		}
		return h.svc.CreateStage(r.Context(), ws, id, StageInput(req))
	})
}
func (h *Handler) updateStage(w http.ResponseWriter, r *http.Request) {
	var req stageRequest
	if !decode(w, r, &req) {
		return
	}
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	pipelineID, err := parsePath(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	stageID, err := parsePath(r, "stageID")
	if err != nil {
		writeError(w, err)
		return
	}
	value, err := h.svc.UpdateStage(r.Context(), ws, pipelineID, stageID, StageInput(req))
	respond(w, http.StatusOK, value, err)
}
func (h *Handler) deleteStage(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	pipelineID, err := parsePath(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	stageID, err := parsePath(r, "stageID")
	if err != nil {
		writeError(w, err)
		return
	}
	if err := h.svc.DeleteStage(r.Context(), ws, pipelineID, stageID); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listDeals(w http.ResponseWriter, r *http.Request) {
	page, err := queryPage(r)
	if err != nil {
		writeError(w, err)
		return
	}
	h.list(w, r, func(ws uuid.UUID) (any, error) { return h.svc.ListDeals(r.Context(), ws, page) })
}
func (h *Handler) getDeal(w http.ResponseWriter, r *http.Request) {
	h.get(w, r, func(ws, id uuid.UUID) (any, error) { return h.svc.GetDeal(r.Context(), ws, id) })
}
func (h *Handler) getBoard(w http.ResponseWriter, r *http.Request) {
	var pipelineID *uuid.UUID
	if raw := r.URL.Query().Get("pipeline_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, validation("pipeline_id must be a UUID"))
			return
		}
		pipelineID = &id
	}
	h.list(w, r, func(ws uuid.UUID) (any, error) { return h.svc.GetBoard(r.Context(), ws, pipelineID) })
}
func (h *Handler) moveDeal(w http.ResponseWriter, r *http.Request) {
	var req moveDealRequest
	if !decode(w, r, &req) {
		return
	}
	h.update(w, r, func(ws, id uuid.UUID, actor Actor) (any, error) {
		return h.svc.MoveDeal(r.Context(), ws, id, MoveDealInput{
			StageID: req.StageID, BeforeDealID: req.BeforeDealID, AfterDealID: req.AfterDealID, Actor: actor,
		})
	})
}
func (h *Handler) listDealThreads(w http.ResponseWriter, r *http.Request) {
	h.get(w, r, func(ws, id uuid.UUID) (any, error) {
		items, err := h.svc.ListDealThreads(r.Context(), ws, id)
		return map[string]any{"items": items}, err
	})
}
func (h *Handler) listEvents(w http.ResponseWriter, r *http.Request) {
	target, err := queryTarget(r)
	if err != nil {
		writeError(w, err)
		return
	}
	h.list(w, r, func(ws uuid.UUID) (any, error) {
		items, err := h.svc.ListEvents(r.Context(), ws, target)
		return map[string]any{"items": items}, err
	})
}
func (h *Handler) getSettings(w http.ResponseWriter, r *http.Request) {
	h.list(w, r, func(ws uuid.UUID) (any, error) { return h.svc.GetSettings(r.Context(), ws) })
}
func (h *Handler) updateSettings(w http.ResponseWriter, r *http.Request) {
	var req settingsRequest
	if !decode(w, r, &req) {
		return
	}
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	value, err := h.svc.UpdateSettings(r.Context(), ws, req.AutoCapturePolicy)
	respond(w, http.StatusOK, value, err)
}
func (h *Handler) createDeal(w http.ResponseWriter, r *http.Request) {
	var req dealRequest
	if !decode(w, r, &req) {
		return
	}
	h.create(w, r, func(ws uuid.UUID, actor Actor) (any, error) {
		return h.svc.CreateDeal(r.Context(), ws, dealInput(req, actor))
	})
}
func (h *Handler) updateDeal(w http.ResponseWriter, r *http.Request) {
	var req dealRequest
	if !decode(w, r, &req) {
		return
	}
	h.update(w, r, func(ws, id uuid.UUID, actor Actor) (any, error) {
		return h.svc.UpdateDeal(r.Context(), ws, id, dealInput(req, actor))
	})
}
func (h *Handler) deleteDeal(w http.ResponseWriter, r *http.Request) {
	h.remove(w, r, h.svc.DeleteDeal)
}

func (h *Handler) listNotes(w http.ResponseWriter, r *http.Request) {
	target, page, err := queryTargetPage(r)
	if err != nil {
		writeError(w, err)
		return
	}
	h.list(w, r, func(ws uuid.UUID) (any, error) { return h.svc.ListNotes(r.Context(), ws, target, page) })
}
func (h *Handler) createNote(w http.ResponseWriter, r *http.Request) {
	var req noteRequest
	if !decode(w, r, &req) {
		return
	}
	h.create(w, r, func(ws uuid.UUID, actor Actor) (any, error) {
		return h.svc.CreateNote(r.Context(), ws, NoteInput{Title: req.Title, Body: req.Body, Target: Target{Type: req.TargetType, ID: req.TargetID}, Actor: actor})
	})
}
func (h *Handler) updateNote(w http.ResponseWriter, r *http.Request) {
	var req noteRequest
	if !decode(w, r, &req) {
		return
	}
	h.update(w, r, func(ws, id uuid.UUID, _ Actor) (any, error) {
		return h.svc.UpdateNote(r.Context(), ws, id, req.Title, req.Body)
	})
}
func (h *Handler) deleteNote(w http.ResponseWriter, r *http.Request) {
	h.remove(w, r, h.svc.DeleteNote)
}

func (h *Handler) listTasks(w http.ResponseWriter, r *http.Request) {
	target, page, err := queryTargetPage(r)
	if err != nil {
		writeError(w, err)
		return
	}
	h.list(w, r, func(ws uuid.UUID) (any, error) { return h.svc.ListTasks(r.Context(), ws, target, page) })
}
func (h *Handler) createTask(w http.ResponseWriter, r *http.Request) {
	var req taskRequest
	if !decode(w, r, &req) {
		return
	}
	h.create(w, r, func(ws uuid.UUID, actor Actor) (any, error) {
		return h.svc.CreateTask(r.Context(), ws, taskInput(req, actor))
	})
}
func (h *Handler) updateTask(w http.ResponseWriter, r *http.Request) {
	var req taskRequest
	if !decode(w, r, &req) {
		return
	}
	h.update(w, r, func(ws, id uuid.UUID, actor Actor) (any, error) {
		return h.svc.UpdateTask(r.Context(), ws, id, taskInput(req, actor))
	})
}
func (h *Handler) deleteTask(w http.ResponseWriter, r *http.Request) {
	h.remove(w, r, h.svc.DeleteTask)
}

func (h *Handler) listContactEmails(w http.ResponseWriter, r *http.Request) {
	h.get(w, r, func(ws, id uuid.UUID) (any, error) {
		items, err := h.svc.ListContactEmails(r.Context(), ws, id)
		return map[string]any{"items": items}, err
	})
}
func (h *Handler) addContactEmail(w http.ResponseWriter, r *http.Request) {
	var req contactEmailRequest
	if !decode(w, r, &req) {
		return
	}
	h.updateStatus(w, r, http.StatusCreated, func(ws, id uuid.UUID, _ Actor) (any, error) {
		return h.svc.AddContactEmail(r.Context(), ws, id, req.Email)
	})
}
func (h *Handler) setPrimaryContactEmail(w http.ResponseWriter, r *http.Request) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	contactID, err := parsePath(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	emailID, err := parsePath(r, "emailID")
	if err != nil {
		writeError(w, err)
		return
	}
	if err := h.svc.SetPrimaryContactEmail(r.Context(), ws, contactID, emailID); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func dealInput(req dealRequest, actor Actor) DealInput {
	return DealInput{PipelineID: req.PipelineID, StageID: req.StageID, CompanyID: req.CompanyID, PrimaryContactID: req.PrimaryContactID, OwnerUserID: req.OwnerUserID, Name: req.Name, AmountMicros: req.AmountMicros, Currency: req.Currency, CloseDate: req.CloseDate, Source: "manual", Actor: actor}
}
func taskInput(req taskRequest, actor Actor) TaskInput {
	return TaskInput{Title: req.Title, Body: req.Body, DueAt: req.DueAt, Status: req.Status, AssigneeUserID: req.AssigneeUserID, Target: Target{Type: req.TargetType, ID: req.TargetID}, Actor: actor}
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request, fn func(uuid.UUID) (any, error)) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	value, err := fn(ws)
	respond(w, http.StatusOK, value, err)
}
func (h *Handler) get(w http.ResponseWriter, r *http.Request, fn func(uuid.UUID, uuid.UUID) (any, error)) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := parsePath(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	value, err := fn(ws, id)
	respond(w, http.StatusOK, value, err)
}
func (h *Handler) create(w http.ResponseWriter, r *http.Request, fn func(uuid.UUID, Actor) (any, error)) {
	ws, actor, ok := attributed(w, r)
	if !ok {
		return
	}
	value, err := fn(ws, actor)
	respond(w, http.StatusCreated, value, err)
}
func (h *Handler) update(w http.ResponseWriter, r *http.Request, fn func(uuid.UUID, uuid.UUID, Actor) (any, error)) {
	h.updateStatus(w, r, http.StatusOK, fn)
}
func (h *Handler) updateStatus(w http.ResponseWriter, r *http.Request, status int, fn func(uuid.UUID, uuid.UUID, Actor) (any, error)) {
	ws, actor, ok := attributed(w, r)
	if !ok {
		return
	}
	id, err := parsePath(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	value, err := fn(ws, id, actor)
	respond(w, status, value, err)
}
func (h *Handler) remove(w http.ResponseWriter, r *http.Request, fn func(context.Context, uuid.UUID, uuid.UUID) error) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	id, err := parsePath(r, "id")
	if err != nil {
		writeError(w, err)
		return
	}
	if err := fn(r.Context(), ws, id); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// attributed resolves the caller's workspace AND the actor every write is
// stamped with. Attribution is not optional: an unidentifiable principal fails
// the request rather than recording an empty actor, which would look like an
// unattributed write in the activity feed and defeat validateDeal's own check.
func attributed(w http.ResponseWriter, r *http.Request) (uuid.UUID, Actor, bool) {
	ws, ok := auth.WorkspaceID(w, r)
	if !ok {
		return uuid.Nil, Actor{}, false
	}
	p, ok := auth.UserFromContext(r.Context())
	if !ok || p.UserID == "" {
		httpx.Error(w, http.StatusUnauthorized, "unauthorized")
		return uuid.Nil, Actor{}, false
	}
	if id, err := uuid.Parse(p.UserID); err == nil {
		return ws, UserActor(id), true
	}
	return ws, Actor{Type: "user", ID: p.UserID}, true
}

// queryPage reads the shared ?limit=&cursor= page controls. A limit that is
// not a positive integer is rejected rather than silently defaulted, so a
// typo'd page size cannot masquerade as a truncated result.
func queryPage(r *http.Request) (PageRequest, error) {
	page := PageRequest{Cursor: r.URL.Query().Get("cursor")}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || limit < 1 {
			return PageRequest{}, validation("limit must be a positive integer")
		}
		page.Limit = int32(limit)
	}
	return page, nil
}

func queryTargetPage(r *http.Request) (Target, PageRequest, error) {
	target, err := queryTarget(r)
	if err != nil {
		return Target{}, PageRequest{}, err
	}
	page, err := queryPage(r)
	if err != nil {
		return Target{}, PageRequest{}, err
	}
	return target, page, nil
}

func queryTarget(r *http.Request) (Target, error) {
	id, err := uuid.Parse(r.URL.Query().Get("target_id"))
	if err != nil {
		return Target{}, validation("target_id must be a UUID")
	}
	return Target{Type: r.URL.Query().Get("target_type"), ID: id}, nil
}
func parsePath(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		return uuid.Nil, validation(name + " must be a UUID")
	}
	return id, nil
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		httpx.Error(w, http.StatusBadRequest, "body must contain one JSON object")
		return false
	}
	return true
}
func respond(w http.ResponseWriter, status int, value any, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	httpx.JSON(w, status, value)
}
func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "CRM record not found")
	case errors.Is(err, ErrConflict):
		httpx.Error(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrValidation):
		httpx.Error(w, http.StatusUnprocessableEntity, err.Error())
	default:
		httpx.Error(w, http.StatusInternalServerError, "CRM request failed")
	}
}
