package agentrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/ai"
)

// This file is the reply-draft one-shot: given a thread transcript, ask the
// workspace's smart model for suggested plain-text reply body. It lives beside
// GenerateTitle because it is the same concern — a single, tool-less,
// non-agentic provider call using only r.Models — and it deliberately does NOT
// send anything: it returns text for a human to edit and send through the
// separate reply endpoint (docs/security.md invariant 48).
//
// It differs from GenerateTitle in ONE important respect: GenerateTitle
// degrades to a heuristic on every failure because a conversation title is
// incidental, whereas a draft is what the user explicitly asked for. Every
// failure here is returned, so the caller can explain it.

const (
	// draftMaxTurns / draftMaxTranscriptRunes bound the transcript so a long
	// thread cannot blow the model's context window (or its cost). Both are
	// applied by dropping the OLDEST turns first: the newest inbound message is
	// what the reply actually answers, so the tail is the part that must
	// survive. A truncated transcript says so in-band, so the model knows it is
	// looking at a fragment rather than the start of the conversation.
	//
	// The budget counts RUNES, not bytes, and every measurement below agrees on
	// that unit. Bytes would silently punish exactly the languages the system
	// prompt invites the model to reply in — 12000 bytes is only ~4000 CJK
	// characters — giving a Japanese thread a quarter of the context an English
	// one gets. Runes are also the closer proxy for tokens in those scripts.
	//
	// This is a local cap rather than agentchat.Prune (which the run loop uses)
	// because Prune trims a DIFFERENT shape: it blanks reasoning parts and
	// truncates tool-result payloads in place, and never drops a message. A
	// draft transcript is a single plain-text user message with no reasoning and
	// no tool results, so Prune would find nothing to trim and return
	// ErrContextExhausted — rejecting a long thread instead of shortening it.
	draftMaxTurns           = 20
	draftMaxTranscriptRunes = 12000
	// draftMaxOutputTokens caps the draft's length. A reply body is short; this
	// is a cost/latency bound, not a quality one, and it is further clamped to
	// the model's own MaxOutputTokens.
	draftMaxOutputTokens = 600
	// draftTimeout bounds the whole provider call. The ai package deliberately
	// sets no overall client timeout (only dial/header waits are bounded, since
	// a generation may legitimately run for minutes), so this context deadline
	// is the ONLY thing standing between a hung provider and a pinned HTTP
	// handler. No retry is layered here either: ai's withStreamRetry already
	// retries failures that happen before the first event.
	draftTimeout = 30 * time.Second
)

// draftSystemPrompt is the stable instruction block. The workspace's own
// additional_instructions (tone/brand guidance) are appended when non-empty,
// the same composition agentchat.SystemPrompt uses for a run.
const draftSystemPrompt = `You are drafting the next reply in an ongoing email conversation. You write AS the sender ("Us"), replying to the contact.

Rules:
- Return ONLY the body of the reply. No subject line, no headers, no quoted original, no commentary about the draft itself.
- Plain text only. No HTML, no Markdown formatting.
- Reply in the same language the contact wrote in, and match their level of formality.
- Be concise: two or three short paragraphs at most.
- Use only facts already present in the conversation. Never invent prices, dates, links, names, or commitments.
- Never leave a placeholder such as [Your Name], [Company], or TBD. If a detail is missing, write around it.
- Do not append a signature block or sign off with a name.`

// DraftTurn is one message of the thread as the drafter sees it: which side
// sent it and its plain-text body. Deliberately not the inbox's Message type —
// app packages do not import each other, so the caller projects its own rows
// onto this shape (the adapter at the composition root).
type DraftTurn struct {
	// FromContact is true for a message the contact sent us, false for one we
	// sent them.
	FromContact bool
	Text        string
}

// DraftReplyInput is everything the prompt is built from. No workspace id here
// — that is a separate argument, so a caller cannot accidentally draft against
// a workspace other than the one it authorized.
type DraftReplyInput struct {
	// ContactFirstName is used to address the contact naturally. "" when the
	// thread has no linked contact (a legacy direct-send match).
	ContactFirstName string
	// Subject is the thread's subject line — the only campaign context
	// available here. It is given to the model as context, NOT as something to
	// restate: the draft is a body, never a subject.
	Subject string
	// FromCampaign marks a thread linked to a campaign, i.e. a conversation WE
	// opened as cold outreach rather than one that arrived unprompted. It
	// changes the register the model should write in.
	FromCampaign bool
	// Turns is the thread's messages, OLDEST FIRST.
	Turns []DraftTurn
}

// DraftReply generates a suggested plain-text reply body for the conversation
// in input, using workspaceID's configured smart model (not the fast one — a
// user-facing draft is worth the quality difference).
//
// It returns an error rather than a fallback on every failure path. A model
// resolution failure wraps ai.ErrNoModel, so a caller can tell "this workspace
// has no AI model configured" (a settings problem the user can fix) apart from
// "the provider call failed"; a provider that outruns draftTimeout surfaces as
// context.DeadlineExceeded.
//
// Nothing about the prompt or the conversation is logged — only ids, the model
// name, token counts and durations (the same discipline the reply classifier
// holds, docs/security.md invariant 21).
func (r *Runtime) DraftReply(ctx context.Context, workspaceID uuid.UUID, input DraftReplyInput) (string, error) {
	transcript, truncated := buildDraftTranscript(input)
	if transcript == "" {
		return "", errors.New("agentrun: cannot draft a reply for an empty conversation")
	}
	model, err := r.Models.Resolve(ctx, workspaceID, ai.SentinelSmartModel)
	if err != nil {
		return "", fmt.Errorf("agentrun: resolve draft model: %w", err)
	}
	// Instructions are tone/brand guidance, not a hard requirement: a failure
	// to read them is still a failure to honor the workspace's configuration,
	// so it is returned rather than silently dropping the workspace's voice.
	instructions, err := r.Models.Instructions(ctx, workspaceID)
	if err != nil {
		return "", fmt.Errorf("agentrun: read workspace instructions: %w", err)
	}
	system := draftSystemPrompt
	if strings.TrimSpace(instructions) != "" {
		system += "\n\n" + strings.TrimSpace(instructions)
	}
	maxTokens := draftMaxOutputTokens
	// A model whose catalog entry reports no output cap (0) must not become a
	// zero-token request; only a real, smaller cap narrows ours.
	if model.MaxOutputTokens > 0 {
		maxTokens = min(model.MaxOutputTokens, draftMaxOutputTokens)
	}

	dctx, cancel := context.WithTimeout(ctx, draftTimeout)
	defer cancel()
	started := time.Now()
	text, usage, err := streamDraft(dctx, model.Streamer, ai.ChatRequest{
		Model:     model.Name,
		System:    system,
		Messages:  []ai.ChatMessage{{Role: ai.RoleUser, Parts: []ai.ChatPart{{Type: ai.PartText, Text: transcript}}}},
		MaxTokens: maxTokens,
	})
	if err != nil {
		// Distinguish OUR budget expiring from the caller giving up: only the
		// former is a provider timeout the caller should report as such.
		if errors.Is(dctx.Err(), context.DeadlineExceeded) && !errors.Is(ctx.Err(), context.Canceled) {
			return "", fmt.Errorf("agentrun: draft reply timed out after %s: %w", draftTimeout, context.DeadlineExceeded)
		}
		return "", fmt.Errorf("agentrun: draft reply: %w", err)
	}
	draft := normalizeDraft(text)
	if draft == "" {
		// An accepted call that produced nothing usable is still a failure the
		// user must see — returning "" would render as a mysteriously empty
		// editor.
		return "", errors.New("agentrun: model returned no usable draft text")
	}
	r.logger().InfoContext(ctx, "inbox_reply_drafted",
		"workspace_id", workspaceID, "model", model.ID,
		"turns", len(input.Turns), "transcript_truncated", truncated,
		"input_tokens", usage.InputTokens, "output_tokens", usage.OutputTokens,
		"draft_chars", len(draft), "duration_ms", time.Since(started).Milliseconds())
	return draft, nil
}

// streamDraft runs one provider turn to completion, accumulating text deltas.
// Reasoning deltas are discarded: they are the model's scratch work, not part
// of the reply body.
func streamDraft(ctx context.Context, streamer ai.ChatStreamer, request ai.ChatRequest) (string, ai.Usage, error) {
	stream, err := streamer.StreamChat(ctx, request)
	if err != nil {
		return "", ai.Usage{}, err
	}
	defer func() { _ = stream.Close() }()
	var out strings.Builder
	var usage ai.Usage
	for {
		event, err := stream.Next()
		if errors.Is(err, io.EOF) {
			return out.String(), usage, nil
		}
		if err != nil {
			// A mid-stream failure is NOT salvaged into whatever text arrived
			// before it: a half-written reply looks finished in an editor and
			// would be sent as-is.
			return "", ai.Usage{}, err
		}
		switch event.Type {
		case ai.EventTextDelta:
			out.WriteString(event.Text)
		case ai.EventUsage:
			if event.Usage != nil {
				addUsage(&usage, *event.Usage)
			}
		}
	}
}

// buildDraftTranscript renders the conversation the model replies to, oldest
// first, and reports whether anything was dropped to fit the caps. Turns with
// no text at all (an attachment-only or HTML-only message we could not project)
// are skipped rather than rendered as an empty labelled line, which would read
// to the model as someone sending a blank email.
func buildDraftTranscript(input DraftReplyInput) (string, bool) {
	turns := make([]DraftTurn, 0, len(input.Turns))
	for _, t := range input.Turns {
		if strings.TrimSpace(t.Text) != "" {
			turns = append(turns, t)
		}
	}
	if len(turns) == 0 {
		return "", false
	}
	truncated := false
	if len(turns) > draftMaxTurns {
		turns = turns[len(turns)-draftMaxTurns:]
		truncated = true
	}
	// Rune-budget cap, again oldest-first. The newest turn is kept even if it
	// alone exceeds the cap (its own text is then clipped): a transcript
	// without the message being replied to is useless.
	for len(turns) > 1 && transcriptRunes(turns) > draftMaxTranscriptRunes {
		turns = turns[1:]
		truncated = true
	}
	if len(turns) == 1 && utf8.RuneCountInString(turns[0].Text) > draftMaxTranscriptRunes {
		turns[0].Text = clipRunes(turns[0].Text, draftMaxTranscriptRunes)
		truncated = true
	}

	var b strings.Builder
	b.WriteString("Conversation so far, oldest first. \"Contact\" is the person you are replying to; \"Us\" is the sender you write as.\n")
	if input.Subject != "" {
		b.WriteString("Subject: ")
		b.WriteString(input.Subject)
		b.WriteString("\n")
	}
	if input.ContactFirstName != "" {
		b.WriteString("Contact's first name: ")
		b.WriteString(input.ContactFirstName)
		b.WriteString("\n")
	}
	if input.FromCampaign {
		b.WriteString("This conversation began with cold outreach we sent.\n")
	}
	if truncated {
		b.WriteString("(Earlier messages have been omitted.)\n")
	}
	b.WriteString("\n")
	for _, t := range turns {
		label := "Us"
		if t.FromContact {
			label = "Contact"
		}
		b.WriteString(label)
		b.WriteString(": ")
		b.WriteString(strings.TrimSpace(t.Text))
		b.WriteString("\n\n")
	}
	b.WriteString("Write the reply body now.")
	return b.String(), truncated
}

func transcriptRunes(turns []DraftTurn) int {
	total := 0
	for _, t := range turns {
		total += utf8.RuneCountInString(t.Text)
	}
	return total
}

// clipRunes truncates to at most limit runes, never splitting one.
func clipRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return strings.TrimSpace(string(runes[:limit]))
}

// normalizeDraft turns raw model output into something safe to drop into a
// plain-text editor: no wrapping quotes, no invented Subject: line, no
// bracketed placeholder signature, no CRLF, and no runs of blank lines.
//
// It is deliberately conservative — it removes only shapes the prompt already
// forbids, so a legitimate reply is never mangled.
func normalizeDraft(raw string) string {
	text := strings.ReplaceAll(raw, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.TrimSpace(text)
	text = stripSubjectLine(text)
	text = stripWrappingQuotes(text)
	text = dropPlaceholderLines(text)
	return collapseBlankLines(text)
}

// stripSubjectLine drops a leading "Subject: ..." line (and any blank line
// after it). Models add one despite being told not to; leaving it in would put
// a header in the middle of the body.
func stripSubjectLine(text string) string {
	first, rest, found := strings.Cut(text, "\n")
	if !found {
		// A draft that is ONLY a subject line is not a body; treat it as empty
		// so the caller reports a failure rather than sending a header.
		if hasSubjectPrefix(first) {
			return ""
		}
		return text
	}
	if !hasSubjectPrefix(first) {
		return text
	}
	return strings.TrimSpace(rest)
}

func hasSubjectPrefix(line string) bool {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	return strings.HasPrefix(lower, "subject:") || strings.HasPrefix(lower, "re: subject:")
}

// stripWrappingQuotes removes ONE matched pair of quotes wrapping the whole
// draft. It leaves a draft that merely starts with a quotation alone: the
// closing quote must be the very last character AND there must be no other
// occurrence of it in between, so `"yes" is what they said` is untouched.
func stripWrappingQuotes(text string) string {
	pairs := [][2]string{{`"`, `"`}, {"'", "'"}, {"“", "”"}, {"‘", "’"}}
	for _, p := range pairs {
		opener, closer := p[0], p[1]
		if len(text) <= len(opener)+len(closer) {
			continue
		}
		if !strings.HasPrefix(text, opener) || !strings.HasSuffix(text, closer) {
			continue
		}
		inner := text[len(opener) : len(text)-len(closer)]
		if strings.Contains(inner, closer) {
			continue // the quotes are part of the prose, not a wrapper
		}
		return strings.TrimSpace(inner)
	}
	return text
}

// dropPlaceholderLines removes lines that are nothing but a bracketed
// placeholder — "[Your Name]", "[Company]", "{{first_name}}" — which is how a
// model signs off when told not to name anyone. A line containing a placeholder
// alongside real prose is LEFT ALONE: silently deleting half a sentence would
// be worse than showing the user a placeholder they can see and fix.
func dropPlaceholderLines(text string) string {
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if isPlaceholderOnly(strings.TrimSpace(line)) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

func isPlaceholderOnly(line string) bool {
	switch {
	case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
		return !strings.Contains(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"), "]")
	case strings.HasPrefix(line, "{{") && strings.HasSuffix(line, "}}"):
		return true
	default:
		return false
	}
}

// collapseBlankLines reduces any run of blank lines to a single one and strips
// trailing whitespace from every line, so removed placeholder/subject lines
// don't leave a gap.
func collapseBlankLines(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			if blank {
				continue
			}
			blank = true
		} else {
			blank = false
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
