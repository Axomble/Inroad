package crm

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// stubReplyLabels is a test double for ReplyLabelReader. found=false models a
// key no label claims, which must degrade to the pre-taxonomy rule.
type stubReplyLabels struct {
	captures map[string]bool
	err      error
}

func (s stubReplyLabels) CapturesDeal(_ context.Context, _ uuid.UUID, key string) (bool, bool, error) {
	if s.err != nil {
		return false, false, s.err
	}
	captures, found := s.captures[key]
	return captures, found, nil
}

// TestCapturesDeal is the relaxation of the old hard `ReplyClass != "positive"`
// reject: auto-capture now fires for ANY label carrying captures_deal, and only
// falls back to the literal "positive" rule where the taxonomy cannot answer.
func TestCapturesDeal(t *testing.T) {
	cases := []struct {
		name   string
		reader ReplyLabelReader
		key    string
		want   bool
	}{
		{"unwired reader still captures positive", nil, "positive", true},
		{"unwired reader captures nothing else", nil, "demo_requested", false},
		{
			name:   "a custom label with captures_deal captures",
			reader: stubReplyLabels{captures: map[string]bool{"demo_requested": true}},
			key:    "demo_requested", want: true,
		},
		{
			name:   "positive with captures_deal turned OFF does not capture",
			reader: stubReplyLabels{captures: map[string]bool{"positive": false}},
			key:    "positive", want: false,
		},
		{
			name:   "a key no label claims falls back to the positive rule",
			reader: stubReplyLabels{captures: map[string]bool{"other": true}},
			key:    "positive", want: true,
		},
		{
			name:   "an unclaimed non-positive key does not capture",
			reader: stubReplyLabels{captures: map[string]bool{}},
			key:    "gone_label", want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService(nil)
			if tc.reader != nil {
				svc = NewService(nil, WithReplyLabels(tc.reader))
			}
			got, err := svc.capturesDeal(context.Background(), uuid.New(), tc.key)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("capturesDeal(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

// TestCapturesDealPropagatesLookupFailure: a taxonomy read that FAILS must not
// be silently read as "does not capture" — that would drop a deal on the floor.
func TestCapturesDealPropagatesLookupFailure(t *testing.T) {
	svc := NewService(nil, WithReplyLabels(stubReplyLabels{err: errors.New("boom")}))
	if _, err := svc.capturesDeal(context.Background(), uuid.New(), "positive"); err == nil {
		t.Fatal("expected the lookup failure to propagate")
	}
}

// TestCapturePositiveReplyGate proves the relaxed check is wired into the entry
// point: a non-capturing label is rejected with a validation error, and a
// capturing one gets PAST the gate (it then fails on the deliberately-nil
// store, which is a different error).
func TestCapturePositiveReplyGate(t *testing.T) {
	in := CaptureReplyInput{EnrollmentID: uuid.New(), SendID: uuid.New(), SenderEmail: "a@b.io"}

	blocked := NewService(nil, WithReplyLabels(stubReplyLabels{captures: map[string]bool{"negative": false}}))
	in.ReplyClass = "negative"
	if _, err := blocked.CapturePositiveReply(context.Background(), uuid.New(), in); !errors.Is(err, ErrValidation) {
		t.Fatalf("a non-capturing label must be refused, got %v", err)
	}

	allowed := NewService(nil, WithReplyLabels(stubReplyLabels{captures: map[string]bool{"demo_requested": true}}))
	in.ReplyClass = "demo_requested"
	_, err := allowed.CapturePositiveReply(context.Background(), uuid.New(), in)
	if errors.Is(err, ErrValidation) {
		t.Fatalf("a captures_deal label must pass the gate, got %v", err)
	}
	if err == nil {
		t.Fatal("expected the nil store to fail after the gate")
	}
}

// TestReplyEventName: the emitted event is derived from the label, so a custom
// capturing label no longer masquerades as reply.positive — while the builtin
// positive label reproduces the historical name byte for byte.
func TestReplyEventName(t *testing.T) {
	cases := map[string]string{
		"positive":       "reply.positive",
		"demo_requested": "reply.demo_requested",
		// Not a valid reply_labels key: degrade to the historical name rather
		// than emitting something a consumer cannot parse.
		"":            "reply.positive",
		"Not A Key":   "reply.positive",
		"1_leading":   "reply.positive",
		"drop table;": "reply.positive",
	}
	for key, want := range cases {
		if got := replyEventName(key); got != want {
			t.Fatalf("replyEventName(%q) = %q, want %q", key, got, want)
		}
	}
}
