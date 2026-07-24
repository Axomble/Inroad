package mail

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	netmail "net/mail"
	"strings"
	"testing"
)

// TestGraphSenderDraftThenSendReturnsInternetMessageID proves GraphSender runs
// Graph's two-step MIME send: create a draft with the base64.StdEncoding MIME,
// then send it by id. The returned id is the AUTHORITATIVE internetMessageId
// Exchange assigned at draft creation — NOT our own MIME Message-Id (Exchange
// may rewrite it). That is the regression guard: reply/bounce matching keys on
// the value Exchange actually used. Network-free via the create/send seams.
func TestGraphSenderDraftThenSendReturnsInternetMessageID(t *testing.T) {
	const exchangeID = "<exchange-assigned@outlook.com>"
	var createdB64 []byte
	var createToken, sendToken, sentID string
	g := &GraphSender{
		createDraftFn: func(_ context.Context, at string, rawB64 []byte) (string, string, error) {
			createToken, createdB64 = at, rawB64
			return "draft-123", exchangeID, nil
		},
		sendDraftFn: func(_ context.Context, at, id string) error {
			sendToken, sentID = at, id
			return nil
		},
	}
	msg := Message{
		FromEmail: "rep@example.com", FromName: "Rep",
		To: "lead@example.com", Subject: "Hello", BodyText: "hi there",
		InReplyTo: "<parent@inroad>", References: "<root@inroad>",
	}
	got, err := g.Send(context.Background(), "tok", msg)
	if err != nil {
		t.Fatal(err)
	}
	if createToken != "tok" || sendToken != "tok" {
		t.Fatalf("access token forwarded = create %q / send %q, want tok", createToken, sendToken)
	}
	// The returned id must be the internetMessageId from draft creation, NOT the
	// MIME's own Message-Id — this is the Exchange-rewrite regression guard.
	if got != exchangeID {
		t.Fatalf("Send returned %q, want the internetMessageId %q", got, exchangeID)
	}
	// The send step must target the draft id create returned.
	if sentID != "draft-123" {
		t.Fatalf("send step called with id %q, want draft-123", sentID)
	}
	// Deterministic: the draft body is canonical base64.StdEncoding (NOT the
	// URL-safe encoding Gmail uses). buildMessage is itself non-deterministic
	// (random Message-Id, Date header), so we can't byte-match a fresh build;
	// instead decode with StdEncoding and re-encode — the round-trip must be
	// identical, which proves the body is exactly standard base64.
	raw, err := base64.StdEncoding.DecodeString(string(createdB64))
	if err != nil {
		t.Fatalf("draft body was not standard base64: %v", err)
	}
	if base64.StdEncoding.EncodeToString(raw) != string(createdB64) {
		t.Fatalf("draft body is not canonical base64.StdEncoding of the MIME")
	}
	parsed, err := netmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decoded MIME did not parse as a message: %v", err)
	}
	if h := parsed.Header.Get("Subject"); h != "Hello" {
		t.Fatalf("Subject header = %q, want Hello", h)
	}
	if h := parsed.Header.Get("To"); !strings.Contains(h, "lead@example.com") {
		t.Fatalf("To header = %q, want it to contain lead@example.com", h)
	}
	if h := parsed.Header.Get("In-Reply-To"); h != "<parent@inroad>" {
		t.Fatalf("In-Reply-To header = %q, want <parent@inroad>", h)
	}
	// Real Exchange-rewrite guard: the returned id must NOT be the Message-Id in
	// the MIME we submitted (parsed from the captured body). Exchange may rewrite
	// it, so we return the internetMessageId Graph reported instead.
	if mimeID := parsed.Header.Get("Message-Id"); mimeID == "" || got == mimeID {
		t.Fatalf("Send returned %q; must differ from the submitted MIME Message-Id %q", got, mimeID)
	}
}

// TestGraphSenderSendFailureDeletesDraft proves that when the send step fails
// after the draft was created, GraphSender best-effort deletes the orphaned
// draft (by the created id) and returns the send error.
func TestGraphSenderSendFailureDeletesDraft(t *testing.T) {
	sendErr := errors.New("graph: send: unexpected status 500")
	var deletedID string
	deleteCalled := false
	g := &GraphSender{
		createDraftFn: func(context.Context, string, []byte) (string, string, error) {
			return "draft-456", "<x@outlook.com>", nil
		},
		sendDraftFn: func(context.Context, string, string) error {
			return sendErr
		},
		deleteDraftFn: func(_ context.Context, _, id string) error {
			deleteCalled, deletedID = true, id
			return nil
		},
	}
	msg := Message{FromEmail: "rep@example.com", To: "lead@example.com", Subject: "Hi", BodyText: "hello"}
	got, err := g.Send(context.Background(), "tok", msg)
	if !errors.Is(err, sendErr) {
		t.Fatalf("Send err = %v, want the send error", err)
	}
	if got != "" {
		t.Fatalf("Send returned id %q on failure, want empty", got)
	}
	if !deleteCalled {
		t.Fatal("expected best-effort delete after send failure")
	}
	if deletedID != "draft-456" {
		t.Fatalf("delete called with id %q, want draft-456", deletedID)
	}
}

// TestGraphSenderCreateFailureDoesNotDelete proves that when draft creation
// fails, GraphSender returns that error with an empty id and NEVER attempts a
// delete — there is no draft to clean up, so the cleanup path must not fire.
func TestGraphSenderCreateFailureDoesNotDelete(t *testing.T) {
	createErr := errors.New("graph: draft: unexpected status 400")
	deleteCalled := false
	g := &GraphSender{
		createDraftFn: func(context.Context, string, []byte) (string, string, error) {
			return "", "", createErr
		},
		sendDraftFn: func(context.Context, string, string) error {
			t.Fatal("send step must not run when draft creation fails")
			return nil
		},
		deleteDraftFn: func(context.Context, string, string) error {
			deleteCalled = true
			return nil
		},
	}
	msg := Message{FromEmail: "rep@example.com", To: "lead@example.com", Subject: "Hi", BodyText: "hello"}
	got, err := g.Send(context.Background(), "tok", msg)
	if !errors.Is(err, createErr) {
		t.Fatalf("Send err = %v, want the create error", err)
	}
	if got != "" {
		t.Fatalf("Send returned id %q on create failure, want empty", got)
	}
	if deleteCalled {
		t.Fatal("delete must not be called when no draft was created")
	}
}
