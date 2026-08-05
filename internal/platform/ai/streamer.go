package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/inroad/inroad/internal/platform/mail"
)

// Streaming transport tuning. A generation may legitimately run for minutes, so
// there is deliberately NO overall client timeout: the caller's context is the
// only deadline. What IS bounded is everything that happens before the first
// token — connect, TLS, and the wait for response headers — because a provider
// that never answers must not pin a run forever.
const (
	streamDialTimeout           = 10 * time.Second
	streamTLSHandshakeTimeout   = 10 * time.Second
	streamResponseHeaderTimeout = 90 * time.Second
	streamExpectContinueTimeout = 1 * time.Second
	streamIdleConnTimeout       = 90 * time.Second
)

// Retry tuning for a provider turn that fails BEFORE producing any output.
// Attempts counts the first try, so 3 means at most two retries. The backoff
// ceiling bounds our own exponential growth; a provider that asks for a longer
// wait through Retry-After is honored up to a separate, larger ceiling, beyond
// which a run would be pinned waiting rather than failing visibly.
const (
	streamRetryAttempts      = 3
	streamRetryBaseDelay     = 500 * time.Millisecond
	streamRetryMaxDelay      = 8 * time.Second
	streamRetryMaxRetryAfter = 30 * time.Second
)

// ErrUnsupportedKind is wrapped by Streamer when asked for a kind outside
// Kinds, so callers can map the whole class to one status.
var ErrUnsupportedKind = errors.New("ai: unsupported provider kind")

// ErrBadProviderConfig is wrapped whenever a provider row's stored config or
// credentials cannot produce a usable client. The message never echoes the
// credential material — only which key is missing.
var ErrBadProviderConfig = errors.New("ai: provider is not usable")

// streamerFactory builds one ChatStreamer per provider row. It holds no
// credentials of its own: unsealing stays with the caller, and the factory is
// handed the plaintext blob for the duration of the construction call only.
type streamerFactory struct {
	allowPrivateBaseURL bool
}

// NewStreamerFactory returns the production StreamerFactory. allowPrivateBaseURL
// is the operator opt-in (INROAD_AI_ALLOW_PRIVATE_BASE_URL) that lets a
// self-host point an openai_compatible door at a loopback Ollama; it is
// threaded into the same SSRF-guarded transport discovery uses.
func NewStreamerFactory(allowPrivateBaseURL bool) StreamerFactory {
	return &streamerFactory{allowPrivateBaseURL: allowPrivateBaseURL}
}

// Streamer dispatches on kind to the adapter that speaks that vendor's wire
// protocol. Three adapters cover eight doors: Anthropic (direct/Bedrock/Vertex),
// OpenAI (direct/Azure/any compatible gateway), Google (AI Studio/Vertex).
//
// Every adapter is wrapped in the shared retry policy, so a routine 429 or a
// provider hiccup before the first token is retried rather than killing the
// caller's turn — one policy for all three families, since only the two
// Stainless SDKs retry on their own and the Gemini SDK does not.
func (f *streamerFactory) Streamer(kind string, creds Credentials, config map[string]string) (ChatStreamer, error) {
	client := f.httpClient()
	switch kind {
	case KindAnthropic, KindBedrock, KindVertexAnthropic:
		s, err := newAnthropicStreamer(kind, creds, config, client)
		if err != nil {
			return nil, err
		}
		return withStreamRetry(s), nil
	case KindOpenAI, KindAzureOpenAI, KindOpenAICompatible:
		s, err := newOpenAIStreamer(kind, creds, config, client)
		if err != nil {
			return nil, err
		}
		return withStreamRetry(s), nil
	case KindGoogle, KindVertexGoogle:
		s, err := newGoogleStreamer(kind, creds, config, client)
		if err != nil {
			return nil, err
		}
		return withStreamRetry(s), nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedKind, kind)
	}
}

// httpClient builds the shared streaming transport. Every kind dials through
// the ONE SSRF guard this repo has (mail.GuardedDialContext): fixed vendor
// hosts pass it trivially, and the user-supplied base_url/endpoint kinds are
// re-vetted at dial time — same posture as HTTPDiscoverer, no second policy.
func (f *streamerFactory) httpClient() *http.Client {
	return &http.Client{
		Transport: streamTransport(f.allowPrivateBaseURL),
		// Credentials must never be replayed to a redirect target. Stored
		// endpoints must be canonical; redirects are provider errors.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func streamTransport(allowPrivate bool) *http.Transport {
	dial := mail.GuardedDialContext(allowPrivate)
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialCtx, cancel := context.WithTimeout(ctx, streamDialTimeout)
			defer cancel()
			return dial(dialCtx, network, addr)
		},
		TLSHandshakeTimeout:   streamTLSHandshakeTimeout,
		ResponseHeaderTimeout: streamResponseHeaderTimeout,
		ExpectContinueTimeout: streamExpectContinueTimeout,
		IdleConnTimeout:       streamIdleConnTimeout,
		ForceAttemptHTTP2:     true,
	}
}

// validateRequest enforces the invariants every provider shares, so a malformed
// ChatRequest fails here rather than as an opaque 400 from a vendor.
func validateRequest(req ChatRequest) error {
	if strings.TrimSpace(req.Model) == "" {
		return fmt.Errorf("%w: model is required", ErrBadProviderConfig)
	}
	if req.MaxTokens <= 0 {
		return fmt.Errorf("%w: max_tokens must be positive", ErrBadProviderConfig)
	}
	if req.MaxTokens > math.MaxInt32 {
		return fmt.Errorf("%w: max_tokens exceeds the supported limit", ErrBadProviderConfig)
	}
	if len(req.Messages) == 0 {
		return fmt.Errorf("%w: at least one message is required", ErrBadProviderConfig)
	}
	for messageIndex, m := range req.Messages {
		if m.Role != RoleUser && m.Role != RoleAssistant {
			return fmt.Errorf("%w: unknown message role %q", ErrBadProviderConfig, m.Role)
		}
		for partIndex, p := range m.Parts {
			switch p.Type {
			case PartText, PartReasoning:
			case PartToolCall:
				if m.Role != RoleAssistant || p.ToolCallID == "" || strings.TrimSpace(p.ToolName) == "" {
					return fmt.Errorf("%w: message %d part %d is not a valid assistant tool call", ErrBadProviderConfig, messageIndex, partIndex)
				}
				if len(p.ToolInput) > 0 && !jsonObject(p.ToolInput) {
					return fmt.Errorf("%w: tool call %q input must be a JSON object", ErrBadProviderConfig, p.ToolCallID)
				}
			case PartToolResult:
				if m.Role != RoleUser || p.ToolCallID == "" {
					return fmt.Errorf("%w: message %d part %d is not a valid user tool result", ErrBadProviderConfig, messageIndex, partIndex)
				}
				if len(p.ToolOutput) > 0 && !json.Valid(p.ToolOutput) {
					return fmt.Errorf("%w: tool result %q output must be valid JSON", ErrBadProviderConfig, p.ToolCallID)
				}
			default:
				return fmt.Errorf("%w: unknown part type %q", ErrBadProviderConfig, p.Type)
			}
		}
	}
	toolNames := make(map[string]struct{}, len(req.Tools))
	for _, tool := range req.Tools {
		if _, exists := toolNames[tool.Name]; exists {
			return fmt.Errorf("%w: duplicate tool name %q", ErrBadProviderConfig, tool.Name)
		}
		toolNames[tool.Name] = struct{}{}
		if _, err := decodeToolSchema(tool); err != nil {
			return err
		}
	}
	return nil
}

func jsonObject(raw json.RawMessage) bool {
	var object map[string]any
	return json.Unmarshal(raw, &object) == nil && object != nil
}

// requireConfig reads a required config key, naming the key (never the value)
// on failure.
func requireConfig(config map[string]string, kind, key string) (string, error) {
	v := strings.TrimSpace(config[key])
	if v == "" {
		return "", fmt.Errorf("%w: kind %q requires config.%s", ErrBadProviderConfig, kind, key)
	}
	return v, nil
}

func requireHTTPBaseURL(config map[string]string, kind, key string) (string, error) {
	raw, err := requireConfig(config, kind, key)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: kind %q config.%s must be an absolute HTTP URL without credentials, query, or fragment", ErrBadProviderConfig, kind, key)
	}
	return raw, nil
}

// normalizeUsage applies the package's hard accounting contract: OutputTokens
// EXCLUDES reasoning tokens for every provider. Adapters hand over the raw
// vendor numbers and this is the single place the subtraction happens, so no
// adapter can drift. A provider that folds reasoning into its output count is
// corrected; one that already reports them separately is left alone by passing
// an output figure that never went below zero.
func normalizeUsage(inputTokens, outputTokens, reasoningTokens, cacheReadTokens int, outputIncludesReasoning bool) Usage {
	if outputIncludesReasoning {
		outputTokens -= reasoningTokens
	}
	return Usage{
		InputTokens:     max(inputTokens, 0),
		OutputTokens:    max(outputTokens, 0),
		ReasoningTokens: max(reasoningTokens, 0),
		CacheReadTokens: max(cacheReadTokens, 0),
	}
}

// toolResultText renders a PartToolResult for the providers that model a tool
// result as TEXT (Anthropic tool_result blocks, OpenAI tool-role messages). The
// runtime stores tool output as JSON, so the JSON is what the model sees; an
// empty output still has to send something, or the block is rejected.
func toolResultText(p ChatPart) string {
	if len(p.ToolOutput) == 0 {
		if p.IsError {
			return `{"error":"tool returned no output"}`
		}
		return "{}"
	}
	return string(p.ToolOutput)
}

// toolSchema is a tool's JSON Schema split the way the Anthropic SDK models it:
// properties and required are typed fields, and anything else the registry put
// on the schema rides along as extras rather than being silently dropped.
type toolSchema struct {
	properties any
	required   []string
	extra      map[string]any
}

func decodeToolSchema(t ToolDef) (toolSchema, error) {
	if t.Name == "" {
		return toolSchema{}, fmt.Errorf("%w: tool name is required", ErrBadProviderConfig)
	}
	out := toolSchema{properties: map[string]any{}}
	if len(t.InputSchema) == 0 {
		return out, nil
	}
	var raw map[string]any
	if err := json.Unmarshal(t.InputSchema, &raw); err != nil {
		return toolSchema{}, fmt.Errorf("%w: tool %q input_schema is not a JSON object: %w", ErrBadProviderConfig, t.Name, err)
	}
	for k, v := range raw {
		switch k {
		case "properties":
			properties, ok := v.(map[string]any)
			if !ok {
				return toolSchema{}, fmt.Errorf("%w: tool %q input_schema.properties must be an object", ErrBadProviderConfig, t.Name)
			}
			out.properties = properties
		case "required":
			required, ok := toStringSlice(v)
			if !ok {
				return toolSchema{}, fmt.Errorf("%w: tool %q input_schema.required must be an array of strings", ErrBadProviderConfig, t.Name)
			}
			out.required = required
		case "type":
			if v != "object" {
				return toolSchema{}, fmt.Errorf("%w: tool %q input_schema.type must be object", ErrBadProviderConfig, t.Name)
			}
		default:
			if out.extra == nil {
				out.extra = map[string]any{}
			}
			out.extra[k] = v
		}
	}
	return out, nil
}

// toolSchemaMap re-renders a tool's schema as a plain JSON-Schema object, the
// shape the OpenAI and Google SDKs take directly.
func toolSchemaMap(t ToolDef) (map[string]any, error) {
	schema, err := decodeToolSchema(t)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"type": "object", "properties": schema.properties}
	if len(schema.required) > 0 {
		out["required"] = schema.required
	}
	for k, v := range schema.extra {
		out[k] = v
	}
	return out, nil
}

func toStringSlice(v any) ([]string, bool) {
	items, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// accumulatedToolInput closes a streamed tool call. Providers omit the argument
// stream entirely for a no-argument tool, and some emit fragments that never
// form valid JSON if the turn was truncated — either way the runtime must get a
// valid JSON value rather than a broken fragment it would have to guess about.
// Empty input becomes the conventional empty object; malformed or non-object
// input becomes null so downstream schema validation fails closed.
func accumulatedToolInput(buffered string) json.RawMessage {
	trimmed := strings.TrimSpace(buffered)
	if trimmed == "" {
		return json.RawMessage(`{}`)
	}
	if !jsonObject(json.RawMessage(trimmed)) {
		// Fail closed: a truncated or non-object argument stream becomes the
		// JSON literal null, which no tool's object schema accepts, so the
		// registry rejects the call. Substituting {} instead would let a
		// half-streamed call execute on guessed defaults.
		return json.RawMessage(`null`)
	}
	return json.RawMessage(trimmed)
}

// eventQueue is the shared spine of every adapter's ChatStream: a provider
// chunk may expand into zero, one, or several StreamEvents, so adapters push a
// burst and Next drains it one at a time.
type eventQueue struct {
	pending []StreamEvent
}

func (q *eventQueue) push(events ...StreamEvent) {
	q.pending = append(q.pending, events...)
}

func (q *eventQueue) pop() (StreamEvent, bool) {
	if len(q.pending) == 0 {
		return StreamEvent{}, false
	}
	ev := q.pending[0]
	q.pending = q.pending[1:]
	return ev, true
}

// usageEvent builds the single terminal event every turn ends with.
func usageEvent(u Usage, stopReason string) StreamEvent {
	if stopReason == "" {
		stopReason = StopEndTurn
	}
	return StreamEvent{Type: EventUsage, Usage: &u, StopReason: stopReason}
}

// providerError sanitizes a vendor transport/stream failure. Provider bodies
// can reflect the request (and on a misconfigured gateway, header material)
// back at us, so only the SDK's own error value travels — never a body we
// read ourselves — and context cancellation is surfaced unwrapped so callers
// can tell "user stopped the run" from "provider broke".
func providerError(kind string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("ai: %s stream: %w", kind, err)
}

// ProviderStatusError is a non-2xx response from a provider. It carries the
// status (and Retry-After when the provider sent one) so callers can tell a
// transient rate limit from a permanently malformed request without parsing a
// message. It deliberately carries NO body: provider bodies can reflect request
// and header material back at us.
type ProviderStatusError struct {
	Kind       string
	StatusCode int
	// RetryAfter is the provider's requested wait; zero when it sent none.
	RetryAfter time.Duration
}

func (e *ProviderStatusError) Error() string {
	return fmt.Sprintf("ai: %s stream: provider returned HTTP %d", e.Kind, e.StatusCode)
}

// Retryable reports whether re-sending the same request could plausibly
// succeed: rate limits, request timeouts, and the transient server-side
// statuses (including Anthropic's 529 overloaded). Everything else — a 400 for
// a malformed request, a 401 for a bad key — fails the same way every time.
func (e *ProviderStatusError) Retryable() bool {
	switch e.StatusCode {
	case http.StatusRequestTimeout, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529:
		return true
	default:
		return false
	}
}

func providerStatusError(kind string, status int, resp *http.Response) error {
	return &ProviderStatusError{Kind: kind, StatusCode: status, RetryAfter: retryAfter(resp)}
}

// retryAfter reads the Retry-After header in either documented form — delay in
// seconds, or an HTTP date. A header we cannot parse is treated as absent so
// the caller falls back to its own backoff.
func retryAfter(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	raw := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	when, err := http.ParseTime(raw)
	if err != nil {
		return 0
	}
	if d := time.Until(when); d > 0 {
		return d
	}
	return 0
}

// retryingStreamer re-opens a provider turn that failed before emitting any
// output. It sits behind the ChatStreamer seam so the runtime needs no change:
// a retried step looks like one that simply took longer to produce its first
// event.
type retryingStreamer struct {
	inner    ChatStreamer
	attempts int
	sleep    func(context.Context, time.Duration) error
}

func withStreamRetry(inner ChatStreamer) ChatStreamer {
	return &retryingStreamer{inner: inner, attempts: streamRetryAttempts, sleep: sleepContext}
}

func (s *retryingStreamer) StreamChat(ctx context.Context, req ChatRequest) (ChatStream, error) {
	first, err := s.inner.StreamChat(ctx, req)
	if err != nil {
		// Request-shaping failures (validation, unusable params) are terminal;
		// only in-flight provider responses are worth another attempt.
		return nil, err
	}
	return &retryStream{
		ctx:       ctx,
		req:       req,
		inner:     s.inner,
		current:   first,
		remaining: s.attempts - 1,
		sleep:     s.sleep,
	}, nil
}

// retryStream owns at most one live provider turn at a time. It retries ONLY
// while no event has been handed to the caller: once a delta is out, re-opening
// would duplicate text the user has already seen, so a mid-stream failure is
// always terminal.
type retryStream struct {
	ctx     context.Context
	req     ChatRequest
	inner   ChatStreamer
	sleep   func(context.Context, time.Duration) error
	current ChatStream

	remaining int
	emitted   bool
	closeOnce sync.Once
}

func (s *retryStream) Next() (StreamEvent, error) {
	for {
		ev, err := s.current.Next()
		if err == nil {
			s.emitted = true
			return ev, nil
		}
		// The end of a healthy turn (io.EOF) and every terminal provider failure
		// leave here; only a retryable status with no output yet loops.
		var status *ProviderStatusError
		if !errors.As(err, &status) || !s.shouldRetry(status) {
			return StreamEvent{}, err
		}
		if waitErr := s.sleep(s.ctx, retryDelay(s.attempt(), status.RetryAfter)); waitErr != nil {
			return StreamEvent{}, waitErr
		}
		_ = s.current.Close()
		next, openErr := s.inner.StreamChat(s.ctx, s.req)
		if openErr != nil {
			return StreamEvent{}, openErr
		}
		s.current = next
		s.remaining--
	}
}

// shouldRetry decides whether to re-open the turn. A stream that has already
// emitted an event is never retried: re-opening would duplicate text the caller
// has seen.
func (s *retryStream) shouldRetry(status *ProviderStatusError) bool {
	return !s.emitted && s.remaining > 0 && s.ctx.Err() == nil && status.Retryable()
}

// attempt is the zero-based index of the retry about to be made, which is what
// the exponential backoff is a function of.
func (s *retryStream) attempt() int {
	return streamRetryAttempts - s.remaining - 1
}

func (s *retryStream) Close() error {
	var err error
	s.closeOnce.Do(func() { err = s.current.Close() })
	return err
}

// retryDelay prefers the provider's own Retry-After and otherwise grows
// exponentially. Jitter is applied to the computed backoff (never to an
// explicit Retry-After) so a workspace's concurrent runs don't all come back at
// the same instant after a rate limit.
func retryDelay(attempt int, providerAsked time.Duration) time.Duration {
	if providerAsked > 0 {
		return min(providerAsked, streamRetryMaxRetryAfter)
	}
	backoff := min(streamRetryBaseDelay<<attempt, streamRetryMaxDelay)
	return backoff/2 + time.Duration(rand.Int64N(int64(backoff/2)))
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
