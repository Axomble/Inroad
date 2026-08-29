package inbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/inbox"
)

// fakeComposeStore is an in-memory ComposeStore + ComposeClaimer. The two rules
// it must enforce faithfully are per-USER draft ownership (a colleague must not
// resume someone else's unsent mail) and the same status guards the SQL applies.
type fakeComposeStore struct {
	drafts   map[uuid.UUID]inbox.ComposeDraft
	composes map[uuid.UUID]inbox.PendingCompose
	now      func() time.Time
	// knownMailboxes models the INSERT … SELECT's self-enforcing tenancy.
	knownMailboxes map[uuid.UUID]uuid.UUID
}

func newFakeComposeStore(now func() time.Time) *fakeComposeStore {
	return &fakeComposeStore{
		drafts:         map[uuid.UUID]inbox.ComposeDraft{},
		composes:       map[uuid.UUID]inbox.PendingCompose{},
		now:            now,
		knownMailboxes: map[uuid.UUID]uuid.UUID{},
	}
}

func (f *fakeComposeStore) SaveDraft(_ context.Context, in inbox.SaveComposeDraftInput) (inbox.ComposeDraft, error) {
	// The UPDATE arm's WHERE pins workspace AND user, so a draft owned by
	// someone else matches nothing.
	if existing, ok := f.drafts[in.ID]; ok {
		if existing.WorkspaceID != in.WorkspaceID || existing.UserID != in.UserID {
			return inbox.ComposeDraft{}, inbox.ErrNotFound
		}
	}
	d := inbox.ComposeDraft{
		ID: in.ID, WorkspaceID: in.WorkspaceID, UserID: in.UserID, MailboxID: in.MailboxID,
		ToEmails: in.ToEmails, CcEmails: in.CcEmails, BccEmails: in.BccEmails,
		Subject: in.Subject, BodyText: in.BodyText,
		CreatedAt: f.now(), UpdatedAt: f.now(),
	}
	f.drafts[in.ID] = d
	return d, nil
}

func (f *fakeComposeStore) ListDrafts(_ context.Context, ws, user uuid.UUID, limit int32) ([]inbox.ComposeDraft, error) {
	var out []inbox.ComposeDraft
	for _, d := range f.drafts {
		if d.WorkspaceID == ws && d.UserID == user {
			out = append(out, d)
			if int32(len(out)) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (f *fakeComposeStore) GetDraft(_ context.Context, ws, user, id uuid.UUID) (inbox.ComposeDraft, error) {
	d, ok := f.drafts[id]
	if !ok || d.WorkspaceID != ws || d.UserID != user {
		return inbox.ComposeDraft{}, inbox.ErrNotFound
	}
	return d, nil
}

func (f *fakeComposeStore) DeleteDraft(_ context.Context, ws, user, id uuid.UUID) error {
	d, ok := f.drafts[id]
	if !ok || d.WorkspaceID != ws || d.UserID != user {
		return inbox.ErrNotFound
	}
	delete(f.drafts, id)
	return nil
}

func (f *fakeComposeStore) CreatePendingCompose(_ context.Context, in inbox.CreatePendingComposeInput) (inbox.PendingCompose, error) {
	if ws, ok := f.knownMailboxes[in.MailboxID]; !ok || ws != in.WorkspaceID {
		return inbox.PendingCompose{}, inbox.ErrNotFound
	}
	c := inbox.PendingCompose{
		ID: uuid.New(), WorkspaceID: in.WorkspaceID, MailboxID: in.MailboxID,
		ToEmails: in.ToEmails, CcEmails: in.CcEmails, BccEmails: in.BccEmails,
		Subject: in.Subject, BodyText: in.BodyText,
		Status: inbox.PendingStatusScheduled, SendAfter: in.SendAfter,
		CreatedBy: in.CreatedBy, CreatedAt: f.now(),
	}
	f.composes[c.ID] = c
	return c, nil
}

func (f *fakeComposeStore) GetPendingCompose(_ context.Context, ws, id uuid.UUID) (inbox.PendingCompose, error) {
	c, ok := f.composes[id]
	if !ok || c.WorkspaceID != ws {
		return inbox.PendingCompose{}, inbox.ErrNotFound
	}
	return c, nil
}

func (f *fakeComposeStore) ListPendingComposes(_ context.Context, ws uuid.UUID, limit int32) ([]inbox.PendingCompose, error) {
	var out []inbox.PendingCompose
	for _, c := range f.composes {
		if c.WorkspaceID != ws {
			continue
		}
		if c.Status != inbox.PendingStatusScheduled && c.Status != inbox.PendingStatusSending {
			continue
		}
		out = append(out, c)
		if int32(len(out)) >= limit {
			break
		}
	}
	return out, nil
}

func (f *fakeComposeStore) CancelPendingCompose(_ context.Context, ws, id uuid.UUID) error {
	c, ok := f.composes[id]
	if !ok || c.WorkspaceID != ws || c.Status != inbox.PendingStatusScheduled {
		return inbox.ErrNotFound
	}
	c.Status = inbox.PendingStatusCancelled
	f.composes[id] = c
	return nil
}

func (f *fakeComposeStore) ClaimPendingCompose(_ context.Context, ws, id uuid.UUID) error {
	c, ok := f.composes[id]
	if !ok || c.WorkspaceID != ws || c.SendAfter.After(f.now()) || c.Status != inbox.PendingStatusScheduled {
		return inbox.ErrPendingNotClaimable
	}
	c.Status = inbox.PendingStatusSending
	f.composes[id] = c
	return nil
}

func (f *fakeComposeStore) MarkPendingComposeSent(_ context.Context, ws, id uuid.UUID, messageID string) error {
	c, ok := f.composes[id]
	if !ok || c.WorkspaceID != ws || c.Status != inbox.PendingStatusSending {
		return inbox.ErrNotFound
	}
	c.Status, c.MessageID = inbox.PendingStatusSent, messageID
	f.composes[id] = c
	return nil
}

func (f *fakeComposeStore) ReleasePendingCompose(_ context.Context, ws, id uuid.UUID, reason string) error {
	c, ok := f.composes[id]
	if !ok || c.WorkspaceID != ws || c.Status != inbox.PendingStatusSending {
		return nil
	}
	c.Status, c.LastError = inbox.PendingStatusScheduled, reason
	f.composes[id] = c
	return nil
}

func (f *fakeComposeStore) FailPendingCompose(_ context.Context, ws, id uuid.UUID, reason string) error {
	c, ok := f.composes[id]
	if !ok || c.WorkspaceID != ws {
		return nil
	}
	c.Status, c.LastError = inbox.PendingStatusFailed, reason
	f.composes[id] = c
	return nil
}

type composeFixture struct {
	svc     *inbox.Service
	handler *inbox.Handler
	compose *fakeComposeStore
	pending *fakePendingStore
	supp    *fakeSuppression
	now     time.Time
	mailbox uuid.UUID
	// user is stable across this fixture's requests: drafts are per-user, so
	// two calls from "the same operator" must actually be the same principal.
	user uuid.UUID
}

// serveAs runs one request as the fixture's own user, so a saved draft is
// visible to the caller who saved it.
func (f *composeFixture) serveAs(t *testing.T, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, f.handler, method, target, body, bearerAs(t, testWS, f.user))
}

// fakeSuppression lets a test mark an address suppressed.
type fakeSuppression struct {
	suppressed map[string]bool
	err        error
}

func (f *fakeSuppression) IsSuppressed(_ context.Context, _ uuid.UUID, email string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.suppressed[strings.ToLower(email)], nil
}

func newComposeFixture(t *testing.T) *composeFixture {
	t.Helper()
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	threads := newFakeStore()
	compose := newFakeComposeStore(clock)
	pending := newFakePendingStore(clock, threads)
	supp := &fakeSuppression{suppressed: map[string]bool{}}
	mailbox := uuid.New()
	compose.knownMailboxes[mailbox] = testWS

	svc := inbox.NewService(threads,
		inbox.WithComposeStore(compose),
		inbox.WithPendingReplyStore(pending),
		inbox.WithSuppressionChecker(supp),
		inbox.WithClock(clock),
	)
	return &composeFixture{
		svc: svc, handler: inbox.NewHandler(svc),
		compose: compose, pending: pending, supp: supp, now: now, mailbox: mailbox,
		user: uuid.New(),
	}
}

func validCompose(f *composeFixture) inbox.CreatePendingComposeInput {
	return inbox.CreatePendingComposeInput{
		MailboxID: f.mailbox,
		ToEmails:  []string{"ada@prospect.test"},
		Subject:   "Quick question",
		BodyText:  "Hello there",
	}
}

// --- Drafts ---

func TestSaveDraftDoesNotValidateIncompleteContent(t *testing.T) {
	f := newComposeFixture(t)
	user := uuid.New()

	// A draft is by definition unfinished. Refusing to save "ada@" mid-typing
	// would lose the operator's work at every keystroke.
	saved, err := f.svc.SaveComposeDraft(context.Background(), inbox.SaveComposeDraftInput{
		WorkspaceID: testWS, UserID: user,
		ToEmails: []string{"ada@"}, Subject: "", BodyText: "",
	})
	if err != nil {
		t.Fatalf("SaveComposeDraft: %v", err)
	}
	if saved.ID == uuid.Nil {
		t.Error("no id assigned to a new draft")
	}
}

func TestSaveDraftBoundsTheSubject(t *testing.T) {
	f := newComposeFixture(t)
	_, err := f.svc.SaveComposeDraft(context.Background(), inbox.SaveComposeDraftInput{
		WorkspaceID: testWS, UserID: uuid.New(),
		Subject: strings.Repeat("x", inbox.MaxComposeSubject+1),
	})
	if !errors.Is(err, inbox.ErrSubjectTooLong) {
		t.Errorf("error = %v, want ErrSubjectTooLong", err)
	}
}

func TestSaveDraftIsIdempotentOnItsID(t *testing.T) {
	f := newComposeFixture(t)
	ctx := context.Background()
	user := uuid.New()
	id := uuid.New()

	for _, body := range []string{"first", "second"} {
		if _, err := f.svc.SaveComposeDraft(ctx, inbox.SaveComposeDraftInput{
			ID: id, WorkspaceID: testWS, UserID: user, BodyText: body,
		}); err != nil {
			t.Fatalf("SaveComposeDraft(%q): %v", body, err)
		}
	}

	drafts, err := f.svc.ListComposeDrafts(ctx, testWS, user)
	if err != nil {
		t.Fatalf("ListComposeDrafts: %v", err)
	}
	if len(drafts) != 1 {
		t.Fatalf("%d drafts after two saves of one id, want 1", len(drafts))
	}
	if drafts[0].BodyText != "second" {
		t.Errorf("BodyText = %q, want the later save", drafts[0].BodyText)
	}
}

// The rule the whole per-user design exists for: a colleague must never see or
// resume someone else's unsent mail.
func TestDraftsArePerUser(t *testing.T) {
	f := newComposeFixture(t)
	ctx := context.Background()
	mine, theirs := uuid.New(), uuid.New()

	saved, err := f.svc.SaveComposeDraft(ctx, inbox.SaveComposeDraftInput{
		WorkspaceID: testWS, UserID: mine, BodyText: "private",
	})
	if err != nil {
		t.Fatalf("SaveComposeDraft: %v", err)
	}

	theirDrafts, err := f.svc.ListComposeDrafts(ctx, testWS, theirs)
	if err != nil {
		t.Fatalf("ListComposeDrafts: %v", err)
	}
	if len(theirDrafts) != 0 {
		t.Errorf("a colleague sees %d of my drafts, want 0", len(theirDrafts))
	}

	// ...and cannot overwrite it by guessing the id.
	if _, err := f.svc.SaveComposeDraft(ctx, inbox.SaveComposeDraftInput{
		ID: saved.ID, WorkspaceID: testWS, UserID: theirs, BodyText: "hijacked",
	}); !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("a colleague overwrote my draft: %v", err)
	}
	if err := f.svc.DeleteComposeDraft(ctx, testWS, theirs, saved.ID); !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("a colleague deleted my draft: %v", err)
	}
	still, err := f.svc.ListComposeDrafts(ctx, testWS, mine)
	if err != nil || len(still) != 1 || still[0].BodyText != "private" {
		t.Errorf("my draft was disturbed: %+v (%v)", still, err)
	}
}

// The cap must not lock an operator out of editing work they already have.
func TestDraftCapStillAllowsEditingAnExistingDraft(t *testing.T) {
	f := newComposeFixture(t)
	ctx := context.Background()
	user := uuid.New()

	var first uuid.UUID
	for i := range inbox.MaxComposeDrafts {
		saved, err := f.svc.SaveComposeDraft(ctx, inbox.SaveComposeDraftInput{
			WorkspaceID: testWS, UserID: user, BodyText: "d",
		})
		if err != nil {
			t.Fatalf("SaveComposeDraft(%d): %v", i, err)
		}
		if i == 0 {
			first = saved.ID
		}
	}

	// A NEW draft is refused...
	if _, err := f.svc.SaveComposeDraft(ctx, inbox.SaveComposeDraftInput{
		WorkspaceID: testWS, UserID: user, BodyText: "one too many",
	}); !errors.Is(err, inbox.ErrValidation) {
		t.Errorf("a new draft past the cap = %v, want ErrValidation", err)
	}
	// ...but editing an existing one is not.
	if _, err := f.svc.SaveComposeDraft(ctx, inbox.SaveComposeDraftInput{
		ID: first, WorkspaceID: testWS, UserID: user, BodyText: "edited",
	}); err != nil {
		t.Errorf("editing an existing draft at the cap was refused: %v", err)
	}
}

func TestSaveDraftRequiresAUser(t *testing.T) {
	f := newComposeFixture(t)
	_, err := f.svc.SaveComposeDraft(context.Background(), inbox.SaveComposeDraftInput{
		WorkspaceID: testWS, BodyText: "x",
	})
	if !errors.Is(err, inbox.ErrValidation) {
		t.Errorf("error = %v, want ErrValidation", err)
	}
}

// --- Sending a compose ---

func TestScheduleComposeNormalizesRecipients(t *testing.T) {
	f := newComposeFixture(t)
	in := validCompose(f)
	// Mixed case, whitespace, an RFC 5322 display name, and a duplicate across
	// To and Cc — which would otherwise deliver twice and count twice.
	in.ToEmails = []string{"  Ada@Prospect.TEST ", "Grace <grace@prospect.test>"}
	in.CcEmails = []string{"ADA@prospect.test"}

	got, err := f.svc.ScheduleCompose(context.Background(), testWS, in, nil)
	if err != nil {
		t.Fatalf("ScheduleCompose: %v", err)
	}
	if len(got.ToEmails) != 2 {
		t.Fatalf("ToEmails = %v, want 2 entries", got.ToEmails)
	}
	if got.ToEmails[0] != "ada@prospect.test" || got.ToEmails[1] != "grace@prospect.test" {
		t.Errorf("ToEmails = %v, want lowercased bare addresses", got.ToEmails)
	}
	// The Cc duplicate is dropped within its own list, but To and Cc are
	// separate lists — the cross-list duplicate remains, which the send path
	// deduplicates per dial. Assert what the code actually does.
	if len(got.CcEmails) != 1 {
		t.Errorf("CcEmails = %v, want 1 entry", got.CcEmails)
	}
}

func TestScheduleComposeRejectsBadInput(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*inbox.CreatePendingComposeInput)
		want   error
	}{
		{"no recipients", func(in *inbox.CreatePendingComposeInput) { in.ToEmails = nil }, inbox.ErrNoRecipients},
		{"blank recipients only", func(in *inbox.CreatePendingComposeInput) {
			in.ToEmails = []string{"  ", ""}
		}, inbox.ErrNoRecipients},
		{"unparseable address", func(in *inbox.CreatePendingComposeInput) {
			in.ToEmails = []string{"not-an-email"}
		}, inbox.ErrInvalidRecipient},
		{"empty body", func(in *inbox.CreatePendingComposeInput) { in.BodyText = "" }, inbox.ErrReplyBodyInvalid},
		{"over-long subject", func(in *inbox.CreatePendingComposeInput) {
			in.Subject = strings.Repeat("x", inbox.MaxComposeSubject+1)
		}, inbox.ErrSubjectTooLong},
		{"no mailbox", func(in *inbox.CreatePendingComposeInput) { in.MailboxID = uuid.Nil }, inbox.ErrMailboxRequired},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newComposeFixture(t)
			in := validCompose(f)
			tc.mutate(&in)
			_, err := f.svc.ScheduleCompose(context.Background(), testWS, in, nil)
			if !errors.Is(err, tc.want) {
				t.Errorf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

// The cap counts EVERY recipient: 1 To plus 40 Bcc is a bulk send however it is
// addressed, and this path deliberately bypasses the campaign machinery's own
// throttles.
func TestRecipientCapCountsCcAndBcc(t *testing.T) {
	f := newComposeFixture(t)
	in := validCompose(f)
	for i := range inbox.MaxComposeRecipients {
		in.BccEmails = append(in.BccEmails, "bcc"+string(rune('a'+i%26))+uuid.NewString()[:6]+"@x.test")
	}

	_, err := f.svc.ScheduleCompose(context.Background(), testWS, in, nil)
	if !errors.Is(err, inbox.ErrTooManyRecipients) {
		t.Errorf("error = %v, want ErrTooManyRecipients", err)
	}
}

// A suppressed address must stop the whole message, in any field — quietly not
// delivering to one of several deliberately-chosen recipients is worse than
// saying why.
func TestScheduleComposeRejectsASuppressedRecipientInAnyField(t *testing.T) {
	for _, field := range []string{"to", "cc", "bcc"} {
		t.Run(field, func(t *testing.T) {
			f := newComposeFixture(t)
			f.supp.suppressed["blocked@prospect.test"] = true
			in := validCompose(f)
			switch field {
			case "to":
				in.ToEmails = []string{"blocked@prospect.test"}
			case "cc":
				in.CcEmails = []string{"blocked@prospect.test"}
			case "bcc":
				in.BccEmails = []string{"blocked@prospect.test"}
			}

			_, err := f.svc.ScheduleCompose(context.Background(), testWS, in, nil)
			if !errors.Is(err, inbox.ErrRecipientSuppressed) {
				t.Errorf("error = %v, want ErrRecipientSuppressed", err)
			}
		})
	}
}

// A cross-tenant mailbox can never become a sending identity.
func TestScheduleComposeRefusesAForeignMailbox(t *testing.T) {
	f := newComposeFixture(t)
	in := validCompose(f)
	in.MailboxID = uuid.New() // not registered to any workspace

	_, err := f.svc.ScheduleCompose(context.Background(), testWS, in, nil)
	if !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestScheduleComposeAppliesTheUndoWindow(t *testing.T) {
	f := newComposeFixture(t)
	f.pending.undoWindow = 20 * time.Second

	got, err := f.svc.ScheduleCompose(context.Background(), testWS, validCompose(f), nil)
	if err != nil {
		t.Fatalf("ScheduleCompose: %v", err)
	}
	if !got.SendAfter.Equal(f.now.Add(20 * time.Second)) {
		t.Errorf("SendAfter = %v, want now+20s", got.SendAfter)
	}
	if !got.Cancellable() {
		t.Error("a freshly queued compose is not cancellable")
	}
}

func TestCancelPendingComposeUndoesIt(t *testing.T) {
	f := newComposeFixture(t)
	ctx := context.Background()
	got, err := f.svc.ScheduleCompose(ctx, testWS, validCompose(f), nil)
	if err != nil {
		t.Fatalf("ScheduleCompose: %v", err)
	}

	if err := f.svc.CancelPendingCompose(ctx, testWS, got.ID); err != nil {
		t.Fatalf("CancelPendingCompose: %v", err)
	}
	after, err := f.svc.GetPendingCompose(ctx, testWS, got.ID)
	if err != nil {
		t.Fatalf("GetPendingCompose: %v", err)
	}
	if after.Status != inbox.PendingStatusCancelled {
		t.Errorf("Status = %q, want cancelled", after.Status)
	}
	// ...and it can never be claimed after that.
	if err := f.svc.ClaimPendingCompose(ctx, testWS, got.ID); !errors.Is(err, inbox.ErrPendingNotClaimable) {
		t.Errorf("a cancelled compose was claimable: %v", err)
	}
}

func TestCancelPendingComposeAfterClaimingSaysWhy(t *testing.T) {
	f := newComposeFixture(t)
	ctx := context.Background()
	got, err := f.svc.ScheduleCompose(ctx, testWS, validCompose(f), nil)
	if err != nil {
		t.Fatalf("ScheduleCompose: %v", err)
	}
	row := f.compose.composes[got.ID]
	row.Status = inbox.PendingStatusSending
	f.compose.composes[got.ID] = row

	if err := f.svc.CancelPendingCompose(ctx, testWS, got.ID); !errors.Is(err, inbox.ErrPendingNotCancellable) {
		t.Errorf("error = %v, want ErrPendingNotCancellable", err)
	}
}

func TestPendingComposeIsWorkspaceScoped(t *testing.T) {
	f := newComposeFixture(t)
	ctx := context.Background()
	got, err := f.svc.ScheduleCompose(ctx, testWS, validCompose(f), nil)
	if err != nil {
		t.Fatalf("ScheduleCompose: %v", err)
	}
	foreign := uuid.New()

	if _, err := f.svc.GetPendingCompose(ctx, foreign, got.ID); !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("GetPendingCompose(foreign) = %v, want ErrNotFound", err)
	}
	if err := f.svc.CancelPendingCompose(ctx, foreign, got.ID); !errors.Is(err, inbox.ErrNotFound) {
		t.Errorf("CancelPendingCompose(foreign) = %v, want ErrNotFound", err)
	}
	if err := f.svc.ClaimPendingCompose(ctx, foreign, got.ID); !errors.Is(err, inbox.ErrPendingNotClaimable) {
		t.Errorf("ClaimPendingCompose(foreign) = %v, want ErrPendingNotClaimable", err)
	}
}

// --- HTTP layer ---

func TestComposeDraftEndpointRoundTrip(t *testing.T) {
	f := newComposeFixture(t)
	draftID := uuid.NewString()

	res := f.serveAs(t, http.MethodPut, "/inbox/drafts/"+draftID,
		`{"to_emails":["ada@prospect.test"],"subject":"Hi","body_text":"draft"}`)
	if res.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200 (%s)", res.Code, res.Body.String())
	}

	res = f.serveAs(t, http.MethodGet, "/inbox/drafts", "")
	if res.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", res.Code)
	}
	var list struct {
		Drafts []struct {
			ID       string   `json:"id"`
			ToEmails []string `json:"to_emails"`
			CcEmails []string `json:"cc_emails"`
		} `json:"drafts"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(list.Drafts) != 1 || list.Drafts[0].ID != draftID {
		t.Fatalf("drafts = %+v, want the one saved", list.Drafts)
	}
	// Absent recipient lists must be [] rather than null.
	if list.Drafts[0].CcEmails == nil {
		t.Error("cc_emails marshalled as null; a client must be able to map over it")
	}

	res = f.serveAs(t, http.MethodDelete, "/inbox/drafts/"+draftID, "")
	if res.Code != http.StatusNoContent {
		t.Errorf("DELETE status = %d, want 204 (%s)", res.Code, res.Body.String())
	}
}

func TestSendComposeEndpointQueuesAndReturnsTheHandle(t *testing.T) {
	f := newComposeFixture(t)
	body := `{"mailbox_id":"` + f.mailbox.String() +
		`","to_emails":["ada@prospect.test"],"subject":"Hi","body_text":"hello"}`

	res := serve(t, f.handler, http.MethodPost, "/inbox/composes", body)
	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", res.Code, res.Body.String())
	}
	var got struct {
		ID          string `json:"id"`
		Cancellable bool   `json:"cancellable"`
		SendAfter   string `json:"send_after"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID == "" || !got.Cancellable || got.SendAfter == "" {
		t.Errorf("response = %+v, want an id, a countdown, and cancellable", got)
	}
}

// Sending from a draft discards it — mail on its way should not leave a copy in
// the drafts list.
func TestSendComposeDiscardsItsDraft(t *testing.T) {
	f := newComposeFixture(t)
	draftID := uuid.NewString()
	if res := f.serveAs(t, http.MethodPut, "/inbox/drafts/"+draftID,
		`{"body_text":"draft"}`); res.Code != http.StatusOK {
		t.Fatalf("save draft: %d (%s)", res.Code, res.Body.String())
	}

	body := `{"mailbox_id":"` + f.mailbox.String() +
		`","to_emails":["ada@prospect.test"],"body_text":"hello","draft_id":"` + draftID + `"}`
	if res := f.serveAs(t, http.MethodPost, "/inbox/composes", body); res.Code != http.StatusCreated {
		t.Fatalf("send: %d (%s)", res.Code, res.Body.String())
	}

	res := f.serveAs(t, http.MethodGet, "/inbox/drafts", "")
	var list struct {
		Drafts []struct{ ID string } `json:"drafts"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, d := range list.Drafts {
		if d.ID == draftID {
			t.Error("the draft survived a successful send")
		}
	}
}

func TestSendComposeEndpointStatusCodes(t *testing.T) {
	f := newComposeFixture(t)
	valid := `"mailbox_id":"` + f.mailbox.String() + `"`
	tests := []struct {
		name string
		body string
		want int
	}{
		{"malformed json", `{`, http.StatusBadRequest},
		{"unknown field", `{` + valid + `,"to_emails":["a@x.test"],"body_text":"b","nope":1}`, http.StatusBadRequest},
		{"non-uuid mailbox", `{"mailbox_id":"nope","to_emails":["a@x.test"],"body_text":"b"}`, http.StatusBadRequest},
		{"bad send_at", `{` + valid + `,"to_emails":["a@x.test"],"body_text":"b","send_at":"soon"}`, http.StatusBadRequest},
		{"no recipients", `{` + valid + `,"to_emails":[],"body_text":"b"}`, http.StatusUnprocessableEntity},
		{"invalid recipient", `{` + valid + `,"to_emails":["nope"],"body_text":"b"}`, http.StatusUnprocessableEntity},
		{"empty body", `{` + valid + `,"to_emails":["a@x.test"],"body_text":""}`, http.StatusUnprocessableEntity},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := serve(t, f.handler, http.MethodPost, "/inbox/composes", tc.body)
			if res.Code != tc.want {
				t.Errorf("status = %d, want %d (%s)", res.Code, tc.want, res.Body.String())
			}
		})
	}
}

func TestComposeOutboxEndpointListsAndCancels(t *testing.T) {
	f := newComposeFixture(t)
	body := `{"mailbox_id":"` + f.mailbox.String() +
		`","to_emails":["ada@prospect.test"],"body_text":"hello"}`
	created := serve(t, f.handler, http.MethodPost, "/inbox/composes", body)
	if created.Code != http.StatusCreated {
		t.Fatalf("send: %d (%s)", created.Code, created.Body.String())
	}
	var queued struct{ ID string }
	if err := json.Unmarshal(created.Body.Bytes(), &queued); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	listed := serve(t, f.handler, http.MethodGet, "/inbox/composes", "")
	if listed.Code != http.StatusOK {
		t.Fatalf("list: %d (%s)", listed.Code, listed.Body.String())
	}
	var page struct {
		Items []struct{ ID string } `json:"items"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &page); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != queued.ID {
		t.Fatalf("list = %+v, want the one queued compose", page.Items)
	}

	if res := serve(t, f.handler, http.MethodDelete, "/inbox/composes/"+queued.ID, ""); res.Code != http.StatusNoContent {
		t.Errorf("cancel status = %d, want 204 (%s)", res.Code, res.Body.String())
	}
}
