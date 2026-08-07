package agentrun

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/agentchat"
	"github.com/inroad/inroad/internal/platform/ai"
)

// textStream builds a streamer that emits one text delta and ends, which is the
// shape a real one-shot draft call takes.
func textStream(text string) *fakeStreamer {
	return &fakeStreamer{turns: [][]ai.StreamEvent{{{Type: ai.EventTextDelta, Text: text}}}}
}

// draftRuntime is a Runtime with ONLY the fields DraftReply uses — proof the
// draft path needs no store, tools, publisher or approvals.
func draftRuntime(streamer ai.ChatStreamer) *Runtime {
	return &Runtime{Models: fakeResolver{streamer: streamer}}
}

func twoTurnInput() DraftReplyInput {
	return DraftReplyInput{
		ContactFirstName: "Dana",
		Subject:          "Quick question about onboarding",
		FromCampaign:     true,
		Turns: []DraftTurn{
			{FromContact: false, Text: "Hi Dana, are you the right person for onboarding?"},
			{FromContact: true, Text: "Yes — what does setup involve?"},
		},
	}
}

func TestDraftReplyReturnsNormalizedText(t *testing.T) {
	streamer := textStream("  Happy to explain.\n\nSetup takes about a day.  ")
	got, err := draftRuntime(streamer).DraftReply(context.Background(), uuid.New(), twoTurnInput())
	if err != nil {
		t.Fatalf("DraftReply: %v", err)
	}
	if got != "Happy to explain.\n\nSetup takes about a day." {
		t.Fatalf("draft = %q", got)
	}
	if len(streamer.requests) != 1 {
		t.Fatalf("provider called %d times, want 1", len(streamer.requests))
	}
	request := streamer.requests[0]
	if request.MaxTokens <= 0 || request.MaxTokens > draftMaxOutputTokens {
		t.Fatalf("MaxTokens = %d, want a positive value at most %d", request.MaxTokens, draftMaxOutputTokens)
	}
	if len(request.Tools) != 0 {
		t.Fatalf("draft request advertised %d tools, want none (a draft is not an agent run)", len(request.Tools))
	}
}

// The workspace's additional_instructions are tone/brand guidance and must
// reach the model; fakeResolver returns "Be direct."
func TestDraftReplyIncludesWorkspaceInstructions(t *testing.T) {
	streamer := textStream("Sure.")
	if _, err := draftRuntime(streamer).DraftReply(context.Background(), uuid.New(), twoTurnInput()); err != nil {
		t.Fatalf("DraftReply: %v", err)
	}
	system := streamer.requests[0].System
	if !strings.Contains(system, draftSystemPrompt) {
		t.Fatal("system prompt lost the stable draft instructions")
	}
	if !strings.Contains(system, "Be direct.") {
		t.Fatalf("system prompt omitted the workspace instructions: %q", system)
	}
}

func TestDraftReplyTranscriptLabelsDirectionAndOrder(t *testing.T) {
	streamer := textStream("Sure.")
	if _, err := draftRuntime(streamer).DraftReply(context.Background(), uuid.New(), twoTurnInput()); err != nil {
		t.Fatalf("DraftReply: %v", err)
	}
	transcript := streamer.requests[0].Messages[0].Parts[0].Text
	ours := strings.Index(transcript, "Us: Hi Dana")
	theirs := strings.Index(transcript, "Contact: Yes — what does setup involve?")
	if ours < 0 || theirs < 0 {
		t.Fatalf("transcript missing a labelled turn:\n%s", transcript)
	}
	if ours > theirs {
		t.Fatal("transcript is not oldest-first")
	}
	for _, want := range []string{"Quick question about onboarding", "Dana", "cold outreach"} {
		if !strings.Contains(transcript, want) {
			t.Fatalf("transcript omitted %q:\n%s", want, transcript)
		}
	}
	if strings.Contains(transcript, "omitted") {
		t.Fatal("a short transcript must not claim it was truncated")
	}
}

// A thread longer than the turn cap keeps the NEWEST turns — the reply answers
// the last inbound message, so the tail is what must survive — and says in-band
// that it is a fragment.
func TestDraftReplyTruncatesOldestTurnsFirst(t *testing.T) {
	in := DraftReplyInput{Subject: "Long thread"}
	for i := range draftMaxTurns + 5 {
		in.Turns = append(in.Turns, DraftTurn{FromContact: i%2 == 1, Text: "message number " + string(rune('a'+i))})
	}
	streamer := textStream("Sure.")
	if _, err := draftRuntime(streamer).DraftReply(context.Background(), uuid.New(), in); err != nil {
		t.Fatalf("DraftReply: %v", err)
	}
	transcript := streamer.requests[0].Messages[0].Parts[0].Text
	if strings.Contains(transcript, "message number a") {
		t.Fatal("oldest turn survived the turn cap")
	}
	newest := in.Turns[len(in.Turns)-1].Text
	if !strings.Contains(transcript, newest) {
		t.Fatalf("newest turn %q was dropped", newest)
	}
	if !strings.Contains(transcript, "Earlier messages have been omitted") {
		t.Fatal("a truncated transcript must say so in-band")
	}
	if got := strings.Count(transcript, "\nUs: ") + strings.Count(transcript, "\nContact: "); got > draftMaxTurns {
		t.Fatalf("transcript carries %d turns, want at most %d", got, draftMaxTurns)
	}
}

// The character cap bites even when the turn count is legal.
func TestDraftReplyTruncatesOnTotalCharacters(t *testing.T) {
	in := DraftReplyInput{Turns: []DraftTurn{
		{FromContact: true, Text: "OLDEST " + strings.Repeat("x", draftMaxTranscriptRunes)},
		{FromContact: true, Text: "NEWEST question"},
	}}
	streamer := textStream("Sure.")
	if _, err := draftRuntime(streamer).DraftReply(context.Background(), uuid.New(), in); err != nil {
		t.Fatalf("DraftReply: %v", err)
	}
	transcript := streamer.requests[0].Messages[0].Parts[0].Text
	if strings.Contains(transcript, "OLDEST") {
		t.Fatal("the oversized oldest turn was not dropped")
	}
	if !strings.Contains(transcript, "NEWEST question") {
		t.Fatal("the newest turn was dropped")
	}
}

// A single turn that alone busts the cap is CLIPPED rather than dropped: a
// transcript without the message being replied to is useless.
func TestDraftReplyClipsALoneOversizedTurn(t *testing.T) {
	in := DraftReplyInput{Turns: []DraftTurn{
		{FromContact: true, Text: strings.Repeat("y", draftMaxTranscriptRunes*2)},
	}}
	streamer := textStream("Sure.")
	if _, err := draftRuntime(streamer).DraftReply(context.Background(), uuid.New(), in); err != nil {
		t.Fatalf("DraftReply: %v", err)
	}
	transcript := streamer.requests[0].Messages[0].Parts[0].Text
	// The header/footer prose contributes a handful of its own 'y's, so the
	// bound is the cap plus that slack — the point is that the 24000-rune turn
	// no longer fits, not an exact count.
	if got := strings.Count(transcript, "y"); got > draftMaxTranscriptRunes+100 {
		t.Fatalf("clipped turn still carries %d 'y' runes, want at most ~%d", got, draftMaxTranscriptRunes)
	}
	if !strings.Contains(transcript, "Contact: yyy") {
		t.Fatal("the lone turn was dropped entirely instead of clipped")
	}
}

func TestDraftReplyRejectsAnEmptyConversation(t *testing.T) {
	streamer := textStream("Sure.")
	cases := map[string]DraftReplyInput{
		"no turns":         {Subject: "Hi"},
		"blank turns only": {Turns: []DraftTurn{{FromContact: true, Text: "   \n  "}}},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := draftRuntime(streamer).DraftReply(context.Background(), uuid.New(), in); err == nil {
				t.Fatal("DraftReply on an empty conversation = nil error, want an error")
			}
			if len(streamer.requests) != 0 {
				t.Fatal("the provider was called for an empty conversation")
			}
		})
	}
}

// A model-resolution failure surfaces wrapping ai.ErrNoModel so a caller can
// tell "configure a model" apart from "the provider broke".
func TestDraftReplyResolveFailureWrapsErrNoModel(t *testing.T) {
	r := &Runtime{Models: erroringResolver{err: errors.Join(ai.ErrNoModel, errors.New("nothing enabled"))}}
	_, err := r.DraftReply(context.Background(), uuid.New(), twoTurnInput())
	if !errors.Is(err, ai.ErrNoModel) {
		t.Fatalf("DraftReply with no model = %v, want an error wrapping ai.ErrNoModel", err)
	}
}

// A mid-stream failure must surface as an error, NOT as the partial text that
// arrived before it: a half-written reply looks finished in an editor.
func TestDraftReplyStreamErrorSurfacesRatherThanPartialText(t *testing.T) {
	boom := errors.New("provider hung up")
	streamer := &fakeStreamer{
		turns:          [][]ai.StreamEvent{{{Type: ai.EventTextDelta, Text: "Happy to ex"}}},
		terminalErrors: []error{boom},
	}
	got, err := draftRuntime(streamer).DraftReply(context.Background(), uuid.New(), twoTurnInput())
	if !errors.Is(err, boom) {
		t.Fatalf("DraftReply with a stream error = %v, want %v", err, boom)
	}
	if got != "" {
		t.Fatalf("DraftReply returned partial text %q alongside an error", got)
	}
}

// A stream that completes with nothing usable is a failure, not an empty draft.
func TestDraftReplyEmptyOutputIsAnError(t *testing.T) {
	got, err := draftRuntime(textStream("   \n  ")).DraftReply(context.Background(), uuid.New(), twoTurnInput())
	if err == nil {
		t.Fatalf("DraftReply with blank output = %q, nil, want an error", got)
	}
}

type erroringResolver struct {
	err error
}

func (e erroringResolver) Resolve(context.Context, uuid.UUID, string) (agentchat.ResolvedModel, error) {
	return agentchat.ResolvedModel{}, e.err
}
func (erroringResolver) Instructions(context.Context, uuid.UUID) (string, error) { return "", nil }

func TestNormalizeDraft(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"strips a wrapping double quote", `"Sounds good, let's talk Tuesday."`, "Sounds good, let's talk Tuesday."},
		{"strips smart quotes", "\u201cSounds good.\u201d", "Sounds good."},
		{"keeps an inner quotation", `They said "yes" already.`, `They said "yes" already.`},
		{"keeps a leading quotation that is not a wrapper", `"yes" is what they said`, `"yes" is what they said`},
		{"drops an invented subject line", "Subject: Re: onboarding\n\nHappy to help.", "Happy to help."},
		{"drops a subject-only draft", "Subject: Re: onboarding", ""},
		{"drops a bracketed signature placeholder", "Talk soon.\n\n[Your Name]", "Talk soon."},
		{"drops a mustache placeholder line", "Talk soon.\n{{first_name}}", "Talk soon."},
		{"keeps a placeholder inside prose", "Send it to [the address on file] please.", "Send it to [the address on file] please."},
		{"normalizes CRLF", "One.\r\n\r\nTwo.", "One.\n\nTwo."},
		{"collapses blank-line runs", "One.\n\n\n\nTwo.", "One.\n\nTwo."},
		{"trims trailing line whitespace", "One.   \nTwo.  ", "One.\nTwo."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeDraft(tc.raw); got != tc.want {
				t.Fatalf("normalizeDraft(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// The transcript budget counts RUNES, not bytes. Regression guard: a byte
// budget would give a CJK thread roughly a third of the context an English one
// gets, silently penalising exactly the languages the system prompt invites the
// model to reply in. A multi-byte transcript that fits the rune budget must
// survive whole.
func TestDraftReplyTranscriptBudgetCountsRunesNotBytes(t *testing.T) {
	// 3 bytes per rune, sized to bust a byte budget while fitting a rune one.
	body := strings.Repeat("あ", draftMaxTranscriptRunes/2)
	if len(body) <= draftMaxTranscriptRunes {
		t.Fatalf("test body is %d bytes; it must exceed the %d budget to be meaningful", len(body), draftMaxTranscriptRunes)
	}
	in := DraftReplyInput{Turns: []DraftTurn{
		{FromContact: false, Text: "OLDEST turn"},
		{FromContact: true, Text: body},
	}}
	streamer := textStream("はい。")
	if _, err := draftRuntime(streamer).DraftReply(context.Background(), uuid.New(), in); err != nil {
		t.Fatalf("DraftReply: %v", err)
	}
	transcript := streamer.requests[0].Messages[0].Parts[0].Text
	if !strings.Contains(transcript, "OLDEST turn") {
		t.Fatal("a transcript within the RUNE budget was truncated as if the budget were bytes")
	}
	if strings.Contains(transcript, "omitted") {
		t.Fatal("transcript claimed truncation despite fitting the rune budget")
	}
	if got := utf8.RuneCountInString(body); !strings.Contains(transcript, body) {
		t.Fatalf("the %d-rune turn did not survive intact", got)
	}
}
