package mail

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The graph delta reply/bounce fixtures reuse the same decoded RFC822 bytes as
// the Gmail reader's tests (gmailReplyRAW / gmailBounceRAW): the Graph $value
// endpoint returns raw MIME directly (no base64 decode step), so the bytes that
// reach parseInbound are byte-identical to what the Gmail path yields after its
// base64url decode. Sharing the fixtures locks that equivalence in.

// graphCursorBase is a host-pinned (graph.microsoft.com) stored-cursor URL: the
// incremental Fetch path rejects any cursor not on Graph's host before dialing
// it, so every test that exercises the delta path must resume from a pinned URL.
const graphCursorBase = "https://graph.microsoft.com/v1.0/me/mailFolders('inbox')/messages/delta"

// TestGraphReaderFirstPollBaselinesAndReturnsNoMessages drives the first-poll
// (sinceCursor=="") path: it ONLY reads the baseline deltaLink ($deltatoken=
// latest) and processes nothing — mirroring GmailReader's first-poll baseline,
// which never treats a mailbox's pre-connect inbox as a flood of replies/bounces.
func TestGraphReaderFirstPollBaselinesAndReturnsNoMessages(t *testing.T) {
	g := &GraphReader{
		baselineFn: func(_ context.Context, _ string) (string, error) {
			return "https://graph/delta?$deltatoken=LATEST", nil
		},
		getRawFn: func(_ context.Context, _, _ string) ([]byte, error) {
			t.Fatal("first poll must not fetch any message bodies")
			return nil, nil
		},
	}

	msgs, cursor, err := g.Fetch(context.Background(), "tok", "", 200)
	if err != nil {
		t.Fatal(err)
	}
	if cursor != "https://graph/delta?$deltatoken=LATEST" {
		t.Fatalf("first-poll cursor = %q, want the baseline deltaLink", cursor)
	}
	if len(msgs) != 0 {
		t.Fatalf("first poll must return no messages, got %d", len(msgs))
	}
}

// TestGraphReaderIncrementalParsesAndAdvancesCursor drives the incremental
// (delta) path via the seam: two changed messages (a reply and a bounce DSN)
// are fetched RAW from /$value and parsed the same way the IMAP/Gmail readers
// build an InboundMessage. It asserts the delta URL is the stored cursor, the
// parsed headers/content-type survive, and the new cursor is the deltaLink
// (a fully-drained final page) — network-free.
func TestGraphReaderIncrementalParsesAndAdvancesCursor(t *testing.T) {
	raws := map[string][]byte{
		"m1": []byte(gmailReplyRAW),
		"m2": []byte(gmailBounceRAW),
	}
	var sawURL string
	g := &GraphReader{
		deltaFn: func(_ context.Context, _, u string) ([]deltaMsg, string, string, error) {
			sawURL = u
			return []deltaMsg{{ID: "m1"}, {ID: "m2"}}, "", graphCursorBase + "?$deltatoken=NEXT", nil
		},
		getRawFn: func(_ context.Context, _, id string) ([]byte, error) {
			return raws[id], nil
		},
	}

	msgs, cursor, err := g.Fetch(context.Background(), "tok", graphCursorBase+"?$skiptoken=CUR", 200)
	if err != nil {
		t.Fatal(err)
	}
	if sawURL != graphCursorBase+"?$skiptoken=CUR" {
		t.Fatalf("delta URL = %q, want the passed cursor", sawURL)
	}
	if cursor != graphCursorBase+"?$deltatoken=NEXT" {
		t.Fatalf("new cursor = %q, want the deltaLink", cursor)
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

// TestGraphReaderMorePagesCheckpointsNextLink proves that when the delta
// response carries an @odata.nextLink (more pages pending), the new cursor is
// that nextLink so the next poll resumes at the next page rather than skipping
// it — the URL analogue of Gmail's mid-window resume.
func TestGraphReaderMorePagesCheckpointsNextLink(t *testing.T) {
	g := &GraphReader{
		deltaFn: func(_ context.Context, _, _ string) ([]deltaMsg, string, string, error) {
			return []deltaMsg{{ID: "m1"}}, graphCursorBase + "?$skiptoken=PAGE2", graphCursorBase + "?$deltatoken=END", nil
		},
		getRawFn: func(_ context.Context, _, _ string) ([]byte, error) {
			return []byte(gmailReplyRAW), nil
		},
	}
	msgs, cursor, err := g.Fetch(context.Background(), "tok", graphCursorBase+"?$skiptoken=CUR", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if cursor != graphCursorBase+"?$skiptoken=PAGE2" {
		t.Fatalf("cursor = %q, want the nextLink (more pages pending)", cursor)
	}
}

// TestGraphReaderSkipsDeletions proves @removed entries (deletions/moves out of
// the folder) are ignored: only new/changed messages are fetched and classified.
func TestGraphReaderSkipsDeletions(t *testing.T) {
	var fetched []string
	g := &GraphReader{
		deltaFn: func(_ context.Context, _, _ string) ([]deltaMsg, string, string, error) {
			return []deltaMsg{
				{ID: "gone", Removed: json.RawMessage(`{"reason":"deleted"}`)},
				{ID: "live"},
			}, "", graphCursorBase + "?$deltatoken=END", nil
		},
		getRawFn: func(_ context.Context, _, id string) ([]byte, error) {
			fetched = append(fetched, id)
			return []byte(gmailReplyRAW), nil
		},
	}
	msgs, _, err := g.Fetch(context.Background(), "tok", graphCursorBase+"?$skiptoken=CUR", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(fetched) != 1 || fetched[0] != "live" {
		t.Fatalf("expected only the non-removed id fetched, got %v", fetched)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 classified message, got %d", len(msgs))
	}
}

// TestGraphReaderDeltaExpiredReBaselines proves an aged-out delta token (410
// Gone / resync-required 400, surfaced as errGraphDeltaExpired) re-baselines to
// a fresh deltaLink and returns no messages, instead of wedging the mailbox on
// a cursor that can never succeed. Mirrors the Gmail 404 re-baseline.
func TestGraphReaderDeltaExpiredReBaselines(t *testing.T) {
	g := &GraphReader{
		deltaFn: func(_ context.Context, _, _ string) ([]deltaMsg, string, string, error) {
			return nil, "", "", errGraphDeltaExpired
		},
		baselineFn: func(_ context.Context, _ string) (string, error) {
			return graphCursorBase + "?$deltatoken=FRESH", nil
		},
		getRawFn: func(_ context.Context, _, _ string) ([]byte, error) {
			t.Fatal("a re-baseline must not fetch any bodies")
			return nil, nil
		},
	}
	msgs, cursor, err := g.Fetch(context.Background(), "tok", graphCursorBase+"?$skiptoken=OLD", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("re-baseline must return no messages, got %d", len(msgs))
	}
	if cursor != graphCursorBase+"?$deltatoken=FRESH" {
		t.Fatalf("re-baseline cursor = %q, want the fresh deltaLink", cursor)
	}
}

// TestGraphReaderRejectsOffHostCursor is the SSRF guard: a stored cursor whose
// host is NOT graph.microsoft.com must never be dialed with the mailbox bearer.
// Fetch must re-baseline (via the fixed $deltatoken=latest URL) without touching
// deltaFn or getRawFn, so a corrupted/hostile stored value can't exfiltrate the
// access token to another host.
func TestGraphReaderRejectsOffHostCursor(t *testing.T) {
	g := &GraphReader{
		deltaFn: func(_ context.Context, _, _ string) ([]deltaMsg, string, string, error) {
			t.Fatal("an off-host cursor must NOT be dialed (token exfil)")
			return nil, "", "", nil
		},
		baselineFn: func(_ context.Context, _ string) (string, error) {
			return graphCursorBase + "?$deltatoken=FRESH", nil
		},
		getRawFn: func(_ context.Context, _, _ string) ([]byte, error) {
			t.Fatal("an off-host cursor must NOT fetch any bodies")
			return nil, nil
		},
	}
	msgs, cursor, err := g.Fetch(context.Background(), "tok", "https://evil.example.com/v1.0/me/messages/delta?$skiptoken=STOLEN", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("off-host cursor must return no messages, got %d", len(msgs))
	}
	if cursor != graphCursorBase+"?$deltatoken=FRESH" {
		t.Fatalf("off-host cursor must re-baseline, got cursor %q", cursor)
	}
}

// TestGraphReaderRejectsNonPositiveMaxN mirrors GmailReader.Fetch's guard.
func TestGraphReaderRejectsNonPositiveMaxN(t *testing.T) {
	g := NewGraphReader()
	if _, _, err := g.Fetch(context.Background(), "tok", graphCursorBase+"?$skiptoken=CUR", 0); err == nil {
		t.Fatal("expected an error for maxN <= 0")
	}
}

// TestCollectDeltaDrainedAdvancesToDeltaLink proves the fully-consumed case:
// every message fits within maxN and there is no nextLink, so the cursor jumps
// to the @odata.deltaLink — the next poll fetches only what changed since.
func TestCollectDeltaDrainedAdvancesToDeltaLink(t *testing.T) {
	value := []deltaMsg{{ID: "a"}, {ID: "b"}}
	ids, cursor := collectDelta(value, "", "deltaX", 200)
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("expected ids [a b], got %v", ids)
	}
	if cursor != "deltaX" {
		t.Fatalf("drained cursor = %q, want the deltaLink deltaX", cursor)
	}
}

// TestCollectDeltaTruncatedCheckpointsNextLink is the resume guard: when more
// messages exist than maxN allows, the cursor must be the nextLink (Graph pages
// a large delta, so an over-maxN page always carries one) — never the deltaLink,
// which would advance past the unconsumed remainder.
func TestCollectDeltaTruncatedCheckpointsNextLink(t *testing.T) {
	value := []deltaMsg{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	// maxN=2: consume a and b, stop before c.
	ids, cursor := collectDelta(value, "nextX", "deltaX", 2)
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("expected ids [a b], got %v", ids)
	}
	if cursor != "nextX" {
		t.Fatalf("truncated cursor = %q, want nextX (NOT the deltaLink)", cursor)
	}
}

// TestCollectDeltaTruncatedNoNextLinkConsumesAllToDeltaLink is the drop-bug
// guard (FIX 1): when ids exceed maxN but there is NO nextLink, the page is
// terminal — there is nothing after it to resume from, so the maxN break is NOT
// honored. All entries are consumed and the cursor is the NON-EMPTY deltaLink,
// never "" (an empty cursor would be misread as a first poll and re-baselined
// over, silently skipping the unconsumed remainder).
func TestCollectDeltaTruncatedNoNextLinkConsumesAllToDeltaLink(t *testing.T) {
	value := []deltaMsg{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	// maxN=2, but no nextLink: consume ALL three, cursor = deltaLink.
	ids, cursor := collectDelta(value, "", "deltaX", 2)
	if len(ids) != 3 || ids[0] != "a" || ids[1] != "b" || ids[2] != "c" {
		t.Fatalf("expected all ids [a b c] consumed on a terminal page, got %v", ids)
	}
	if cursor == "" {
		t.Fatal("cursor must never be empty (would re-baseline and drop the remainder)")
	}
	if cursor != "deltaX" {
		t.Fatalf("terminal-page cursor = %q, want the deltaLink deltaX", cursor)
	}
}

// TestCollectDeltaMorePagesCheckpointsNextLink proves that even when this page
// fits within maxN, a nextLink means more pages exist — so the cursor pins to
// the nextLink, not the deltaLink.
func TestCollectDeltaMorePagesCheckpointsNextLink(t *testing.T) {
	value := []deltaMsg{{ID: "a"}}
	ids, cursor := collectDelta(value, "nextX", "deltaX", 200)
	if len(ids) != 1 || ids[0] != "a" {
		t.Fatalf("expected ids [a], got %v", ids)
	}
	if cursor != "nextX" {
		t.Fatalf("paginated cursor = %q, want nextX (more pages pending)", cursor)
	}
}

// TestCollectDeltaEmptyAdvancesToDeltaLink locks the monotonic-advance
// guarantee: an empty delta page still advances the cursor to the deltaLink so
// the same range is never re-scanned.
func TestCollectDeltaEmptyAdvancesToDeltaLink(t *testing.T) {
	ids, cursor := collectDelta(nil, "", "deltaX", 200)
	if len(ids) != 0 {
		t.Fatalf("expected no ids, got %v", ids)
	}
	if cursor != "deltaX" {
		t.Fatalf("empty cursor = %q, want the deltaLink deltaX", cursor)
	}
}

// TestCollectDeltaSkipsRemoved proves @removed entries never become ids and
// don't count toward the maxN budget for the messages that do matter.
func TestCollectDeltaSkipsRemoved(t *testing.T) {
	value := []deltaMsg{
		{ID: "gone", Removed: json.RawMessage(`{}`)},
		{ID: "a"},
	}
	ids, cursor := collectDelta(value, "", "deltaX", 200)
	if len(ids) != 1 || ids[0] != "a" {
		t.Fatalf("expected only the non-removed id, got %v", ids)
	}
	if cursor != "deltaX" {
		t.Fatalf("cursor = %q, want deltaX", cursor)
	}
}

// TestGraphResyncRequired covers the 400-error-code mapping that decides whether
// a stale-token 400 is re-baselined (resync) or surfaced as a hard error. Graph
// signals a resync with codes like syncStateNotFound / resyncRequired; any other
// code, or a malformed body, must NOT be treated as a resync.
func TestGraphResyncRequired(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"syncStateNotFound", `{"error":{"code":"syncStateNotFound","message":"x"}}`, true},
		{"resyncRequired", `{"error":{"code":"resyncRequired"}}`, true},
		{"mixed case syncState", `{"error":{"code":"SyncStateNotFound"}}`, true},
		{"unrelated 400 code", `{"error":{"code":"invalidRequest"}}`, false},
		{"malformed json", `{not json`, false},
		{"empty body", ``, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := graphResyncRequired(strings.NewReader(tc.body)); got != tc.want {
				t.Fatalf("graphResyncRequired(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}
