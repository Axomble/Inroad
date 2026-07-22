package mail

import (
	"bytes"
	"context"
	netmail "net/mail"
	"strings"
	"testing"
)

// TestGmailSenderAssemblesMessageAndReturnsOurMessageID proves GmailSender
// reuses buildMessage (headers/threading/Message-ID identical to the SMTP path),
// forwards the access token to the transport, and returns OUR Message-ID header
// (embedded in the RAW handed to Gmail) — not a Gmail resource id — so reply
// matching stays transport-agnostic. Network-free via the transmit seam.
func TestGmailSenderAssemblesMessageAndReturnsOurMessageID(t *testing.T) {
	var captured []byte
	var gotToken string
	g := &GmailSender{transmitFn: func(_ context.Context, at string, raw []byte) error {
		gotToken, captured = at, raw
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
	parsed, err := netmail.ReadMessage(bytes.NewReader(captured))
	if err != nil {
		t.Fatalf("assembled RAW did not parse as a message: %v", err)
	}
	if got := parsed.Header.Get("Subject"); got != "Hello" {
		t.Fatalf("Subject header = %q, want Hello", got)
	}
	if got := parsed.Header.Get("In-Reply-To"); got != "<parent@inroad>" {
		t.Fatalf("In-Reply-To header = %q, want <parent@inroad>", got)
	}
	// The returned id is OUR built Message-ID (FindSendByMessageID keys on it), so
	// it must be the one embedded in the RAW we hand to Gmail.
	if !strings.Contains(string(captured), msgID) {
		t.Fatalf("returned Message-ID %q absent from assembled RAW", msgID)
	}
}
