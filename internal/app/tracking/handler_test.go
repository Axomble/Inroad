package tracking

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/botfilter"
	"github.com/inroad/inroad/internal/platform/httpx"
	"github.com/inroad/inroad/internal/platform/track"
)

const testUA = "test-agent/1.0"

var testSecret = []byte("0123456789abcdef0123456789abcdef")

// testSentAt anchors every timing assertion: the fixture send went out here,
// and the handler's clock is pinned relative to it.
var testSentAt = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

// fakeStore is a no-database double for Store: sends holds the fixture of
// sends that "exist" (ResolveSend succeeds only for keys present here), and
// calls records every RecordEvent invocation so tests can assert exactly
// what (and how often) was recorded.
type fakeStore struct {
	sends map[uuid.UUID]Send
	calls []Event
	// prior is what PriorEvents returns, and priorErr the error it returns
	// with it — so a test can drive the ordering rules and the degraded path.
	prior    botfilter.Prior
	priorErr error
	// subnets records the subnet PriorEvents was asked about, proving the
	// burst query is skipped when there is no usable client IP.
	subnets []netip.Prefix
}

func (f *fakeStore) RecordEvent(_ context.Context, ev Event) error {
	f.calls = append(f.calls, ev)
	return nil
}

func (f *fakeStore) ResolveSend(_ context.Context, sendID uuid.UUID) (Send, bool) {
	s, ok := f.sends[sendID]
	if !ok {
		return Send{}, false
	}
	return s, true
}

func (f *fakeStore) PriorEvents(_ context.Context, _ uuid.UUID, subnet netip.Prefix, _ time.Time) (botfilter.Prior, error) {
	f.subnets = append(f.subnets, subnet)
	return f.prior, f.priorErr
}

// newTestHandler wires a Handler over a fakeStore seeded with one known
// send, mounted the same way cmd/inroad mounts it in production (under /t).
// The clock is pinned so the prefetch-window rule is driven by the offset a
// test chooses rather than by how long the test took to run.
func newTestHandler(t *testing.T) (http.Handler, *fakeStore, uuid.UUID) {
	t.Helper()
	return newTestHandlerAt(t, testSentAt.Add(time.Hour))
}

func newTestHandlerAt(t *testing.T, now time.Time) (http.Handler, *fakeStore, uuid.UUID) {
	t.Helper()
	sendID := uuid.New()
	store := &fakeStore{sends: map[uuid.UUID]Send{
		sendID: {WorkspaceID: uuid.New(), CampaignID: uuid.New(), SentAt: testSentAt},
	}}
	// No trusted proxies: the resolver takes RemoteAddr and ignores any
	// X-Forwarded-For, which is the production default and the safe one.
	h := NewHandler(NewService(testSecret, store, nil), httpx.NewClientIPResolver(nil))
	h.now = func() time.Time { return now }
	r := chi.NewRouter()
	r.Mount("/t", h.Routes())
	return r, store, sendID
}

// get issues a tracking request with a chosen User-Agent and source address.
func get(t *testing.T, r http.Handler, path, ua, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, http.NoBody)
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestOpenGIF_ValidToken_RecordsEventAndServesPixel(t *testing.T) {
	r, store, sendID := newTestHandler(t)
	tok := track.MakeOpenToken(testSecret, sendID.String())

	req := httptest.NewRequest(http.MethodGet, "/t/o/"+tok+".gif", http.NoBody)
	req.Header.Set("User-Agent", testUA)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/gif" {
		t.Errorf("Content-Type = %q, want image/gif", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	if w.Body.Len() == 0 {
		t.Error("expected a non-empty pixel body")
	}
	if len(store.calls) != 1 {
		t.Fatalf("recorded %d events, want 1", len(store.calls))
	}
	c := store.calls[0]
	if c.Kind != botfilter.KindOpen || c.SendID != sendID || c.UserAgent != testUA || c.URL != "" {
		t.Errorf("recorded event = %+v, want kind=open sendID=%s ua=%s url=\"\"", c, sendID, testUA)
	}
	if c.Verdict != botfilter.Human || c.Reason != botfilter.ReasonNone {
		t.Errorf("verdict = %v/%v, want human/none for an ordinary open an hour after the send", c.Verdict, c.Reason)
	}
}

func TestOpenGIF_InvalidToken_ServesPixelButRecordsNothing(t *testing.T) {
	r, store, _ := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/t/o/not-a-real-token.gif", http.NoBody)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the pixel must never fail, even for a bad token)", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/gif" {
		t.Errorf("Content-Type = %q, want image/gif", ct)
	}
	if len(store.calls) != 0 {
		t.Fatalf("recorded %d events for an invalid token, want 0", len(store.calls))
	}
}

func TestOpenGIF_UnknownSend_ServesPixelButRecordsNothing(t *testing.T) {
	r, store, _ := newTestHandler(t)
	tok := track.MakeOpenToken(testSecret, uuid.New().String()) // validly signed, no such send

	req := httptest.NewRequest(http.MethodGet, "/t/o/"+tok+".gif", http.NoBody)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if len(store.calls) != 0 {
		t.Fatalf("recorded %d events for an unknown send, want 0", len(store.calls))
	}
}

// A well-behaved click an hour after the send, with no recorded open, is the
// images-blocked reader: no pixel will ever fire for them, so this must stay a
// human click or the feature deletes the engagement of the most
// privacy-conscious recipients.
func TestClickRedirect_ValidToken_RecordsEventAndRedirects(t *testing.T) {
	r, store, sendID := newTestHandler(t)
	dest := "https://example.test/landing?utm_source=inroad"
	tok := track.MakeClickToken(testSecret, sendID.String(), dest)

	w := get(t, r, "/t/c/"+tok, testUA, "")

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != dest {
		t.Fatalf("Location = %q, want %q", loc, dest)
	}
	if len(store.calls) != 1 {
		t.Fatalf("recorded %d events, want 1", len(store.calls))
	}
	c := store.calls[0]
	if c.Kind != botfilter.KindClick || c.SendID != sendID || c.URL != dest || c.UserAgent != testUA {
		t.Errorf("recorded event = %+v, want kind=click sendID=%s url=%s ua=%s", c, sendID, dest, testUA)
	}
}

func TestClickRedirect_TamperedToken_404NoRedirectNoEvent(t *testing.T) {
	r, store, sendID := newTestHandler(t)
	tok := track.MakeClickToken(testSecret, sendID.String(), "https://example.test/landing")

	// Tamper deterministically: a token is base64url(payload)."."base64url(sig),
	// so flipping the final base64 char can leave the DECODED signature bytes
	// unchanged (non-canonical low bits) and still verify. Decode the signature
	// segment, flip a byte in the middle of the raw bytes, and re-encode — this
	// always changes the signature the HMAC is compared against, so a tampered
	// token can NEVER verify.
	dot := strings.LastIndexByte(tok, '.')
	if dot < 0 {
		t.Fatalf("token %q has no signature separator", tok)
	}
	sig, err := base64.RawURLEncoding.DecodeString(tok[dot+1:])
	if err != nil {
		t.Fatalf("decode signature segment: %v", err)
	}
	sig[len(sig)/2] ^= 0xFF
	tampered := tok[:dot+1] + base64.RawURLEncoding.EncodeToString(sig)

	req := httptest.NewRequest(http.MethodGet, "/t/c/"+tampered, http.NoBody)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "" {
		t.Fatalf("Location = %q, want no redirect", loc)
	}
	if len(store.calls) != 0 {
		t.Fatalf("recorded %d events for a tampered token, want 0", len(store.calls))
	}
}

// TestClickRedirect_UnsafeScheme_404NoRedirectNoEvent covers the token
// integrity vs. URL safety distinction: the HMAC proves the payload wasn't
// altered, but says nothing about whether the URL it names was ever safe to
// redirect to. A javascript: URL, however it was signed, must never 302.
func TestClickRedirect_UnsafeScheme_404NoRedirectNoEvent(t *testing.T) {
	r, store, sendID := newTestHandler(t)
	tok := track.MakeClickToken(testSecret, sendID.String(), "javascript:alert(1)")

	req := httptest.NewRequest(http.MethodGet, "/t/c/"+tok, http.NoBody)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "" {
		t.Fatalf("Location = %q, want no redirect", loc)
	}
	if len(store.calls) != 0 {
		t.Fatalf("recorded %d events for an unsafe scheme, want 0", len(store.calls))
	}
}

func TestClickRedirect_UnknownSend_404NoRedirectNoEvent(t *testing.T) {
	r, store, _ := newTestHandler(t)
	tok := track.MakeClickToken(testSecret, uuid.New().String(), "https://example.test/landing")

	req := httptest.NewRequest(http.MethodGet, "/t/c/"+tok, http.NoBody)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if len(store.calls) != 0 {
		t.Fatalf("recorded %d events for an unknown send, want 0", len(store.calls))
	}
}

// --- Bot / prefetch classification ---
//
// The load-bearing assertion in every case below is that a machine event is
// still RECORDED. Dropping it would make "N opens, M of them machine"
// unanswerable and would silently present a truncated count as the truth.

// A known proxy UA is classified machine and STORED.
func TestOpenGIF_KnownProxyUA_RecordsMachineEventAndStillServesPixel(t *testing.T) {
	r, store, sendID := newTestHandler(t)
	tok := track.MakeOpenToken(testSecret, sendID.String())
	const proxyUA = "Mozilla/5.0 (Windows NT 5.1; rv:11.0) Gecko Firefox/11.0 (via ggpht.com GoogleImageProxy)"

	w := get(t, r, "/t/o/"+tok+".gif", proxyUA, "")

	if w.Code != http.StatusOK || w.Body.Len() == 0 {
		t.Fatalf("status = %d, body = %d bytes; the pixel must be served regardless of the verdict", w.Code, w.Body.Len())
	}
	if len(store.calls) != 1 {
		t.Fatalf("recorded %d events, want 1 — a machine open must be STORED, never dropped", len(store.calls))
	}
	c := store.calls[0]
	if c.Verdict != botfilter.Machine || c.Reason != botfilter.ReasonProxyUserAgent {
		t.Errorf("verdict = %v/%v, want machine/proxy_user_agent", c.Verdict, c.Reason)
	}
	if c.SendID != sendID || c.Kind != botfilter.KindOpen {
		t.Errorf("recorded %+v, want an open for send %s", c, sendID)
	}
	if c.UserAgent != proxyUA {
		t.Errorf("user agent = %q, want the UA stored as observed", c.UserAgent)
	}
}

// A plain browser UA well after the send is the human baseline.
func TestOpenGIF_PlainBrowserUA_RecordsHumanEvent(t *testing.T) {
	r, store, sendID := newTestHandler(t)
	tok := track.MakeOpenToken(testSecret, sendID.String())
	const browserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.6 Safari/605.1.15"

	get(t, r, "/t/o/"+tok+".gif", browserUA, "")

	if len(store.calls) != 1 {
		t.Fatalf("recorded %d events, want 1", len(store.calls))
	}
	if c := store.calls[0]; c.Verdict != botfilter.Human || c.Reason != botfilter.ReasonNone {
		t.Errorf("verdict = %v/%v, want human/none for a real reader", c.Verdict, c.Reason)
	}
}

// An open within the prefetch window is a machine fetch even from a browser UA
// — this is the Apple MPP case, which forwards the original client's UA and so
// is invisible to the UA list.
func TestOpenGIF_SubSecondOpen_RecordsMachineEvent(t *testing.T) {
	r, store, sendID := newTestHandlerAt(t, testSentAt.Add(500*time.Millisecond))
	tok := track.MakeOpenToken(testSecret, sendID.String())
	const browserUA = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148"

	w := get(t, r, "/t/o/"+tok+".gif", browserUA, "")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if len(store.calls) != 1 {
		t.Fatalf("recorded %d events, want 1 — a prefetch is stored, not dropped", len(store.calls))
	}
	if c := store.calls[0]; c.Verdict != botfilter.Machine || c.Reason != botfilter.ReasonPrefetchWindow {
		t.Errorf("verdict = %v/%v, want machine/prefetch_window", c.Verdict, c.Reason)
	}
}

// A click that arrives instantly with no preceding open is a link scanner
// walking the message body. It is recorded as machine — and STILL REDIRECTED,
// because a scanner that got a 404 would report the link broken and the human
// it protects would never reach the page.
func TestClickRedirect_ClickWithoutOpen_RecordsMachineEventAndStillRedirects(t *testing.T) {
	r, store, sendID := newTestHandlerAt(t, testSentAt.Add(300*time.Millisecond))
	dest := "https://example.test/landing"
	tok := track.MakeClickToken(testSecret, sendID.String(), dest)

	w := get(t, r, "/t/c/"+tok, "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36", "")

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 — the verdict governs reporting, never delivery", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != dest {
		t.Errorf("Location = %q, want %q", loc, dest)
	}
	if len(store.calls) != 1 {
		t.Fatalf("recorded %d events, want 1 — a scanner click is STORED", len(store.calls))
	}
	if c := store.calls[0]; c.Verdict != botfilter.Machine || c.Reason != botfilter.ReasonClickWithoutOpen {
		t.Errorf("verdict = %v/%v, want machine/click_without_open", c.Verdict, c.Reason)
	}
}

// A click after a genuine human open is human, and the prior open is what
// vouches for it.
func TestClickRedirect_ClickAfterHumanOpen_RecordsHumanEvent(t *testing.T) {
	r, store, sendID := newTestHandlerAt(t, testSentAt.Add(2*time.Hour))
	store.prior = botfilter.Prior{HumanOpens: 1, LastHumanOpenAt: testSentAt.Add(time.Hour)}
	tok := track.MakeClickToken(testSecret, sendID.String(), "https://example.test/landing")

	get(t, r, "/t/c/"+tok, testUA, "")

	if len(store.calls) != 1 {
		t.Fatalf("recorded %d events, want 1", len(store.calls))
	}
	if c := store.calls[0]; c.Verdict != botfilter.Human || c.Reason != botfilter.ReasonNone {
		t.Errorf("verdict = %v/%v, want human/none", c.Verdict, c.Reason)
	}
}

// The burst rule fires off the prior-event count for the hit's own subnet.
func TestOpenGIF_BurstFromOneSubnet_RecordsMachineEvent(t *testing.T) {
	r, store, sendID := newTestHandler(t)
	store.prior = botfilter.Prior{OpensFromSubnet: botfilter.BurstSubnetOpenThreshold - 1}
	tok := track.MakeOpenToken(testSecret, sendID.String())

	get(t, r, "/t/o/"+tok+".gif", testUA, "203.0.113.9:41234")

	if len(store.calls) != 1 {
		t.Fatalf("recorded %d events, want 1", len(store.calls))
	}
	c := store.calls[0]
	if c.Verdict != botfilter.Machine || c.Reason != botfilter.ReasonBurstFromSubnet {
		t.Errorf("verdict = %v/%v, want machine/burst_from_subnet", c.Verdict, c.Reason)
	}
	if c.ClientIP.String() != "203.0.113.9" {
		t.Errorf("stored client IP = %v, want 203.0.113.9", c.ClientIP)
	}
	if len(store.subnets) != 1 || store.subnets[0].String() != "203.0.113.0/24" {
		t.Errorf("PriorEvents asked about %v, want one query for 203.0.113.0/24", store.subnets)
	}
}

// With no resolvable client IP there is nothing to count against, so the burst
// query is skipped (an invalid Prefix) and NULL is stored rather than a
// placeholder address the burst rule would later group on.
func TestOpenGIF_UnresolvableIP_SkipsBurstQueryAndStoresNoAddress(t *testing.T) {
	r, store, sendID := newTestHandler(t)
	tok := track.MakeOpenToken(testSecret, sendID.String())

	get(t, r, "/t/o/"+tok+".gif", testUA, "not-an-address")

	if len(store.calls) != 1 {
		t.Fatalf("recorded %d events, want 1", len(store.calls))
	}
	if c := store.calls[0]; c.ClientIP.IsValid() {
		t.Errorf("stored client IP = %v, want an invalid Addr (SQL NULL)", c.ClientIP)
	}
	if len(store.subnets) != 1 || store.subnets[0].IsValid() {
		t.Errorf("PriorEvents asked about %v, want an invalid Prefix so the burst count is skipped", store.subnets)
	}
}

// An unauthenticated caller must not be able to forge its source address. With
// no trusted proxies configured, X-Forwarded-For is ignored entirely.
func TestOpenGIF_ForgedForwardedForIsIgnored(t *testing.T) {
	r, store, sendID := newTestHandler(t)
	tok := track.MakeOpenToken(testSecret, sendID.String())

	req := httptest.NewRequest(http.MethodGet, "/t/o/"+tok+".gif", http.NoBody)
	req.Header.Set("User-Agent", testUA)
	req.Header.Set("X-Forwarded-For", "8.8.8.8") // a datacenter range, if believed
	req.RemoteAddr = "203.0.113.9:41234"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if len(store.calls) != 1 {
		t.Fatalf("recorded %d events, want 1", len(store.calls))
	}
	c := store.calls[0]
	if c.ClientIP.String() != "203.0.113.9" {
		t.Errorf("stored client IP = %v, want the direct peer 203.0.113.9 — XFF from an untrusted peer is forgeable", c.ClientIP)
	}
	if c.Verdict != botfilter.Human {
		t.Errorf("verdict = %v, want human; a forged XFF must not be able to mark another party's hit as a bot", c.Verdict)
	}
}

// A failure reading prior events must DEGRADE the classification, never fail
// the hit and never mark everything machine — a database blip would otherwise
// permanently zero a campaign's open rate.
func TestOpenGIF_PriorEventsError_StillRecordsOnRemainingSignals(t *testing.T) {
	r, store, sendID := newTestHandler(t)
	store.priorErr = errors.New("db is down")
	tok := track.MakeOpenToken(testSecret, sendID.String())

	w := get(t, r, "/t/o/"+tok+".gif", testUA, "203.0.113.9:41234")

	if w.Code != http.StatusOK || w.Body.Len() == 0 {
		t.Fatalf("status = %d, body = %d bytes; the pixel must never fail", w.Code, w.Body.Len())
	}
	if len(store.calls) != 1 {
		t.Fatalf("recorded %d events, want 1", len(store.calls))
	}
	if c := store.calls[0]; c.Verdict != botfilter.Human {
		t.Errorf("verdict = %v, want human — an unreadable history must not condemn the hit", c.Verdict)
	}
}

// Even with the history unreadable, a self-identifying proxy is still caught:
// the UA rule needs no prior events at all.
func TestOpenGIF_PriorEventsError_StillCatchesAKnownProxy(t *testing.T) {
	r, store, sendID := newTestHandler(t)
	store.priorErr = errors.New("db is down")
	tok := track.MakeOpenToken(testSecret, sendID.String())

	get(t, r, "/t/o/"+tok+".gif", "GoogleImageProxy", "")

	if len(store.calls) != 1 {
		t.Fatalf("recorded %d events, want 1", len(store.calls))
	}
	if c := store.calls[0]; c.Verdict != botfilter.Machine || c.Reason != botfilter.ReasonProxyUserAgent {
		t.Errorf("verdict = %v/%v, want machine/proxy_user_agent", c.Verdict, c.Reason)
	}
}

// The tenant on a machine event is resolved from the sends row exactly as it is
// for a human one — classification must not become a path around the pin.
func TestMachineEventCarriesTheServerResolvedTenant(t *testing.T) {
	r, store, sendID := newTestHandler(t)
	want := store.sends[sendID]
	tok := track.MakeOpenToken(testSecret, sendID.String())

	get(t, r, "/t/o/"+tok+".gif", "GoogleImageProxy", "")

	if len(store.calls) != 1 {
		t.Fatalf("recorded %d events, want 1", len(store.calls))
	}
	c := store.calls[0]
	if c.WorkspaceID != want.WorkspaceID || c.CampaignID != want.CampaignID {
		t.Errorf("tenant = %s/%s, want %s/%s from the sends row",
			c.WorkspaceID, c.CampaignID, want.WorkspaceID, want.CampaignID)
	}
}
