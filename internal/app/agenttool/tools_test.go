package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/deliverability"
)

var (
	campaignA = uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000001")
	campaignB = uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000002")
	mailboxA  = uuid.MustParse("bbbbbbbb-0000-0000-0000-000000000001")
	listA     = uuid.MustParse("cccccccc-0000-0000-0000-000000000001")
	listB     = uuid.MustParse("cccccccc-0000-0000-0000-000000000002")
	contactA  = uuid.MustParse("dddddddd-0000-0000-0000-000000000001")
)

// ok runs a tool and requires a successful Result.
func ok(t *testing.T, reg *Reg, p Principal, name, args string) map[string]any {
	t.Helper()
	res, err := reg.Execute(context.Background(), p, name, json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s: unexpected error: %v", name, err)
	}
	if !res.Success {
		t.Fatalf("%s: failed: %s", name, res.Error)
	}
	raw, err := json.Marshal(res.Data)
	if err != nil {
		t.Fatalf("%s: result data does not marshal: %v", name, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s: result data is not a JSON object: %v", name, err)
	}
	return out
}

// fails runs a tool and requires a recoverable failure carrying instructions.
func fails(t *testing.T, reg *Reg, p Principal, name, args string) string {
	t.Helper()
	res, err := reg.Execute(context.Background(), p, name, json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s: got an error where a recoverable failure was expected: %v", name, err)
	}
	if res.Success {
		t.Fatalf("%s: succeeded, want a failure", name)
	}
	if res.Error == "" {
		t.Fatalf("%s: failure carries no recovery instruction", name)
	}
	return res.Error
}

// faults runs a tool and requires an infrastructure error return.
func faults(t *testing.T, reg *Reg, p Principal, name, args string) error {
	t.Helper()
	_, err := reg.Execute(context.Background(), p, name, json.RawMessage(args))
	if err == nil {
		t.Fatalf("%s: want an error return for an infrastructure fault, got nil", name)
	}
	return err
}

func seededCampaigns() *fakeCampaigns {
	return &fakeCampaigns{
		campaigns: []gen.Campaign{
			{ID: campaignA, WorkspaceID: wsID, Name: "Q3 outbound", Status: "running", Subject: "quick question",
				ListID: listA, MailboxID: mailboxA, Timezone: "UTC", CreatedAt: ts(time.Unix(1700000000, 0))},
			{ID: campaignB, WorkspaceID: otherWS, Name: "someone else's", Status: "draft"},
		},
		stats: map[string]int64{"sent": 120, "failed": 3},
		enrollments: []gen.ListCampaignEnrollmentsRow{
			{Email: "lead@example.com", FirstName: "Lee", Status: "stopped",
				ReplyClass: strPtr("interested"), RepliedAt: ts(time.Unix(1700100000, 0))},
		},
	}
}

func strPtr(s string) *string { return &s }

// ---------------------------------------------------------------------------
// campaigns

func TestCampaignReadListIsWorkspacePinned(t *testing.T) {
	camps := seededCampaigns()
	reg := New(Deps{Campaigns: camps})

	out := ok(t, reg, member(), "inroad_campaign_read", `{"loading_message":"Listing campaigns","method":"list"}`)
	items, _ := out["campaigns"].([]any)
	if len(items) != 1 {
		t.Fatalf("campaigns = %v, want only this workspace's one campaign", out["campaigns"])
	}
	first, _ := items[0].(map[string]any)
	if first["name"] != "Q3 outbound" || first["status"] != "running" {
		t.Errorf("campaign summary = %v", first)
	}
	if camps.gotWS != wsID {
		t.Errorf("store called with workspace %s, want the principal's %s", camps.gotWS, wsID)
	}
}

func TestCampaignReadListPagesWithOffset(t *testing.T) {
	reg := New(Deps{Campaigns: seededCampaigns()})
	out := ok(t, reg, member(), "inroad_campaign_read",
		`{"loading_message":"Paging","method":"list","offset":5}`)
	if items, _ := out["campaigns"].([]any); len(items) != 0 {
		t.Errorf("offset past the end returned %v", items)
	}
	if out["total"] != float64(1) {
		t.Errorf("total = %v, want the unpaged count", out["total"])
	}
}

func TestCampaignReadGetResolvesNames(t *testing.T) {
	reg := New(Deps{
		Campaigns: seededCampaigns(),
		Lists:     &fakeLists{lists: []gen.List{{ID: listA, WorkspaceID: wsID, Name: "Series A founders"}}},
		Mailboxes: &fakeMailboxes{mailboxes: []MailboxView{{ID: mailboxA, Email: "ali@acme.com", Status: "active"}}},
	})
	out := ok(t, reg, member(), "inroad_campaign_read",
		fmt.Sprintf(`{"loading_message":"Reading","method":"get","campaign_id":%q}`, campaignA))

	if out["list_name"] != "Series A founders" {
		t.Errorf("list_name = %v, want the resolved list name", out["list_name"])
	}
	if out["mailbox_email"] != "ali@acme.com" {
		t.Errorf("mailbox_email = %v", out["mailbox_email"])
	}
	if out["list_id"] != listA.String() {
		t.Errorf("list_id = %v; ids stay because follow-up reads need them", out["list_id"])
	}
}

// A campaign belonging to another workspace must read as absent, not as
// forbidden — the model is told to list what it can see.
func TestCampaignReadGetRejectsCrossTenantID(t *testing.T) {
	reg := New(Deps{Campaigns: seededCampaigns()})
	msg := fails(t, reg, member(), "inroad_campaign_read",
		fmt.Sprintf(`{"loading_message":"Reading","method":"get","campaign_id":%q}`, campaignB))
	if !strings.Contains(msg, "method=list") {
		t.Errorf("recovery text does not tell the model how to find a real campaign: %q", msg)
	}
}

func TestCampaignReadStatsAndEnrollments(t *testing.T) {
	camps := seededCampaigns()
	reg := New(Deps{Campaigns: camps})

	stats := ok(t, reg, member(), "inroad_campaign_read",
		fmt.Sprintf(`{"loading_message":"Stats","method":"stats","campaign_id":%q}`, campaignA))
	counts, _ := stats["send_counts"].(map[string]any)
	if counts["sent"] != float64(120) {
		t.Errorf("send_counts = %v", stats["send_counts"])
	}

	enr := ok(t, reg, member(), "inroad_campaign_read",
		fmt.Sprintf(`{"loading_message":"Enrollments","method":"enrollments","campaign_id":%q,"limit":10,"offset":20}`, campaignA))
	rows, _ := enr["enrollments"].([]any)
	if len(rows) != 1 {
		t.Fatalf("enrollments = %v", enr["enrollments"])
	}
	row, _ := rows[0].(map[string]any)
	if row["contact_email"] != "lead@example.com" || row["reply_class"] != "interested" {
		t.Errorf("enrollment row = %v", row)
	}
	if camps.gotLimit != 10 || camps.gotOffset != 20 {
		t.Errorf("paging passed through as limit=%d offset=%d", camps.gotLimit, camps.gotOffset)
	}
}

func TestCampaignReadRejectsBadArguments(t *testing.T) {
	reg := New(Deps{Campaigns: seededCampaigns()})
	cases := map[string]string{
		"unknown method":  `{"loading_message":"x","method":"destroy"}`,
		"missing id":      `{"loading_message":"x","method":"get"}`,
		"malformed id":    `{"loading_message":"x","method":"get","campaign_id":"not-a-uuid"}`,
		"limit too large": `{"loading_message":"x","method":"list","limit":500}`,
		"negative offset": `{"loading_message":"x","method":"list","offset":-1}`,
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) { fails(t, reg, member(), "inroad_campaign_read", args) })
	}
}

func TestCampaignReadPropagatesInfrastructureFaults(t *testing.T) {
	reg := New(Deps{Campaigns: &fakeCampaigns{err: errBoom}})
	err := faults(t, reg, member(), "inroad_campaign_read", `{"loading_message":"x","method":"list"}`)
	if !errors.Is(err, errBoom) {
		t.Errorf("err = %v, want it to wrap the store failure", err)
	}
}

func TestCampaignControlPausesAndResumes(t *testing.T) {
	ctrl := &fakeCampaignAdmin{}
	reg := New(Deps{Campaigns: seededCampaigns(), CampaignAdmin: ctrl})

	out := ok(t, reg, admin(), "inroad_campaign_control",
		fmt.Sprintf(`{"loading_message":"Pausing Q3 outbound","method":"pause","campaign_id":%q}`, campaignA))
	if out["campaign"] != "Q3 outbound" || out["action"] != "pause" {
		t.Errorf("result = %v", out)
	}
	if ctrl.paused != campaignA {
		t.Errorf("paused %s, want %s", ctrl.paused, campaignA)
	}
	if ctrl.gotWS != wsID {
		t.Errorf("controller called with workspace %s, want %s", ctrl.gotWS, wsID)
	}

	ok(t, reg, admin(), "inroad_campaign_control",
		fmt.Sprintf(`{"loading_message":"Resuming","method":"resume","campaign_id":%q}`, campaignA))
	if ctrl.resumed != campaignA {
		t.Errorf("resumed %s, want %s", ctrl.resumed, campaignA)
	}
}

func TestCampaignControlRejectsUnknownCampaign(t *testing.T) {
	reg := New(Deps{Campaigns: seededCampaigns(), CampaignAdmin: &fakeCampaignAdmin{}})
	fails(t, reg, admin(), "inroad_campaign_control",
		fmt.Sprintf(`{"loading_message":"x","method":"pause","campaign_id":%q}`, campaignB))
}

func TestCampaignControlSurfacesControllerFault(t *testing.T) {
	reg := New(Deps{Campaigns: seededCampaigns(), CampaignAdmin: &fakeCampaignAdmin{err: errBoom}})
	faults(t, reg, admin(), "inroad_campaign_control",
		fmt.Sprintf(`{"loading_message":"x","method":"pause","campaign_id":%q}`, campaignA))
}

// ---------------------------------------------------------------------------
// contacts

func TestContactReadSearchAndList(t *testing.T) {
	contacts := &fakeContacts{page: ContactPage{
		Matches: []ContactMatch{{ID: contactA, Email: "lee@acme.com", FirstName: "Lee", CreatedAt: time.Unix(1700000000, 0)}},
		Total:   10000, TotalIsCapped: true,
	}}
	reg := New(Deps{Contacts: contacts})

	out := ok(t, reg, member(), "inroad_contact_read", `{"loading_message":"Searching","method":"search","query":"acme"}`)
	if out["total_is_at_least"] != true {
		t.Errorf("capped total not reported as a floor: %v", out)
	}
	if contacts.gotQ.Query != "acme" || contacts.gotWS != wsID {
		t.Errorf("query = %+v, workspace = %s", contacts.gotQ, contacts.gotWS)
	}

	ok(t, reg, member(), "inroad_contact_read", `{"loading_message":"Listing","method":"list"}`)
	if contacts.gotQ.Query != "" {
		t.Errorf("list passed a text filter: %q", contacts.gotQ.Query)
	}
	if contacts.gotQ.Limit != defaultLimit {
		t.Errorf("limit = %d, want the default %d", contacts.gotQ.Limit, defaultLimit)
	}
}

func TestContactReadRejectsShortQuery(t *testing.T) {
	reg := New(Deps{Contacts: &fakeContacts{}})
	msg := fails(t, reg, member(), "inroad_contact_read", `{"loading_message":"x","method":"search","query":"a"}`)
	if !strings.Contains(msg, "method=list") {
		t.Errorf("recovery text does not offer the listing alternative: %q", msg)
	}
}

func TestContactReadReportsUnknownList(t *testing.T) {
	reg := New(Deps{Contacts: &fakeContacts{err: pgx.ErrNoRows}})
	fails(t, reg, member(), "inroad_contact_read",
		fmt.Sprintf(`{"loading_message":"x","method":"list","list_id":%q}`, listA))
}

func TestContactWriteCreate(t *testing.T) {
	writes := &fakeContactWrites{id: contactA, created: true}
	reg := New(Deps{ContactWrites: writes})

	out := ok(t, reg, member(), "inroad_contact_write",
		`{"loading_message":"Adding Lee","method":"create","email":" lee@acme.com ","first_name":"Lee","company":"Acme"}`)
	if out["created"] != true || out["id"] != contactA.String() {
		t.Errorf("result = %v", out)
	}
	if writes.gotIn.Email != "lee@acme.com" {
		t.Errorf("email = %q, want it trimmed", writes.gotIn.Email)
	}
	if writes.gotWS != wsID {
		t.Errorf("write pinned to %s, want %s", writes.gotWS, wsID)
	}
}

// An email that already exists is reported as created=false rather than
// duplicated, so the model can carry on with the id it got back.
func TestContactWriteCreateReportsExisting(t *testing.T) {
	reg := New(Deps{ContactWrites: &fakeContactWrites{id: contactA, created: false}})
	out := ok(t, reg, member(), "inroad_contact_write",
		`{"loading_message":"Adding","method":"create","email":"lee@acme.com"}`)
	if out["created"] != false {
		t.Errorf("created = %v, want false for an existing contact", out["created"])
	}
}

func TestContactWriteRejectsBadEmail(t *testing.T) {
	reg := New(Deps{ContactWrites: &fakeContactWrites{}})
	for _, email := range []string{``, `not-an-email`, `a@b.com, c@d.com`} {
		args := fmt.Sprintf(`{"loading_message":"x","method":"create","email":%q}`, email)
		fails(t, reg, member(), "inroad_contact_write", args)
	}
}

func TestContactWriteAddToList(t *testing.T) {
	writes := &fakeContactWrites{}
	reg := New(Deps{ContactWrites: writes})
	args := fmt.Sprintf(`{"loading_message":"Adding to list","method":"add_to_list","contact_id":%q,"list_id":%q}`, contactA, listA)
	out := ok(t, reg, member(), "inroad_contact_write", args)
	if out["added"] != true || writes.gotList != listA {
		t.Errorf("result = %v, list = %s", out, writes.gotList)
	}

	writes.addErr = pgx.ErrNoRows
	msg := fails(t, reg, member(), "inroad_contact_write", args)
	if !strings.Contains(msg, "inroad_list_read") {
		t.Errorf("recovery text does not name the tool that finds a real list: %q", msg)
	}
}

// ---------------------------------------------------------------------------
// mailboxes

func TestMailboxReadNeverExposesCredentials(t *testing.T) {
	reg := New(Deps{Mailboxes: &fakeMailboxes{mailboxes: []MailboxView{
		{ID: mailboxA, Email: "ali@acme.com", Provider: "smtp", Status: "active", DailyCap: 50},
	}}})
	res, err := reg.Execute(context.Background(), member(), "inroad_mailbox_read",
		json.RawMessage(`{"loading_message":"Reading mailboxes","method":"list"}`))
	if err != nil || !res.Success {
		t.Fatalf("err=%v res=%+v", err, res)
	}
	raw, err := json.Marshal(res.Data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, forbidden := range []string{"secret", "ciphertext", "password", "token", "username"} {
		if strings.Contains(strings.ToLower(string(raw)), forbidden) {
			t.Errorf("mailbox payload mentions %q: %s", forbidden, raw)
		}
	}
}

func TestMailboxReadGetAndMiss(t *testing.T) {
	boxes := &fakeMailboxes{mailboxes: []MailboxView{{ID: mailboxA, Email: "ali@acme.com", Status: "error", LastError: "auth failed"}}}
	reg := New(Deps{Mailboxes: boxes})

	out := ok(t, reg, member(), "inroad_mailbox_read",
		fmt.Sprintf(`{"loading_message":"Reading","method":"get","mailbox_id":%q}`, mailboxA))
	if out["last_error"] != "auth failed" {
		t.Errorf("last_error = %v", out["last_error"])
	}

	fails(t, reg, member(), "inroad_mailbox_read",
		fmt.Sprintf(`{"loading_message":"x","method":"get","mailbox_id":%q}`, campaignA))
}

func TestMailboxReadCapsResults(t *testing.T) {
	many := make([]MailboxView, 40)
	for i := range many {
		many[i] = MailboxView{ID: uuid.New(), Email: fmt.Sprintf("m%d@acme.com", i)}
	}
	reg := New(Deps{Mailboxes: &fakeMailboxes{mailboxes: many}})
	out := ok(t, reg, member(), "inroad_mailbox_read", `{"loading_message":"x","method":"list"}`)
	if items, _ := out["mailboxes"].([]any); len(items) != defaultLimit {
		t.Errorf("returned %d mailboxes, want the default cap of %d", len(items), defaultLimit)
	}
	if out["total"] != float64(40) {
		t.Errorf("total = %v, want the uncapped count so the model knows more exist", out["total"])
	}
}

// ---------------------------------------------------------------------------
// lists

func TestListReadGetCountsMembersOnlyAfterOwnershipCheck(t *testing.T) {
	lists := &fakeLists{lists: []gen.List{{ID: listA, WorkspaceID: wsID, Name: "Series A founders"}}, members: 412}
	reg := New(Deps{Lists: lists})

	out := ok(t, reg, member(), "inroad_list_read",
		fmt.Sprintf(`{"loading_message":"Reading","method":"get","list_id":%q}`, listA))
	if out["members"] != float64(412) {
		t.Errorf("members = %v", out["members"])
	}
	if lists.counted != listA {
		t.Errorf("counted %s, want %s", lists.counted, listA)
	}

	// A list in another workspace must never reach the un-scoped count query.
	lists.counted = uuid.Nil
	fails(t, reg, member(), "inroad_list_read",
		fmt.Sprintf(`{"loading_message":"x","method":"get","list_id":%q}`, campaignA))
	if lists.counted != uuid.Nil {
		t.Errorf("member count ran for an unowned list %s", lists.counted)
	}
}

// The listing reports each list's size WITHOUT a per-list count query: the
// size rides along on the listing row (ListLists aggregates it in the same
// statement). Both halves matter — the number must be there, and `counted`
// must stay untouched, or the tool has quietly become N+1 in the workspace's
// list count.
func TestListReadListReportsMemberCountsWithoutFanningOut(t *testing.T) {
	lists := &fakeLists{
		lists: []gen.List{
			{ID: listA, WorkspaceID: wsID, Name: "Founders"},
			{ID: listB, WorkspaceID: wsID, Name: "Operators"},
		},
		members: 9,
	}
	reg := New(Deps{Lists: lists})

	out := ok(t, reg, member(), "inroad_list_read", `{"loading_message":"x","method":"list"}`)
	items, _ := out["lists"].([]any)
	if len(items) != 2 {
		t.Fatalf("lists = %v, want 2 rows", out["lists"])
	}
	for _, item := range items {
		row, _ := item.(map[string]any)
		if got, ok := row["members"].(float64); !ok || got != 9 {
			t.Errorf("row %v: members = %v, want 9", row["name"], row["members"])
		}
	}
	if lists.counted != uuid.Nil {
		t.Errorf("listing fanned out to a per-list count for %s", lists.counted)
	}
}

func TestListWriteCreatesAndValidates(t *testing.T) {
	writes := &fakeListWrites{created: gen.List{ID: listA, WorkspaceID: wsID, Name: "Founders"}}
	reg := New(Deps{ListWrites: writes})

	out := ok(t, reg, member(), "inroad_list_write", `{"loading_message":"Creating list","name":"  Founders  "}`)
	if out["name"] != "Founders" || writes.gotName != "Founders" {
		t.Errorf("result = %v, stored name = %q", out, writes.gotName)
	}
	if writes.gotWS != wsID {
		t.Errorf("created in workspace %s, want %s", writes.gotWS, wsID)
	}

	fails(t, reg, member(), "inroad_list_write", `{"loading_message":"x","name":"   "}`)
	fails(t, reg, member(), "inroad_list_write",
		fmt.Sprintf(`{"loading_message":"x","name":%q}`, strings.Repeat("a", maxListNameLen+1)))
}

// ---------------------------------------------------------------------------
// deliverability

func TestDeliverabilityReadWorkspaceKeepsUnmeasuredComponents(t *testing.T) {
	rate := 2.5
	health := &fakeHealth{workspace: WorkspaceHealth{
		Score: deliverability.Score{Value: 82, Delivered: 400, Confidence: deliverability.ConfidenceMedium,
			Components: []deliverability.Component{
				{Key: "bounce", Label: "Bounce rate", Measured: true, Penalty: 8, Rate: &rate},
				{Key: "complaint", Label: "Complaint rate", Measured: false, Detail: "no feed connected"},
			}},
		AtRiskMailboxes: []HealthRisk{{Label: "ali@acme.com", Reason: "spam placement rising"}},
	}}
	reg := New(Deps{Deliverability: health})

	out := ok(t, reg, member(), "inroad_deliverability_read", `{"loading_message":"Checking health","method":"workspace"}`)
	score, _ := out["score"].(map[string]any)
	comps, _ := score["components"].([]any)
	if len(comps) != 2 {
		t.Fatalf("components = %v", score["components"])
	}
	complaint, _ := comps[1].(map[string]any)
	if complaint["measured"] != false {
		t.Errorf("unmeasured component reported as measured: %v", complaint)
	}
	if _, present := complaint["rate_pct"]; present {
		t.Errorf("unmeasured component carries a rate, which reads as 0%%: %v", complaint)
	}
	if health.gotWS != wsID {
		t.Errorf("read workspace %s, want %s", health.gotWS, wsID)
	}
}

func TestDeliverabilityReadPulseAndCampaign(t *testing.T) {
	health := &fakeHealth{
		snapshot: Snapshot{
			MailboxesTotal: 4, MailboxesActive: 3, CampaignsRunning: 2, SentToday: 90, DailyCap: 200,
			Attention: []SnapshotAttention{{Kind: "mailbox_error", Severity: "high", Count: 1, Reason: "auth failed"}},
		},
		campaign: CampaignHealth{Verdict: "warn", AutoPauseEnabled: true, BouncePausePct: 5,
			PauseEvents: []HealthPauseEvent{{Reason: "bounce rate", Metric: "bounce", Value: 6, Threshold: 5, Delivered: 300, CreatedAt: time.Unix(1700000000, 0)}}},
	}
	reg := New(Deps{Deliverability: health})

	pulse := ok(t, reg, member(), "inroad_deliverability_read", `{"loading_message":"x","method":"pulse"}`)
	sending, _ := pulse["sending_today"].(map[string]any)
	if sending["sent"] != float64(90) || sending["daily_cap"] != float64(200) {
		t.Errorf("sending_today = %v", pulse["sending_today"])
	}
	if rows, _ := pulse["attention"].([]any); len(rows) != 1 {
		t.Errorf("attention = %v", pulse["attention"])
	}

	camp := ok(t, reg, member(), "inroad_deliverability_read",
		fmt.Sprintf(`{"loading_message":"x","method":"campaign","campaign_id":%q}`, campaignA))
	if camp["verdict"] != "warn" {
		t.Errorf("verdict = %v", camp["verdict"])
	}
	if events, _ := camp["pause_events"].([]any); len(events) != 1 {
		t.Errorf("pause_events = %v", camp["pause_events"])
	}
}

func TestDeliverabilityReadMissingCampaign(t *testing.T) {
	reg := New(Deps{Deliverability: &fakeHealth{campErr: pgx.ErrNoRows}})
	fails(t, reg, member(), "inroad_deliverability_read",
		fmt.Sprintf(`{"loading_message":"x","method":"campaign","campaign_id":%q}`, campaignA))
}

func TestDeliverabilityReadRequiresCampaignID(t *testing.T) {
	reg := New(Deps{Deliverability: &fakeHealth{}})
	fails(t, reg, member(), "inroad_deliverability_read", `{"loading_message":"x","method":"campaign"}`)
}

// ---------------------------------------------------------------------------
// warmup

func TestWarmupReadOverview(t *testing.T) {
	warm := &fakeWarmup{overview: WarmupOverview{PoolSize: 1, Active: false, Mailboxes: []WarmupMailbox{
		{MailboxID: mailboxA, Email: "ali@acme.com", Enabled: true, HealthState: "watch", HealthReason: "spam rising", TodaySent: 4, TodayTarget: 10},
	}}}
	reg := New(Deps{Warmup: warm})

	out := ok(t, reg, member(), "inroad_warmup_read", `{"loading_message":"Reading warmup","method":"overview"}`)
	if out["active"] != false || out["pool_size"] != float64(1) {
		t.Errorf("pool summary = %v", out)
	}
	boxes, _ := out["mailboxes"].([]any)
	first, _ := boxes[0].(map[string]any)
	if first["health_reason"] != "spam rising" {
		t.Errorf("mailbox row = %v", first)
	}
	if warm.gotWS != wsID {
		t.Errorf("read workspace %s, want %s", warm.gotWS, wsID)
	}
}

func TestWarmupReadDetailTrimsSeriesToRecentDays(t *testing.T) {
	series := make([]WarmupDay, 30)
	for i := range series {
		series[i] = WarmupDay{Day: fmt.Sprintf("2026-07-%02d", i+1), Sent: int32(i)}
	}
	reg := New(Deps{Warmup: &fakeWarmup{detail: WarmupDetail{
		Participant: WarmupParticipant{MailboxID: mailboxA, Enabled: true, MaxVolume: 40, HealthState: "healthy"},
		Series:      series,
	}}})

	out := ok(t, reg, member(), "inroad_warmup_read",
		fmt.Sprintf(`{"loading_message":"x","method":"get","mailbox_id":%q}`, mailboxA))
	days, _ := out["series"].([]any)
	if len(days) != maxWarmupSeries {
		t.Fatalf("series length = %d, want %d", len(days), maxWarmupSeries)
	}
	// The tail is the recent history; keeping the head would answer a "how is
	// it trending" question with three-week-old numbers.
	last, _ := days[len(days)-1].(map[string]any)
	if last["sent"] != float64(29) {
		t.Errorf("last day = %v, want the most recent", last)
	}
}

func TestWarmupReadMissingMailbox(t *testing.T) {
	reg := New(Deps{Warmup: &fakeWarmup{getErr: pgx.ErrNoRows}})
	fails(t, reg, member(), "inroad_warmup_read",
		fmt.Sprintf(`{"loading_message":"x","method":"get","mailbox_id":%q}`, mailboxA))
}

// ---------------------------------------------------------------------------
// cross-object search

func TestSearchSpansObjectTypes(t *testing.T) {
	reg := New(Deps{
		Campaigns: seededCampaigns(),
		Contacts:  &fakeContacts{page: ContactPage{Matches: []ContactMatch{{ID: contactA, Email: "q3@acme.com"}}}},
		Mailboxes: &fakeMailboxes{mailboxes: []MailboxView{{ID: mailboxA, Email: "q3-sender@acme.com", Status: "active"}}},
		Lists:     &fakeLists{lists: []gen.List{{ID: listA, WorkspaceID: wsID, Name: "Q3 targets"}}},
	})
	out := ok(t, reg, member(), "inroad_search", `{"loading_message":"Searching for Q3","query":"q3"}`)
	hits, _ := out["results"].([]any)
	seen := map[string]bool{}
	for _, h := range hits {
		hit, _ := h.(map[string]any)
		seen[hit["type"].(string)] = true
	}
	for _, want := range []string{objectCampaign, objectContact, objectMailbox, objectList} {
		if !seen[want] {
			t.Errorf("no %s hit in %v", want, hits)
		}
	}
}

func TestSearchTypeFilterAndUnknownType(t *testing.T) {
	reg := New(Deps{
		Campaigns: seededCampaigns(),
		Lists:     &fakeLists{lists: []gen.List{{ID: listA, WorkspaceID: wsID, Name: "Q3 targets"}}},
	})
	out := ok(t, reg, member(), "inroad_search", `{"loading_message":"x","query":"q3","types":["list"]}`)
	hits, _ := out["results"].([]any)
	if len(hits) != 1 {
		t.Fatalf("results = %v, want only the list", hits)
	}
	if hit, _ := hits[0].(map[string]any); hit["type"] != objectList {
		t.Errorf("hit = %v", hit)
	}

	msg := fails(t, reg, member(), "inroad_search", `{"loading_message":"x","query":"q3","types":["deal"]}`)
	if !strings.Contains(msg, objectCampaign) {
		t.Errorf("recovery text does not list the valid types: %q", msg)
	}
}

func TestSearchRejectsShortQueryAndReportsNoMatches(t *testing.T) {
	reg := New(Deps{Campaigns: seededCampaigns()})
	fails(t, reg, member(), "inroad_search", `{"loading_message":"x","query":"q"}`)

	out := ok(t, reg, member(), "inroad_search", `{"loading_message":"x","query":"zzzznothing"}`)
	if out["returned"] != float64(0) {
		t.Fatalf("returned = %v", out["returned"])
	}
	if out["note"] == nil {
		t.Error("an empty search gives the model no next step")
	}
}

func TestSearchPropagatesFaults(t *testing.T) {
	reg := New(Deps{Contacts: &fakeContacts{err: errBoom}})
	if err := faults(t, reg, member(), "inroad_search", `{"loading_message":"x","query":"acme"}`); !errors.Is(err, errBoom) {
		t.Errorf("err = %v, want the wrapped store failure", err)
	}
}
