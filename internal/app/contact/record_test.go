package contact

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
)

// recordFake is the record-page half of fakeStore (declared in import_test.go).
// Each aggregate is canned independently and the call counters prove which reads
// a request actually performed — that is how "ownership is resolved before
// anything is aggregated" is asserted rather than assumed.
type recordFake struct {
	get    Record
	getErr error

	suppression    *RecordSuppression
	suppressionErr error

	companyExists    bool
	companyExistsErr error
	setCompanyErr    error
	// setCompanyCalls records every link/unlink the service asked for, so a test
	// can prove a rejected request never reached the write.
	setCompanyCalls []*uuid.UUID

	deals    []RecordDeal
	dealsErr error

	sends    SendStats
	sendsErr error

	tracking    TrackingStats
	trackingErr error

	stopReasons    map[string]int64
	stopReasonsErr error

	campaigns    []CampaignEnrollment
	campaignsErr error

	dealLimit     int32
	campaignLimit int32
	aggregates    int
}

func (f *fakeStore) Get(context.Context, uuid.UUID, uuid.UUID) (Record, error) {
	return f.record.get, f.record.getErr
}

func (f *fakeStore) Suppression(context.Context, uuid.UUID, uuid.UUID) (*RecordSuppression, error) {
	return f.record.suppression, f.record.suppressionErr
}

func (f *fakeStore) CompanyExists(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return f.record.companyExists, f.record.companyExistsErr
}

func (f *fakeStore) SetCompany(_ context.Context, _, _ uuid.UUID, companyID *uuid.UUID) error {
	f.record.setCompanyCalls = append(f.record.setCompanyCalls, companyID)
	if f.record.setCompanyErr != nil {
		return f.record.setCompanyErr
	}
	if companyID == nil {
		f.record.get.Company = nil
		return nil
	}
	f.record.get.Company = &RecordCompany{ID: *companyID, Name: "Acme"}
	return nil
}

func (f *fakeStore) ListDeals(_ context.Context, _, _ uuid.UUID, limit int32) ([]RecordDeal, error) {
	f.record.dealLimit = limit
	return f.record.deals, f.record.dealsErr
}

func (f *fakeStore) SendStats(context.Context, uuid.UUID, uuid.UUID) (SendStats, error) {
	f.record.aggregates++
	return f.record.sends, f.record.sendsErr
}

func (f *fakeStore) TrackingStats(context.Context, uuid.UUID, uuid.UUID) (TrackingStats, error) {
	f.record.aggregates++
	return f.record.tracking, f.record.trackingErr
}

func (f *fakeStore) EnrollmentCounts(context.Context, uuid.UUID, uuid.UUID) (map[string]int64, error) {
	f.record.aggregates++
	return f.record.stopReasons, f.record.stopReasonsErr
}

func (f *fakeStore) ListCampaigns(_ context.Context, _, _ uuid.UUID, limit int32) ([]CampaignEnrollment, error) {
	f.record.aggregates++
	f.record.campaignLimit = limit
	return f.record.campaigns, f.record.campaignsErr
}

// A contact nobody has ever been sent to must read as zeroes, not as a division
// by zero. This is the branch a rate calculation gets wrong.
func TestComputeEngagementWithNoSends(t *testing.T) {
	id := uuid.New()
	got := computeEngagement(id, SendStats{}, TrackingStats{}, map[string]int64{}, nil, false)

	if got.EmailsSent != 0 || got.CampaignsEnrolled != 0 {
		t.Fatalf("counts = sent %d enrolled %d, want 0 and 0", got.EmailsSent, got.CampaignsEnrolled)
	}
	if got.OpenRate != 0 || got.ClickRate != 0 {
		t.Fatalf("rates = %v/%v, want 0/0", got.OpenRate, got.ClickRate)
	}
	if math.IsNaN(got.OpenRate) || math.IsNaN(got.ClickRate) {
		t.Fatal("a rate divided by zero sends")
	}
	if got.LastActivityAt != nil {
		t.Fatalf("last activity = %v, want nil", got.LastActivityAt)
	}
	// The wire contract marks campaigns required, so it must serialise as [] and
	// never as null.
	if got.Campaigns == nil {
		t.Fatal("campaigns = nil, want an empty slice")
	}
}

// The counts and the two denominators, checked against hand arithmetic. Replies/
// bounces/unsubscribes come off stop_reason; the lifetime enrollment count is the
// sum of every reason bucket INCLUDING the not-stopped one, so the total can
// never disagree with its parts.
func TestComputeEngagementCountsAndRates(t *testing.T) {
	stopReasons := map[string]int64{"": 2, stopReasonReplied: 1, stopReasonBounced: 1}
	got := computeEngagement(uuid.New(),
		SendStats{EmailsSent: 4},
		TrackingStats{OpensIndicative: 1, Clicks: 2},
		stopReasons, nil, false)

	checks := []struct {
		name string
		got  int64
		want int64
	}{
		{"emails_sent", got.EmailsSent, 4},
		{"opens_indicative", got.OpensIndicative, 1},
		{"clicks", got.Clicks, 2},
		{"replies", got.Replies, 1},
		{"bounces", got.Bounces, 1},
		{"unsubscribes", got.Unsubscribes, 0},
		{"campaigns_enrolled", got.CampaignsEnrolled, 4},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
	if got.OpenRate != 0.25 {
		t.Errorf("open_rate = %v, want 0.25", got.OpenRate)
	}
	if got.ClickRate != 0.5 {
		t.Errorf("click_rate = %v, want 0.5", got.ClickRate)
	}
}

// last_activity_at is the later of the two clocks, whichever that is, and nil
// only when neither ever happened.
func TestComputeEngagementLastActivity(t *testing.T) {
	early := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	late := early.Add(48 * time.Hour)

	tests := []struct {
		name      string
		lastSent  *time.Time
		lastEvent *time.Time
		want      *time.Time
	}{
		{"neither", nil, nil, nil},
		{"only a send", &early, nil, &early},
		{"only an event", nil, &early, &early},
		{"the event is later", &early, &late, &late},
		{"the send is later", &late, &early, &late},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := computeEngagement(uuid.New(),
				SendStats{LastSentAt: tc.lastSent},
				TrackingStats{LastEventAt: tc.lastEvent},
				nil, nil, false).LastActivityAt
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("last activity = %v, want nil", got)
			case tc.want != nil && got == nil:
				t.Fatalf("last activity = nil, want %v", tc.want)
			case tc.want != nil && !got.Equal(*tc.want):
				t.Fatalf("last activity = %v, want %v", got, tc.want)
			}
		})
	}
}

func recordService(store *fakeStore) *Service {
	return NewService(store, &fakeChecker{exists: true}, &fakeFieldStore{})
}

// The cap is enforced by asking for one row beyond it and reporting the surplus,
// so a contact with more deals than the cap must return exactly DealCap of them
// with the flag set — never the extra row, and never an unbounded list.
func TestRecordCapsDeals(t *testing.T) {
	tests := []struct {
		name          string
		rows          int
		wantItems     int
		wantTruncated bool
	}{
		{"under the cap", 3, 3, false},
		{"exactly the cap", DealCap, DealCap, false},
		{"one over the cap", DealCap + 1, DealCap, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{}
			store.record.deals = make([]RecordDeal, tc.rows)
			got, err := recordService(store).Record(context.Background(), testWS, uuid.New())
			if err != nil {
				t.Fatalf("Record: %v", err)
			}
			if len(got.Deals) != tc.wantItems {
				t.Fatalf("deals = %d, want %d", len(got.Deals), tc.wantItems)
			}
			if got.DealsTruncated != tc.wantTruncated {
				t.Fatalf("deals_truncated = %v, want %v", got.DealsTruncated, tc.wantTruncated)
			}
			if store.record.dealLimit != DealCap+1 {
				t.Fatalf("store asked for %d rows, want the cap plus one lookahead (%d)",
					store.record.dealLimit, DealCap+1)
			}
		})
	}
}

// Same lookahead contract for the enrollment list, and the counts stay exact
// while the list is truncated — they come from aggregates, not from this list.
func TestEngagementCapsCampaigns(t *testing.T) {
	store := &fakeStore{}
	store.record.campaigns = make([]CampaignEnrollment, CampaignCap+1)
	store.record.stopReasons = map[string]int64{"": 500}

	got, err := recordService(store).Engagement(context.Background(), testWS, uuid.New())
	if err != nil {
		t.Fatalf("Engagement: %v", err)
	}
	if len(got.Campaigns) != CampaignCap || !got.CampaignsTruncated {
		t.Fatalf("campaigns = %d (truncated %v), want %d and true",
			len(got.Campaigns), got.CampaignsTruncated, CampaignCap)
	}
	if got.CampaignsEnrolled != 500 {
		t.Fatalf("campaigns_enrolled = %d, want the exact 500 despite truncation", got.CampaignsEnrolled)
	}
	if store.record.campaignLimit != CampaignCap+1 {
		t.Fatalf("store asked for %d rows, want %d", store.record.campaignLimit, CampaignCap+1)
	}
}

// A contact the caller does not own must be refused before any aggregate runs:
// otherwise a foreign id still costs a scan, and a leaked count is a leaked fact.
func TestEngagementResolvesOwnershipBeforeAggregating(t *testing.T) {
	store := &fakeStore{}
	store.record.getErr = ErrNotFound

	_, err := recordService(store).Engagement(context.Background(), testWS, uuid.New())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if store.record.aggregates != 0 {
		t.Fatalf("%d aggregate queries ran for a contact the caller does not own", store.record.aggregates)
	}
}

// serveRecord runs one record-page GET through the real auth middleware and a
// chi router, so the workspace comes from the JWT and {id} is resolved the way
// production resolves it.
func serveRecord(t *testing.T, h *Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	tok, err := auth.IssueToken(testSecret, auth.Claims{
		UserID: uuid.NewString(), WorkspaceID: testWS.String(), Role: "owner", SessionID: uuid.NewString(),
	}, time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	router := chi.NewRouter()
	router.Get("/{id}", h.getContact)
	router.Get("/{id}/engagement", h.getContactEngagement)

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, http.NoBody)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	auth.RequireAuth(auth.NewJWTVerifier(testSecret))(router).ServeHTTP(w, r)
	return w
}

func TestRecordStatusMapping(t *testing.T) {
	id := uuid.New()
	boom := errors.New("connection reset")

	tests := []struct {
		name   string
		path   string
		getErr error
		want   int
	}{
		{"detail", "/" + id.String(), nil, http.StatusOK},
		{"engagement", "/" + id.String() + "/engagement", nil, http.StatusOK},
		// A contact in another workspace reaches the store's workspace-pinned
		// query, finds no row, and is reported exactly like one that never
		// existed.
		{"unknown or cross-workspace contact", "/" + id.String(), ErrNotFound, http.StatusNotFound},
		{"cross-workspace engagement", "/" + id.String() + "/engagement", ErrNotFound, http.StatusNotFound},
		{"id that is not a uuid", "/not-a-uuid", nil, http.StatusBadRequest},
		{"store failure", "/" + id.String(), boom, http.StatusInternalServerError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{}
			store.record.get = Record{ID: id}
			store.record.getErr = tc.getErr
			w := serveRecord(t, NewHandler(recordService(store)), tc.path)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

// Unauthenticated requests must not reach the store at all.
func TestRecordRequiresAuth(t *testing.T) {
	store := &fakeStore{}
	router := chi.NewRouter()
	router.Get("/{id}", NewHandler(recordService(store)).getContact)

	w := httptest.NewRecorder()
	auth.RequireAuth(auth.NewJWTVerifier(testSecret))(router).
		ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/"+uuid.NewString(), http.NoBody))

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if store.record.dealLimit != 0 {
		t.Fatal("an unauthenticated request reached the store")
	}
}

// The committed ContactDetail schema: every required field present, and an
// absent company/close_date an explicit null rather than a missing key.
func TestContactDetailResponseShape(t *testing.T) {
	id, companyID := uuid.New(), uuid.New()
	created := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	store := &fakeStore{}
	store.record.get = Record{
		ID: id, Email: "dana@acme.test", FirstName: "Dana", LastName: "Customer",
		JobTitle: "VP Ops", LinkedInURL: "https://linkedin.test/in/dana",
		Company:   &RecordCompany{ID: companyID, Name: "Acme", Domain: "acme.test"},
		CreatedAt: created, UpdatedAt: created,
	}
	store.record.deals = []RecordDeal{{
		ID: uuid.New(), Name: "Acme rollout", PipelineID: uuid.New(), StageID: uuid.New(),
		StageLabel: "Qualified", StageColor: "#3B82F6", Currency: "USD",
		CreatedAt: created, UpdatedAt: created,
	}}

	w := serveRecord(t, NewHandler(recordService(store)), "/"+id.String())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, key := range []string{"id", "email", "first_name", "last_name", "job_title",
		"linkedin_url", "suppression", "company", "deals", "deal_count", "deals_truncated",
		"created_at", "updated_at"} {
		if _, ok := body[key]; !ok {
			t.Errorf("response is missing required field %q", key)
		}
	}
	company, ok := body["company"].(map[string]any)
	if !ok {
		t.Fatalf("company = %v, want an object", body["company"])
	}
	if company["id"] != companyID.String() || company["name"] != "Acme" || company["domain"] != "acme.test" {
		t.Fatalf("company = %v, want the linked company's id, name and domain", company)
	}
	deals, ok := body["deals"].([]any)
	if !ok || len(deals) != 1 {
		t.Fatalf("deals = %v, want one row", body["deals"])
	}
	deal, ok := deals[0].(map[string]any)
	if !ok {
		t.Fatalf("deal = %v, want an object", deals[0])
	}
	closeDate, present := deal["close_date"]
	if !present || closeDate != nil {
		t.Fatalf("close_date = %v (present %v), want an explicit null", closeDate, present)
	}
}

// An unlinked contact serialises company as null, not as an empty object.
func TestContactDetailWithoutCompany(t *testing.T) {
	id := uuid.New()
	store := &fakeStore{}
	store.record.get = Record{ID: id, Email: "solo@x.test"}

	w := serveRecord(t, NewHandler(recordService(store)), "/"+id.String())
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	value, present := body["company"]
	if !present || value != nil {
		t.Fatalf("company = %v (present %v), want an explicit null", value, present)
	}
	if deals, ok := body["deals"].([]any); !ok || len(deals) != 0 {
		t.Fatalf("deals = %v, want []", body["deals"])
	}
}

func TestContactEngagementResponseShape(t *testing.T) {
	id, campaignID := uuid.New(), uuid.New()
	sent := time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC)
	reason := stopReasonReplied
	store := &fakeStore{}
	store.record.get = Record{ID: id}
	store.record.sends = SendStats{EmailsSent: 4, LastSentAt: &sent}
	store.record.tracking = TrackingStats{OpensIndicative: 2, Clicks: 1}
	store.record.stopReasons = map[string]int64{"": 1, stopReasonReplied: 1}
	store.record.campaigns = []CampaignEnrollment{{
		CampaignID: campaignID, CampaignName: "Q2 outbound", Status: "stopped",
		CurrentStep: 2, StopReason: &reason, EnrolledAt: sent, LastSentAt: &sent,
	}}

	w := serveRecord(t, NewHandler(recordService(store)), "/"+id.String()+"/engagement")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, key := range []string{"contact_id", "emails_sent", "opens_indicative", "clicks",
		"replies", "bounces", "unsubscribes", "open_rate", "click_rate", "campaigns_enrolled",
		"opens_measurable", "last_activity_at", "campaigns", "campaigns_truncated"} {
		if _, ok := body[key]; !ok {
			t.Errorf("response is missing required field %q", key)
		}
	}
	if body["contact_id"] != id.String() {
		t.Errorf("contact_id = %v, want %s", body["contact_id"], id)
	}
	if body["emails_sent"] != float64(4) || body["open_rate"] != 0.5 || body["click_rate"] != 0.25 {
		t.Errorf("sent/open_rate/click_rate = %v/%v/%v, want 4/0.5/0.25",
			body["emails_sent"], body["open_rate"], body["click_rate"])
	}
	if body["campaigns_enrolled"] != float64(2) {
		t.Errorf("campaigns_enrolled = %v, want 2", body["campaigns_enrolled"])
	}
	campaigns, ok := body["campaigns"].([]any)
	if !ok || len(campaigns) != 1 {
		t.Fatalf("campaigns = %v, want one row", body["campaigns"])
	}
	enrollment, ok := campaigns[0].(map[string]any)
	if !ok {
		t.Fatalf("enrollment = %v, want an object", campaigns[0])
	}
	if enrollment["campaign_name"] != "Q2 outbound" || enrollment["stop_reason"] != stopReasonReplied {
		t.Fatalf("enrollment = %v, want the campaign name and its stop reason", enrollment)
	}
}

// Suppression rides on the record and is not flattened into a boolean: the
// reason literal survives to the wire, and the two situations a suppression can
// describe stay distinguishable.
func TestContactDetailCarriesSuppressionWithItsReason(t *testing.T) {
	since := time.Date(2026, 1, 9, 8, 7, 6, 0, time.UTC)
	for _, reason := range []string{SuppressionUnsubscribe, SuppressionBounce, SuppressionComplaint, SuppressionManual} {
		t.Run(reason, func(t *testing.T) {
			id := uuid.New()
			store := &fakeStore{}
			store.record.get = Record{ID: id, Email: "dana@acme.test"}
			store.record.suppression = &RecordSuppression{
				Reason: reason, Email: "dana@acme.test", IsPrimaryEmail: true, SuppressedAt: since,
			}

			w := serveRecord(t, NewHandler(recordService(store)), "/"+id.String())
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			suppression, ok := body["suppression"].(map[string]any)
			if !ok {
				t.Fatalf("suppression = %v, want an object", body["suppression"])
			}
			// The literal, not a boolean: "they asked to stop" and "they reported
			// us as spam" must not arrive looking the same.
			if suppression["reason"] != reason {
				t.Errorf("reason = %v, want %q", suppression["reason"], reason)
			}
			if suppression["email"] != "dana@acme.test" || suppression["is_primary_email"] != true {
				t.Errorf("suppression = %v, want the primary address flagged", suppression)
			}
			if suppression["suppressed_at"] == nil {
				t.Error("suppressed_at is missing")
			}
		})
	}
}

// A contact who may be emailed reports suppression as an explicit null, so the
// client can tell "clear to send" from "the server did not answer".
func TestContactDetailSuppressionIsNullWhenClear(t *testing.T) {
	id := uuid.New()
	store := &fakeStore{}
	store.record.get = Record{ID: id, Email: "clear@acme.test"}

	w := serveRecord(t, NewHandler(recordService(store)), "/"+id.String())
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	value, present := body["suppression"]
	if !present || value != nil {
		t.Fatalf("suppression = %v (present %v), want an explicit null", value, present)
	}
}

// A suppression read that fails must fail the request rather than rendering a
// contact as emailable. Reporting "clear to send" because a query errored is the
// one wrong answer here.
func TestRecordFailsWhenSuppressionCannotBeRead(t *testing.T) {
	store := &fakeStore{}
	store.record.get = Record{ID: uuid.New()}
	store.record.suppressionErr = errors.New("connection reset")

	if _, err := recordService(store).Record(context.Background(), testWS, uuid.New()); err == nil {
		t.Fatal("Record succeeded despite an unreadable suppression state")
	}
}

// deal_count is counted independently of the capped list, so a truncated page
// still reports the true total. Deriving it from len(deals) would silently cap
// the number as well as the list.
func TestContactDetailDealCountSurvivesTruncation(t *testing.T) {
	id := uuid.New()
	store := &fakeStore{}
	store.record.get = Record{ID: id, DealCount: 38}
	store.record.deals = make([]RecordDeal, DealCap+1)

	w := serveRecord(t, NewHandler(recordService(store)), "/"+id.String())
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["deal_count"] != float64(38) {
		t.Fatalf("deal_count = %v, want the true total 38", body["deal_count"])
	}
	deals, ok := body["deals"].([]any)
	if !ok || len(deals) != DealCap {
		t.Fatalf("deals = %d rows, want the cap %d", len(deals), DealCap)
	}
	if body["deals_truncated"] != true {
		t.Fatalf("deals_truncated = %v, want true", body["deals_truncated"])
	}
}

// tracking_enabled rides on each enrollment: it is the only way a client can
// tell "nobody opened" from "opens were never recorded".
func TestContactEngagementReportsTrackingPerCampaign(t *testing.T) {
	id := uuid.New()
	store := &fakeStore{}
	store.record.get = Record{ID: id}
	store.record.sends = SendStats{EmailsSent: 2}
	store.record.campaigns = []CampaignEnrollment{
		{CampaignID: uuid.New(), CampaignName: "Tracked", TrackingEnabled: true, Status: "active"},
		{CampaignID: uuid.New(), CampaignName: "Untracked", TrackingEnabled: false, Status: "active"},
	}

	w := serveRecord(t, NewHandler(recordService(store)), "/"+id.String()+"/engagement")
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	campaigns, ok := body["campaigns"].([]any)
	if !ok || len(campaigns) != 2 {
		t.Fatalf("campaigns = %v, want two rows", body["campaigns"])
	}
	first, _ := campaigns[0].(map[string]any)
	second, _ := campaigns[1].(map[string]any)
	if first["tracking_enabled"] != true || second["tracking_enabled"] != false {
		t.Fatalf("tracking_enabled = %v / %v, want true then false",
			first["tracking_enabled"], second["tracking_enabled"])
	}
	// Opens stay 0 rather than being adjusted upward: the flag explains the zero,
	// it does not correct it (campaign.Metrics behaves the same way).
	if body["opens_indicative"] != float64(0) {
		t.Fatalf("opens_indicative = %v, want 0 left as-is", body["opens_indicative"])
	}
}

// opens_measurable comes from the send aggregate, NOT from the capped campaigns
// list. This is the case that makes the difference: every enrollment visible to
// the client has tracking off and the list is truncated, yet the contact WAS
// tracked on an older campaign — so a client-side some(tracking_enabled) would
// answer false and explain away a real zero. The server must say true.
func TestEngagementOpensMeasurableIgnoresTheCampaignCap(t *testing.T) {
	id := uuid.New()
	store := &fakeStore{}
	store.record.get = Record{ID: id}
	store.record.sends = SendStats{EmailsSent: 40, OpensMeasurable: true}
	// Every visible enrollment is untracked, and there are more than fit.
	store.record.campaigns = make([]CampaignEnrollment, CampaignCap+1)
	for i := range store.record.campaigns {
		store.record.campaigns[i] = CampaignEnrollment{CampaignID: uuid.New(), TrackingEnabled: false}
	}

	got, err := recordService(store).Engagement(context.Background(), testWS, id)
	if err != nil {
		t.Fatalf("Engagement: %v", err)
	}
	if !got.CampaignsTruncated {
		t.Fatal("fixture did not truncate; the case under test needs a capped list")
	}
	visibleSuggests := false
	for _, c := range got.Campaigns {
		if c.TrackingEnabled {
			visibleSuggests = true
		}
	}
	if visibleSuggests {
		t.Fatal("fixture leaked a tracked campaign into the visible window")
	}
	if !got.OpensMeasurable {
		t.Fatal("opens_measurable was inferred from the capped list, not the full history")
	}
}

// The flag is a passthrough of the send aggregate in both directions, so a
// genuinely never-tracked contact still reads false.
func TestEngagementOpensMeasurableFollowsTheSendAggregate(t *testing.T) {
	for _, measurable := range []bool{true, false} {
		t.Run(map[bool]string{true: "tracked", false: "never tracked"}[measurable], func(t *testing.T) {
			store := &fakeStore{}
			store.record.sends = SendStats{EmailsSent: 3, OpensMeasurable: measurable}

			got, err := recordService(store).Engagement(context.Background(), testWS, uuid.New())
			if err != nil {
				t.Fatalf("Engagement: %v", err)
			}
			if got.OpensMeasurable != measurable {
				t.Fatalf("opens_measurable = %v, want %v", got.OpensMeasurable, measurable)
			}
			// The counts are never adjusted by the flag either way.
			if got.OpensIndicative != 0 || got.Clicks != 0 {
				t.Fatalf("counts = %d/%d, want the raw zeros left alone",
					got.OpensIndicative, got.Clicks)
			}
		})
	}
}

// serveLink runs one PUT /contacts/{id}/company through the real auth middleware
// and chi router, so {id} and the scope gate behave as they do in production.
func serveLink(t *testing.T, h *Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	tok, err := auth.IssueToken(testSecret, auth.Claims{
		UserID: uuid.NewString(), WorkspaceID: testWS.String(), Role: "owner", SessionID: uuid.NewString(),
	}, time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	router := chi.NewRouter()
	router.Put("/{id}/company", h.putContactCompany)

	r := httptest.NewRequestWithContext(context.Background(), http.MethodPut, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	auth.RequireAuth(auth.NewJWTVerifier(testSecret))(router).ServeHTTP(w, r)
	return w
}

// Linking and unlinking are the same operation with a present or null company_id,
// which is the whole reason this is a PUT on a sub-resource and not a PATCH.
func TestSetContactCompanyLinksAndUnlinks(t *testing.T) {
	companyID := uuid.New()
	store := &fakeStore{}
	store.record.get = Record{ID: uuid.New()}
	store.record.companyExists = true

	linked, err := recordService(store).SetCompany(context.Background(), testWS, store.record.get.ID, &companyID)
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	if linked.Company == nil || linked.Company.ID != companyID {
		t.Fatalf("company = %+v, want the linked company", linked.Company)
	}

	unlinked, err := recordService(store).SetCompany(context.Background(), testWS, store.record.get.ID, nil)
	if err != nil {
		t.Fatalf("unlink: %v", err)
	}
	if unlinked.Company != nil {
		t.Fatalf("company = %+v, want nil after unlinking", unlinked.Company)
	}
	if len(store.record.setCompanyCalls) != 2 || store.record.setCompanyCalls[1] != nil {
		t.Fatalf("store calls = %v, want a link then an explicit nil", store.record.setCompanyCalls)
	}
}

// A company outside the workspace is refused BEFORE the write, so the tenant FK
// never has to reject it and no partial state is possible.
func TestSetContactCompanyRejectsForeignCompanyBeforeWriting(t *testing.T) {
	companyID := uuid.New()
	store := &fakeStore{}
	store.record.get = Record{ID: uuid.New()}
	store.record.companyExists = false

	_, err := recordService(store).SetCompany(context.Background(), testWS, store.record.get.ID, &companyID)
	if !errors.Is(err, ErrCompanyNotFound) {
		t.Fatalf("err = %v, want ErrCompanyNotFound", err)
	}
	if len(store.record.setCompanyCalls) != 0 {
		t.Fatalf("the write ran for a company the caller does not own: %v", store.record.setCompanyCalls)
	}
}

// Unlinking must not require a company to exist — there is no company id to check.
func TestSetContactCompanyUnlinkSkipsTheOwnershipCheck(t *testing.T) {
	store := &fakeStore{}
	store.record.get = Record{ID: uuid.New(), Company: &RecordCompany{ID: uuid.New()}}
	store.record.companyExists = false // would refuse a link

	got, err := recordService(store).SetCompany(context.Background(), testWS, store.record.get.ID, nil)
	if err != nil {
		t.Fatalf("unlink: %v", err)
	}
	if got.Company != nil {
		t.Fatalf("company = %+v, want nil", got.Company)
	}
}

func TestSetContactCompanyStatusMapping(t *testing.T) {
	id, companyID := uuid.New(), uuid.New()

	tests := []struct {
		name          string
		path          string
		body          string
		companyExists bool
		setErr        error
		want          int
	}{
		{"link", "/" + id.String() + "/company", `{"company_id":"` + companyID.String() + `"}`, true, nil, http.StatusOK},
		{"unlink", "/" + id.String() + "/company", `{"company_id":null}`, false, nil, http.StatusOK},
		{"foreign company", "/" + id.String() + "/company", `{"company_id":"` + companyID.String() + `"}`, false, nil, http.StatusNotFound},
		{"unknown contact", "/" + id.String() + "/company", `{"company_id":null}`, true, ErrNotFound, http.StatusNotFound},
		{"company deleted mid-write", "/" + id.String() + "/company", `{"company_id":"` + companyID.String() + `"}`, true, ErrCompanyNotFound, http.StatusNotFound},
		{"id is not a uuid", "/nope/company", `{"company_id":null}`, true, nil, http.StatusBadRequest},
		{"unknown field", "/" + id.String() + "/company", `{"company_id":null,"typo":1}`, true, nil, http.StatusBadRequest},
		{"trailing json", "/" + id.String() + "/company", `{"company_id":null} {}`, true, nil, http.StatusBadRequest},
		{"not an object", "/" + id.String() + "/company", `[]`, true, nil, http.StatusBadRequest},
		{"company_id not a uuid", "/" + id.String() + "/company", `{"company_id":"nope"}`, true, nil, http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{}
			store.record.get = Record{ID: id}
			store.record.companyExists = tc.companyExists
			store.record.setCompanyErr = tc.setErr
			w := serveLink(t, NewHandler(recordService(store)), tc.path, tc.body)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

// The two 404s say which record was missing. Both are workspace-scoped, so naming
// your own missing record leaks nothing, and a client needs to know which id to fix.
func TestSetContactCompanyDistinguishesTheTwo404s(t *testing.T) {
	id, companyID := uuid.New(), uuid.New()

	store := &fakeStore{}
	store.record.get = Record{ID: id}
	store.record.companyExists = false
	w := serveLink(t, NewHandler(recordService(store)), "/"+id.String()+"/company",
		`{"company_id":"`+companyID.String()+`"}`)
	if !strings.Contains(w.Body.String(), "company not found") {
		t.Fatalf("body = %s, want it to name the company", w.Body.String())
	}

	store = &fakeStore{}
	store.record.get = Record{ID: id}
	store.record.companyExists = true
	store.record.setCompanyErr = ErrNotFound
	w = serveLink(t, NewHandler(recordService(store)), "/"+id.String()+"/company", `{"company_id":null}`)
	if !strings.Contains(w.Body.String(), "contact not found") {
		t.Fatalf("body = %s, want it to name the contact", w.Body.String())
	}
}
