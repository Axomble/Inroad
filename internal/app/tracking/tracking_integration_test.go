//go:build integration

package tracking

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inroad/inroad/internal/app/campaign"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/track"
)

func dsn() string {
	if v := os.Getenv("INROAD_DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://inroad:inroad@localhost:5433/inroad?sslmode=disable"
}

// itSecret signs every token minted in this file -- a fixed value (rather
// than random per-test) so failures are reproducible.
var itSecret = []byte("0123456789abcdef0123456789abcdef")

func connect(t *testing.T) (*pgxpool.Pool, *gen.Queries, func()) {
	t.Helper()
	ctx := context.Background()
	if err := db.Migrate(dsn()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(ctx, dsn())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return pool, gen.New(pool), pool.Close
}

// itFixture is one seeded workspace/campaign/send, ready to have open/click
// tokens minted against it.
type itFixture struct {
	ws, campaignID, sendID uuid.UUID
}

// seedSend seeds a workspace, mailbox, list, contact, and campaign, then
// creates a sends row the same way production does (EnqueueSends, off a real
// list member), and marks it 'sent'. sent_at is then backdated an hour so
// CountHumanOpens' "fires more than 2s after sent_at" prefetch filter always
// passes deterministically, regardless of how quickly the test fires its
// open request after seeding.
func seedSend(t *testing.T, ctx context.Context, pool *pgxpool.Pool, q *gen.Queries) itFixture {
	t.Helper()
	ws, err := q.CreateWorkspace(ctx, "Track IT "+uuid.NewString())
	if err != nil {
		t.Fatalf("workspace: %v", err)
	}
	mb, err := q.CreateMailbox(ctx, gen.CreateMailboxParams{
		WorkspaceID: ws.ID, Provider: "smtp", Email: "from@acme.test", DisplayName: "Acme",
		SmtpHost: "smtp.acme.test", SmtpPort: 587, SmtpUsername: "from@acme.test",
		ImapHost: "imap.acme.test", ImapPort: 993, ImapUsername: "from@acme.test",
		SecretCiphertext: "ct", UseTls: true, DailyCap: 500, MinIntervalSeconds: 0,
		RampEnabled: false, RampStartCap: 5, RampDays: 30,
	})
	if err != nil {
		t.Fatalf("mailbox: %v", err)
	}
	lst, err := q.CreateList(ctx, gen.CreateListParams{WorkspaceID: ws.ID, Name: "L"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	ct, err := q.UpsertContact(ctx, gen.UpsertContactParams{WorkspaceID: ws.ID, Email: "alice-" + uuid.NewString() + "@x.test", FirstName: "Alice"})
	if err != nil {
		t.Fatalf("contact: %v", err)
	}
	if err := q.AddListMember(ctx, gen.AddListMemberParams{ListID: lst.ID, ContactID: ct.ID}); err != nil {
		t.Fatalf("member: %v", err)
	}
	cam, err := q.CreateCampaign(ctx, gen.CreateCampaignParams{
		WorkspaceID: ws.ID, Name: "Camp", MailboxID: mb.ID, ListID: lst.ID,
		Subject: "Hi", BodyText: "hello",
	})
	if err != nil {
		t.Fatalf("campaign: %v", err)
	}
	sendIDs, err := q.EnqueueSends(ctx, gen.EnqueueSendsParams{ID: cam.ID, WorkspaceID: ws.ID})
	if err != nil || len(sendIDs) != 1 {
		t.Fatalf("enqueue sends: %v ids=%d", err, len(sendIDs))
	}
	if err := q.SetSendResult(ctx, gen.SetSendResultParams{ID: sendIDs[0], Status: "sent", WorkspaceID: ws.ID}); err != nil {
		t.Fatalf("set send result: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sends SET sent_at = now() - interval '1 hour' WHERE id = $1`, sendIDs[0]); err != nil {
		t.Fatalf("backdate sent_at: %v", err)
	}
	return itFixture{ws: ws.ID, campaignID: cam.ID, sendID: sendIDs[0]}
}

// mountHandler wires a real Handler (PgStore backed) mounted at "/t", the
// same prefix cmd/inroad mounts it at in production.
func mountHandler(pool *pgxpool.Pool) http.Handler {
	h := NewHandler(NewService(itSecret, NewPgStore(pool)))
	r := chi.NewRouter()
	r.Mount("/t", h.Routes())
	return r
}

// countEvents returns how many tracking_events rows exist for sendID,
// regardless of kind -- used to assert the "no event" side of a rejected
// click.
func countEvents(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sendID uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM tracking_events WHERE send_id = $1`, sendID).Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	return n
}

// TestOpenPixel_RecordsEvent drives the real open-pixel endpoint against
// Postgres: a valid token for a real send must record an 'open' event with
// the send's actual workspace/campaign (resolved server-side, not from the
// token) and the request's User-Agent.
func TestOpenPixel_RecordsEvent(t *testing.T) {
	ctx := context.Background()
	pool, q, closeFn := connect(t)
	defer closeFn()
	fx := seedSend(t, ctx, pool, q)
	r := mountHandler(pool)

	tok := track.MakeOpenToken(itSecret, fx.sendID.String())
	req := httptest.NewRequest(http.MethodGet, "/t/o/"+tok+".gif", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (real client)")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/gif" {
		t.Errorf("Content-Type = %q, want image/gif", ct)
	}

	var wsID, camID uuid.UUID
	var kind, ua string
	row := pool.QueryRow(ctx, `SELECT workspace_id, campaign_id, kind, user_agent FROM tracking_events WHERE send_id = $1`, fx.sendID)
	if err := row.Scan(&wsID, &camID, &kind, &ua); err != nil {
		t.Fatalf("scan event: %v", err)
	}
	if wsID != fx.ws || camID != fx.campaignID || kind != "open" || ua != "Mozilla/5.0 (real client)" {
		t.Fatalf("recorded event ws=%s cam=%s kind=%s ua=%s, want ws=%s cam=%s kind=open ua=%q",
			wsID, camID, kind, ua, fx.ws, fx.campaignID, "Mozilla/5.0 (real client)")
	}
}

// TestClickRedirect_RecordsEventAndRedirects mirrors the open-pixel test for
// the click endpoint: 302 to the signed URL, plus a 'click' event resolved to
// the send's real tenant.
func TestClickRedirect_RecordsEventAndRedirects(t *testing.T) {
	ctx := context.Background()
	pool, q, closeFn := connect(t)
	defer closeFn()
	fx := seedSend(t, ctx, pool, q)
	r := mountHandler(pool)

	dest := "https://example.test/landing?utm_source=inroad"
	tok := track.MakeClickToken(itSecret, fx.sendID.String(), dest)
	req := httptest.NewRequest(http.MethodGet, "/t/c/"+tok, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (real client)")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != dest {
		t.Fatalf("Location = %q, want %q", loc, dest)
	}

	var wsID, camID uuid.UUID
	var kind, url string
	row := pool.QueryRow(ctx, `SELECT workspace_id, campaign_id, kind, url FROM tracking_events WHERE send_id = $1`, fx.sendID)
	if err := row.Scan(&wsID, &camID, &kind, &url); err != nil {
		t.Fatalf("scan event: %v", err)
	}
	if wsID != fx.ws || camID != fx.campaignID || kind != "click" || url != dest {
		t.Fatalf("recorded event ws=%s cam=%s kind=%s url=%s, want ws=%s cam=%s kind=click url=%s",
			wsID, camID, kind, url, fx.ws, fx.campaignID, dest)
	}
}

// TestClickRedirect_DataURL_404NoRedirectNoEvent covers the security case the
// javascript: scheme check already has unit coverage for (handler_test.go):
// a data: URL, however validly signed, must never 302 and must never record
// an event -- the HMAC only proves the payload wasn't tampered with, not
// that the URL it names is safe to redirect to.
func TestClickRedirect_DataURL_404NoRedirectNoEvent(t *testing.T) {
	ctx := context.Background()
	pool, q, closeFn := connect(t)
	defer closeFn()
	fx := seedSend(t, ctx, pool, q)
	r := mountHandler(pool)

	tok := track.MakeClickToken(itSecret, fx.sendID.String(), "data:text/html,pwned")
	req := httptest.NewRequest(http.MethodGet, "/t/c/"+tok, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "" {
		t.Fatalf("Location = %q, want no redirect", loc)
	}
	if n := countEvents(t, ctx, pool, fx.sendID); n != 0 {
		t.Fatalf("recorded %d events for a data: URL, want 0", n)
	}
}

// TestClickRedirect_ProtocolRelativeURL_404NoRedirectNoEvent covers the other
// deferred security case: a protocol-relative URL (no scheme, e.g.
// "//evil.com/x") parses with an empty Scheme, which the http(s)-only check
// rejects the same as any other non-http(s) scheme.
func TestClickRedirect_ProtocolRelativeURL_404NoRedirectNoEvent(t *testing.T) {
	ctx := context.Background()
	pool, q, closeFn := connect(t)
	defer closeFn()
	fx := seedSend(t, ctx, pool, q)
	r := mountHandler(pool)

	tok := track.MakeClickToken(itSecret, fx.sendID.String(), "//evil.com/x")
	req := httptest.NewRequest(http.MethodGet, "/t/c/"+tok, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "" {
		t.Fatalf("Location = %q, want no redirect", loc)
	}
	if n := countEvents(t, ctx, pool, fx.sendID); n != 0 {
		t.Fatalf("recorded %d events for a protocol-relative URL, want 0", n)
	}
}

// TestOpenPixel_GoogleImageProxy_RecordedButExcludedFromHumanOpens proves the
// prefetch filter lives in the read path (CountHumanOpens), not the write
// path: a GoogleImageProxy hit is still recorded as a real event (so raw
// event data is never lossy), but the metrics query that computes
// OpensIndicative must not count it.
func TestOpenPixel_GoogleImageProxy_RecordedButExcludedFromHumanOpens(t *testing.T) {
	ctx := context.Background()
	pool, q, closeFn := connect(t)
	defer closeFn()
	fx := seedSend(t, ctx, pool, q)
	r := mountHandler(pool)

	tok := track.MakeOpenToken(itSecret, fx.sendID.String())
	req := httptest.NewRequest(http.MethodGet, "/t/o/"+tok+".gif", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; GoogleImageProxy)")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if n := countEvents(t, ctx, pool, fx.sendID); n != 1 {
		t.Fatalf("recorded %d events for a GoogleImageProxy open, want 1 (write path must not filter)", n)
	}

	n, err := q.CountHumanOpens(ctx, gen.CountHumanOpensParams{CampaignID: fx.campaignID, WorkspaceID: fx.ws})
	if err != nil {
		t.Fatalf("CountHumanOpens: %v", err)
	}
	if n != 0 {
		t.Fatalf("CountHumanOpens = %d, want 0 (GoogleImageProxy must be excluded)", n)
	}
}

// TestCampaignMetrics_FromSeededEvents drives one human open and one click
// through the real handlers, then asserts campaign.Service.Detail (the
// GET /campaigns/{id} use case) rolls them up into Metrics with the expected
// counts and 1.0 rates for a single-send campaign; a second, unrelated
// workspace asking for the same campaign id must get ErrNotFound rather than
// its metrics.
func TestCampaignMetrics_FromSeededEvents(t *testing.T) {
	ctx := context.Background()
	pool, q, closeFn := connect(t)
	defer closeFn()
	fx := seedSend(t, ctx, pool, q)
	r := mountHandler(pool)

	openTok := track.MakeOpenToken(itSecret, fx.sendID.String())
	reqO := httptest.NewRequest(http.MethodGet, "/t/o/"+openTok+".gif", nil)
	reqO.Header.Set("User-Agent", "Mozilla/5.0 (real client)")
	r.ServeHTTP(httptest.NewRecorder(), reqO)

	clickTok := track.MakeClickToken(itSecret, fx.sendID.String(), "https://example.test/landing")
	reqC := httptest.NewRequest(http.MethodGet, "/t/c/"+clickTok, nil)
	r.ServeHTTP(httptest.NewRecorder(), reqC)

	campSvc := campaign.NewService(campaign.NewPgStore(pool), nil)
	detail, err := campSvc.Detail(ctx, fx.ws, fx.campaignID)
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	m := detail.Metrics
	if m.Sent != 1 || m.OpensIndicative != 1 || m.Clicks != 1 {
		t.Fatalf("Metrics = %+v, want Sent=1 OpensIndicative=1 Clicks=1", m)
	}
	if m.OpenRate != 1.0 || m.ClickRate != 1.0 {
		t.Fatalf("Metrics rates = %+v, want OpenRate=1.0 ClickRate=1.0", m)
	}

	// Cross-tenant: a workspace that doesn't own this campaign must not be
	// able to read its metrics via the campaign id alone.
	other, err := q.CreateWorkspace(ctx, "Track IT other "+uuid.NewString())
	if err != nil {
		t.Fatalf("other workspace: %v", err)
	}
	if _, err := campSvc.Detail(ctx, other.ID, fx.campaignID); !errors.Is(err, campaign.ErrNotFound) {
		t.Fatalf("cross-tenant Detail: want ErrNotFound, got %v", err)
	}
}

// Note on tracking_enabled=false: exercising the full "campaign toggled off
// -> sender injects no pixel/links" path end-to-end would require wiring the
// sequence worker's advance handler and a real SMTP-shaped Sender into this
// domain's integration test, which is redundant with the coverage that
// already exists at the unit level: internal/worker/sequence/advance_test.go
// (TestAdvanceSkipsTrackingWhenDisabled) and internal/worker/sender/sender_test.go
// (TestHandlerSkipsTrackingWhenDisabled) both assert TrackingEnabled=false
// leaves the body untouched (RewriteHTML/injection never called).
