package agenttool

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// errBoom stands in for an infrastructure fault: a tool must propagate it as
// an error return, never as a Result the model is invited to retry.
var errBoom = errors.New("database is on fire")

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

var (
	wsID    = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	otherWS = uuid.MustParse("22222222-2222-2222-2222-222222222222")
	userID  = uuid.MustParse("33333333-3333-3333-3333-333333333333")
)

func member() Principal { return Principal{WorkspaceID: wsID, UserID: userID, Role: "member"} }
func admin() Principal  { return Principal{WorkspaceID: wsID, UserID: userID, Role: "admin"} }

type fakeCampaigns struct {
	campaigns   []gen.Campaign
	stats       map[string]int64
	enrollments []gen.ListCampaignEnrollmentsRow
	err         error
	gotWS       uuid.UUID
	gotLimit    int32
	gotOffset   int32
	panicOnList bool
}

func (f *fakeCampaigns) List(_ context.Context, ws uuid.UUID) ([]gen.Campaign, error) {
	if f.panicOnList {
		panic("fake exploded")
	}
	f.gotWS = ws
	if f.err != nil {
		return nil, f.err
	}
	return f.byWorkspace(ws), nil
}

func (f *fakeCampaigns) Get(_ context.Context, ws, id uuid.UUID) (gen.Campaign, error) {
	if f.err != nil {
		return gen.Campaign{}, f.err
	}
	for _, c := range f.byWorkspace(ws) {
		if c.ID == id {
			return c, nil
		}
	}
	return gen.Campaign{}, pgx.ErrNoRows
}

func (f *fakeCampaigns) Stats(_ context.Context, ws, id uuid.UUID) (map[string]int64, error) {
	f.gotWS = ws
	if f.err != nil {
		return nil, f.err
	}
	return f.stats, nil
}

func (f *fakeCampaigns) ListEnrollments(_ context.Context, ws, id uuid.UUID, limit, offset int32) ([]gen.ListCampaignEnrollmentsRow, error) {
	f.gotWS, f.gotLimit, f.gotOffset = ws, limit, offset
	if f.err != nil {
		return nil, f.err
	}
	return f.enrollments, nil
}

// byWorkspace is the tenant filter a real store applies in SQL; the fake
// applies it too so a tool that forgot to pass the principal's workspace
// fails the test instead of passing by accident.
func (f *fakeCampaigns) byWorkspace(ws uuid.UUID) []gen.Campaign {
	var out []gen.Campaign
	for _, c := range f.campaigns {
		if c.WorkspaceID == ws {
			out = append(out, c)
		}
	}
	return out
}

type fakeCampaignAdmin struct {
	paused, resumed uuid.UUID
	gotWS           uuid.UUID
	err             error
}

func (f *fakeCampaignAdmin) Pause(_ context.Context, ws, id uuid.UUID) error {
	f.gotWS, f.paused = ws, id
	return f.err
}

func (f *fakeCampaignAdmin) Resume(_ context.Context, ws, id uuid.UUID) error {
	f.gotWS, f.resumed = ws, id
	return f.err
}

type fakeContacts struct {
	page  ContactPage
	err   error
	gotWS uuid.UUID
	gotQ  ContactQuery
}

func (f *fakeContacts) Search(_ context.Context, ws uuid.UUID, q ContactQuery) (ContactPage, error) {
	f.gotWS, f.gotQ = ws, q
	if f.err != nil {
		return ContactPage{}, f.err
	}
	return f.page, nil
}

type fakeContactWrites struct {
	id      uuid.UUID
	created bool
	err     error
	addErr  error
	gotWS   uuid.UUID
	gotIn   ContactInput
	gotList uuid.UUID
}

type fakeContactImports struct {
	result  ContactImportResult
	err     error
	gotWS   uuid.UUID
	gotList uuid.UUID
	gotRows []ContactInput
}

func (f *fakeContactImports) Import(_ context.Context, ws, listID uuid.UUID, rows []ContactInput) (ContactImportResult, error) {
	f.gotWS, f.gotList = ws, listID
	f.gotRows = append([]ContactInput(nil), rows...)
	return f.result, f.err
}

func (f *fakeContactWrites) Create(_ context.Context, ws uuid.UUID, in ContactInput) (uuid.UUID, bool, error) {
	f.gotWS, f.gotIn = ws, in
	if f.err != nil {
		return uuid.Nil, false, f.err
	}
	return f.id, f.created, nil
}

func (f *fakeContactWrites) AddToList(_ context.Context, ws, listID, contactID uuid.UUID) error {
	f.gotWS, f.gotList, f.id = ws, listID, contactID
	return f.addErr
}

type fakeMailboxes struct {
	mailboxes []MailboxView
	err       error
	gotWS     uuid.UUID
}

func (f *fakeMailboxes) List(_ context.Context, ws uuid.UUID) ([]MailboxView, error) {
	f.gotWS = ws
	if f.err != nil {
		return nil, f.err
	}
	return f.mailboxes, nil
}

func (f *fakeMailboxes) Get(_ context.Context, ws, id uuid.UUID) (MailboxView, error) {
	f.gotWS = ws
	if f.err != nil {
		return MailboxView{}, f.err
	}
	for _, m := range f.mailboxes {
		if m.ID == id {
			return m, nil
		}
	}
	return MailboxView{}, pgx.ErrNoRows
}

type fakeLists struct {
	lists    []gen.List
	members  int64
	err      error
	countErr error
	gotWS    uuid.UUID
	counted  uuid.UUID
	panicNow bool
}

// List projects the fake's gen.List fixtures into listing rows, attaching
// `members` as every row's count — the real query aggregates a per-list count
// in the same statement, and tests that care assert it arrives.
func (f *fakeLists) List(_ context.Context, ws uuid.UUID) ([]gen.ListListsRow, error) {
	if f.panicNow {
		panic("fake list store exploded")
	}
	f.gotWS = ws
	if f.err != nil {
		return nil, f.err
	}
	rows := make([]gen.ListListsRow, 0, len(f.lists))
	for _, l := range f.lists {
		rows = append(rows, gen.ListListsRow{
			ID: l.ID, WorkspaceID: l.WorkspaceID, Name: l.Name, CreatedAt: l.CreatedAt,
			ContactCount: f.members,
		})
	}
	return rows, nil
}

func (f *fakeLists) Get(_ context.Context, ws, id uuid.UUID) (gen.List, error) {
	f.gotWS = ws
	if f.err != nil {
		return gen.List{}, f.err
	}
	for _, l := range f.lists {
		if l.ID == id && l.WorkspaceID == ws {
			return l, nil
		}
	}
	return gen.List{}, pgx.ErrNoRows
}

func (f *fakeLists) MemberCount(_ context.Context, id uuid.UUID) (int64, error) {
	f.counted = id
	return f.members, f.countErr
}

type fakeListWrites struct {
	created gen.List
	err     error
	gotWS   uuid.UUID
	gotName string
}

func (f *fakeListWrites) Create(_ context.Context, ws uuid.UUID, name string) (gen.List, error) {
	f.gotWS, f.gotName = ws, name
	if f.err != nil {
		return gen.List{}, f.err
	}
	return f.created, nil
}

type fakeHealth struct {
	workspace WorkspaceHealth
	campaign  CampaignHealth
	snapshot  Snapshot
	err       error
	campErr   error
	gotWS     uuid.UUID
}

func (f *fakeHealth) WorkspaceHealth(_ context.Context, ws uuid.UUID) (WorkspaceHealth, error) {
	f.gotWS = ws
	if f.err != nil {
		return WorkspaceHealth{}, f.err
	}
	return f.workspace, nil
}

func (f *fakeHealth) CampaignHealth(_ context.Context, ws, _ uuid.UUID) (CampaignHealth, error) {
	f.gotWS = ws
	if f.campErr != nil {
		return CampaignHealth{}, f.campErr
	}
	return f.campaign, nil
}

func (f *fakeHealth) Snapshot(_ context.Context, ws uuid.UUID) (Snapshot, error) {
	f.gotWS = ws
	if f.err != nil {
		return Snapshot{}, f.err
	}
	return f.snapshot, nil
}

type fakeWarmup struct {
	overview WarmupOverview
	detail   WarmupDetail
	err      error
	getErr   error
	gotWS    uuid.UUID
}

func (f *fakeWarmup) Overview(_ context.Context, ws uuid.UUID) (WarmupOverview, error) {
	f.gotWS = ws
	if f.err != nil {
		return WarmupOverview{}, f.err
	}
	return f.overview, nil
}

func (f *fakeWarmup) Detail(_ context.Context, ws, _ uuid.UUID) (WarmupDetail, error) {
	f.gotWS = ws
	if f.getErr != nil {
		return WarmupDetail{}, f.getErr
	}
	return f.detail, nil
}
