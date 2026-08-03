package deliverability

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/deliverability"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef")

// serve runs one request through the REAL auth middleware and the domain's own
// routers, so the workspace these tests assert on is the one from the JWT and the
// {id} path parameter is parsed by chi exactly as in production. The per-campaign
// routes are registered as a sub-router under /campaigns, mirroring cmd/inroad.
func serve(t *testing.T, h *Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	tok, err := auth.IssueToken(testSecret, auth.Claims{
		UserID: uuid.NewString(), WorkspaceID: testWS.String(), Role: "owner", SessionID: uuid.NewString(),
	}, time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	r.Header.Set("Authorization", "Bearer "+tok)

	root := chi.NewRouter()
	root.Mount("/deliverability", h.Routes())
	root.Route("/campaigns", h.Register)

	w := httptest.NewRecorder()
	auth.RequireAuth(auth.NewJWTVerifier(testSecret))(root).ServeHTTP(w, r)
	return w
}

func handlerWith(store Store) *Handler {
	svc := NewService(store)
	svc.now = func() time.Time { return fixedNow }
	return NewHandler(svc)
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding %s: %v", w.Body, err)
	}
	return out
}

// An unmeasured component serializes rate as JSON NULL, never 0 — the whole point
// of invariant 4 reaching the wire.
func TestUnmeasuredComponentSerializesAsNull(t *testing.T) {
	store := &fakeStore{
		counts: Counts{Delivered: 200, Bounced: 4, ComplaintFeed: false},
		series: []Point{{Day: fixedNow, Delivered: 10, Bounced: 1, Complained: 0, SpamPlaced: 0}},
	}
	w := serve(t, handlerWith(store), http.MethodGet, "/deliverability", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	body := decode[map[string]any](t, w)
	comps, ok := body["score"].(map[string]any)["components"].([]any)
	if !ok {
		t.Fatalf("no components in %s", w.Body)
	}
	seen := false
	for _, raw := range comps {
		c := raw.(map[string]any)
		if c["key"] != deliverability.KeyComplaint {
			continue
		}
		seen = true
		if c["measured"] != false {
			t.Errorf("complaint measured = %v, want false", c["measured"])
		}
		if c["rate"] != nil {
			t.Errorf("complaint rate = %v, want null", c["rate"])
		}
	}
	if !seen {
		t.Fatal("no complaint component in the response")
	}
	// The series follows the score: an unmeasured day is null, not zero.
	points := body["series"].([]any)
	if len(points) != 1 {
		t.Fatalf("%d series points, want 1", len(points))
	}
	p := points[0].(map[string]any)
	if p["complained"] != nil || p["spam_placed"] != nil {
		t.Errorf("series point = %v, want null complained and spam_placed", p)
	}
	if p["date"] != "2026-08-04" {
		t.Errorf("date = %v, want 2026-08-04", p["date"])
	}
	// A measured signal is present as a number, so null really does mean absent.
	if _, hasBounced := p["bounced"].(float64); !hasBounced {
		t.Errorf("series point has no numeric bounced: %v", p)
	}
}

func TestWorkspaceReportEmitsTheFrozenWireShape(t *testing.T) {
	store := &fakeStore{
		counts:        Counts{Delivered: 1000, Bounced: 50, Complained: 1, ComplaintFeed: true},
		signals:       Signals{InboxPlaced: 90, SpamPlaced: 10, WarmupState: deliverability.WarmupWatch, DomainState: deliverability.DomainPassing},
		series:        []Point{{Day: fixedNow, Delivered: 100, Bounced: 5, Complained: 1, SpamPlaced: 2}},
		atRiskMbx:     []Risk{{Label: "slow@acme.test", Reason: "throttled: spam placement 22%"}},
		atRiskDomains: []Risk{{Label: "acme.test", Reason: "no SPF record"}},
	}
	w := serve(t, handlerWith(store), http.MethodGet, "/deliverability", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	got := decode[map[string]any](t, w)
	for _, key := range []string{"score", "series", "at_risk_mailboxes", "at_risk_domains"} {
		if _, ok := got[key]; !ok {
			t.Errorf("response is missing required field %q: %s", key, w.Body)
		}
	}
	score := got["score"].(map[string]any)
	for _, key := range []string{"value", "confidence", "delivered", "components"} {
		if _, ok := score[key]; !ok {
			t.Errorf("score is missing required field %q", key)
		}
	}
	if score["delivered"].(float64) != 1000 {
		t.Errorf("delivered = %v, want 1000", score["delivered"])
	}
	risk := got["at_risk_mailboxes"].([]any)[0].(map[string]any)
	if risk["label"] != "slow@acme.test" || risk["reason"] == "" {
		t.Errorf("at-risk mailbox = %v", risk)
	}
}

// Empty lists must serialize as [] rather than null: the frozen schema says array,
// and a null would make the UI's .map() throw rather than render an empty state.
func TestEmptyCollectionsSerializeAsArrays(t *testing.T) {
	w := serve(t, handlerWith(&fakeStore{}), http.MethodGet, "/deliverability", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	for _, field := range []string{`"series":[]`, `"at_risk_mailboxes":[]`, `"at_risk_domains":[]`} {
		if !strings.Contains(w.Body.String(), field) {
			t.Errorf("body does not contain %s: %s", field, w.Body)
		}
	}
}

func TestCampaignReportEmitsTheFrozenWireShape(t *testing.T) {
	store := &fakeStore{
		config: runningCampaign(),
		counts: Counts{Delivered: 218, Bounced: 20, ComplaintFeed: true},
		events: []PauseEvent{{
			Reason:    deliverability.ReasonBounceSpike,
			Metric:    deliverability.MetricBounceRate,
			Value:     9.174311926605505,
			Threshold: 8,
			Delivered: 218,
			CreatedAt: fixedNow,
		}},
	}
	store.config.Status = "paused"
	w := serve(t, handlerWith(store), http.MethodGet, "/campaigns/"+testCampaign.String()+"/deliverability", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	got := decode[map[string]any](t, w)
	for _, key := range []string{"score", "guardrails", "pause_events", "verdict"} {
		if _, ok := got[key]; !ok {
			t.Errorf("response is missing required field %q: %s", key, w.Body)
		}
	}
	if got["verdict"] != verdictPaused {
		t.Errorf("verdict = %v, want %q", got["verdict"], verdictPaused)
	}
	ev := got["pause_events"].([]any)[0].(map[string]any)
	// Everything the card needs to say "paused automatically on 4 Aug: bounce rate
	// 9.17% over 218 delivered, threshold 8%".
	if ev["value"].(float64) != 9.17 {
		t.Errorf("value = %v, want 9.17 (rounded for the wire)", ev["value"])
	}
	if ev["threshold"].(float64) != 8 || ev["delivered"].(float64) != 218 {
		t.Errorf("pause event = %v", ev)
	}
	if ev["created_at"] != fixedNow.Format(time.RFC3339) {
		t.Errorf("created_at = %v, want RFC3339 %s", ev["created_at"], fixedNow.Format(time.RFC3339))
	}
	if ev["reason"] != deliverability.ReasonBounceSpike || ev["metric"] != deliverability.MetricBounceRate {
		t.Errorf("pause event reason/metric = %v/%v", ev["reason"], ev["metric"])
	}
	// The frozen CampaignDeliverability schema has no series, so emitting one would
	// be payload the generated client cannot reach.
	if _, extra := got["series"]; extra {
		t.Error("campaign response carries a series field the frozen schema does not declare")
	}
}

func TestCampaignRoutesRejectAMalformedID(t *testing.T) {
	h := handlerWith(&fakeStore{config: runningCampaign()})
	for _, target := range []string{"/campaigns/not-a-uuid/deliverability", "/campaigns/not-a-uuid/guardrails"} {
		method := http.MethodGet
		body := ""
		if strings.HasSuffix(target, "guardrails") {
			method, body = http.MethodPut, `{"auto_pause_enabled":true,"bounce_pause_pct":8,"complaint_pause_pct":1.5}`
		}
		w := serve(t, h, method, target, body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s %s: status = %d, want 400", method, target, w.Code)
		}
	}
}

func TestCampaignReportIsA404ForAForeignCampaign(t *testing.T) {
	w := serve(t, handlerWith(&fakeStore{configErr: ErrNotFound}), http.MethodGet,
		"/campaigns/"+testCampaign.String()+"/deliverability", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", w.Code, w.Body)
	}
}

func TestPutGuardrailsRoundTrips(t *testing.T) {
	store := &fakeStore{}
	w := serve(t, handlerWith(store), http.MethodPut, "/campaigns/"+testCampaign.String()+"/guardrails",
		`{"auto_pause_enabled":false,"bounce_pause_pct":5.5,"complaint_pause_pct":0.5}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	got := decode[guardrailsResponse](t, w)
	want := guardrailsResponse{AutoPauseEnabled: false, BouncePausePct: 5.5, ComplaintPausePct: 0.5}
	if got != want {
		t.Errorf("response = %+v, want %+v", got, want)
	}
	if store.saved == nil || *store.saved != (Guardrails{false, 5.5, 0.5}) {
		t.Errorf("stored = %+v", store.saved)
	}
}

// A PUT missing a field is rejected rather than defaulted. Silently reading an
// absent auto_pause_enabled as false would disable the safeguard on a request
// that meant only to change a threshold.
func TestPutGuardrailsRejectsAPartialBody(t *testing.T) {
	for _, body := range []string{
		`{"bounce_pause_pct":8,"complaint_pause_pct":1.5}`,
		`{"auto_pause_enabled":true,"complaint_pause_pct":1.5}`,
		`{"auto_pause_enabled":true,"bounce_pause_pct":8}`,
		`{}`,
	} {
		store := &fakeStore{}
		w := serve(t, handlerWith(store), http.MethodPut, "/campaigns/"+testCampaign.String()+"/guardrails", body)
		if w.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status = %d, want 422 (%s)", body, w.Code, w.Body)
		}
		if store.saved != nil {
			t.Errorf("%s: persisted %+v from a partial body", body, store.saved)
		}
	}
}

func TestPutGuardrailsRejectsAnOutOfRangeThreshold(t *testing.T) {
	store := &fakeStore{}
	w := serve(t, handlerWith(store), http.MethodPut, "/campaigns/"+testCampaign.String()+"/guardrails",
		`{"auto_pause_enabled":true,"bounce_pause_pct":0,"complaint_pause_pct":1.5}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (%s)", w.Code, w.Body)
	}
	if store.saved != nil {
		t.Error("a zero threshold was persisted")
	}
}

func TestPutGuardrailsRejectsInvalidJSON(t *testing.T) {
	w := serve(t, handlerWith(&fakeStore{}), http.MethodPut,
		"/campaigns/"+testCampaign.String()+"/guardrails", `{not json`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", w.Code, w.Body)
	}
}

// 202 on accept, 200 on a duplicate replay. Both mean "we have it"; the
// distinction is what tells a pipeline it is redelivering.
func TestIngestStatusCodes(t *testing.T) {
	body := `{"kind":"complaint","email":"a@b.test","provider_event_id":"ses-1"}`

	fresh := &fakeStore{ingestNew: true}
	if w := serve(t, handlerWith(fresh), http.MethodPost, "/deliverability/events", body); w.Code != http.StatusAccepted {
		t.Errorf("first delivery: status = %d, want 202 (%s)", w.Code, w.Body)
	}
	replay := &fakeStore{ingestNew: false}
	if w := serve(t, handlerWith(replay), http.MethodPost, "/deliverability/events", body); w.Code != http.StatusOK {
		t.Errorf("replay: status = %d, want 200 (%s)", w.Code, w.Body)
	}
}

func TestIngestRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{name: "not json", body: `{`, want: http.StatusBadRequest},
		{
			name: "unknown kind",
			body: `{"kind":"whatever","email":"a@b.test","provider_event_id":"1"}`,
			want: http.StatusUnprocessableEntity,
		},
		{
			name: "missing email",
			body: `{"kind":"bounce","provider_event_id":"1"}`,
			want: http.StatusUnprocessableEntity,
		},
		{
			name: "missing idempotency key",
			body: `{"kind":"bounce","email":"a@b.test"}`,
			want: http.StatusUnprocessableEntity,
		},
		{
			name: "send_id is not a uuid",
			body: `{"kind":"bounce","email":"a@b.test","provider_event_id":"1","send_id":"nope"}`,
			want: http.StatusUnprocessableEntity,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := &fakeStore{ingestNew: true}
			w := serve(t, handlerWith(store), http.MethodPost, "/deliverability/events", c.body)
			if w.Code != c.want {
				t.Errorf("status = %d, want %d (%s)", w.Code, c.want, w.Body)
			}
			if len(store.ingested) != 0 {
				t.Errorf("a rejected event was recorded: %+v", store.ingested)
			}
		})
	}
}

// A null send_id is the normal case (most feeds report an address, not our id) and
// must be accepted, not rejected as a malformed uuid.
func TestIngestAcceptsANullSendID(t *testing.T) {
	store := &fakeStore{ingestNew: true}
	w := serve(t, handlerWith(store), http.MethodPost, "/deliverability/events",
		`{"kind":"complaint","email":"a@b.test","provider_event_id":"1","send_id":null}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (%s)", w.Code, w.Body)
	}
	if len(store.ingested) != 1 || store.ingested[0].SendID != nil {
		t.Errorf("ingested = %+v, want one event with no send id", store.ingested)
	}
}

// The tenant comes from the credential, never the body: there is no workspace
// field to send, and an unauthenticated ingest is rejected outright.
func TestIngestRequiresAuthentication(t *testing.T) {
	store := &fakeStore{ingestNew: true}
	r := httptest.NewRequest(http.MethodPost, "/deliverability/events",
		strings.NewReader(`{"kind":"complaint","email":"a@b.test","provider_event_id":"1"}`))
	root := chi.NewRouter()
	root.Mount("/deliverability", handlerWith(store).Routes())
	w := httptest.NewRecorder()
	auth.RequireAuth(auth.NewJWTVerifier(testSecret))(root).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (%s)", w.Code, w.Body)
	}
	if len(store.ingested) != 0 {
		t.Error("an unauthenticated event was recorded")
	}
}
