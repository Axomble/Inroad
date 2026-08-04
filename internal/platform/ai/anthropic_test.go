package ai

import (
	"encoding/json"
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

func TestAnthropicStopReason(t *testing.T) {
	if anthropicStopReason("tool_use") != StopToolUse || anthropicStopReason("max_tokens") != StopMaxTokens || anthropicStopReason("stop_sequence") != StopEndTurn {
		t.Fatal("stop reason mapping")
	}
}
