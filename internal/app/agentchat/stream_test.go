package agentchat

import "testing"

func TestSplitEnvelopePreservesJSONPayload(t *testing.T) {
	seq, payload, ok := splitEnvelope(`12:{"type":"text_delta","text":"a:b"}`)
	if !ok || seq != 12 || string(payload) != `{"type":"text_delta","text":"a:b"}` {
		t.Fatalf("seq=%d payload=%q ok=%v", seq, payload, ok)
	}
}

func TestIsTerminal(t *testing.T) {
	if !isTerminal([]byte(`{"type":"done"}`)) {
		t.Fatal("done event must be terminal")
	}
	if isTerminal([]byte(`{"type":"text_delta"}`)) || isTerminal([]byte("not-json")) {
		t.Fatal("non-terminal payload reported terminal")
	}
}
