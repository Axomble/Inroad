package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/bedrock"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/ssestream"
	"github.com/anthropics/anthropic-sdk-go/vertex"
	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awscreds "github.com/aws/aws-sdk-go-v2/credentials"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// vertexScope is the OAuth2 scope a service account needs to call Vertex AI.
const vertexScope = "https://www.googleapis.com/auth/cloud-platform"

// anthropicStreamer speaks the Messages API. All three Anthropic doors share
// it: Bedrock and Vertex are the SAME protocol behind SDK middleware that
// rewrites auth and the URL, so only client construction differs — request and
// event mapping below are identical for the three kinds.
type anthropicStreamer struct {
	kind   string
	client anthropic.Client
}

func newAnthropicStreamer(kind string, creds Credentials, config map[string]string, hc *http.Client) (*anthropicStreamer, error) {
	opts, err := anthropicOptions(kind, creds, config, hc)
	if err != nil {
		return nil, err
	}
	return &anthropicStreamer{kind: kind, client: anthropic.NewClient(opts...)}, nil
}

// anthropicOptions builds the per-kind auth options. Bedrock signs with SigV4
// from static workspace keys and Vertex mints OAuth tokens from the service
// account — both delegated to the SDK's subpackages rather than hand-rolled.
//
// Every branch ends by installing hc so the guarded, timeout-bearing transport
// is what actually reaches the wire. Vertex needs care: its option installs a
// token-bearing client of its own, so we replace it with an oauth2 client
// layered over hc rather than a bare one, which would drop authentication.
func anthropicOptions(kind string, creds Credentials, config map[string]string, hc *http.Client) (opts []option.RequestOption, err error) {
	// vertex.WithCredentials panics rather than returning on a malformed
	// credential; a bad provider row must surface as an error, not a crash in
	// the request path.
	defer func() {
		if r := recover(); r != nil {
			opts, err = nil, fmt.Errorf("%w: kind %q credentials were rejected", ErrBadProviderConfig, kind)
		}
	}()

	switch kind {
	case KindAnthropic:
		if creds.APIKey == "" {
			return nil, fmt.Errorf("%w: kind %q requires credentials.api_key", ErrBadProviderConfig, kind)
		}
		return []option.RequestOption{option.WithAPIKey(creds.APIKey), option.WithHTTPClient(hc)}, nil

	case KindBedrock:
		region, err := requireConfig(config, kind, "region")
		if err != nil {
			return nil, err
		}
		if creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
			return nil, fmt.Errorf("%w: kind %q requires credentials.access_key_id and secret_access_key", ErrBadProviderConfig, kind)
		}
		cfg := awssdk.Config{
			Region:      region,
			Credentials: awscreds.NewStaticCredentialsProvider(creds.AccessKeyID, creds.SecretAccessKey, ""),
		}
		return []option.RequestOption{bedrock.WithConfig(cfg), option.WithHTTPClient(hc)}, nil

	case KindVertexAnthropic:
		region, err := requireConfig(config, kind, "region")
		if err != nil {
			return nil, err
		}
		project, err := requireConfig(config, kind, "project_id")
		if err != nil {
			return nil, err
		}
		authCtx := context.WithValue(context.Background(), oauth2.HTTPClient, hc)
		gcreds, err := google.CredentialsFromJSONWithType(
			authCtx,
			[]byte(creds.ServiceAccountJSON),
			google.ServiceAccount,
			vertexScope,
		)
		if err != nil {
			return nil, fmt.Errorf("%w: kind %q service account was rejected", ErrBadProviderConfig, kind)
		}
		authed := oauth2.NewClient(authCtx, gcreds.TokenSource)
		return []option.RequestOption{
			vertex.WithCredentials(context.Background(), region, project, gcreds),
			option.WithHTTPClient(authed),
		}, nil

	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedKind, kind)
	}
}

func (s *anthropicStreamer) StreamChat(ctx context.Context, req ChatRequest) (ChatStream, error) {
	if err := validateRequest(req); err != nil {
		return nil, err
	}
	params, err := anthropicParams(req)
	if err != nil {
		return nil, err
	}
	// NewStreaming defers its transport error to the first Next(), so the
	// stream is returned here and connection failures surface from Next.
	return &anthropicStream{kind: s.kind, src: s.client.Messages.NewStreaming(ctx, params)}, nil
}

// anthropicParams translates the neutral ChatRequest into Messages API params.
//
// Reasoning parts are deliberately DROPPED on replay: Anthropic only accepts a
// thinking block together with its signature, which the neutral ChatPart does
// not carry, and an unsigned block is rejected outright.
func anthropicParams(req ChatRequest) (anthropic.MessageNewParams, error) {
	params := anthropic.MessageNewParams{
		Model:     req.Model,
		MaxTokens: int64(req.MaxTokens),
	}
	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.System}}
	}
	for _, m := range req.Messages {
		blocks := make([]anthropic.ContentBlockParamUnion, 0, len(m.Parts))
		for _, p := range m.Parts {
			switch p.Type {
			case PartText:
				if p.Text != "" {
					blocks = append(blocks, anthropic.NewTextBlock(p.Text))
				}
			case PartReasoning:
				continue
			case PartToolCall:
				input := p.ToolInput
				if len(input) == 0 {
					input = json.RawMessage(`{}`)
				}
				blocks = append(blocks, anthropic.NewToolUseBlock(p.ToolCallID, input, p.ToolName))
			case PartToolResult:
				blocks = append(blocks, anthropic.NewToolResultBlock(p.ToolCallID, toolResultText(p), p.IsError))
			default:
				return params, fmt.Errorf("%w: unknown part type %q", ErrBadProviderConfig, p.Type)
			}
		}
		if len(blocks) == 0 {
			continue
		}
		if m.Role == RoleAssistant {
			params.Messages = append(params.Messages, anthropic.NewAssistantMessage(blocks...))
			continue
		}
		params.Messages = append(params.Messages, anthropic.NewUserMessage(blocks...))
	}
	if len(params.Messages) == 0 {
		return params, fmt.Errorf("%w: request carried no sendable content", ErrBadProviderConfig)
	}
	for _, t := range req.Tools {
		tool, err := anthropicTool(t)
		if err != nil {
			return params, err
		}
		params.Tools = append(params.Tools, tool)
	}
	return params, nil
}

func anthropicTool(t ToolDef) (anthropic.ToolUnionParam, error) {
	schema, err := decodeToolSchema(t)
	if err != nil {
		return anthropic.ToolUnionParam{}, err
	}
	tool := anthropic.ToolParam{
		Name:        t.Name,
		InputSchema: anthropic.ToolInputSchemaParam{Properties: schema.properties, Required: schema.required, ExtraFields: schema.extra},
	}
	if t.Description != "" {
		tool.Description = anthropic.String(t.Description)
	}
	return anthropic.ToolUnionParam{OfTool: &tool}, nil
}

// anthropicStream turns the Messages SSE event sequence into the neutral
// StreamEvent sequence. Anthropic frames content as INDEXED blocks, so tool
// calls are tracked by block index between their start and stop events.
type anthropicStream struct {
	kind string
	src  *ssestream.Stream[anthropic.MessageStreamEventUnion]
	eventQueue

	toolBlocks map[int64]*anthropicToolBlock
	toolOrder  []int64
	usage      Usage
	// rawOutputTokens is the vendor's output count BEFORE normalization, and
	// reportedThinking the reasoning slice folded inside it.
	rawOutputTokens  int
	reportedThinking int
	stopReason       string
	emittedUsage     bool
	closeOnce        sync.Once
}

type anthropicToolBlock struct {
	id    string
	name  string
	input strings.Builder
}

func (s *anthropicStream) Next() (StreamEvent, error) {
	for {
		if ev, ok := s.pop(); ok {
			return ev, nil
		}
		if !s.src.Next() {
			if err := s.src.Err(); err != nil {
				var apiErr *anthropic.Error
				if errors.As(err, &apiErr) {
					return StreamEvent{}, providerStatusError(s.kind, apiErr.StatusCode)
				}
				return StreamEvent{}, providerError(s.kind, err)
			}
			if !s.emittedUsage {
				s.emittedUsage = true
				s.closeOpenToolBlocks()
				s.push(s.finalUsage())
				continue
			}
			return StreamEvent{}, io.EOF
		}
		s.consume(s.src.Current())
	}
}

func (s *anthropicStream) finalUsage() StreamEvent {
	// Anthropic's output_tokens is inclusive of thinking tokens (the API
	// documents output_tokens - thinking_tokens as the non-reasoning output),
	// so the same subtraction the OpenAI family needs applies here.
	u := normalizeUsage(s.usage.InputTokens, s.rawOutputTokens, s.reportedThinking, s.usage.CacheReadTokens, true)
	return usageEvent(u, s.stopReason)
}

func (s *anthropicStream) consume(ev anthropic.MessageStreamEventUnion) {
	switch ev.Type {
	case "message_start":
		s.absorbUsage(int(ev.Message.Usage.InputTokens), int(ev.Message.Usage.OutputTokens),
			int(ev.Message.Usage.OutputTokensDetails.ThinkingTokens), int(ev.Message.Usage.CacheReadInputTokens))

	case "content_block_start":
		if ev.ContentBlock.Type != "tool_use" {
			return
		}
		if s.toolBlocks == nil {
			s.toolBlocks = map[int64]*anthropicToolBlock{}
		}
		s.toolBlocks[ev.Index] = &anthropicToolBlock{id: ev.ContentBlock.ID, name: ev.ContentBlock.Name}
		s.toolOrder = append(s.toolOrder, ev.Index)
		s.push(StreamEvent{Type: EventToolCallStart, ToolCallID: ev.ContentBlock.ID, ToolName: ev.ContentBlock.Name})

	case "content_block_delta":
		switch ev.Delta.Type {
		case "text_delta":
			if ev.Delta.Text != "" {
				s.push(StreamEvent{Type: EventTextDelta, Text: ev.Delta.Text})
			}
		case "thinking_delta":
			if ev.Delta.Thinking != "" {
				s.push(StreamEvent{Type: EventReasoningDelta, Text: ev.Delta.Thinking})
			}
		case "input_json_delta":
			block, ok := s.toolBlocks[ev.Index]
			if !ok || ev.Delta.PartialJSON == "" {
				return
			}
			block.input.WriteString(ev.Delta.PartialJSON)
			s.push(StreamEvent{Type: EventToolInputDelta, ToolCallID: block.id, Text: ev.Delta.PartialJSON})
		}

	case "content_block_stop":
		block, ok := s.toolBlocks[ev.Index]
		if !ok {
			return
		}
		delete(s.toolBlocks, ev.Index)
		s.push(StreamEvent{
			Type:       EventToolCallEnd,
			ToolCallID: block.id,
			ToolName:   block.name,
			ToolInput:  accumulatedToolInput(block.input.String()),
		})

	case "message_delta":
		s.stopReason = anthropicStopReason(string(ev.Delta.StopReason))
		s.absorbUsage(int(ev.Usage.InputTokens), int(ev.Usage.OutputTokens),
			int(ev.Usage.OutputTokensDetails.ThinkingTokens), int(ev.Usage.CacheReadInputTokens))
	}
}

func (s *anthropicStream) closeOpenToolBlocks() {
	for _, index := range s.toolOrder {
		block, ok := s.toolBlocks[index]
		if !ok {
			continue
		}
		delete(s.toolBlocks, index)
		s.push(StreamEvent{
			Type:       EventToolCallEnd,
			ToolCallID: block.id,
			ToolName:   block.name,
			ToolInput:  accumulatedToolInput(block.input.String()),
		})
	}
	s.toolOrder = nil
}

// absorbUsage keeps the highest figure seen per counter: message_start reports
// the prompt side and message_delta the cumulative completion side, and a
// missing counter arrives as a zero we must not treat as a reset.
func (s *anthropicStream) absorbUsage(input, output, thinking, cacheRead int) {
	s.usage.InputTokens = max(s.usage.InputTokens, input)
	s.rawOutputTokens = max(s.rawOutputTokens, output)
	s.reportedThinking = max(s.reportedThinking, thinking)
	s.usage.CacheReadTokens = max(s.usage.CacheReadTokens, cacheRead)
}

// anthropicStopReason folds the vendor vocabulary into the three neutral
// values. Anything that is not "ran out of room" or "wants a tool" is a
// completed turn from the runtime's point of view.
func anthropicStopReason(reason string) string {
	switch reason {
	case "tool_use":
		return StopToolUse
	case "max_tokens":
		return StopMaxTokens
	default:
		return StopEndTurn
	}
}

func (s *anthropicStream) Close() error {
	var err error
	s.closeOnce.Do(func() { err = s.src.Close() })
	return err
}
