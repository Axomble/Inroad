package mail

import (
	"context"
	"testing"
)

// TestMultiSenderDispatch proves Provider routing: a "gmail" job reaches the
// GmailSender (whose wire call is stubbed) and the SMTP sender — passed nil — is
// never consulted, so a mis-route would nil-panic instead of silently sending.
func TestMultiSenderDispatch(t *testing.T) {
	var gotGmail bool
	g := &GmailSender{transmitFn: func(context.Context, string, []byte) error {
		gotGmail = true
		return nil
	}}
	ms := NewMultiSender(nil, g, nil)
	msg := Message{FromEmail: "rep@example.com", To: "lead@example.com", Subject: "Hi", BodyText: "hello"}
	if _, err := ms.Send(context.Background(), OutboundJob{Provider: "gmail", AccessToken: "at"}, msg); err != nil {
		t.Fatal(err)
	}
	if !gotGmail {
		t.Fatal("gmail branch not taken")
	}
}

// TestMultiSenderDispatchM365 proves an "m365" job routes to the GraphSender
// (whose wire call is stubbed); SMTP and Gmail — passed nil — are never
// consulted, so a mis-route would nil-panic instead of silently sending.
func TestMultiSenderDispatchM365(t *testing.T) {
	var gotGraph bool
	var gotToken string
	gr := &GraphSender{transmitFn: func(_ context.Context, at string, _ []byte) error {
		gotGraph, gotToken = true, at
		return nil
	}}
	ms := NewMultiSender(nil, nil, gr)
	msg := Message{FromEmail: "rep@example.com", To: "lead@example.com", Subject: "Hi", BodyText: "hello"}
	if _, err := ms.Send(context.Background(), OutboundJob{Provider: "m365", AccessToken: "at"}, msg); err != nil {
		t.Fatal(err)
	}
	if !gotGraph {
		t.Fatal("m365 branch not taken")
	}
	if gotToken != "at" {
		t.Fatalf("access token forwarded = %q, want at", gotToken)
	}
}
