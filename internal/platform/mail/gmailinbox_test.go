package mail

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	gmail "google.golang.org/api/gmail/v1"
)

// gmailReplyRAW is a plain in-thread reply; gmailBounceRAW is a multipart/report
// DSN. Both are decoded RFC822 bytes exactly as messages.get(format=RAW) would
// yield after base64url decoding, so the parse path is identical to the IMAP
// reader's.
const gmailReplyRAW = "From: alice@example.com\r\n" +
	"To: rep@example.com\r\n" +
	"Subject: Re: Hello\r\n" +
	"In-Reply-To: <root@inroad>\r\n" +
	"Message-ID: <reply1@example.com>\r\n" +
	"\r\n" +
	"Sounds good.\r\n"

const gmailBounceRAW = "From: mailer-daemon@example.com\r\n" +
	"To: rep@example.com\r\n" +
	"Subject: Delivery Status Notification (Failure)\r\n" +
	"Content-Type: multipart/report; report-type=delivery-status; boundary=\"b0\"\r\n" +
	"\r\n" +
	"--b0\r\n" +
	"Content-Type: message/delivery-status\r\n" +
	"\r\n" +
	"Reporting-MTA: dns; mail.example.com\r\n" +
	"\r\n" +
	"Final-Recipient: rfc822; nobody@example.com\r\n" +
	"Status: 5.1.1\r\n" +
	"\r\n" +
	"--b0--\r\n"

// stubNoService is the newServiceFn seam stub: it skips building a real Gmail
// service (the history/get/profile stubs ignore srv anyway), keeping every test
// network-free.
func stubNoService(context.Context, string) (*gmail.Service, error) { return nil, nil }

// TestGmailReaderIncrementalParsesAndAdvancesCursor drives the incremental
// (history) path via the stub seam: two added messages (a reply and a bounce
// DSN) are fetched RAW and parsed the same way the IMAP reader builds an
// InboundMessage. It asserts the parsed headers/content-type survive and the
// new cursor is the resume cursor from the history seam — network-free.
func TestGmailReaderIncrementalParsesAndAdvancesCursor(t *testing.T) {
	raws := map[string][]byte{
		"m1": []byte(gmailReplyRAW),
		"m2": []byte(gmailBounceRAW),
	}
	var sawStart string
	g := &GmailReader{
		newServiceFn: stubNoService,
		historyFn: func(_ context.Context, _ *gmail.Service, startHistoryID string, _ int) ([]string, string, error) {
			sawStart = startHistoryID
			return []string{"m1", "m2"}, "998877", nil
		},
		getFn: func(_ context.Context, _ *gmail.Service, id string) ([]byte, []string, error) {
			return raws[id], nil, nil
		},
	}

	msgs, cursor, err := g.Fetch(context.Background(), "tok", "1000", 200)
	if err != nil {
		t.Fatal(err)
	}
	if sawStart != "1000" {
		t.Fatalf("history start = %q, want the passed cursor 1000", sawStart)
	}
	if cursor != "998877" {
		t.Fatalf("new cursor = %q, want the history resume cursor 998877", cursor)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	// Reply: header parsed, In-Reply-To preserved for the reply matcher.
	if got := msgs[0].Header.Get("From"); got != "alice@example.com" {
		t.Fatalf("reply From = %q, want alice@example.com", got)
	}
	if got := msgs[0].Header.Get("In-Reply-To"); got != "<root@inroad>" {
		t.Fatalf("reply In-Reply-To = %q, want <root@inroad>", got)
	}
	// Bounce: Content-Type carries multipart/report so ParseDSN can classify it,
	// and the delivery-status body is preserved after the outer header.
	if ct := msgs[1].ContentType; !strings.HasPrefix(ct, "multipart/report") {
		t.Fatalf("bounce ContentType = %q, want multipart/report...", ct)
	}
	if !strings.Contains(string(msgs[1].Body), "Status: 5.1.1") {
		t.Fatalf("bounce body did not preserve the delivery-status part: %q", msgs[1].Body)
	}
}

// TestGmailReaderFirstPollBaselinesAndReturnsNoMessages drives the first-poll
// (sinceHistoryID=="") path: it ONLY reads the profile's current historyId as
// the baseline cursor and processes nothing — mirroring the IMAP reader, which
// never treats a mailbox's pre-connect inbox as a flood of replies/bounces.
func TestGmailReaderFirstPollBaselinesAndReturnsNoMessages(t *testing.T) {
	g := &GmailReader{
		newServiceFn: stubNoService,
		profileFn: func(_ context.Context, _ *gmail.Service) (string, error) {
			return "555000", nil
		},
		getFn: func(_ context.Context, _ *gmail.Service, _ string) ([]byte, []string, error) {
			t.Fatal("first poll must not fetch any message bodies")
			return nil, nil, nil
		},
	}

	msgs, cursor, err := g.Fetch(context.Background(), "tok", "", 200)
	if err != nil {
		t.Fatal(err)
	}
	if cursor != "555000" {
		t.Fatalf("first-poll cursor = %q, want the profile historyId 555000", cursor)
	}
	if len(msgs) != 0 {
		t.Fatalf("first poll must return no messages, got %d", len(msgs))
	}
}

// TestGmailReaderEmptyWindowAdvancesCursor locks the monotonic-advance
// guarantee: a window with no new messages still advances the cursor to the
// value the history seam reports, so the same empty range is never re-scanned.
func TestGmailReaderEmptyWindowAdvancesCursor(t *testing.T) {
	g := &GmailReader{
		newServiceFn: stubNoService,
		historyFn: func(_ context.Context, _ *gmail.Service, _ string, _ int) ([]string, string, error) {
			return []string{}, "999", nil
		},
		getFn: func(_ context.Context, _ *gmail.Service, _ string) ([]byte, []string, error) {
			t.Fatal("empty window must not fetch any bodies")
			return nil, nil, nil
		},
	}
	msgs, cursor, err := g.Fetch(context.Background(), "tok", "500", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("expected no messages for an empty window, got %d", len(msgs))
	}
	if cursor != "999" {
		t.Fatalf("empty-window cursor = %q, want 999 (must still advance)", cursor)
	}
}

// TestGmailReaderHistoryExpiredReBaselines proves an aged-out cursor (404 from
// history.list, surfaced as errGmailHistoryExpired) re-baselines to the profile
// historyId and returns no messages, instead of wedging the mailbox on a cursor
// that can never succeed. Mirrors the IMAP UIDVALIDITY-reset re-baseline.
func TestGmailReaderHistoryExpiredReBaselines(t *testing.T) {
	g := &GmailReader{
		newServiceFn: stubNoService,
		historyFn: func(_ context.Context, _ *gmail.Service, _ string, _ int) ([]string, string, error) {
			return nil, "", errGmailHistoryExpired
		},
		profileFn: func(_ context.Context, _ *gmail.Service) (string, error) {
			return "700000", nil
		},
		getFn: func(_ context.Context, _ *gmail.Service, _ string) ([]byte, []string, error) {
			t.Fatal("a re-baseline must not fetch any bodies")
			return nil, nil, nil
		},
	}
	msgs, cursor, err := g.Fetch(context.Background(), "tok", "1", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("re-baseline must return no messages, got %d", len(msgs))
	}
	if cursor != "700000" {
		t.Fatalf("re-baseline cursor = %q, want the fresh profile historyId 700000", cursor)
	}
}

// The label set is the ONLY place a tab is observable, and the mapping from it to
// a tab name decides a reputation signal — so every arm is pinned here, pure and
// network-free.
//
// Two arms are the ones that matter. CATEGORY_PERSONAL is Gmail's label for the
// PRIMARY tab, so it must name no tab: treating it as one would report a ~100%
// tabbed rate for exactly the mailboxes whose mail lands where we want it. An
// UNKNOWN category, by contrast, counts as a tab — it is a category, and recording
// it as the primary inbox is the optimistic direction, which is the wrong one for a
// reputation signal.
func TestGmailPlacementCategoryNamesTheTabOrNothing(t *testing.T) {
	cases := []struct {
		name   string
		labels []string
		want   string
	}{
		{"promotions", []string{"INBOX", "CATEGORY_PROMOTIONS"}, "promotions"},
		{"updates", []string{"INBOX", "CATEGORY_UPDATES"}, "updates"},
		{"social", []string{"INBOX", "CATEGORY_SOCIAL"}, "social"},
		{"forums", []string{"INBOX", "CATEGORY_FORUMS"}, "forums"},
		{"the primary tab is not a tab", []string{"INBOX", "CATEGORY_PERSONAL"}, ""},
		{"inbox only", []string{"INBOX"}, ""},
		{"no labels at all", nil, ""},
		{"an unknown category still counts as a tab", []string{"INBOX", "CATEGORY_NEWSLETTERS"}, "newsletters"},
		{"spam wins over a category", []string{"SPAM", "CATEGORY_PROMOTIONS"}, ""},
		{"spam wins even listed after the category", []string{"CATEGORY_PROMOTIONS", "SPAM"}, ""},
		{"unread and starred are not categories", []string{"INBOX", "UNREAD", "STARRED"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := gmailPlacementCategory(tc.labels); got != tc.want {
				t.Errorf("gmailPlacementCategory(%v) = %q, want %q", tc.labels, got, tc.want)
			}
		})
	}
}

// The labels are on the Message the reader ALREADY fetches, and were discarded one
// line before they became useful. This is the guard that they now reach the
// InboundMessage the poller sees — without it the mapping above is dead code.
func TestGmailReaderCarriesTheTabThroughToTheMessage(t *testing.T) {
	g := &GmailReader{
		newServiceFn: stubNoService,
		historyFn: func(_ context.Context, _ *gmail.Service, _ string, _ int) ([]string, string, error) {
			return []string{"tabbed", "primary"}, "42", nil
		},
		getFn: func(_ context.Context, _ *gmail.Service, id string) ([]byte, []string, error) {
			if id == "tabbed" {
				return []byte(gmailReplyRAW), []string{"INBOX", "CATEGORY_PROMOTIONS"}, nil
			}
			return []byte(gmailReplyRAW), []string{"INBOX", "CATEGORY_PERSONAL"}, nil
		},
	}

	msgs, _, err := g.Fetch(context.Background(), "tok", "1", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if got := msgs[0].PlacementCategory; got != "promotions" {
		t.Errorf("promotions message PlacementCategory = %q, want promotions", got)
	}
	if got := msgs[1].PlacementCategory; got != "" {
		t.Errorf("primary-tab message PlacementCategory = %q, want empty (it landed in the primary inbox)", got)
	}
}

// The labels ride on the same API Message the RAW fetch was already making, and
// were dropped here. Everything but the network call is exercised, because "return
// the labels the API gave us" is precisely the line whose absence made every
// Promotions landing look like a primary-inbox one.
func TestGmailMessageRAWKeepsTheLabelsAndRejectsUndecodableBytes(t *testing.T) {
	m := &gmail.Message{
		Raw:      base64.URLEncoding.EncodeToString([]byte(gmailReplyRAW)),
		LabelIds: []string{"INBOX", "CATEGORY_UPDATES"},
	}
	raw, labels, err := gmailMessageRAW(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != gmailReplyRAW {
		t.Errorf("decoded raw = %q, want the original message", raw)
	}
	if len(labels) != 2 || labels[1] != "CATEGORY_UPDATES" {
		t.Errorf("labels = %v, want the API's labelIds carried through", labels)
	}

	// A body that will not decode is an error, never a silently empty message: the
	// poll must retry rather than record a warmup receipt for mail it cannot read.
	if _, _, err := gmailMessageRAW(&gmail.Message{Raw: "!! not base64 !!"}); err == nil {
		t.Fatal("an undecodable body returned no error")
	}
}

// The SPAM scan runs over messages that are not in the inbox at all, so no tab
// applies to them however Gmail also categorised them. Asserted at the reader
// because that is where a category could leak onto a spam placement.
func TestGmailReaderReportsNoTabForSpam(t *testing.T) {
	g := &GmailReader{
		newServiceFn: stubNoService,
		spamListFn: func(_ context.Context, _ *gmail.Service, _ int) ([]string, error) {
			return []string{"s1"}, nil
		},
		getFn: func(_ context.Context, _ *gmail.Service, _ string) ([]byte, []string, error) {
			return []byte(gmailReplyRAW), []string{"SPAM", "CATEGORY_PROMOTIONS"}, nil
		},
	}

	msgs, err := g.FetchSpam(context.Background(), "tok", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 spam message, got %d", len(msgs))
	}
	if got := msgs[0].PlacementCategory; got != "" {
		t.Errorf("spam message PlacementCategory = %q, want empty: a spam-foldered message is in no tab", got)
	}
}

// TestGmailReaderRejectsNonPositiveMaxN mirrors NetInboxReader.Fetch's guard.
func TestGmailReaderRejectsNonPositiveMaxN(t *testing.T) {
	g := NewGmailReader()
	if _, _, err := g.Fetch(context.Background(), "tok", "1", 0); err == nil {
		t.Fatal("expected an error for maxN <= 0")
	}
}

// TestCollectHistoryResumesMidWindowWhenTruncated is the FIX-2 guard: when more
// messageAdded records exist than maxN allows, the cursor must NOT jump to the
// mailbox's current top (which would skip the untaken remainder forever) — it
// pins to the LAST CONSUMED record's Id so the next poll resumes there.
func TestCollectHistoryResumesMidWindowWhenTruncated(t *testing.T) {
	records := []*gmail.History{
		{Id: 101, MessagesAdded: []*gmail.HistoryMessageAdded{{Message: &gmail.Message{Id: "a"}}}},
		{Id: 102, MessagesAdded: []*gmail.HistoryMessageAdded{{Message: &gmail.Message{Id: "b"}}}},
		{Id: 103, MessagesAdded: []*gmail.HistoryMessageAdded{{Message: &gmail.Message{Id: "c"}}}},
	}
	// maxN=2: consume records 101 and 102 (ids a,b), stop before 103.
	ids, cursor := collectHistory(records, 999 /* respHistoryID (current top) */, "", 2)
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("expected ids [a b], got %v", ids)
	}
	if cursor != "102" {
		t.Fatalf("truncated cursor = %q, want 102 (last consumed record, NOT the top 999)", cursor)
	}
}

// TestCollectHistoryDrainedAdvancesToTop proves the fully-consumed case: when
// every record fits within maxN and there is no further page, the cursor jumps
// to the mailbox's current top so the window is never re-scanned.
func TestCollectHistoryDrainedAdvancesToTop(t *testing.T) {
	records := []*gmail.History{
		{Id: 101, MessagesAdded: []*gmail.HistoryMessageAdded{{Message: &gmail.Message{Id: "a"}}}},
		{Id: 102, MessagesAdded: []*gmail.HistoryMessageAdded{{Message: &gmail.Message{Id: "b"}}}},
	}
	ids, cursor := collectHistory(records, 999, "", 200)
	if len(ids) != 2 {
		t.Fatalf("expected 2 ids, got %v", ids)
	}
	if cursor != "999" {
		t.Fatalf("drained cursor = %q, want the current top 999", cursor)
	}
}

// TestCollectHistoryUnfollowedPageResumesFromLastRecord proves that even when
// this page fits within maxN, a NextPageToken means more records exist that we
// did not follow — so the cursor pins to the last consumed record, not the top.
func TestCollectHistoryUnfollowedPageResumesFromLastRecord(t *testing.T) {
	records := []*gmail.History{
		{Id: 101, MessagesAdded: []*gmail.HistoryMessageAdded{{Message: &gmail.Message{Id: "a"}}}},
	}
	ids, cursor := collectHistory(records, 999, "next-page", 200)
	if len(ids) != 1 {
		t.Fatalf("expected 1 id, got %v", ids)
	}
	if cursor != "101" {
		t.Fatalf("paginated cursor = %q, want 101 (last consumed, more pages pending)", cursor)
	}
}
