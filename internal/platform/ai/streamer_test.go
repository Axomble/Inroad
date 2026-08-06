package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func validChatRequest() ChatRequest {
	return ChatRequest{Model: "model", MaxTokens: 128, Messages: []ChatMessage{{Role: RoleUser, Parts: []ChatPart{{Type: PartText, Text: "hello"}}}}}
}

func TestStreamingHTTPClientRejectsRedirects(t *testing.T) {
	c := (&streamerFactory{}).httpClient()
	if err := c.CheckRedirect(&http.Request{}, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatal(err)
	}
}

func TestValidateRequest(t *testing.T) {
	r := validChatRequest()
	if err := validateRequest(r); err != nil {
		t.Fatal(err)
	}
	r.MaxTokens = math.MaxInt32 + 1
	if err := validateRequest(r); !errors.Is(err, ErrBadProviderConfig) {
		t.Fatalf("error = %v", err)
	}
	r = validChatRequest()
	r.Messages[0].Role = "system"
	if err := validateRequest(r); !errors.Is(err, ErrBadProviderConfig) {
		t.Fatalf("error = %v", err)
	}
}

func TestToolValidation(t *testing.T) {
	r := validChatRequest()
	r.Tools = []ToolDef{{Name: "x", InputSchema: json.RawMessage(`{"type":"array"}`)}}
	if err := validateRequest(r); !errors.Is(err, ErrBadProviderConfig) {
		t.Fatalf("error = %v", err)
	}
	r.Tools = []ToolDef{{Name: "x"}, {Name: "x"}}
	if err := validateRequest(r); !errors.Is(err, ErrBadProviderConfig) {
		t.Fatalf("error = %v", err)
	}
}

func TestRequireHTTPBaseURL(t *testing.T) {
	for _, raw := range []string{"relative", "ftp://example.test", "https://user:pass@example.test", "https://example.test?q=1"} {
		if _, err := requireHTTPBaseURL(map[string]string{"base_url": raw}, KindOpenAICompatible, "base_url"); !errors.Is(err, ErrBadProviderConfig) {
			t.Errorf("%q error = %v", raw, err)
		}
	}
	got, err := requireHTTPBaseURL(map[string]string{"base_url": "http://localhost:11434/v1"}, KindOpenAICompatible, "base_url")
	if err != nil || got == "" {
		t.Fatalf("valid URL = %q, %v", got, err)
	}
}

func TestSharedStreamHelpers(t *testing.T) {
	got := normalizeUsage(10, 8, 3, 2, true)
	want := Usage{InputTokens: 10, OutputTokens: 5, ReasoningTokens: 3, CacheReadTokens: 2}
	if got != want {
		t.Fatalf("usage = %+v", got)
	}
	for input, want := range map[string]string{"": "{}", "{": "null", "[]": "null"} {
		if got := string(accumulatedToolInput(input)); got != want {
			t.Errorf("%q => %s", input, got)
		}
	}
	if err := providerError(KindOpenAI, context.Canceled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// TestRequestBuildersAreDeterministic guards the invariant every provider's
// caching rests on: prompt caching is a PREFIX-BYTE match, so building the same
// ChatRequest twice must serialize identically. OpenAI and Google have no
// explicit breakpoints at all — their automatic prefix caching works only for
// as long as nothing here reorders tools, ranges a map into the wire format, or
// injects a per-request value. The tool schema below carries several extra
// JSON-Schema keys because a map with one key cannot expose an ordering bug.
func TestRequestBuildersAreDeterministic(t *testing.T) {
	req := validChatRequest()
	req.System = "you are an agent"
	req.Tools = []ToolDef{{
		Name:        "lookup",
		Description: "find",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {"q": {"type": "string"}, "limit": {"type": "integer"}, "deep": {"type": "boolean"}},
			"required": ["q"],
			"additionalProperties": false,
			"$schema": "https://json-schema.org/draft/2020-12/schema",
			"title": "lookup input",
			"description": "arguments",
			"$defs": {"unused": {"type": "string"}}
		}`),
	}}

	builders := map[string]func() ([]byte, error){
		"anthropic": func() ([]byte, error) {
			params, err := anthropicParams(req)
			if err != nil {
				return nil, err
			}
			return json.Marshal(params)
		},
		"openai": func() ([]byte, error) {
			params, err := openAIParams(req, false)
			if err != nil {
				return nil, err
			}
			return json.Marshal(params)
		},
		"google": func() ([]byte, error) {
			contents, cfg, err := googleParams(req)
			if err != nil {
				return nil, err
			}
			return json.Marshal(map[string]any{"contents": contents, "config": cfg})
		},
	}

	for name, build := range builders {
		want, err := build()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		// Map iteration order varies per range, so one repeat can pass by luck.
		for range 50 {
			got, err := build()
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s serialization is not stable:\n first: %s\nlater: %s", name, want, got)
			}
		}
	}
}

// fakeChatStream replays a fixed event burst and then fails (or ends) with err.
type fakeChatStream struct {
	events []StreamEvent
	err    error

	next   int
	closes int
}

func (s *fakeChatStream) Next() (StreamEvent, error) {
	if s.next < len(s.events) {
		s.next++
		return s.events[s.next-1], nil
	}
	return StreamEvent{}, s.err
}

func (s *fakeChatStream) Close() error {
	s.closes++
	return nil
}

// fakeStreamer hands out one prepared stream per open so a test can make the
// first attempt fail and a later one succeed.
type fakeStreamer struct {
	attempts []*fakeChatStream
	opens    int
}

func (f *fakeStreamer) StreamChat(context.Context, ChatRequest) (ChatStream, error) {
	f.opens++
	return f.attempts[min(f.opens, len(f.attempts))-1], nil
}

func rateLimited(retryAfter time.Duration) error {
	return &ProviderStatusError{Kind: KindOpenAI, StatusCode: http.StatusTooManyRequests, RetryAfter: retryAfter}
}

func textDelta(text string) StreamEvent {
	return StreamEvent{Type: EventTextDelta, Text: text}
}

// retryUnderTest wires a retrying streamer with a sleep the test controls, so
// backoff behavior is asserted without spending wall-clock time.
func retryUnderTest(t *testing.T, inner ChatStreamer, sleep func(context.Context, time.Duration) error) ChatStream {
	t.Helper()
	s := &retryingStreamer{inner: inner, attempts: streamRetryAttempts, sleep: sleep}
	stream, err := s.StreamChat(context.Background(), validChatRequest())
	if err != nil {
		t.Fatal(err)
	}
	return stream
}

func drain(t *testing.T, stream ChatStream) ([]StreamEvent, error) {
	t.Helper()
	var got []StreamEvent
	for {
		ev, err := stream.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return got, nil
			}
			return got, err
		}
		got = append(got, ev)
	}
}

func TestStreamRetriesRateLimitBeforeAnyOutput(t *testing.T) {
	inner := &fakeStreamer{attempts: []*fakeChatStream{
		{err: rateLimited(0)},
		{events: []StreamEvent{textDelta("hello")}, err: io.EOF},
	}}
	var slept []time.Duration
	stream := retryUnderTest(t, inner, func(context.Context, time.Duration) error {
		slept = append(slept, 0)
		return nil
	})

	got, err := drain(t, stream)
	if err != nil {
		t.Fatalf("drain = %v", err)
	}
	if len(got) != 1 || got[0].Text != "hello" {
		t.Fatalf("events = %+v", got)
	}
	if inner.opens != 2 || len(slept) != 1 {
		t.Fatalf("opens = %d, sleeps = %d", inner.opens, len(slept))
	}
	if inner.attempts[0].closes != 1 {
		t.Fatalf("abandoned stream closes = %d", inner.attempts[0].closes)
	}
}

func TestStreamDoesNotRetryAfterDeltas(t *testing.T) {
	inner := &fakeStreamer{attempts: []*fakeChatStream{
		{events: []StreamEvent{textDelta("par")}, err: rateLimited(0)},
		{events: []StreamEvent{textDelta("tial")}, err: io.EOF},
	}}
	stream := retryUnderTest(t, inner, func(context.Context, time.Duration) error {
		t.Error("slept before a retry that must not happen")
		return nil
	})

	got, err := drain(t, stream)
	var status *ProviderStatusError
	if !errors.As(err, &status) || status.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("error = %v", err)
	}
	if len(got) != 1 || inner.opens != 1 {
		t.Fatalf("events = %+v, opens = %d", got, inner.opens)
	}
}

func TestStreamDoesNotRetryTerminalStatus(t *testing.T) {
	inner := &fakeStreamer{attempts: []*fakeChatStream{
		{err: &ProviderStatusError{Kind: KindOpenAI, StatusCode: http.StatusBadRequest}},
	}}
	stream := retryUnderTest(t, inner, func(context.Context, time.Duration) error {
		t.Error("slept before a retry that must not happen")
		return nil
	})

	if _, err := drain(t, stream); err == nil || inner.opens != 1 {
		t.Fatalf("error = %v, opens = %d", err, inner.opens)
	}
}

func TestStreamRetryExhaustsAttempts(t *testing.T) {
	inner := &fakeStreamer{attempts: []*fakeChatStream{{err: rateLimited(0)}}}
	stream := retryUnderTest(t, inner, func(context.Context, time.Duration) error { return nil })

	if _, err := drain(t, stream); err == nil {
		t.Fatal("exhausted retries returned no error")
	}
	if inner.opens != streamRetryAttempts {
		t.Fatalf("opens = %d, want %d", inner.opens, streamRetryAttempts)
	}
}

func TestStreamRetryUnwindsOnCancel(t *testing.T) {
	inner := &fakeStreamer{attempts: []*fakeChatStream{{err: rateLimited(time.Hour)}}}
	ctx, cancel := context.WithCancel(context.Background())
	s := &retryingStreamer{inner: inner, attempts: streamRetryAttempts, sleep: func(ctx context.Context, d time.Duration) error {
		cancel() // the run is stopped while the backoff is in flight
		return sleepContext(ctx, d)
	}}
	stream, err := s.StreamChat(ctx, validChatRequest())
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	if _, err := stream.Next(); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("unwound after %s", elapsed)
	}
	if inner.opens != 1 {
		t.Fatalf("opens = %d", inner.opens)
	}
}

func TestProviderStatusErrorRetryAfter(t *testing.T) {
	statuses := map[int]bool{429: true, 500: true, 502: true, 503: true, 529: true, 400: false, 401: false, 404: false}
	for status, want := range statuses {
		err := &ProviderStatusError{Kind: KindGoogle, StatusCode: status}
		if err.Retryable() != want {
			t.Errorf("status %d retryable = %v", status, err.Retryable())
		}
	}

	withRetryAfter := func(value string) time.Duration {
		return retryAfter(&http.Response{Header: http.Header{"Retry-After": []string{value}}})
	}
	if got := withRetryAfter("7"); got != 7*time.Second {
		t.Errorf("seconds form = %s", got)
	}
	if got := withRetryAfter(time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)); got < 80*time.Second {
		t.Errorf("date form = %s", got)
	}
	for _, raw := range []string{"", "soon", "-1", "0"} {
		if got := withRetryAfter(raw); got != 0 {
			t.Errorf("%q = %s, want 0", raw, got)
		}
	}
	if got := retryAfter(nil); got != 0 {
		t.Errorf("nil response = %s", got)
	}
}

func TestRetryDelayHonorsProviderAndCaps(t *testing.T) {
	if got := retryDelay(0, 3*time.Second); got != 3*time.Second {
		t.Errorf("provider ask = %s", got)
	}
	if got := retryDelay(0, time.Hour); got != streamRetryMaxRetryAfter {
		t.Errorf("capped provider ask = %s", got)
	}
	for attempt := range 6 {
		got := retryDelay(attempt, 0)
		if got < streamRetryBaseDelay/2 || got > streamRetryMaxDelay {
			t.Errorf("attempt %d delay = %s", attempt, got)
		}
	}
}

// openAICompatibleSSE is a minimal Chat Completions stream: one text delta plus
// the usage-bearing terminal chunk.
const openAICompatibleSSE = "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"pong\"}}]}\n\n" +
	"data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":2,\"total_tokens\":11}}\n\n" +
	"data: [DONE]\n\n"

// TestStreamRetryOverHTTPRecoversFromRateLimit drives the real OpenAI-compatible
// adapter against a local gateway that rate-limits before it answers. The SDK
// makes its own attempts first, so the server refuses enough times to exhaust
// those and prove the package's retry is what recovers the turn.
func TestStreamRetryOverHTTPRecoversFromRateLimit(t *testing.T) {
	const refusals = 3
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		if requests <= refusals {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, openAICompatibleSSE)
	}))
	defer server.Close()

	factory := &streamerFactory{allowPrivateBaseURL: true}
	streamer, err := factory.Streamer(KindOpenAICompatible, Credentials{APIKey: "k"}, map[string]string{"base_url": server.URL})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := streamer.StreamChat(context.Background(), validChatRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	got, err := drain(t, stream)
	if err != nil {
		t.Fatalf("drain = %v", err)
	}
	if len(got) != 2 || got[0].Text != "pong" || got[1].Type != EventUsage {
		t.Fatalf("events = %+v", got)
	}
	if got[1].Usage.InputTokens != 9 || got[1].Usage.OutputTokens != 2 {
		t.Fatalf("usage = %+v", got[1].Usage)
	}
	if requests != refusals+1 {
		t.Fatalf("requests = %d, want %d", requests, refusals+1)
	}
}

// TestStreamRetryOverHTTPFailsFastOnBadRequest pins the other half: a status the
// provider will return identically forever must surface immediately, typed.
func TestStreamRetryOverHTTPFailsFastOnBadRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	factory := &streamerFactory{allowPrivateBaseURL: true}
	streamer, err := factory.Streamer(KindOpenAICompatible, Credentials{}, map[string]string{"base_url": server.URL})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := streamer.StreamChat(context.Background(), validChatRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	_, err = drain(t, stream)
	var status *ProviderStatusError
	if !errors.As(err, &status) || status.StatusCode != http.StatusBadRequest {
		t.Fatalf("error = %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}
