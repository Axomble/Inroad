package mail

import (
	"bytes"
	"context"
	"encoding/base64"
	netmail "net/mail"
	"strings"
	"testing"
)

// TestGraphSenderAssemblesMessageAndReturnsOurMessageID proves GraphSender
// reuses buildMessage (headers/threading/Message-ID identical to the SMTP and
// Gmail paths), forwards the access token, and returns OUR Message-ID header
// (embedded in the MIME handed to Graph) — not a Graph resource id — so reply
// matching stays transport-agnostic. Network-free via the transmit seam. The
// captured body is STANDARD base64 (Graph sendMail), decoded back to the raw
// RFC822 message before assertion.
func TestGraphSenderAssemblesMessageAndReturnsOurMessageID(t *testing.T) {
	var capturedB64 []byte
	var gotToken string
	g := &GraphSender{transmitFn: func(_ context.Context, at string, rawB64 []byte) error {
		gotToken, capturedB64 = at, rawB64
		return nil
	}}
	msg := Message{
		FromEmail: "rep@example.com", FromName: "Rep",
		To: "lead@example.com", Subject: "Hello", BodyText: "hi there",
		InReplyTo: "<parent@inroad>", References: "<root@inroad>",
	}
	msgID, err := g.Send(context.Background(), "tok", msg)
	if err != nil {
		t.Fatal(err)
	}
	if gotToken != "tok" {
		t.Fatalf("access token forwarded = %q, want tok", gotToken)
	}
	if msgID == "" {
		t.Fatal("expected our Message-ID header, got empty")
	}
	// Graph's sendMail body is the base64 of the raw MIME (StdEncoding). Decode
	// it and confirm it parses as the message we built.
	raw, err := base64.StdEncoding.DecodeString(string(capturedB64))
	if err != nil {
		t.Fatalf("captured body was not standard base64: %v", err)
	}
	parsed, err := netmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decoded MIME did not parse as a message: %v", err)
	}
	if got := parsed.Header.Get("Subject"); got != "Hello" {
		t.Fatalf("Subject header = %q, want Hello", got)
	}
	if got := parsed.Header.Get("To"); !strings.Contains(got, "lead@example.com") {
		t.Fatalf("To header = %q, want it to contain lead@example.com", got)
	}
	if got := parsed.Header.Get("In-Reply-To"); got != "<parent@inroad>" {
		t.Fatalf("In-Reply-To header = %q, want <parent@inroad>", got)
	}
	// The returned id is OUR built Message-ID (FindSendByMessageID keys on it), so
	// it must be the one embedded in the decoded MIME we hand to Graph.
	if !strings.Contains(string(raw), msgID) {
		t.Fatalf("returned Message-ID %q absent from decoded MIME", msgID)
	}
}
