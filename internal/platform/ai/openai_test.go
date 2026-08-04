package ai

import (
	"encoding/json"
	"testing"

	"github.com/openai/openai-go/v3"
)

func TestOpenAIParams(t *testing.T) {
	req := validChatRequest()
	req.System = "system"
	req.Tools = []ToolDef{{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	modern, err := openAIParams(req, false)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := openAIParams(req, true)
	if err != nil {
		t.Fatal(err)
	}
	modernJSON, _ := json.Marshal(modern)
	legacyJSON, _ := json.Marshal(legacy)
	var m, l map[string]any
	json.Unmarshal(modernJSON, &m)
	json.Unmarshal(legacyJSON, &l)
	if m["max_completion_tokens"] != float64(128) || l["max_tokens"] != float64(128) {
		t.Fatalf("modern=%s legacy=%s", modernJSON, legacyJSON)
	}
	if len(m["messages"].([]any)) != 2 || len(m["tools"].([]any)) != 1 {
		t.Fatalf("params=%s", modernJSON)
	}
}

func TestOpenAIStreamToolAndUsage(t *testing.T) {
	s := &openAIStream{}
	s.consumeToolCall(openai.ChatCompletionChunkChoiceDeltaToolCall{Index: 0, ID: "call", Function: openai.ChatCompletionChunkChoiceDeltaToolCallFunction{Name: "lookup", Arguments: `{"q":`}})
	s.consumeToolCall(openai.ChatCompletionChunkChoiceDeltaToolCall{Index: 0, Function: openai.ChatCompletionChunkChoiceDeltaToolCallFunction{Arguments: `"x"}`}})
	s.closeOpenToolCalls()
	for _, want := range []string{EventToolCallStart, EventToolInputDelta, EventToolInputDelta, EventToolCallEnd} {
		got, ok := s.pop()
		if !ok || got.Type != want {
			t.Fatalf("event = %+v, want %s", got, want)
		}
	}
	s.usage.InputTokens, s.rawOutput, s.reasoning = 10, 8, 3
	usage := s.finalUsage()
	if *usage.Usage != (Usage{InputTokens: 10, OutputTokens: 5, ReasoningTokens: 3}) {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestOpenAIStopReason(t *testing.T) {
	if openAIStopReason("tool_calls") != StopToolUse || openAIStopReason("length") != StopMaxTokens || openAIStopReason("stop") != StopEndTurn {
		t.Fatal("stop reason mapping")
	}
}
