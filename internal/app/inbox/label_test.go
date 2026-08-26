package inbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/inbox"
)

// fakeLabelStore is an in-memory LabelStore that enforces the same two rules
// the schema does: case-insensitive name uniqueness per workspace (so the
// search-or-create path is exercised for real), and workspace isolation on
// every read and write.
type fakeLabelStore struct {
	labels map[uuid.UUID]inbox.Label
	// assigned is the join table: thread id -> set of label ids.
	assigned map[uuid.UUID]map[uuid.UUID]bool
	listErr  error
}

func newFakeLabelStore() *fakeLabelStore {
	return &fakeLabelStore{
		labels:   map[uuid.UUID]inbox.Label{},
		assigned: map[uuid.UUID]map[uuid.UUID]bool{},
	}
}

func (f *fakeLabelStore) CreateLabel(_ context.Context, ws uuid.UUID, name, color string) (inbox.Label, error) {
	for _, l := range f.labels {
		if l.WorkspaceID == ws && strings.EqualFold(l.Name, name) {
			return inbox.Label{}, inbox.ErrLabelNameTaken
		}
	}
	l := inbox.Label{
		ID: uuid.New(), WorkspaceID: ws, Name: name, Color: color,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	f.labels[l.ID] = l
	return l, nil
}

func (f *fakeLabelStore) ListLabels(_ context.Context, ws uuid.UUID) ([]inbox.Label, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []inbox.Label
	for _, l := range f.labels {
		if l.WorkspaceID == ws {
			out = append(out, l)
		}
	}
	return out, nil
}

func (f *fakeLabelStore) GetLabel(_ context.Context, ws, id uuid.UUID) (inbox.Label, error) {
	l, ok := f.labels[id]
	if !ok || l.WorkspaceID != ws {
		return inbox.Label{}, inbox.ErrNotFound
	}
	return l, nil
}

func (f *fakeLabelStore) FindLabelByName(_ context.Context, ws uuid.UUID, name string) (inbox.Label, error) {
	for _, l := range f.labels {
		if l.WorkspaceID == ws && strings.EqualFold(l.Name, name) {
			return l, nil
		}
	}
	return inbox.Label{}, inbox.ErrNotFound
}

func (f *fakeLabelStore) UpdateLabel(_ context.Context, ws, id uuid.UUID, name, color string) (inbox.Label, error) {
	l, ok := f.labels[id]
	if !ok || l.WorkspaceID != ws {
		return inbox.Label{}, inbox.ErrNotFound
	}
	for otherID, other := range f.labels {
		if otherID != id && other.WorkspaceID == ws && strings.EqualFold(other.Name, name) {
			return inbox.Label{}, inbox.ErrLabelNameTaken
		}
	}
	l.Name, l.Color, l.UpdatedAt = name, color, time.Now().UTC()
	f.labels[id] = l
	return l, nil
}

func (f *fakeLabelStore) DeleteLabel(_ context.Context, ws, id uuid.UUID) error {
	l, ok := f.labels[id]
	if !ok || l.WorkspaceID != ws {
		return inbox.ErrNotFound
	}
	delete(f.labels, id)
	// The join's ON DELETE CASCADE, modelled: deleting a label unfiles every
	// thread it was on.
	for _, set := range f.assigned {
		delete(set, id)
	}
	return nil
}

func (f *fakeLabelStore) AssignLabel(_ context.Context, _, threadID, labelID uuid.UUID) error {
	if f.assigned[threadID] == nil {
		f.assigned[threadID] = map[uuid.UUID]bool{}
	}
	f.assigned[threadID][labelID] = true
	return nil
}

func (f *fakeLabelStore) UnassignLabel(_ context.Context, _, threadID, labelID uuid.UUID) error {
	if !f.assigned[threadID][labelID] {
		return inbox.ErrNotFound
	}
	delete(f.assigned[threadID], labelID)
	return nil
}

func (f *fakeLabelStore) LabelsForThread(_ context.Context, ws, threadID uuid.UUID) ([]inbox.Label, error) {
	var out []inbox.Label
	for labelID := range f.assigned[threadID] {
		if l, ok := f.labels[labelID]; ok && l.WorkspaceID == ws {
			out = append(out, l)
		}
	}
	return out, nil
}

func (f *fakeLabelStore) LabelsForThreads(ctx context.Context, ws uuid.UUID, threadIDs []uuid.UUID) (map[uuid.UUID][]inbox.Label, error) {
	out := make(map[uuid.UUID][]inbox.Label, len(threadIDs))
	for _, id := range threadIDs {
		labels, err := f.LabelsForThread(ctx, ws, id)
		if err != nil {
			return nil, err
		}
		if len(labels) > 0 {
			out[id] = labels
		}
	}
	return out, nil
}

func (f *fakeLabelStore) CountThreadsByLabel(_ context.Context, ws uuid.UUID) ([]inbox.LabelCount, error) {
	counts := map[uuid.UUID]int64{}
	for _, set := range f.assigned {
		for labelID := range set {
			if l, ok := f.labels[labelID]; ok && l.WorkspaceID == ws {
				counts[labelID]++
			}
		}
	}
	out := make([]inbox.LabelCount, 0, len(counts))
	for id, n := range counts {
		out = append(out, inbox.LabelCount{LabelID: id, Total: n})
	}
	return out, nil
}

// labelFixture wires a Service over the thread and label fakes.
type labelFixture struct {
	svc     *inbox.Service
	handler *inbox.Handler
	threads *fakeStore
	labels  *fakeLabelStore
	thread  inbox.Thread
}

func newLabelFixture(t *testing.T) *labelFixture {
	t.Helper()
	threads, labels := newFakeStore(), newFakeLabelStore()
	svc := inbox.NewService(threads, inbox.WithLabelStore(labels))
	th, err := threads.UpsertThread(context.Background(), inbox.UpsertThreadInput{
		WorkspaceID: testWS, MailboxID: uuid.New(), RootMessageID: "<label@s.test>", Subject: "S",
	})
	if err != nil {
		t.Fatalf("seed thread: %v", err)
	}
	return &labelFixture{svc: svc, handler: inbox.NewHandler(svc), threads: threads, labels: labels, thread: th}
}

func TestCreateLabelNormalizesTheName(t *testing.T) {
	f := newLabelFixture(t)
	// Surrounding and internal whitespace collapse, so "Big  Deals " and
	// "Big Deals" cannot become two labels that look identical in the picker.
	label, err := f.svc.CreateLabel(context.Background(), testWS, "  Big   Deals  ", "#3B82F6")
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
	if label.Name != "Big Deals" {
		t.Errorf("Name = %q, want %q", label.Name, "Big Deals")
	}
	// Colour is lowercased so two labels never differ only by hex case.
	if label.Color != "#3b82f6" {
		t.Errorf("Color = %q, want %q", label.Color, "#3b82f6")
	}
}

func TestCreateLabelAppliesTheDefaultColor(t *testing.T) {
	f := newLabelFixture(t)
	label, err := f.svc.CreateLabel(context.Background(), testWS, "Invoices", "")
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
	if label.Color != inbox.DefaultLabelColor {
		t.Errorf("Color = %q, want the default %q", label.Color, inbox.DefaultLabelColor)
	}
}

func TestCreateLabelRejectsBadInput(t *testing.T) {
	tests := []struct {
		name  string
		label string
		color string
	}{
		{"empty", "", ""},
		{"whitespace only", "   ", ""},
		{"over the length cap", strings.Repeat("x", inbox.MaxLabelNameLength+1), ""},
		{"not a hex colour", "Fine", "blue"},
		{"three-digit hex", "Fine", "#abc"},
		{"missing hash", "Fine", "3b82f6"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newLabelFixture(t)
			_, err := f.svc.CreateLabel(context.Background(), testWS, tc.label, tc.color)
			if !errors.Is(err, inbox.ErrValidation) {
				t.Errorf("error = %v, want ErrValidation", err)
			}
		})
	}
}

// The length cap counts RUNES: a 40-character name in a non-Latin script must
// not be rejected for a byte length it never had.
func TestLabelNameLengthIsCountedInRunes(t *testing.T) {
	f := newLabelFixture(t)
	name := strings.Repeat("é", inbox.MaxLabelNameLength)
	if _, err := f.svc.CreateLabel(context.Background(), testWS, name, ""); err != nil {
		t.Errorf("a %d-rune name was rejected: %v", inbox.MaxLabelNameLength, err)
	}
}

// The picker's core behaviour: typing an existing name files under it rather
// than failing.
func TestEnsureLabelResolvesAnExistingNameCaseInsensitively(t *testing.T) {
	f := newLabelFixture(t)
	ctx := context.Background()
	first, err := f.svc.CreateLabel(ctx, testWS, "Invoices", "#3b82f6")
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}

	again, err := f.svc.EnsureLabel(ctx, testWS, "invoices", "#ef4444")
	if err != nil {
		t.Fatalf("EnsureLabel: %v", err)
	}
	if again.ID != first.ID {
		t.Errorf("EnsureLabel created a second label (%s) instead of resolving to %s", again.ID, first.ID)
	}
	// The existing label keeps its colour: resolving is not editing.
	if again.Color != "#3b82f6" {
		t.Errorf("Color = %q, want the original %q", again.Color, "#3b82f6")
	}
	if len(f.labels.labels) != 1 {
		t.Errorf("%d labels exist, want 1", len(f.labels.labels))
	}
}

func TestCreateLabelIsWorkspaceScoped(t *testing.T) {
	f := newLabelFixture(t)
	ctx := context.Background()
	other := uuid.New()
	if _, err := f.svc.CreateLabel(ctx, testWS, "Shared", ""); err != nil {
		t.Fatalf("CreateLabel(ws1): %v", err)
	}
	// The same name in a DIFFERENT workspace is a different label, not a clash.
	if _, err := f.svc.CreateLabel(ctx, other, "Shared", ""); err != nil {
		t.Errorf("the same name in another workspace was rejected: %v", err)
	}

	mine, err := f.svc.ListLabels(ctx, testWS)
	if err != nil {
		t.Fatalf("ListLabels: %v", err)
	}
	if len(mine) != 1 {
		t.Errorf("ListLabels(ws1) = %d labels, want 1 — another workspace's leaked in", len(mine))
	}
}

func TestCreateLabelEnforcesTheWorkspaceCap(t *testing.T) {
	f := newLabelFixture(t)
	ctx := context.Background()
	for i := range inbox.MaxLabelsPerWorkspace {
		if _, err := f.svc.CreateLabel(ctx, testWS, "L"+string(rune('a'+i%26))+uuid.NewString(), ""); err != nil {
			t.Fatalf("CreateLabel(%d): %v", i, err)
		}
	}
	_, err := f.svc.CreateLabel(ctx, testWS, "one too many", "")
	if !errors.Is(err, inbox.ErrTooManyLabels) {
		t.Errorf("error = %v, want ErrTooManyLabels", err)
	}
}

func TestAssignLabelIsIdempotent(t *testing.T) {
	f := newLabelFixture(t)
	ctx := context.Background()
	label, err := f.svc.CreateLabel(ctx, testWS, "Invoices", "")
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}

	for range 2 {
		if err := f.svc.AssignLabel(ctx, testWS, f.thread.ID, label.ID); err != nil {
			t.Fatalf("AssignLabel: %v", err)
		}
	}

	detail, err := f.svc.GetThread(ctx, testWS, f.thread.ID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if len(detail.Labels) != 1 {
		t.Errorf("thread carries %d labels after two assigns, want 1", len(detail.Labels))
	}
}

// A foreign thread or label must 404 before the insert, or a foreign-key error
// would either leak the row's existence or file another workspace's thread.
func TestAssignLabelRejectsForeignIDs(t *testing.T) {
	f := newLabelFixture(t)
	ctx := context.Background()
	label, err := f.svc.CreateLabel(ctx, testWS, "Invoices", "")
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
	foreignThread, err := f.threads.UpsertThread(ctx, inbox.UpsertThreadInput{
		WorkspaceID: uuid.New(), MailboxID: uuid.New(), RootMessageID: "<foreign-label@s.test>",
	})
	if err != nil {
		t.Fatalf("seed foreign thread: %v", err)
	}

	if err := f.svc.AssignLabel(ctx, testWS, foreignThread.ID, label.ID); !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("foreign thread: error = %v, want ErrNotFound", err)
	}
	if err := f.svc.AssignLabel(ctx, testWS, f.thread.ID, uuid.New()); !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("unknown label: error = %v, want ErrNotFound", err)
	}
}

func TestUnassignLabelThatWasNotAppliedIsNotFound(t *testing.T) {
	f := newLabelFixture(t)
	ctx := context.Background()
	label, err := f.svc.CreateLabel(ctx, testWS, "Invoices", "")
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
	if err := f.svc.UnassignLabel(ctx, testWS, f.thread.ID, label.ID); !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

// Deleting a label unfiles every thread it was on. The threads survive.
func TestDeletingALabelUnfilesItsThreads(t *testing.T) {
	f := newLabelFixture(t)
	ctx := context.Background()
	label, err := f.svc.CreateLabel(ctx, testWS, "Invoices", "")
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
	if err := f.svc.AssignLabel(ctx, testWS, f.thread.ID, label.ID); err != nil {
		t.Fatalf("AssignLabel: %v", err)
	}
	if err := f.svc.DeleteLabel(ctx, testWS, label.ID); err != nil {
		t.Fatalf("DeleteLabel: %v", err)
	}

	detail, err := f.svc.GetThread(ctx, testWS, f.thread.ID)
	if err != nil {
		t.Fatalf("the thread did not survive its label's deletion: %v", err)
	}
	if len(detail.Labels) != 0 {
		t.Errorf("thread still carries %d labels", len(detail.Labels))
	}
}

// The list view resolves a whole page's labels in ONE store call, not one per
// row — the N+1 this design exists to avoid.
func TestListThreadsResolvesLabelsInOneQuery(t *testing.T) {
	f := newLabelFixture(t)
	ctx := context.Background()
	label, err := f.svc.CreateLabel(ctx, testWS, "Invoices", "")
	if err != nil {
		t.Fatalf("CreateLabel: %v", err)
	}
	if err := f.svc.AssignLabel(ctx, testWS, f.thread.ID, label.ID); err != nil {
		t.Fatalf("AssignLabel: %v", err)
	}

	page, err := f.svc.ListThreads(ctx, testWS, inbox.ListFilter{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	var found bool
	for _, item := range page.Items {
		if item.ID == f.thread.ID {
			found = true
			if len(item.Labels) != 1 || item.Labels[0].Name != "Invoices" {
				t.Errorf("listed thread labels = %+v, want [Invoices]", item.Labels)
			}
		}
	}
	if !found {
		t.Fatal("the labelled thread was not listed")
	}
}

// A Service with no label store must still serve threads — labels are optional.
func TestThreadsWorkWithoutALabelStore(t *testing.T) {
	threads := newFakeStore()
	svc := inbox.NewService(threads)
	th, err := threads.UpsertThread(context.Background(), inbox.UpsertThreadInput{
		WorkspaceID: testWS, MailboxID: uuid.New(), RootMessageID: "<no-labels@s.test>",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	detail, err := svc.GetThread(context.Background(), testWS, th.ID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if len(detail.Labels) != 0 {
		t.Errorf("Labels = %+v, want none", detail.Labels)
	}
	if _, err := svc.ListThreads(context.Background(), testWS, inbox.ListFilter{}); err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
}

// --- HTTP layer ---

func TestLabelEndpointsRoundTrip(t *testing.T) {
	f := newLabelFixture(t)

	res := serve(t, f.handler, http.MethodPost, "/inbox/labels", `{"name":"Invoices","color":"#3b82f6"}`)
	if res.Code != http.StatusOK {
		t.Fatalf("POST status = %d, want 200 (%s)", res.Code, res.Body.String())
	}
	var created struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if created.Name != "Invoices" || created.Color != "#3b82f6" {
		t.Errorf("created = %+v", created)
	}

	res = serve(t, f.handler, http.MethodGet, "/inbox/labels", "")
	if res.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", res.Code)
	}
	var list struct {
		Labels []struct{ ID string } `json:"labels"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(list.Labels) != 1 {
		t.Fatalf("%d labels listed, want 1", len(list.Labels))
	}

	res = serve(t, f.handler, http.MethodPut, "/inbox/labels/"+created.ID, `{"name":"Billing","color":"#ef4444"}`)
	if res.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200 (%s)", res.Code, res.Body.String())
	}

	// Assign, then unassign.
	assignPath := "/inbox/threads/" + f.thread.ID.String() + "/labels/" + created.ID
	if res = serve(t, f.handler, http.MethodPut, assignPath, ""); res.Code != http.StatusNoContent {
		t.Fatalf("assign status = %d, want 204 (%s)", res.Code, res.Body.String())
	}
	if res = serve(t, f.handler, http.MethodDelete, assignPath, ""); res.Code != http.StatusNoContent {
		t.Fatalf("unassign status = %d, want 204 (%s)", res.Code, res.Body.String())
	}

	if res = serve(t, f.handler, http.MethodDelete, "/inbox/labels/"+created.ID, ""); res.Code != http.StatusNoContent {
		t.Errorf("DELETE status = %d, want 204 (%s)", res.Code, res.Body.String())
	}
}

// POST is search-or-create: an existing name resolves to it with 200, so the
// picker never has to handle a conflict.
func TestCreateLabelEndpointResolvesAnExistingName(t *testing.T) {
	f := newLabelFixture(t)
	first := serve(t, f.handler, http.MethodPost, "/inbox/labels", `{"name":"Invoices","color":"#3b82f6"}`)
	second := serve(t, f.handler, http.MethodPost, "/inbox/labels", `{"name":"invoices","color":"#ef4444"}`)
	if second.Code != http.StatusOK {
		t.Fatalf("second POST status = %d, want 200 (%s)", second.Code, second.Body.String())
	}

	var a, b struct{ ID string }
	if err := json.Unmarshal(first.Body.Bytes(), &a); err != nil {
		t.Fatalf("unmarshal first: %v", err)
	}
	if err := json.Unmarshal(second.Body.Bytes(), &b); err != nil {
		t.Fatalf("unmarshal second: %v", err)
	}
	if a.ID != b.ID {
		t.Errorf("ids differ (%s vs %s) — a duplicate label was created", a.ID, b.ID)
	}
}

func TestLabelEndpointStatusCodes(t *testing.T) {
	f := newLabelFixture(t)
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		want   int
	}{
		{"malformed json", http.MethodPost, "/inbox/labels", `{`, http.StatusBadRequest},
		{"unknown field", http.MethodPost, "/inbox/labels", `{"name":"x","nope":1}`, http.StatusBadRequest},
		{"empty name", http.MethodPost, "/inbox/labels", `{"name":""}`, http.StatusBadRequest},
		{"bad colour", http.MethodPost, "/inbox/labels", `{"name":"x","color":"blue"}`, http.StatusBadRequest},
		{"non-uuid label id", http.MethodDelete, "/inbox/labels/not-a-uuid", "", http.StatusBadRequest},
		{"unknown label id", http.MethodDelete, "/inbox/labels/" + uuid.NewString(), "", http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := serve(t, f.handler, tc.method, tc.path, tc.body)
			if res.Code != tc.want {
				t.Errorf("status = %d, want %d (%s)", res.Code, tc.want, res.Body.String())
			}
		})
	}
}

func TestListThreadsLabelFilterMapsToTheStore(t *testing.T) {
	f := newLabelFixture(t)
	labelID := uuid.New()
	res := serve(t, f.handler, http.MethodGet, "/inbox/threads?label="+labelID.String(), "")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", res.Code, res.Body.String())
	}
	got := f.threads.lastListFilter.LabelID
	if got == nil || *got != labelID {
		t.Errorf("LabelID = %v, want %s", got, labelID)
	}
}

func TestListThreadsRejectsANonUUIDLabelFilter(t *testing.T) {
	f := newLabelFixture(t)
	res := serve(t, f.handler, http.MethodGet, "/inbox/threads?label=nope", "")
	if res.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (%s)", res.Code, res.Body.String())
	}
}

// An unlabelled thread must marshal `labels: []`, never null, so a client can
// map over it unconditionally.
func TestThreadLabelsMarshalAsAnEmptyArray(t *testing.T) {
	f := newLabelFixture(t)
	res := serve(t, f.handler, http.MethodGet, "/inbox/threads/"+f.thread.ID.String(), "")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", res.Code, res.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(res.Body.Bytes(), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(raw["labels"]) != "[]" {
		t.Errorf("labels = %s, want []", raw["labels"])
	}
}
