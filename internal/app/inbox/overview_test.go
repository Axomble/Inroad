package inbox_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/inbox"
)

// seedThreadDetail puts one fully-specified thread — and optionally its
// messages — straight into the fake. Distinct from handler_test.go's
// seedThread, which fixes unread=true and last_message_at=now: the overview's
// counters are *about* those two fields plus the message direction, so these
// tests need to state all of them.
func seedThreadDetail(f *fakeStore, ws uuid.UUID, th inbox.Thread, msgs ...inbox.Message) uuid.UUID {
	if th.ID == uuid.Nil {
		th.ID = uuid.New()
	}
	th.WorkspaceID = ws
	if th.MailboxID == uuid.Nil {
		th.MailboxID = uuid.New()
	}
	if th.LastMessageAt.IsZero() {
		th.LastMessageAt = time.Now().UTC()
	}
	f.threads[th.ID] = th
	if len(msgs) > 0 {
		f.messages[th.ID] = msgs
	}
	return th.ID
}

func TestGetOverviewCountsByScope(t *testing.T) {
	store := newFakeStore()
	svc := inbox.NewService(store)
	now := time.Now().UTC()
	mailbox := uuid.New()

	// Unread, from today, awaiting our reply (newest message is inbound).
	seedThreadDetail(store, testWS, inbox.Thread{
		MailboxID: mailbox, Unread: true, LastMessageAt: now, LastReplyClass: "positive",
	}, inbox.Message{Direction: "inbound", OccurredAt: now})
	// Read, from today, NOT awaiting (we replied last).
	seedThreadDetail(store, testWS, inbox.Thread{
		MailboxID: mailbox, Unread: false, LastMessageAt: now, LastReplyClass: "positive",
	}, inbox.Message{Direction: "inbound", OccurredAt: now.Add(-time.Hour)},
		inbox.Message{Direction: "outbound", OccurredAt: now})
	// Old enough to fall outside both the day and the week window.
	seedThreadDetail(store, testWS, inbox.Thread{
		MailboxID: mailbox, Unread: true, LastMessageAt: now.AddDate(0, 0, -30),
	})
	// Another workspace's thread must not appear in any counter.
	seedThreadDetail(store, uuid.New(), inbox.Thread{Unread: true, LastMessageAt: now})

	// A window whose boundaries are unambiguous relative to the seeds above,
	// rather than derived from the real clock (which would make "today" flake
	// for a test running at midnight).
	window := inbox.OverviewWindow{
		TodayStart: now.Add(-2 * time.Hour),
		WeekStart:  now.AddDate(0, 0, -7),
	}

	got, err := svc.GetOverview(context.Background(), testWS, window)
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}

	if got.Total != 3 {
		t.Errorf("Total = %d, want 3 (the foreign workspace's thread must be excluded)", got.Total)
	}
	if got.Unread != 2 {
		t.Errorf("Unread = %d, want 2", got.Unread)
	}
	if got.Today != 2 {
		t.Errorf("Today = %d, want 2", got.Today)
	}
	if got.ThisWeek != 2 {
		t.Errorf("ThisWeek = %d, want 2", got.ThisWeek)
	}
	if got.AwaitingReply != 1 {
		t.Errorf("AwaitingReply = %d, want 1 (only the thread whose newest message is inbound)", got.AwaitingReply)
	}
	if len(got.ByMailbox) != 1 || got.ByMailbox[0].Total != 3 {
		t.Errorf("ByMailbox = %+v, want one entry totalling 3", got.ByMailbox)
	}
	// The 30-day-old thread has no reply class, so it must not appear as a
	// filterable class — '' is the absence of a class.
	if len(got.ByReplyClass) != 1 || got.ByReplyClass[0].Key != "positive" || got.ByReplyClass[0].Total != 2 {
		t.Errorf("ByReplyClass = %+v, want only {positive, total 2}", got.ByReplyClass)
	}
}

// The case a rule reading only inbox_messages gets wrong: the contact replied,
// then the CAMPAIGN's own follow-up step went out. That send lives in `sends`
// and is only synthesized into the thread at read time, so a thread like this
// has an inbound message as its newest inbox_messages row while in fact the
// sequence has already answered on our behalf. It must NOT be reported as
// awaiting our reply.
func TestGetOverviewAwaitingReplyAccountsForCampaignSends(t *testing.T) {
	store := newFakeStore()
	svc := inbox.NewService(store)
	now := time.Now().UTC()
	window := inbox.OverviewWindow{TodayStart: now.Add(-24 * time.Hour), WeekStart: now.AddDate(0, 0, -7)}

	// Contact replied an hour ago and nothing has gone out since: awaiting us.
	seedThreadDetail(store, testWS, inbox.Thread{LastMessageAt: now},
		inbox.Message{Direction: "inbound", OccurredAt: now.Add(-time.Hour)})

	// Contact replied two hours ago, but a campaign step went out one hour
	// ago — already answered, so NOT awaiting us.
	answered := seedThreadDetail(store, testWS, inbox.Thread{LastMessageAt: now},
		inbox.Message{Direction: "inbound", OccurredAt: now.Add(-2 * time.Hour)})
	store.sentAt[answered] = now.Add(-time.Hour)

	got, err := svc.GetOverview(context.Background(), testWS, window)
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	if got.AwaitingReply != 1 {
		t.Errorf("AwaitingReply = %d, want 1 — a thread the campaign already followed up on is not awaiting us", got.AwaitingReply)
	}
}

// A thread the contact has never replied on is not awaiting us: there is
// nothing to reply to. Falls out of the SQL's NULL comparison rather than a
// special case, so it is worth pinning.
func TestGetOverviewAwaitingReplyExcludesThreadsWithNoInboundMessage(t *testing.T) {
	store := newFakeStore()
	svc := inbox.NewService(store)
	now := time.Now().UTC()

	seedThreadDetail(store, testWS, inbox.Thread{LastMessageAt: now},
		inbox.Message{Direction: "outbound", OccurredAt: now})
	// And one with no messages at all.
	seedThreadDetail(store, testWS, inbox.Thread{LastMessageAt: now})

	got, err := svc.GetOverview(context.Background(), testWS, inbox.OverviewWindow{
		TodayStart: now.Add(-24 * time.Hour), WeekStart: now.AddDate(0, 0, -7),
	})
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	if got.AwaitingReply != 0 {
		t.Errorf("AwaitingReply = %d, want 0", got.AwaitingReply)
	}
}

func TestGetOverviewEmptyWorkspace(t *testing.T) {
	svc := inbox.NewService(newFakeStore())
	got, err := svc.GetOverview(context.Background(), testWS, inbox.OverviewWindow{
		TodayStart: time.Now().UTC(), WeekStart: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	if got.Total != 0 || got.Unread != 0 || len(got.ByMailbox) != 0 {
		t.Errorf("empty workspace returned %+v, want all zero", got)
	}
}

// The endpoint must marshal empty breakdowns as [] rather than null, so a
// client can map over them unconditionally.
func TestOverviewEndpointEmptyArraysNotNull(t *testing.T) {
	h := inbox.NewHandler(inbox.NewService(newFakeStore()))
	res := serve(t, h, http.MethodGet, "/inbox/overview", "")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", res.Code, res.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(res.Body.Bytes(), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, field := range []string{"by_mailbox", "by_reply_class"} {
		if string(raw[field]) != "[]" {
			t.Errorf("%s = %s, want []", field, raw[field])
		}
	}
}

func TestOverviewEndpointRejectsImplausibleTimezoneOffset(t *testing.T) {
	h := inbox.NewHandler(inbox.NewService(newFakeStore()))
	for _, tz := range []string{"9999", "-9999", "abc"} {
		res := serve(t, h, http.MethodGet, "/inbox/overview?tz_offset="+tz, "")
		if res.Code != http.StatusBadRequest {
			t.Errorf("tz_offset=%s: status = %d, want 400", tz, res.Code)
		}
	}
}

// A viewer's offset must shift the day boundary the counts are measured
// against — the whole reason the offset is a parameter rather than the
// server's own zone.
func TestOverviewEndpointHonorsTimezoneOffset(t *testing.T) {
	store := newFakeStore()
	h := inbox.NewHandler(inbox.NewService(store))

	res := serve(t, h, http.MethodGet, "/inbox/overview?tz_offset=600", "")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	atPlusTen := store.lastOverviewWindow.TodayStart

	res = serve(t, h, http.MethodGet, "/inbox/overview?tz_offset=-480", "")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	atMinusEight := store.lastOverviewWindow.TodayStart

	if atPlusTen.Equal(atMinusEight) {
		t.Errorf("today-start is %v for both +10:00 and -08:00; the offset was ignored", atPlusTen)
	}
	// The week boundary must land on a Monday in the viewer's own zone.
	if wd := store.lastOverviewWindow.WeekStart.Weekday(); wd != time.Monday {
		t.Errorf("WeekStart falls on %v, want Monday", wd)
	}
}

// Exact boundaries at a fixed instant, which is the whole reason
// overviewWindowAt takes `now` as a parameter. A weaker "WeekStart is a
// Monday" assertion passes for an off-by-seven-days implementation too.
func TestOverviewWindowBoundaries(t *testing.T) {
	tests := []struct {
		name          string
		now           time.Time
		offsetMinutes int
		wantToday     string
		wantWeek      string
	}{
		{
			// A Wednesday: the week starts two days earlier.
			name:      "midweek in UTC",
			now:       time.Date(2026, 8, 26, 15, 30, 0, 0, time.UTC),
			wantToday: "2026-08-26T00:00:00Z",
			wantWeek:  "2026-08-24T00:00:00Z",
		},
		{
			// Sunday is Weekday()==0; a naive implementation would treat it as
			// the start of the week and return the 30th instead of the 24th.
			name:      "Sunday resolves back to the preceding Monday",
			now:       time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
			wantToday: "2026-08-30T00:00:00Z",
			wantWeek:  "2026-08-24T00:00:00Z",
		},
		{
			name:      "Monday is its own week start",
			now:       time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC),
			wantToday: "2026-08-24T00:00:00Z",
			wantWeek:  "2026-08-24T00:00:00Z",
		},
		{
			// 23:30 UTC on the 26th is already 09:30 on the 27th at +10:00, so
			// the viewer's "today" is a day ahead of the server's.
			name:          "a positive offset can put the viewer into the next day",
			now:           time.Date(2026, 8, 26, 23, 30, 0, 0, time.UTC),
			offsetMinutes: 600,
			wantToday:     "2026-08-27T00:00:00+10:00",
			wantWeek:      "2026-08-24T00:00:00+10:00",
		},
		{
			// 02:00 UTC on the 27th is still 18:00 on the 26th at -08:00.
			name:          "a negative offset can keep the viewer in the previous day",
			now:           time.Date(2026, 8, 27, 2, 0, 0, 0, time.UTC),
			offsetMinutes: -480,
			wantToday:     "2026-08-26T00:00:00-08:00",
			wantWeek:      "2026-08-24T00:00:00-08:00",
		},
		{
			// Crossing a month boundary backwards: Tue 1 Sep 2026's week began
			// in August.
			name:      "a week start may fall in the previous month",
			now:       time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC),
			wantToday: "2026-09-01T00:00:00Z",
			wantWeek:  "2026-08-31T00:00:00Z",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := inbox.OverviewWindowAt(tc.now, tc.offsetMinutes)
			if gotToday := got.TodayStart.Format(time.RFC3339); gotToday != tc.wantToday {
				t.Errorf("TodayStart = %s, want %s", gotToday, tc.wantToday)
			}
			if gotWeek := got.WeekStart.Format(time.RFC3339); gotWeek != tc.wantWeek {
				t.Errorf("WeekStart = %s, want %s", gotWeek, tc.wantWeek)
			}
			if got.WeekStart.After(got.TodayStart) {
				t.Errorf("WeekStart %v is after TodayStart %v", got.WeekStart, got.TodayStart)
			}
		})
	}
}

func TestListThreadsScopeMapsToFilter(t *testing.T) {
	tests := []struct {
		scope  string
		assert func(*testing.T, inbox.ListFilter)
	}{
		{"", func(t *testing.T, f inbox.ListFilter) {
			if f.UnreadOnly || f.AwaitingReplyOnly || f.SinceLastMessageAt != nil {
				t.Errorf("absent scope set a filter: %+v", f)
			}
		}},
		{"all", func(t *testing.T, f inbox.ListFilter) {
			if f.UnreadOnly || f.AwaitingReplyOnly || f.SinceLastMessageAt != nil {
				t.Errorf("scope=all set a filter: %+v", f)
			}
		}},
		{"unread", func(t *testing.T, f inbox.ListFilter) {
			if !f.UnreadOnly {
				t.Error("scope=unread did not set UnreadOnly")
			}
		}},
		{"awaiting_reply", func(t *testing.T, f inbox.ListFilter) {
			if !f.AwaitingReplyOnly {
				t.Error("scope=awaiting_reply did not set AwaitingReplyOnly")
			}
		}},
		{"today", func(t *testing.T, f inbox.ListFilter) {
			if f.SinceLastMessageAt == nil {
				t.Error("scope=today did not set SinceLastMessageAt")
			}
		}},
		{"this_week", func(t *testing.T, f inbox.ListFilter) {
			if f.SinceLastMessageAt == nil {
				t.Fatal("scope=this_week did not set SinceLastMessageAt")
			}
			if wd := f.SinceLastMessageAt.Weekday(); wd != time.Monday {
				t.Errorf("this_week starts on %v, want Monday", wd)
			}
		}},
	}
	for _, tc := range tests {
		t.Run("scope="+tc.scope, func(t *testing.T) {
			store := newFakeStore()
			h := inbox.NewHandler(inbox.NewService(store))
			res := serve(t, h, http.MethodGet, "/inbox/threads?scope="+tc.scope, "")
			if res.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (%s)", res.Code, res.Body.String())
			}
			tc.assert(t, store.lastListFilter)
		})
	}
}

// An unknown scope must 400 rather than silently return the unscoped inbox,
// which would show a list that contradicts the scope the operator clicked.
func TestListThreadsRejectsUnknownScope(t *testing.T) {
	h := inbox.NewHandler(inbox.NewService(newFakeStore()))
	res := serve(t, h, http.MethodGet, "/inbox/threads?scope=nonsense", "")
	if res.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (%s)", res.Code, res.Body.String())
	}
}

// A scope and the keyset cursor must coexist: paging deep into "today" needs
// the scope's lower bound AND the cursor's upper bound at once.
func TestListThreadsScopeCombinesWithCursor(t *testing.T) {
	store := newFakeStore()
	h := inbox.NewHandler(inbox.NewService(store))
	at := time.Now().UTC().Format(time.RFC3339)
	target := "/inbox/threads?scope=today&before_last_message_at=" + at + "&before_id=" + uuid.New().String()

	res := serve(t, h, http.MethodGet, target, "")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", res.Code, res.Body.String())
	}
	got := store.lastListFilter
	if got.SinceLastMessageAt == nil {
		t.Error("scope's lower bound was dropped when a cursor was present")
	}
	if got.BeforeLastMessageAt == nil || got.BeforeID == nil {
		t.Error("cursor was dropped when a scope was present")
	}
}
