package inbox_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/app/inbox"
	"github.com/inroad/inroad/internal/platform/ai"
)

// fakeDrafter is DraftReply's mocked AI seam: it records the input it was given
// and returns a canned draft or a canned error. No provider is ever dialed.
type fakeDrafter struct {
	draft string
	err   error
	calls int
	last  inbox.DraftReplyInput
	lastW uuid.UUID
}

func (f *fakeDrafter) DraftReply(_ context.Context, ws uuid.UUID, in inbox.DraftReplyInput) (string, error) {
	f.calls++
	f.last, f.lastW = in, ws
	return f.draft, f.err
}

// seedThreadWithConversation seeds a two-sided thread (we opened it, the contact
// answered) and returns its id.
func seedThreadWithConversation(store *fakeStore, ws uuid.UUID) uuid.UUID {
	campaignID := uuid.New()
	th := inbox.Thread{
		ID: uuid.New(), WorkspaceID: ws, MailboxID: uuid.New(), CampaignID: &campaignID,
		Subject: "Quick question", ContactFirstName: "Dana", Unread: true,
	}
	store.threads[th.ID] = th
	base := time.Now().Add(-time.Hour)
	store.messages[th.ID] = []inbox.Message{
		{ThreadID: th.ID, Direction: "outbound", FromEmail: "me@us.test", BodyText: "Are you the right person?", OccurredAt: base},
		{ThreadID: th.ID, Direction: "inbound", FromEmail: "dana@x.test", BodyText: "Yes, tell me more.", OccurredAt: base.Add(time.Minute)},
	}
	return th.ID
}

func TestDraftReplyUnknownThreadIsNotFound(t *testing.T) {
	svc := inbox.NewService(newFakeStore(), inbox.WithReplyDrafter(&fakeDrafter{draft: "hi"}))
	if _, err := svc.DraftReply(context.Background(), uuid.New(), uuid.New()); !errors.Is(err, inbox.ErrNotFound) {
		t.Fatalf("DraftReply on unknown thread = %v, want ErrNotFound", err)
	}
}

// A thread in another workspace is indistinguishable from one that does not
// exist, and — critically — its content never reaches a prompt.
func TestDraftReplyCrossWorkspaceThreadIsNotFoundAndDraftsNothing(t *testing.T) {
	store := newFakeStore()
	wsA, wsB := uuid.New(), uuid.New()
	threadID := seedThreadWithConversation(store, wsA)
	drafter := &fakeDrafter{draft: "hi"}
	svc := inbox.NewService(store, inbox.WithReplyDrafter(drafter))

	if _, err := svc.DraftReply(context.Background(), wsB, threadID); !errors.Is(err, inbox.ErrNotFound) {
		t.Fatalf("DraftReply across workspaces = %v, want ErrNotFound", err)
	}
	if drafter.calls != 0 {
		t.Fatal("a foreign thread's conversation was sent to the drafter")
	}
}

func TestDraftReplyWithNoInboundMessageIsConflict(t *testing.T) {
	store := newFakeStore()
	ws := uuid.New()
	th := inbox.Thread{ID: uuid.New(), WorkspaceID: ws, MailboxID: uuid.New(), Subject: "Hi"}
	store.threads[th.ID] = th
	store.messages[th.ID] = []inbox.Message{
		{ThreadID: th.ID, Direction: "outbound", BodyText: "anyone there?", OccurredAt: time.Now()},
	}
	drafter := &fakeDrafter{draft: "hi"}
	svc := inbox.NewService(store, inbox.WithReplyDrafter(drafter))

	if _, err := svc.DraftReply(context.Background(), ws, th.ID); !errors.Is(err, inbox.ErrNoInboundMessage) {
		t.Fatalf("DraftReply with no inbound message = %v, want ErrNoInboundMessage", err)
	}
	if drafter.calls != 0 {
		t.Fatal("drafted a reply for a thread with nothing to reply to")
	}
}

func TestDraftReplyReturnsTheDraftAndProjectsTheThread(t *testing.T) {
	store := newFakeStore()
	ws := uuid.New()
	threadID := seedThreadWithConversation(store, ws)
	drafter := &fakeDrafter{draft: "Happy to explain."}
	svc := inbox.NewService(store, inbox.WithReplyDrafter(drafter))

	got, err := svc.DraftReply(context.Background(), ws, threadID)
	if err != nil {
		t.Fatalf("DraftReply: %v", err)
	}
	if got != "Happy to explain." {
		t.Fatalf("draft = %q", got)
	}
	if drafter.lastW != ws {
		t.Fatalf("drafted against workspace %s, want %s", drafter.lastW, ws)
	}
	in := drafter.last
	if in.ContactFirstName != "Dana" || in.Subject != "Quick question" || !in.FromCampaign {
		t.Fatalf("projected input = %+v", in)
	}
	if len(in.Turns) != 2 {
		t.Fatalf("projected %d turns, want 2", len(in.Turns))
	}
	if in.Turns[0].FromContact || in.Turns[0].Text != "Are you the right person?" {
		t.Fatalf("turn 0 = %+v, want our outbound message first", in.Turns[0])
	}
	if !in.Turns[1].FromContact || in.Turns[1].Text != "Yes, tell me more." {
		t.Fatalf("turn 1 = %+v, want the contact's inbound message", in.Turns[1])
	}
}

// Drafting is not sending: nothing is enqueued, and the thread's unread state is
// untouched (unlike Reply, which marks it read).
func TestDraftReplyNeverEnqueuesASendOrMarksThreadRead(t *testing.T) {
	store := newFakeStore()
	ws := uuid.New()
	threadID := seedThreadWithConversation(store, ws)
	enq := &fakeReplyEnqueuer{}
	suppression := &fakeSuppressionChecker{suppressed: true} // would BLOCK a real reply
	svc := inbox.NewService(store,
		inbox.WithReplyEnqueuer(enq),
		inbox.WithSuppressionChecker(suppression),
		inbox.WithReplyDrafter(&fakeDrafter{draft: "Happy to explain."}))

	if _, err := svc.DraftReply(context.Background(), ws, threadID); err != nil {
		t.Fatalf("DraftReply: %v", err)
	}
	if enq.calls != 0 {
		t.Fatalf("DraftReply enqueued %d sends, want 0", enq.calls)
	}
	if suppression.lastEmail != "" {
		t.Fatal("DraftReply consulted the suppression list; only sending does that")
	}
	detail, err := svc.GetThread(context.Background(), ws, threadID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if !detail.Thread.Unread {
		t.Fatal("DraftReply marked the thread read; only an actual reply does that")
	}
}

func TestDraftReplyWithoutADrafterReportsNoModel(t *testing.T) {
	store := newFakeStore()
	ws := uuid.New()
	threadID := seedThreadWithConversation(store, ws)
	svc := inbox.NewService(store)

	if _, err := svc.DraftReply(context.Background(), ws, threadID); !errors.Is(err, inbox.ErrDraftModelUnavailable) {
		t.Fatalf("DraftReply with no drafter wired = %v, want ErrDraftModelUnavailable", err)
	}
}

func TestDraftReplyClassifiesDrafterFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{"no model configured", errors.Join(ai.ErrNoModel, errors.New("nothing enabled")), inbox.ErrDraftModelUnavailable},
		{"provider timeout", context.DeadlineExceeded, inbox.ErrDraftTimeout},
		{"provider failure", errors.New("502 from provider"), inbox.ErrDraftUpstream},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			ws := uuid.New()
			threadID := seedThreadWithConversation(store, ws)
			svc := inbox.NewService(store, inbox.WithReplyDrafter(&fakeDrafter{err: tc.err}))

			_, err := svc.DraftReply(context.Background(), ws, threadID)
			if !errors.Is(err, tc.want) {
				t.Fatalf("DraftReply = %v, want %v", err, tc.want)
			}
			if !errors.Is(err, tc.err) {
				t.Fatalf("DraftReply = %v, want the cause %v still wrapped", err, tc.err)
			}
		})
	}
}

// A drafter that returns "" with no error violates its own contract; the domain
// must not pass an empty draft on to a caller.
func TestDraftReplyRejectsAnEmptyDraft(t *testing.T) {
	store := newFakeStore()
	ws := uuid.New()
	threadID := seedThreadWithConversation(store, ws)
	svc := inbox.NewService(store, inbox.WithReplyDrafter(&fakeDrafter{draft: "  \n "}))

	if _, err := svc.DraftReply(context.Background(), ws, threadID); !errors.Is(err, inbox.ErrDraftUpstream) {
		t.Fatalf("DraftReply with a blank draft = %v, want ErrDraftUpstream", err)
	}
}

// An HTML-only message still contributes prose to the transcript: body_text is
// preferred, and the fallback strips markup (including script/style CONTENT)
// rather than feeding tags to the model.
func TestDraftReplyProjectsHTMLOnlyMessages(t *testing.T) {
	store := newFakeStore()
	ws := uuid.New()
	th := inbox.Thread{ID: uuid.New(), WorkspaceID: ws, MailboxID: uuid.New(), Subject: "Hi"}
	store.threads[th.ID] = th
	store.messages[th.ID] = []inbox.Message{{
		ThreadID: th.ID, Direction: "inbound", FromEmail: "dana@x.test", OccurredAt: time.Now(),
		BodyHTML: "<html><head><style>p{color:red}</style><script>track('open')</script></head>" +
			"<body><p>Yes &amp; thanks.</p><p>Call me   Tuesday?</p></body></html>",
	}}
	drafter := &fakeDrafter{draft: "Sure."}
	svc := inbox.NewService(store, inbox.WithReplyDrafter(drafter))

	if _, err := svc.DraftReply(context.Background(), ws, th.ID); err != nil {
		t.Fatalf("DraftReply: %v", err)
	}
	if len(drafter.last.Turns) != 1 {
		t.Fatalf("projected %d turns, want 1", len(drafter.last.Turns))
	}
	text := drafter.last.Turns[0].Text
	if text != "Yes & thanks.\nCall me Tuesday?" {
		t.Fatalf("HTML projection = %q", text)
	}
}

// A message with no usable body at all (attachment-only) is skipped rather than
// rendered as a blank turn.
func TestDraftReplySkipsBodylessMessages(t *testing.T) {
	store := newFakeStore()
	ws := uuid.New()
	th := inbox.Thread{ID: uuid.New(), WorkspaceID: ws, MailboxID: uuid.New(), Subject: "Hi"}
	store.threads[th.ID] = th
	store.messages[th.ID] = []inbox.Message{
		{ThreadID: th.ID, Direction: "outbound", OccurredAt: time.Now().Add(-time.Hour)},
		{ThreadID: th.ID, Direction: "inbound", BodyText: "Yes.", OccurredAt: time.Now()},
	}
	drafter := &fakeDrafter{draft: "Sure."}
	svc := inbox.NewService(store, inbox.WithReplyDrafter(drafter))

	if _, err := svc.DraftReply(context.Background(), ws, th.ID); err != nil {
		t.Fatalf("DraftReply: %v", err)
	}
	if len(drafter.last.Turns) != 1 || drafter.last.Turns[0].Text != "Yes." {
		t.Fatalf("projected turns = %+v, want only the message that had a body", drafter.last.Turns)
	}
}

// --- HTTP layer -------------------------------------------------------------

func draftHandler(t *testing.T, drafter inbox.ReplyDrafter) (*inbox.Handler, uuid.UUID) {
	t.Helper()
	store := newFakeStore()
	threadID := seedThreadWithConversation(store, testWS)
	opts := []inbox.ServiceOption{inbox.WithReplyEnqueuer(&fakeReplyEnqueuer{})}
	if drafter != nil {
		opts = append(opts, inbox.WithReplyDrafter(drafter))
	}
	return inbox.NewHandler(inbox.NewService(store, opts...)), threadID
}

func TestDraftReplyEndpointReturnsBodyText(t *testing.T) {
	h, threadID := draftHandler(t, &fakeDrafter{draft: "Happy to explain."})
	w := serve(t, h, http.MethodPost, "/inbox/threads/"+threadID.String()+"/draft-reply", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var got struct {
		BodyText string `json:"body_text"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.BodyText != "Happy to explain." {
		t.Fatalf("body_text = %q", got.BodyText)
	}
}

func TestDraftReplyEndpointStatusCodes(t *testing.T) {
	tests := []struct {
		name    string
		drafter *fakeDrafter
		want    int
	}{
		// 422, deliberately NOT 503: the missing model is the one draft failure a
		// retry can never fix, and 503 invites intermediaries to retry.
		{"no model configured", &fakeDrafter{err: errors.Join(ai.ErrNoModel, errors.New("none"))}, http.StatusUnprocessableEntity},
		{"provider timeout", &fakeDrafter{err: context.DeadlineExceeded}, http.StatusGatewayTimeout},
		{"provider failure", &fakeDrafter{err: errors.New("boom")}, http.StatusBadGateway},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, threadID := draftHandler(t, tc.drafter)
			w := serve(t, h, http.MethodPost, "/inbox/threads/"+threadID.String()+"/draft-reply", "")
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d (%s)", w.Code, tc.want, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "boom") {
				t.Fatalf("response leaked the upstream error detail: %s", w.Body.String())
			}
		})
	}
}

func TestDraftReplyEndpointUnknownThreadIs404(t *testing.T) {
	h, _ := draftHandler(t, &fakeDrafter{draft: "hi"})
	w := serve(t, h, http.MethodPost, "/inbox/threads/"+uuid.New().String()+"/draft-reply", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", w.Code, w.Body.String())
	}
}

func TestDraftReplyEndpointRequiresAuth(t *testing.T) {
	h, threadID := draftHandler(t, &fakeDrafter{draft: "hi"})
	w := do(t, h, http.MethodPost, "/inbox/threads/"+threadID.String()+"/draft-reply", "", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// The route is behind the draft throttle, so a limiter that denies produces a
// 429 and the handler never runs. This asserts the WIRING; the limiter itself is
// tested in internal/platform/throttle.
func TestDraftReplyEndpointHonorsTheThrottle(t *testing.T) {
	h, threadID := draftHandler(t, &fakeDrafter{draft: "hi"})
	denyAll := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		})
	}
	root := chi.NewRouter()
	root.Mount("/inbox", h.Routes(denyAll))
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
		"/inbox/threads/"+threadID.String()+"/draft-reply", http.NoBody)
	r.Header.Set("Authorization", bearer(t, testWS))
	w := httptest.NewRecorder()
	auth.RequireAuth(auth.NewJWTVerifier(testSecret))(root).ServeHTTP(w, r)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", w.Code)
	}
}

// The throttle must NOT sit in front of the other inbox routes: reading a thread
// costs nothing and is not rate-limited by this cap.
func TestDraftThrottleAppliesOnlyToTheDraftRoute(t *testing.T) {
	h, threadID := draftHandler(t, &fakeDrafter{draft: "hi"})
	denyAll := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		})
	}
	root := chi.NewRouter()
	root.Mount("/inbox", h.Routes(denyAll))
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet,
		"/inbox/threads/"+threadID.String(), http.NoBody)
	r.Header.Set("Authorization", bearer(t, testWS))
	w := httptest.NewRecorder()
	auth.RequireAuth(auth.NewJWTVerifier(testSecret))(root).ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("GET thread status = %d, want 200 — the draft throttle leaked onto another route", w.Code)
	}
}

// Non-ASCII HTML must survive the fallback intact. Regression guard: an earlier
// version lower-cased the whole document once and indexed it in parallel with
// the original, which misaligns whenever ToLower changes a rune's byte length.
func TestDraftReplyHTMLFallbackHandlesNonASCII(t *testing.T) {
	store := newFakeStore()
	ws := uuid.New()
	th := inbox.Thread{ID: uuid.New(), WorkspaceID: ws, MailboxID: uuid.New(), Subject: "Merhaba"}
	store.threads[th.ID] = th
	store.messages[th.ID] = []inbox.Message{{
		ThreadID: th.ID, Direction: "inbound", FromEmail: "dana@x.test", OccurredAt: time.Now(),
		BodyHTML: "<p>İstanbul'da mısınız?</p><p>Große Grüße, ЖЖ</p>",
	}}
	drafter := &fakeDrafter{draft: "Evet."}
	svc := inbox.NewService(store, inbox.WithReplyDrafter(drafter))

	if _, err := svc.DraftReply(context.Background(), ws, th.ID); err != nil {
		t.Fatalf("DraftReply: %v", err)
	}
	if len(drafter.last.Turns) != 1 {
		t.Fatalf("projected %d turns, want 1", len(drafter.last.Turns))
	}
	if got := drafter.last.Turns[0].Text; got != "İstanbul'da mısınız?\nGroße Grüße, ЖЖ" {
		t.Fatalf("non-ASCII HTML projection = %q", got)
	}
}

// Precedence: the throttle runs BEFORE the handler, so an over-cap request gets
// 429 even when the underlying failure would have been the non-retryable 422
// "no model configured". That ordering is deliberate, not an oversight.
//
// Resolving a model is a database read (workspace AI settings, provider rows,
// the model catalog). Checking it ahead of the rate limit would let an
// unthrottled caller drive that work at will — the throttle exists precisely to
// bound work triggered before any policy decision. A rate limiter also has to
// count REQUESTS, not outcomes: the outcome isn't known until the handler runs,
// and refunding a failed request's slot afterwards is racy and lets a
// no-model workspace hammer the database for free.
//
// The user-facing cost is small: the caps are 20/min per IP and 60/min per
// workspace, so a human clicking a button sees the 422 every time and only a
// scripted caller reaches the 429 — after 20 clear "configure a model"
// responses.
func TestDraftThrottlePrecedesTheNoModelCheck(t *testing.T) {
	h, threadID := draftHandler(t, &fakeDrafter{err: errors.Join(ai.ErrNoModel, errors.New("none"))})
	denyAll := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "60")
			httpxErrorTooManyRequests(w)
		})
	}
	root := chi.NewRouter()
	root.Mount("/inbox", h.Routes(denyAll))
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost,
		"/inbox/threads/"+threadID.String()+"/draft-reply", http.NoBody)
	r.Header.Set("Authorization", bearer(t, testWS))
	w := httptest.NewRecorder()
	auth.RequireAuth(auth.NewJWTVerifier(testSecret))(root).ServeHTTP(w, r)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (the throttle must short-circuit before the handler)", w.Code)
	}
	// Retry-After is load-bearing for the client's "wait N seconds" copy.
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("429 carried no Retry-After header")
	}
}

func httpxErrorTooManyRequests(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write([]byte(`{"error":"too many requests"}`))
}

// A failed draft is logged with a STABLE reason token so an operator can tell
// "nobody has a model configured" from "the provider is broken" without reading
// message content — and specifically so "workspaces that look configured are
// still getting no_model_configured" (a revoked provider key) is answerable from
// logs. Content must never appear in that line.
func TestDraftReplyLogsFailuresWithAStableReasonAndNoContent(t *testing.T) {
	tests := []struct {
		name       string
		drafterErr error
		nilDrafter bool
		emptyDraft bool
		wantReason string
	}{
		{name: "no model", drafterErr: errors.Join(ai.ErrNoModel, errors.New("none")), wantReason: "no_model_configured"},
		{name: "timeout", drafterErr: context.DeadlineExceeded, wantReason: "provider_timeout"},
		{name: "provider failure", drafterErr: errors.New("upstream exploded"), wantReason: "provider_failed"},
		{name: "drafter not wired", nilDrafter: true, wantReason: "drafter_not_wired"},
		{name: "empty draft", emptyDraft: true, wantReason: "empty_draft"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			restore := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
			t.Cleanup(func() { slog.SetDefault(restore) })

			store := newFakeStore()
			ws := uuid.New()
			threadID := seedThreadWithConversation(store, ws)
			opts := []inbox.ServiceOption{}
			switch {
			case tc.nilDrafter:
				// no drafter wired
			case tc.emptyDraft:
				opts = append(opts, inbox.WithReplyDrafter(&fakeDrafter{draft: "   "}))
			default:
				opts = append(opts, inbox.WithReplyDrafter(&fakeDrafter{err: tc.drafterErr}))
			}
			svc := inbox.NewService(store, opts...)

			if _, err := svc.DraftReply(context.Background(), ws, threadID); err == nil {
				t.Fatal("DraftReply = nil error, want a failure")
			}
			logged := buf.String()
			if !strings.Contains(logged, `"reason":"`+tc.wantReason+`"`) {
				t.Fatalf("log missing reason %q:\n%s", tc.wantReason, logged)
			}
			if !strings.Contains(logged, ws.String()) || !strings.Contains(logged, threadID.String()) {
				t.Fatalf("log missing workspace/thread id:\n%s", logged)
			}
			// The seeded conversation's actual message text must never appear.
			for _, content := range []string{"Are you the right person?", "Yes, tell me more.", "Dana"} {
				if strings.Contains(logged, content) {
					t.Fatalf("log leaked message content %q:\n%s", content, logged)
				}
			}
		})
	}
}

// A caller that closed the connection is not an operational event and must not
// be logged as a failure — it would bury the failures that matter.
func TestDraftReplyDoesNotLogACancelledCaller(t *testing.T) {
	var buf bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(restore) })

	store := newFakeStore()
	ws := uuid.New()
	threadID := seedThreadWithConversation(store, ws)
	svc := inbox.NewService(store, inbox.WithReplyDrafter(&fakeDrafter{err: context.Canceled}))

	if _, err := svc.DraftReply(context.Background(), ws, threadID); !errors.Is(err, context.Canceled) {
		t.Fatalf("DraftReply = %v, want context.Canceled", err)
	}
	if strings.Contains(buf.String(), "inbox_reply_draft_failed") {
		t.Fatalf("a cancelled caller was logged as a draft failure:\n%s", buf.String())
	}
}

// A provider's error text is never logged, because some providers echo a snippet
// of the offending input in a 4xx body. For *ai.ProviderStatusError the log
// carries the kind/status/retryable facts instead — and this asserts the type
// survives the REAL wrapping chain (provider -> agentrun's %w -> the domain's
// %w), which is what makes errors.As reachable at all.
func TestDraftReplyLogsProviderStatusNotProviderText(t *testing.T) {
	var buf bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(restore) })

	// Wrapped exactly the way agentrun.DraftReply wraps a stream failure.
	providerErr := &ai.ProviderStatusError{Kind: ai.KindOpenAI, StatusCode: http.StatusBadRequest}
	wrapped := fmt.Errorf("agentrun: draft reply: %w", providerErr)

	store := newFakeStore()
	ws := uuid.New()
	threadID := seedThreadWithConversation(store, ws)
	svc := inbox.NewService(store, inbox.WithReplyDrafter(&fakeDrafter{err: wrapped}))

	if _, err := svc.DraftReply(context.Background(), ws, threadID); !errors.Is(err, inbox.ErrDraftUpstream) {
		t.Fatalf("DraftReply = %v, want ErrDraftUpstream", err)
	}
	logged := buf.String()
	for _, want := range []string{`"reason":"provider_failed"`, `"provider_status":400`, `"provider_retryable":false`, ai.KindOpenAI} {
		if !strings.Contains(logged, want) {
			t.Fatalf("log missing %q:\n%s", want, logged)
		}
	}
	// The provider's own message must not appear in any form.
	if strings.Contains(logged, providerErr.Error()) || strings.Contains(logged, `"err":`) {
		t.Fatalf("log quoted the provider error text:\n%s", logged)
	}
}

// An upstream failure that is NOT a ProviderStatusError may be an SDK error
// embedding a response body, so only its Go TYPE is logged — never its text.
func TestDraftReplyWithholdsTextFromUnknownProviderErrors(t *testing.T) {
	var buf bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(restore) })

	// Stands in for an SDK error that echoed request material back at us.
	secret := "prompt fragment: Are you the right person?"
	store := newFakeStore()
	ws := uuid.New()
	threadID := seedThreadWithConversation(store, ws)
	svc := inbox.NewService(store, inbox.WithReplyDrafter(&fakeDrafter{err: errors.New(secret)}))

	if _, err := svc.DraftReply(context.Background(), ws, threadID); !errors.Is(err, inbox.ErrDraftUpstream) {
		t.Fatalf("DraftReply = %v, want ErrDraftUpstream", err)
	}
	logged := buf.String()
	if strings.Contains(logged, secret) || strings.Contains(logged, "right person") {
		t.Fatalf("log leaked an untrusted provider error's text:\n%s", logged)
	}
	if !strings.Contains(logged, `"err_type":`) {
		t.Fatalf("log omitted the error type, the one safe debugging signal:\n%s", logged)
	}
}

// Our OWN errors keep their full value: a resolution failure names a model or
// provider id, which is what makes the line actionable, and cannot carry
// message content.
func TestDraftReplyKeepsOurOwnErrorText(t *testing.T) {
	var buf bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(restore) })

	store := newFakeStore()
	ws := uuid.New()
	threadID := seedThreadWithConversation(store, ws)
	resolveErr := errors.Join(ai.ErrNoModel, errors.New("model openai-row/gpt-5.2 is not enabled"))
	svc := inbox.NewService(store, inbox.WithReplyDrafter(&fakeDrafter{err: resolveErr}))

	if _, err := svc.DraftReply(context.Background(), ws, threadID); !errors.Is(err, inbox.ErrDraftModelUnavailable) {
		t.Fatalf("DraftReply = %v, want ErrDraftModelUnavailable", err)
	}
	logged := buf.String()
	if !strings.Contains(logged, "openai-row/gpt-5.2") {
		t.Fatalf("our own resolution error lost its actionable detail:\n%s", logged)
	}
}
