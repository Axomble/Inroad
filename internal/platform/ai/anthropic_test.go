package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicParams(t *testing.T) {
	req := validChatRequest()
	req.System = "system"
	req.Tools = []ToolDef{{Name: "lookup", Description: "find", InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`)}}
	params, err := anthropicParams(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "model" || len(got["messages"].([]any)) != 1 || len(got["tools"].([]any)) != 1 {
		t.Fatalf("params = %s", raw)
	}
}

func TestAnthropicStreamUsageAndOpenTool(t *testing.T) {
	s := &anthropicStream{
		toolBlocks:       map[int64]*anthropicToolBlock{2: {id: "call", name: "lookup"}},
		toolOrder:        []int64{2},
		usage:            Usage{InputTokens: 11, CacheReadTokens: 4},
		rawOutputTokens:  9,
		reportedThinking: 3,
		stopReason:       StopToolUse,
	}
	s.toolBlocks[2].input.WriteString("{")
	s.closeOpenToolBlocks()
	end, ok := s.pop()
	if !ok || end.Type != EventToolCallEnd || string(end.ToolInput) != "null" {
		t.Fatalf("end = %+v", end)
	}
	usage := s.finalUsage()
	if usage.StopReason != StopToolUse || *usage.Usage != (Usage{InputTokens: 11, OutputTokens: 6, ReasoningTokens: 3, CacheReadTokens: 4}) {
		t.Fatalf("usage = %+v", usage)
	}
}

// cachingChatRequest is a mid-run agent transcript: system prompt, tools, and
// an assistant tool call answered by a tool result — the shape whose prefix has
// to be cached or every step re-prefills the whole conversation.
func cachingChatRequest() ChatRequest {
	req := validChatRequest()
	req.System = "you are an agent"
	req.Tools = []ToolDef{
		{Name: "lookup", Description: "find", InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`)},
		{Name: "write", InputSchema: json.RawMessage(`{"type":"object","properties":{"body":{"type":"string"}}}`)},
	}
	req.Messages = append(req.Messages,
		ChatMessage{Role: RoleAssistant, Parts: []ChatPart{
			{Type: PartText, Text: "looking"},
			{Type: PartToolCall, ToolCallID: "call", ToolName: "lookup", ToolInput: json.RawMessage(`{"q":"x"}`)},
		}},
		ChatMessage{Role: RoleUser, Parts: []ChatPart{{Type: PartToolResult, ToolCallID: "call", ToolOutput: json.RawMessage(`{"ok":true}`)}}},
	)
	return req
}

func marshalAnthropic(t *testing.T, req ChatRequest) map[string]any {
	t.Helper()
	params, err := anthropicParams(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	return body
}

// hasCacheControl reports whether the last element of a JSON array carries a
// cache_control breakpoint.
func hasCacheControl(t *testing.T, body map[string]any, key string) bool {
	t.Helper()
	items, ok := body[key].([]any)
	if !ok || len(items) == 0 {
		t.Fatalf("%s = %v", key, body[key])
	}
	last, ok := items[len(items)-1].(map[string]any)
	if !ok {
		t.Fatalf("%s last element = %v", key, items[len(items)-1])
	}
	_, marked := last["cache_control"]
	return marked
}

func TestAnthropicCachesStablePrefix(t *testing.T) {
	body := marshalAnthropic(t, cachingChatRequest())

	if !hasCacheControl(t, body, "tools") {
		t.Errorf("tools carry no cache breakpoint: %v", body["tools"])
	}
	if !hasCacheControl(t, body, "system") {
		t.Errorf("system carries no cache breakpoint: %v", body["system"])
	}

	// The final two turns each get a breakpoint: the tail is what the next
	// iteration of the agent loop reads back.
	messages := body["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages = %v", messages)
	}
	for offset := 1; offset <= 2; offset++ {
		message := messages[len(messages)-offset].(map[string]any)
		blocks := message["content"].([]any)
		last := blocks[len(blocks)-1].(map[string]any)
		if _, marked := last["cache_control"]; !marked {
			t.Errorf("message -%d last block carries no breakpoint: %v", offset, last)
		}
	}
	// The first turn is left unmarked: four is the API's hard ceiling.
	first := messages[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if _, marked := first["cache_control"]; marked {
		t.Error("spent a breakpoint on the oldest turn")
	}
}

func TestAnthropicCachingWithoutToolsOrSystem(t *testing.T) {
	req := validChatRequest()
	body := marshalAnthropic(t, req)
	if _, present := body["system"]; present {
		t.Fatalf("system = %v", body["system"])
	}
	messages := body["messages"].([]any)
	only := messages[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if _, marked := only["cache_control"]; !marked {
		t.Errorf("sole message carries no breakpoint: %v", only)
	}
}

// anthropicCachedSSE is a Messages turn served entirely from a warm cache:
// 9000 of the 9120 prompt tokens are cache reads, which is what a mid-run agent
// step should look like once the prefix is cached.
const anthropicCachedSSE = `event: message_start
data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"model","content":[],"stop_reason":null,"usage":{"input_tokens":120,"cache_read_input_tokens":9000,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

`

// TestAnthropicSendsCacheControlAndReadsCacheTokens closes the loop the params
// test can only open: the breakpoints reach the wire on a real SDK request, and
// the cache_read_input_tokens the provider answers with arrive on Usage. Before
// the breakpoints existed, Usage.CacheReadTokens was structurally always zero —
// the field was read but nothing ever asked the provider to cache.
func TestAnthropicSendsCacheControlAndReadsCacheTokens(t *testing.T) {
	var sent []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, anthropicCachedSSE)
	}))
	defer server.Close()
	t.Setenv("ANTHROPIC_BASE_URL", server.URL)

	factory := &streamerFactory{allowPrivateBaseURL: true}
	streamer, err := factory.Streamer(KindAnthropic, Credentials{APIKey: "k"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := streamer.StreamChat(context.Background(), cachingChatRequest())
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	got, err := drain(t, stream)
	if err != nil {
		t.Fatalf("drain = %v", err)
	}
	usage := got[len(got)-1]
	if usage.Type != EventUsage {
		t.Fatalf("terminal event = %+v", usage)
	}
	if usage.Usage.CacheReadTokens != 9000 || usage.Usage.InputTokens != 120 || usage.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v", usage.Usage)
	}

	// Four breakpoints on the wire: tools, system, and the last two turns.
	if breakpoints := strings.Count(string(sent), `"cache_control"`); breakpoints != anthropicCacheBreakpoints {
		t.Fatalf("cache_control count = %d, want %d: %s", breakpoints, anthropicCacheBreakpoints, sent)
	}
}

func TestAnthropicStopReason(t *testing.T) {
	if anthropicStopReason("tool_use") != StopToolUse || anthropicStopReason("max_tokens") != StopMaxTokens || anthropicStopReason("stop_sequence") != StopEndTurn {
		t.Fatal("stop reason mapping")
	}
}
