// Package crm owns the relational CRM foundation: companies, pipelines,
// deals, notes, tasks, and contact email aliases. It deliberately keeps these
// tightly related aggregates in one app package so their transactional writes
// do not require app-to-app imports.
package crm

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrNotFound   = errors.New("crm: record not found")
	ErrConflict   = errors.New("crm: record conflicts with existing data")
	ErrValidation = errors.New("crm: invalid request")
)

// PageRequest is one page of a keyset-paginated listing. Cursor is opaque: a
// client round-trips the previous page's NextCursor untouched and never mints
// one. Limit is clamped by the service (see normalizePage).
type PageRequest struct {
	Limit  int32
	Cursor string
}

// Page is a listing's wire shape. NextCursor is empty on the last page, which
// is the only "no more rows" signal a client needs — a full page with no
// cursor is the end, never a silent truncation.
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

const (
	TargetContact = "contact"
	TargetCompany = "company"
	TargetDeal    = "deal"

	TaskOpen       = "open"
	TaskInProgress = "in_progress"
	TaskDone       = "done"
	TaskCancelled  = "cancelled"
)

type Actor struct {
	Type       string  `json:"type"`
	ID         string  `json:"id,omitempty"`
	OnBehalfOf *string `json:"on_behalf_of_user_id,omitempty"`
	ClientID   string  `json:"client_id,omitempty"`
	ApprovalID *string `json:"approval_id,omitempty"`
	ThreadID   string  `json:"thread_id,omitempty"`
	RunID      string  `json:"run_id,omitempty"`
}

func UserActor(userID uuid.UUID) Actor {
	return Actor{Type: "user", ID: userID.String()}
}

func (a Actor) JSON() ([]byte, error) { return json.Marshal(a) }

type Company struct {
	ID                  uuid.UUID  `json:"id"`
	WorkspaceID         uuid.UUID  `json:"-"`
	Name                string     `json:"name"`
	Domain              string     `json:"domain,omitempty"`
	OwnerUserID         *uuid.UUID `json:"owner_user_id,omitempty"`
	AnnualRevenueMicros *int64     `json:"annual_revenue_micros,omitempty"`
	Currency            string     `json:"currency"`
	DealCount           int64      `json:"deal_count"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

type Stage struct {
	ID          uuid.UUID `json:"id"`
	PipelineID  uuid.UUID `json:"pipeline_id"`
	WorkspaceID uuid.UUID `json:"-"`
	Key         string    `json:"key"`
	Label       string    `json:"label"`
	Color       string    `json:"color"`
	Position    int32     `json:"position"`
	IsWon       bool      `json:"is_won"`
	IsLost      bool      `json:"is_lost"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Pipeline struct {
	ID          uuid.UUID `json:"id"`
	WorkspaceID uuid.UUID `json:"-"`
	Name        string    `json:"name"`
	IsDefault   bool      `json:"is_default"`
	Stages      []Stage   `json:"stages"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Deal struct {
	ID               uuid.UUID  `json:"id"`
	WorkspaceID      uuid.UUID  `json:"-"`
	PipelineID       uuid.UUID  `json:"pipeline_id"`
	StageID          uuid.UUID  `json:"stage_id"`
	CompanyID        *uuid.UUID `json:"company_id,omitempty"`
	PrimaryContactID *uuid.UUID `json:"primary_contact_id,omitempty"`
	OwnerUserID      *uuid.UUID `json:"owner_user_id,omitempty"`
	Name             string     `json:"name"`
	AmountMicros     *int64     `json:"amount_micros,omitempty"`
	Currency         string     `json:"currency"`
	CloseDate        *time.Time `json:"close_date,omitempty"`
	// Position is the fractional board ordering within a stage. Moves are
	// computed server-side (POST /crm/deals/{id}/move) — a client reads this to
	// render order, it never writes one.
	Position         float64         `json:"position"`
	Source           string          `json:"source"`
	SourceCampaignID *uuid.UUID      `json:"source_campaign_id,omitempty"`
	SourceThreadRef  string          `json:"source_thread_ref,omitempty"`
	CreatedByActor   json.RawMessage `json:"created_by_actor"`
	PipelineName     string          `json:"pipeline_name"`
	StageLabel       string          `json:"stage_label"`
	StageColor       string          `json:"stage_color"`
	StageIsWon       bool            `json:"stage_is_won"`
	StageIsLost      bool            `json:"stage_is_lost"`
	CompanyName      string          `json:"company_name,omitempty"`
	ContactEmail     string          `json:"contact_email,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

type CRMSettings struct {
	AutoCapturePolicy string    `json:"auto_capture_policy"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type MoveDealInput struct {
	StageID      uuid.UUID
	BeforeDealID *uuid.UUID
	AfterDealID  *uuid.UUID
	Actor        Actor
}

type BoardStage struct {
	Stage        Stage  `json:"stage"`
	Deals        []Deal `json:"deals"`
	DealCount    int64  `json:"deal_count"`
	AmountMicros int64  `json:"amount_micros"`
}

type Board struct {
	Pipeline Pipeline     `json:"pipeline"`
	Stages   []BoardStage `json:"stages"`
}

type ThreadParticipant struct {
	Email       string     `json:"email"`
	DisplayName string     `json:"display_name,omitempty"`
	ContactID   *uuid.UUID `json:"contact_id,omitempty"`
}

type ThreadMessage struct {
	ID             uuid.UUID `json:"id"`
	Direction      string    `json:"direction"`
	Kind           string    `json:"kind"`
	MessageID      string    `json:"message_id,omitempty"`
	SenderEmail    string    `json:"sender_email,omitempty"`
	RecipientEmail string    `json:"recipient_email,omitempty"`
	Subject        string    `json:"subject,omitempty"`
	ReplyClass     string    `json:"reply_class,omitempty"`
	OccurredAt     time.Time `json:"occurred_at"`
}

type Thread struct {
	ID            uuid.UUID           `json:"id"`
	ThreadRef     string              `json:"thread_ref"`
	Subject       string              `json:"subject,omitempty"`
	ReplyClass    string              `json:"reply_class,omitempty"`
	CampaignID    *uuid.UUID          `json:"campaign_id,omitempty"`
	ContactID     *uuid.UUID          `json:"contact_id,omitempty"`
	LastMessageAt time.Time           `json:"last_message_at"`
	Participants  []ThreadParticipant `json:"participants"`
	Messages      []ThreadMessage     `json:"messages"`
}

type Event struct {
	ID                     uuid.UUID       `json:"id"`
	Name                   string          `json:"name"`
	Kind                   string          `json:"kind"`
	ObjectType             string          `json:"object_type,omitempty"`
	ObjectID               *uuid.UUID      `json:"object_id,omitempty"`
	ContactID              *uuid.UUID      `json:"contact_id,omitempty"`
	CompanyID              *uuid.UUID      `json:"company_id,omitempty"`
	DealID                 *uuid.UUID      `json:"deal_id,omitempty"`
	Actor                  json.RawMessage `json:"actor"`
	Data                   json.RawMessage `json:"data"`
	LinkedRecordCachedName string          `json:"linked_record_cached_name,omitempty"`
	SourceMessageID        string          `json:"source_message_id,omitempty"`
	SourceThreadRef        string          `json:"source_thread_ref,omitempty"`
	OccurredAt             time.Time       `json:"occurred_at"`
	MergedCount            int             `json:"merged_count,omitempty"`
}

type EventInput struct {
	Name                   string
	Kind                   string
	ObjectType             string
	ObjectID               *uuid.UUID
	ContactID              *uuid.UUID
	CompanyID              *uuid.UUID
	DealID                 *uuid.UUID
	Actor                  Actor
	Data                   json.RawMessage
	LinkedRecordCachedName string
	SourceMessageID        string
	SourceThreadRef        string
	OccurredAt             time.Time
}

type CaptureReplyInput struct {
	EnrollmentID      uuid.UUID
	SendID            uuid.UUID
	ThreadRef         string
	MessageID         string
	Subject           string
	SenderEmail       string
	RecipientEmail    string
	SenderDisplayName string
	ReplyClass        string
	OccurredAt        time.Time
}

type Target struct {
	Type string
	ID   uuid.UUID
}

type Note struct {
	ID             uuid.UUID       `json:"id"`
	WorkspaceID    uuid.UUID       `json:"-"`
	Title          string          `json:"title"`
	Body           string          `json:"body"`
	CreatedByActor json.RawMessage `json:"created_by_actor"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

type Task struct {
	ID             uuid.UUID       `json:"id"`
	WorkspaceID    uuid.UUID       `json:"-"`
	Title          string          `json:"title"`
	Body           string          `json:"body"`
	DueAt          *time.Time      `json:"due_at,omitempty"`
	Status         string          `json:"status"`
	AssigneeUserID *uuid.UUID      `json:"assignee_user_id,omitempty"`
	CreatedByActor json.RawMessage `json:"created_by_actor"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

type ContactEmail struct {
	ID          uuid.UUID `json:"id"`
	ContactID   uuid.UUID `json:"contact_id"`
	WorkspaceID uuid.UUID `json:"-"`
	Email       string    `json:"email"`
	IsPrimary   bool      `json:"is_primary"`
	CreatedAt   time.Time `json:"created_at"`
}

type CompanyInput struct {
	Name                string
	Domain              string
	OwnerUserID         *uuid.UUID
	AnnualRevenueMicros *int64
	Currency            string
}

type PipelineInput struct{ Name string }

type StageInput struct {
	Label    string
	Color    string
	Position int32
	IsWon    bool
	IsLost   bool
}

type DealInput struct {
	PipelineID       uuid.UUID
	StageID          uuid.UUID
	CompanyID        *uuid.UUID
	PrimaryContactID *uuid.UUID
	OwnerUserID      *uuid.UUID
	Name             string
	AmountMicros     *int64
	Currency         string
	CloseDate        *time.Time
	Source           string
	SourceCampaignID *uuid.UUID
	SourceThreadRef  string
	Actor            Actor
}

type NoteInput struct {
	Title  string
	Body   string
	Target Target
	Actor  Actor
}

type TaskInput struct {
	Title          string
	Body           string
	DueAt          *time.Time
	Status         string
	AssigneeUserID *uuid.UUID
	Target         Target
	Actor          Actor
}
