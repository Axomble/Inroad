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
	ms := NewMultiSender(nil, g)
	msg := Message{FromEmail: "rep@example.com", To: "lead@example.com", Subject: "Hi", BodyText: "hello"}
	if _, err := ms.Send(context.Background(), OutboundJob{Provider: "gmail", AccessToken: "at"}, msg); err != nil {
		t.Fatal(err)
	}
	if !gotGmail {
		t.Fatal("gmail branch not taken")
	}
}
