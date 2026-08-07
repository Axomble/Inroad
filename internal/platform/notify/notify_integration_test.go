//go:build integration

package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// These tests drive the REAL smtp driver against a live mail catcher (Mailpit,
// wired into deploy/compose/docker-compose.dev.yml). That matters because the
// console driver cannot catch a delivery bug: it logs the recipient and returns
// nil no matter what. Sending an actual message and reading it back out of the
// catcher is the only test that proves an email is deliverable at all.
//
// Override the endpoints with INROAD_TEST_MAILPIT_SMTP / _API.

func mailpitSMTP() string {
	if v := os.Getenv("INROAD_TEST_MAILPIT_SMTP"); v != "" {
		return v
	}
	return "localhost:1025"
}

func mailpitAPI() string {
	if v := os.Getenv("INROAD_TEST_MAILPIT_API"); v != "" {
		return v
	}
	return "http://localhost:8025"
}

type mailpitMessage struct {
	ID      string `json:"ID"`
	Subject string `json:"Subject"`
	To      []struct {
		Address string `json:"Address"`
	} `json:"To"`
	From struct {
		Address string `json:"Address"`
	} `json:"From"`
}

// mailpitDo issues one request against the catcher's API, decoding the JSON
// response into out when out is non-nil. Every call goes through here so the
// context and timeout handling live in one place.
func mailpitDo(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, mailpitAPI()+path, http.NoBody)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: status %d", method, path, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type mailpitInbox struct {
	Messages []mailpitMessage `json:"messages"`
}

// requireMailpit skips (rather than fails) when no catcher is reachable, so the
// integration suite still runs on a machine without the dev stack up.
func requireMailpit(t *testing.T) {
	t.Helper()
	if err := mailpitDo(t.Context(), http.MethodGet, "/readyz", nil); err != nil {
		t.Skipf("mailpit not reachable at %s (%v) — start the dev stack to run this", mailpitAPI(), err)
	}
	// Start from an empty inbox so "exactly one message" assertions mean it.
	if err := mailpitDo(t.Context(), http.MethodDelete, "/api/v1/messages", nil); err != nil {
		t.Fatalf("clearing mailpit: %v", err)
	}
}

func inbox(t *testing.T) mailpitInbox {
	t.Helper()
	var got mailpitInbox
	if err := mailpitDo(t.Context(), http.MethodGet, "/api/v1/messages", &got); err != nil {
		t.Fatalf("listing messages: %v", err)
	}
	return got
}

// waitForMessage polls until a message is caught, since delivery completes
// asynchronously from the catcher's point of view.
func waitForMessage(t *testing.T) mailpitMessage {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if got := inbox(t); len(got.Messages) > 0 {
			return got.Messages[0]
		}
		if time.Now().After(deadline) {
			t.Fatal("no message arrived in the catcher within 10s")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func messageText(t *testing.T, id string) string {
	t.Helper()
	var body struct {
		Text string `json:"Text"`
		HTML string `json:"HTML"`
	}
	if err := mailpitDo(t.Context(), http.MethodGet, "/api/v1/message/"+id, &body); err != nil {
		t.Fatalf("fetching message %s: %v", id, err)
	}
	return body.Text + body.HTML
}

func catcherConfig(t *testing.T) Config {
	t.Helper()
	host, port, ok := strings.Cut(mailpitSMTP(), ":")
	if !ok {
		t.Fatalf("INROAD_TEST_MAILPIT_SMTP %q is not host:port", mailpitSMTP())
	}
	var p int
	if _, err := fmt.Sscanf(port, "%d", &p); err != nil {
		t.Fatalf("bad port in %q: %v", mailpitSMTP(), err)
	}
	return Config{
		Driver: "smtp", SMTPHost: host, SMTPPort: p,
		From: "no-reply@inroad.test",
		// The catcher speaks plaintext and advertises no AUTH; this is the
		// explicit opt-out, never a default.
		AllowPlaintext: true,
	}
}

// TestIntegrationSMTPDeliversToTheRecipient is the end-to-end proof of the
// recipient fix: a verification email built by the template constructor arrives
// at the catcher addressed to the intended user, with a usable link in the body.
func TestIntegrationSMTPDeliversToTheRecipient(t *testing.T) {
	requireMailpit(t)

	sender, err := New(catcherConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const to = "recipient@inroad.test"
	const link = "http://localhost:5173/verify-email?token=integration-token"
	if err := sender.Send(t.Context(), VerifyEmail(to, link)); err != nil {
		t.Fatalf("Send: %v", err)
	}

	got := waitForMessage(t)
	if len(got.To) != 1 || got.To[0].Address != to {
		t.Fatalf("envelope recipient = %+v, want exactly [%s]", got.To, to)
	}
	if got.Subject != "Verify your email" {
		t.Errorf("subject = %q, want %q", got.Subject, "Verify your email")
	}
	if body := messageText(t, got.ID); !strings.Contains(body, link) {
		t.Errorf("message body is missing the verify link %q", link)
	}
}

// TestIntegrationSMTPRejectsUnaddressedMessage confirms the guard holds on the
// real driver: nothing is delivered and the error is ErrNoRecipient, rather than
// the SMTP server rejecting an empty envelope with something opaque.
func TestIntegrationSMTPRejectsUnaddressedMessage(t *testing.T) {
	requireMailpit(t)

	sender, err := New(catcherConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = sender.Send(t.Context(), Message{Subject: "Verify your email", TextBody: "body"})
	if !errors.Is(err, ErrNoRecipient) {
		t.Fatalf("Send with an empty recipient: got %v, want ErrNoRecipient", err)
	}
	if got := inbox(t); len(got.Messages) != 0 {
		t.Fatalf("an unaddressed message reached the mail server: %+v", got.Messages)
	}
}
