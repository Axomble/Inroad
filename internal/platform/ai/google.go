package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"strconv"
	"sync"

	"cloud.google.com/go/auth/credentials"
	"cloud.google.com/go/auth/httptransport"
	"github.com/google/uuid"
	"google.golang.org/genai"
)

// googleStreamer speaks the Gemini generateContent protocol. AI Studio and
// Vertex are the same protocol behind different auth and hosts, which the genai
// SDK models as a backend switch — so only client construction differs between
// the two kinds.
type googleStreamer struct {
	kind   string
	client *genai.Client
}

func newGoogleStreamer(kind string, creds Credentials, config map[string]string, hc *http.Client) (*googleStreamer, error) {
	ctx := context.Background()
	cfg := &genai.ClientConfig{}

	switch kind {
	case KindGoogle:
		if creds.APIKey == "" {
			return nil, fmt.Errorf("%w: kind %q requires credentials.api_key", ErrBadProviderConfig, kind)
		}
		// Backend is pinned rather than inferred: the SDK otherwise consults
		// GOOGLE_GENAI_USE_VERTEXAI and friends, and a host env var must not be
		// able to redirect a workspace's provider row.
		cfg.Backend = genai.BackendGeminiAPI
		cfg.APIKey = creds.APIKey
		cfg.HTTPClient = hc

	case KindVertexGoogle:
		region, err := requireConfig(config, kind, "region")
		if err != nil {
			return nil, err
		}
		project, err := requireConfig(config, kind, "project_id")
		if err != nil {
			return nil, err
		}
		gcreds, err := credentials.DetectDefault(&credentials.DetectOptions{
			CredentialsJSON: []byte(creds.ServiceAccountJSON),
			Scopes:          []string{vertexScope},
		})
		if err != nil {
			return nil, fmt.Errorf("%w: kind %q service account was rejected", ErrBadProviderConfig, kind)
		}
		cfg.Backend = genai.BackendVertexAI
		cfg.Project = project
		cfg.Location = region
		cfg.Credentials = gcreds
		// Layer Vertex authorization over the shared guarded client. Leaving the
		// client nil would make the SDK allocate its own unguarded transport.
		if err := httptransport.AddAuthorizationMiddleware(hc, gcreds); err != nil {
			return nil, fmt.Errorf("%w: kind %q authenticated transport could not be built", ErrBadProviderConfig, kind)
		}
		cfg.HTTPClient = hc

	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedKind, kind)
	}

	client, err := genai.NewClient(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("%w: kind %q client could not be built", ErrBadProviderConfig, kind)
	}
	return &googleStreamer{kind: kind, client: client}, nil
}

func (s *googleStreamer) StreamChat(ctx context.Context, req ChatRequest) (ChatStream, error) {
	if err := validateRequest(req); err != nil {
		return nil, err
	}
	contents, cfg, err := googleParams(req)
	if err != nil {
		return nil, err
	}
	seq := s.client.Models.GenerateContentStream(ctx, req.Model, contents, cfg)
	next, stop := iter.Pull2(seq)
	return &googleStream{kind: s.kind, next: next, stop: stop}, nil
}

// googleParams translates the neutral ChatRequest into Gemini contents plus
// config. Three shape changes matter: the assistant role is called "model", the
// system prompt is a separate Content rather than a message, and a tool result
// is a functionResponse part keyed by the tool NAME — which the neutral
// ChatPart may not carry on the result, so it is resolved from the tool call it
// answers.
func googleParams(req ChatRequest) ([]*genai.Content, *genai.GenerateContentConfig, error) {
	cfg := &genai.GenerateContentConfig{MaxOutputTokens: int32(req.MaxTokens)}
	if req.System != "" {
		cfg.SystemInstruction = &genai.Content{Parts: []*genai.Part{{Text: req.System}}}
	}

	names := toolNamesByCallID(req.Messages)
	contents := make([]*genai.Content, 0, len(req.Messages))
	for _, m := range req.Messages {
		parts, err := googleParts(m, names)
		if err != nil {
			return nil, nil, err
		}
		if len(parts) == 0 {
			continue
		}
		role := genai.RoleUser
		if m.Role == RoleAssistant {
			role = genai.RoleModel
		}
		contents = append(contents, &genai.Content{Role: role, Parts: parts})
	}
	if len(contents) == 0 {
		return nil, nil, fmt.Errorf("%w: request carried no sendable content", ErrBadProviderConfig)
	}

	if len(req.Tools) > 0 {
		decls := make([]*genai.FunctionDeclaration, 0, len(req.Tools))
		for _, t := range req.Tools {
			schema, err := toolSchemaMap(t)
			if err != nil {
				return nil, nil, err
			}
			decls = append(decls, &genai.FunctionDeclaration{
				Name:                 t.Name,
				Description:          t.Description,
				ParametersJsonSchema: schema,
			})
		}
		cfg.Tools = []*genai.Tool{{FunctionDeclarations: decls}}
	}
	return contents, cfg, nil
}

// toolNamesByCallID indexes every tool call in the transcript so a later tool
// RESULT can be given the function name Gemini requires on a functionResponse.
func toolNamesByCallID(messages []ChatMessage) map[string]string {
	names := map[string]string{}
	for _, m := range messages {
		for _, p := range m.Parts {
			if p.Type == PartToolCall && p.ToolCallID != "" && p.ToolName != "" {
				names[p.ToolCallID] = p.ToolName
			}
		}
	}
	return names
}

func googleParts(m ChatMessage, names map[string]string) ([]*genai.Part, error) {
	parts := make([]*genai.Part, 0, len(m.Parts))
	for _, p := range m.Parts {
		switch p.Type {
		case PartText:
			if p.Text != "" {
				parts = append(parts, &genai.Part{Text: p.Text})
			}
		case PartReasoning:
			continue
		case PartToolCall:
			args, err := googleArgs(p.ToolInput)
			if err != nil {
				return nil, fmt.Errorf("%w: tool call %q arguments are not a JSON object", ErrBadProviderConfig, p.ToolCallID)
			}
			parts = append(parts, &genai.Part{FunctionCall: &genai.FunctionCall{
				ID: p.ToolCallID, Name: p.ToolName, Args: args,
			}})
		case PartToolResult:
			name := p.ToolName
			if name == "" {
				name = names[p.ToolCallID]
			}
			if name == "" {
				return nil, fmt.Errorf("%w: tool result %q has no matching tool name", ErrBadProviderConfig, p.ToolCallID)
			}
			parts = append(parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{
				ID: p.ToolCallID, Name: name, Response: googleToolResponse(p),
			}})
		default:
			return nil, fmt.Errorf("%w: unknown part type %q", ErrBadProviderConfig, p.Type)
		}
	}
	return parts, nil
}

func googleArgs(input json.RawMessage) (map[string]any, error) {
	if len(input) == 0 {
		return map[string]any{}, nil
	}
	var args map[string]any
	if err := json.Unmarshal(input, &args); err != nil {
		return nil, err
	}
	if args == nil {
		args = map[string]any{}
	}
	return args, nil
}

// googleToolResponse shapes a tool result as the JSON OBJECT Gemini demands.
// Tool output is arbitrary JSON, so a non-object (an array, a bare string) is
// wrapped rather than rejected — a tool's return shape is not the model's
// problem.
func googleToolResponse(p ChatPart) map[string]any {
	key := "output"
	if p.IsError {
		key = "error"
	}
	if len(p.ToolOutput) == 0 {
		return map[string]any{key: ""}
	}
	var obj map[string]any
	if err := json.Unmarshal(p.ToolOutput, &obj); err == nil && obj != nil && !p.IsError {
		return obj
	}
	var value any
	if err := json.Unmarshal(p.ToolOutput, &value); err != nil {
		return map[string]any{key: string(p.ToolOutput)}
	}
	return map[string]any{key: value}
}

// googleStream drains the SDK's push iterator through iter.Pull2 so the
// contract's pull-shaped Next/Close can sit on top; stop() is what unwinds the
// underlying request and releases the body.
type googleStream struct {
	kind string
	next func() (*genai.GenerateContentResponse, error, bool)
	stop func()
	eventQueue

	usage      Usage
	reasoning  int
	stopReason string
	// callSeed namespaces synthesized tool-call ids to THIS stream, and callSeq
	// orders them within it. A bare ordinal would repeat "call_1" on every turn
	// of a thread, so persisted tool_call_ids would collide and a tool result
	// could be matched to the wrong call.
	callSeed     string
	callSeq      int
	sawToolCall  bool
	emittedUsage bool
	closeOnce    sync.Once
}

func (s *googleStream) Next() (StreamEvent, error) {
	for {
		if ev, ok := s.pop(); ok {
			return ev, nil
		}
		resp, err, ok := s.next()
		if err != nil {
			var apiErr genai.APIError
			if errors.As(err, &apiErr) {
				// The genai SDK surfaces only the status code, so there is no
				// Retry-After to read; backoff falls back to our own schedule.
				return StreamEvent{}, providerStatusError(s.kind, apiErr.Code, nil)
			}
			return StreamEvent{}, providerError(s.kind, err)
		}
		if !ok {
			if s.emittedUsage {
				return StreamEvent{}, io.EOF
			}
			s.emittedUsage = true
			s.push(s.finalUsage())
			continue
		}
		s.consume(resp)
	}
}

// finalUsage applies the package's accounting contract. Gemini reports thinking
// tokens in thoughtsTokenCount SEPARATELY from candidatesTokenCount, so the
// output figure is already exclusive and nothing is subtracted.
func (s *googleStream) finalUsage() StreamEvent {
	stop := s.stopReason
	if s.sawToolCall && stop != StopMaxTokens {
		// Gemini closes a tool-calling turn with STOP; the neutral contract
		// needs the runtime to know a tool was requested.
		stop = StopToolUse
	}
	u := normalizeUsage(s.usage.InputTokens, s.usage.OutputTokens, s.reasoning, s.usage.CacheReadTokens, false)
	return usageEvent(u, stop)
}

func (s *googleStream) consume(resp *genai.GenerateContentResponse) {
	if resp == nil {
		return
	}
	if m := resp.UsageMetadata; m != nil {
		s.usage.InputTokens = int(m.PromptTokenCount)
		s.usage.OutputTokens = int(m.CandidatesTokenCount)
		s.reasoning = int(m.ThoughtsTokenCount)
		s.usage.CacheReadTokens = int(m.CachedContentTokenCount)
	}
	for _, cand := range resp.Candidates {
		if cand == nil {
			continue
		}
		if cand.FinishReason != "" {
			s.stopReason = googleStopReason(cand.FinishReason)
		}
		if cand.Content == nil {
			continue
		}
		for _, part := range cand.Content.Parts {
			s.consumePart(part)
		}
	}
}

func (s *googleStream) consumePart(part *genai.Part) {
	if part == nil {
		return
	}
	if call := part.FunctionCall; call != nil {
		s.pushToolCall(call)
		return
	}
	if part.Text == "" {
		return
	}
	if part.Thought {
		s.push(StreamEvent{Type: EventReasoningDelta, Text: part.Text})
		return
	}
	s.push(StreamEvent{Type: EventTextDelta, Text: part.Text})
}

// pushToolCall expands a Gemini function call into the contract's three-event
// shape. Gemini delivers arguments whole rather than as fragments, so the whole
// object arrives as a single input delta — consumers accumulate identically
// either way.
func (s *googleStream) pushToolCall(call *genai.FunctionCall) {
	s.sawToolCall = true
	id := call.ID
	if id == "" {
		// AI Studio omits call ids; the runtime correlates results by id, so one
		// is synthesized — seeded from a UUID minted once per stream so ids stay
		// unique across every turn of a thread.
		if s.callSeed == "" {
			s.callSeed = uuid.NewString()
		}
		s.callSeq++
		id = "call_" + s.callSeed + "_" + strconv.Itoa(s.callSeq)
	}
	args, err := json.Marshal(call.Args)
	if err != nil || call.Args == nil {
		args = []byte(`{}`)
	}
	s.push(
		StreamEvent{Type: EventToolCallStart, ToolCallID: id, ToolName: call.Name},
		StreamEvent{Type: EventToolInputDelta, ToolCallID: id, Text: string(args)},
		StreamEvent{Type: EventToolCallEnd, ToolCallID: id, ToolName: call.Name, ToolInput: json.RawMessage(args)},
	)
}

func googleStopReason(reason genai.FinishReason) string {
	switch reason {
	case genai.FinishReasonMaxTokens:
		return StopMaxTokens
	default:
		return StopEndTurn
	}
}

func (s *googleStream) Close() error {
	s.closeOnce.Do(s.stop)
	return nil
}
