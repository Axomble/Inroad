package ai

import (
	"encoding/json"
	"errors"
	"testing"

	"google.golang.org/genai"
)

func TestGoogleParamsAndToolResult(t *testing.T) {
	req := validChatRequest()
	req.System = "system"
	req.Messages = append(req.Messages,
		ChatMessage{Role: RoleAssistant, Parts: []ChatPart{{Type: PartToolCall, ToolCallID: "call", ToolName: "lookup", ToolInput: json.RawMessage(`{"q":"x"}`)}}},
		ChatMessage{Role: RoleUser, Parts: []ChatPart{{Type: PartToolResult, ToolCallID: "call", ToolOutput: json.RawMessage(`{"ok":true}`)}}},
	)
	contents, cfg, err := googleParams(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(contents) != 3 || cfg.SystemInstruction.Parts[0].Text != "system" {
		t.Fatalf("contents=%+v cfg=%+v", contents, cfg)
	}
	response := contents[2].Parts[0].FunctionResponse
	if response.Name != "lookup" || response.Response["ok"] != true {
		t.Fatalf("response=%+v", response)
	}
	req.Messages[2].Parts[0].ToolCallID = "missing"
	if _, _, err := googleParams(req); !errors.Is(err, ErrBadProviderConfig) {
		t.Fatalf("error=%v", err)
	}
}

func TestGoogleStreamEventsAndUsage(t *testing.T) {
	s := &googleStream{}
	s.consume(&genai.GenerateContentResponse{
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 5, ThoughtsTokenCount: 3, CachedContentTokenCount: 2},
		Candidates:    []*genai.Candidate{{FinishReason: genai.FinishReasonStop, Content: &genai.Content{Parts: []*genai.Part{{Text: "think", Thought: true}, {Text: "answer"}, {FunctionCall: &genai.FunctionCall{Name: "lookup", Args: map[string]any{"q": "x"}}}}}}},
	})
	for _, want := range []string{EventReasoningDelta, EventTextDelta, EventToolCallStart, EventToolInputDelta, EventToolCallEnd} {
		got, ok := s.pop()
		if !ok || got.Type != want {
			t.Fatalf("event=%+v want=%s", got, want)
		}
	}
	usage := s.finalUsage()
	if usage.StopReason != StopToolUse || *usage.Usage != (Usage{InputTokens: 10, OutputTokens: 5, ReasoningTokens: 3, CacheReadTokens: 2}) {
		t.Fatalf("usage=%+v", usage)
	}
}

// TestGoogleSynthesizedCallIDsAreUniquePerStream covers the AI Studio path that
// omits call ids: ids must be ordered within a turn AND distinct across turns of
// the same thread, or two persisted tool_call_ids collide and a tool result can
// be matched to the wrong call.
func TestGoogleSynthesizedCallIDsAreUniquePerStream(t *testing.T) {
	call := func(name string) *genai.FunctionCall {
		return &genai.FunctionCall{Name: name, Args: map[string]any{}}
	}
	idsFrom := func(s *googleStream) []string {
		s.pushToolCall(call("lookup"))
		s.pushToolCall(call("write"))
		var ids []string
		for {
			ev, ok := s.pop()
			if !ok {
				return ids
			}
			if ev.Type == EventToolCallEnd {
				ids = append(ids, ev.ToolCallID)
			}
		}
	}

	first, second := idsFrom(&googleStream{}), idsFrom(&googleStream{})
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("ids = %v, %v", first, second)
	}
	seen := map[string]bool{}
	for _, id := range append(append([]string{}, first...), second...) {
		if id == "" || seen[id] {
			t.Fatalf("duplicate or empty id %q across turns: %v, %v", id, first, second)
		}
		seen[id] = true
	}

	// A provider-supplied id is always preferred over a synthesized one.
	explicit := &googleStream{}
	explicit.pushToolCall(&genai.FunctionCall{ID: "vertex-id", Name: "lookup"})
	ev, _ := explicit.pop()
	if ev.ToolCallID != "vertex-id" {
		t.Fatalf("explicit id = %q", ev.ToolCallID)
	}
}

func TestGoogleArgsRejectsNonObject(t *testing.T) {
	if _, err := googleArgs(json.RawMessage("[]")); err == nil {
		t.Fatal("array accepted")
	}
}
