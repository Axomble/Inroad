package inbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/platform/ai"
)

// This file is the AI reply-DRAFT path: it produces suggested reply text for a
// human to edit and then send through Service.Reply. It NEVER sends mail and
// never enqueues anything — drafting and sending are separate operations behind
// separate calls (docs/security.md invariant 48).

// Sentinel errors Service.DraftReply returns on top of the ones it shares with
// Reply (ErrNotFound for an unknown/foreign thread, ErrNoInboundMessage for a
// thread with nothing to reply to).
var (
	// ErrDraftModelUnavailable means this workspace has no usable AI model
	// configured — a settings problem the user can fix, not a server fault, so
	// it is kept distinct from every other failure to let the UI point at
	// Settings → AI instead of reporting a generic error.
	ErrDraftModelUnavailable = errors.New("inbox: no AI model is configured for this workspace")
	// ErrDraftTimeout means the provider did not finish within the drafter's
	// own budget. Retrying may work, which is what separates it from
	// ErrDraftUpstream.
	ErrDraftTimeout = errors.New("inbox: drafting a reply timed out")
	// ErrDraftUpstream means the AI provider call failed (or returned nothing
	// usable). Deliberately opaque: an upstream error string can carry provider
	// detail, and this one reaches an HTTP response.
	ErrDraftUpstream = errors.New("inbox: drafting a reply failed upstream")
)

// draftMaxBodyChars bounds how much of ONE message body is handed to the
// drafter. The drafter caps the whole transcript too; this per-message cap
// exists so a single enormous message (a forwarded newsletter) cannot crowd
// every other turn out of that budget.
const draftMaxBodyChars = 4000

// ReplyDrafter generates suggested reply text from a thread transcript. Owned
// by this package (the consumer) and satisfied at wiring time by an adapter
// over the agent runtime: app packages never import each other, so this domain
// does not know agentrun/agentchat exist.
//
// Contract for implementations, because Service.DraftReply classifies against
// it:
//   - The returned string is a plain-text reply BODY, never empty on success,
//     never HTML.
//   - No mail is sent and no send is scheduled. This seam is read-only with
//     respect to the workspace's mail.
//   - A workspace with no usable model wraps ai.ErrNoModel.
//   - Exceeding the implementation's own time budget wraps
//     context.DeadlineExceeded.
//   - Any other error is an AI-subsystem failure; the domain reports those as
//     upstream rather than guessing at a finer cause.
type ReplyDrafter interface {
	DraftReply(ctx context.Context, workspaceID uuid.UUID, in DraftReplyInput) (string, error)
}

// DraftReplyInput is the thread, projected onto what a prompt needs. The
// drafter builds the transcript and applies its own caps; this domain's job is
// only to say what the conversation was, oldest first.
type DraftReplyInput struct {
	ContactFirstName string
	Subject          string
	FromCampaign     bool
	Turns            []DraftTurn
}

// DraftTurn is one message: which side sent it, and its plain text.
type DraftTurn struct {
	FromContact bool
	Text        string
}

// WithReplyDrafter wires Service.DraftReply's AI seam. Without it, DraftReply
// reports ErrDraftModelUnavailable — the same thing the user sees when the
// workspace has no model configured, because from their side "this server
// cannot draft for me" is one condition with one fix (configure AI), not two.
func WithReplyDrafter(d ReplyDrafter) ServiceOption {
	return func(s *Service) { s.drafter = d }
}

// DraftReply returns suggested reply text for threadID, generated from the
// thread's own message history. It performs the SAME workspace-pinned
// ownership check Reply does (an unknown or foreign thread is
// indistinguishably ErrNotFound) and requires an inbound message to reply to,
// so a caller cannot spend the workspace's AI budget on a thread it cannot see.
//
// It does NOT check suppression and does not mark the thread read: nothing is
// being sent, and the caller has not committed to sending. Suppression is
// enforced where it matters, at Reply and again in the worker before the dial.
func (s *Service) DraftReply(ctx context.Context, ws, threadID uuid.UUID) (string, error) {
	detail, err := s.store.GetThread(ctx, ws, threadID)
	if err != nil {
		return "", err // already ErrNotFound-mapped by the store
	}
	if _, ok := latestInboundMessage(detail.Messages); !ok {
		return "", ErrNoInboundMessage
	}
	if s.drafter == nil {
		logDraftFailure(ctx, ws, threadID, "drafter_not_wired", nil)
		return "", ErrDraftModelUnavailable
	}
	draft, err := s.drafter.DraftReply(ctx, ws, buildDraftInput(detail))
	if err != nil {
		classified := classifyDraftError(err)
		logDraftFailure(ctx, ws, threadID, draftFailureReason(classified), err)
		return "", classified
	}
	if strings.TrimSpace(draft) == "" {
		logDraftFailure(ctx, ws, threadID, "empty_draft", nil)
		// Belt and braces on the seam's own contract: an empty draft would
		// render as an inexplicably blank editor.
		return "", ErrDraftUpstream
	}
	return draft, nil
}

// logDraftFailure records a failed draft with a STABLE reason token, so an
// operator can answer "is anyone hitting this, and why" from logs alone —
// notably "are workspaces that appear to have a model configured still getting
// no_model_configured", which is how a revoked or rotated provider credential
// shows up (the enabled-model list is an operator allowlist, not a health
// signal, so it keeps listing a model whose key no longer works).
//
// Ids and a reason token always; an error value only where that value is OURS
// and provably content-free — never the prompt, the transcript, or any message
// content (docs/security.md invariants 21 and 48). See draftErrorAttrs for the
// one class whose text is withheld and why.
//
// A caller that went away is not logged at all: a closed browser tab is not an
// operational event, and logging it would bury the failures that are.
func logDraftFailure(ctx context.Context, ws, threadID uuid.UUID, reason string, err error) {
	if reason == "" {
		return
	}
	attrs := []any{"workspace_id", ws, "thread_id", threadID, "reason", reason}
	attrs = append(attrs, draftErrorAttrs(reason, err)...)
	slog.WarnContext(ctx, "inbox_reply_draft_failed", attrs...)
}

// draftErrorAttrs describes err without ever quoting a PROVIDER's message.
//
// The two provider classes crossed an AI provider's HTTP boundary, and some
// providers echo a snippet of the offending input back in a 4xx body — so that
// text is not ours to trust, and logging it would make invariant 48's "no prompt
// or message content is ever logged" merely mostly true. Those classes
// contribute machine facts only:
//
//   - *ai.ProviderStatusError — the provider kind, its HTTP status, and whether
//     that status is retryable. That type carries no body BY CONSTRUCTION (see
//     its own doc), which is exactly why it is the shape worth reaching for, and
//     the status is strictly more useful to an operator than a message anyway.
//   - any other shape (a provider SDK error, which may embed a response body) —
//     its Go type and nothing else. A type name is a real debugging signal that
//     cannot carry content.
//
// Every other class is ours and provably content-free: a resolution failure
// names a model or provider id, and drafter_not_wired / empty_draft carry no
// error at all. Those keep their full error value, which is what makes the line
// actionable.
//
// Deliberately NOT a length cap on all error text: truncating still leaks a
// prefix, and it would mangle our own legible errors to no benefit.
func draftErrorAttrs(reason string, err error) []any {
	if err == nil {
		return nil
	}
	if reason != "provider_failed" && reason != "provider_timeout" {
		return []any{"err", err}
	}
	var status *ai.ProviderStatusError
	if errors.As(err, &status) {
		return []any{
			"provider_kind", status.Kind,
			"provider_status", status.StatusCode,
			"provider_retryable", status.Retryable(),
		}
	}
	return []any{"err_type", fmt.Sprintf("%T", err)}
}

// draftFailureReason maps a classified failure to its stable log token. ""
// means "do not log" (see logDraftFailure).
func draftFailureReason(classified error) string {
	switch {
	case errors.Is(classified, ErrDraftModelUnavailable):
		return "no_model_configured"
	case errors.Is(classified, ErrDraftTimeout):
		return "provider_timeout"
	case errors.Is(classified, ErrDraftUpstream):
		return "provider_failed"
	default:
		return "" // caller cancelled: not an operational event
	}
}

// classifyDraftError maps the drafter's failure onto this domain's sentinels.
// The residual case is ErrDraftUpstream rather than a bare 500 because every
// remaining failure mode behind this seam belongs to the AI subsystem (model
// resolution or the provider call) — see ReplyDrafter's contract. The original
// error is wrapped, not discarded, so it still appears in server logs.
func classifyDraftError(err error) error {
	switch {
	case errors.Is(err, ai.ErrNoModel):
		return fmt.Errorf("%w: %w", ErrDraftModelUnavailable, err)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%w: %w", ErrDraftTimeout, err)
	case errors.Is(err, context.Canceled):
		// The CALLER went away (closed connection). Not an upstream fault, and
		// nothing will be written to the response anyway.
		return err
	default:
		return fmt.Errorf("%w: %w", ErrDraftUpstream, err)
	}
}

// buildDraftInput projects a thread onto the prompt's input. Every message is
// included, oldest first (the store already orders them that way), so the model
// sees both sides of the conversation and can match the contact's register.
//
// The thread's subject is the only campaign context available here: this domain
// has no campaign name to join on, and reaching into app/campaign for a prompt
// nicety would break the "app packages don't import each other" rule for
// little gain. CampaignID != nil still tells the model this began as outreach
// we sent.
func buildDraftInput(detail ThreadDetail) DraftReplyInput {
	turns := make([]DraftTurn, 0, len(detail.Messages))
	for _, m := range detail.Messages {
		text := messageDraftText(m)
		if text == "" {
			continue
		}
		turns = append(turns, DraftTurn{FromContact: m.Direction == "inbound", Text: text})
	}
	return DraftReplyInput{
		ContactFirstName: detail.Thread.ContactFirstName,
		Subject:          detail.Thread.Subject,
		FromCampaign:     detail.Thread.CampaignID != nil,
		Turns:            turns,
	}
}

// messageDraftText projects one message onto plain text, clipped to
// draftMaxBodyChars.
//
// body_text is preferred and is what essentially every real message carries
// (both the poller and our own sends populate it). Only an HTML-ONLY message
// falls back to a reduction of its markup — there is no HTML-to-text facility
// in this repo, and pulling one in to improve a prompt is not worth the
// dependency, so the fallback is deliberately crude: it is a hint for a model,
// never something rendered to a user or sent as mail.
func messageDraftText(m Message) string {
	if text := strings.TrimSpace(m.BodyText); text != "" {
		return clipDraftText(text)
	}
	return clipDraftText(reduceHTMLToText(m.BodyHTML))
}

func clipDraftText(s string) string {
	runes := []rune(s)
	if len(runes) <= draftMaxBodyChars {
		return s
	}
	return strings.TrimSpace(string(runes[:draftMaxBodyChars]))
}

// reduceHTMLToText strips tags from an HTML-only body. It drops <script> and
// <style> element CONTENT (not just their tags — script source read as prose is
// noise at best), turns block boundaries into newlines, removes every remaining
// tag, and unescapes the handful of entities that matter. It is not a parser
// and does not try to be: an imperfect reduction feeding a prompt is acceptable
// where an imperfect reduction feeding a send would not be.
func reduceHTMLToText(html string) string {
	if strings.TrimSpace(html) == "" {
		return ""
	}
	var b strings.Builder
	depth := 0 // >0 while inside a script/style element
	for i := 0; i < len(html); {
		if html[i] != '<' {
			if depth == 0 {
				b.WriteByte(html[i])
			}
			i++
			continue
		}
		end := strings.IndexByte(html[i:], '>')
		if end < 0 {
			break // unterminated tag: everything after it is markup, not prose
		}
		// Lower-cased per tag rather than once over the whole document:
		// strings.ToLower can change a string's BYTE length for some non-ASCII
		// runes, which would misalign a parallel lower-cased copy's indices
		// against html's — and non-English replies are routine here.
		tag := strings.ToLower(html[i : i+end+1])
		switch {
		case strings.HasPrefix(tag, "<script") || strings.HasPrefix(tag, "<style"):
			depth++
		case strings.HasPrefix(tag, "</script") || strings.HasPrefix(tag, "</style"):
			if depth > 0 {
				depth--
			}
		case depth == 0 && isBlockBoundaryTag(tag):
			b.WriteByte('\n')
		}
		i += end + 1
	}
	return collapseWhitespace(unescapeBasicEntities(b.String()))
}

// isBlockBoundaryTag reports whether a tag ends a visual line, so the reduction
// keeps paragraph structure instead of running every sentence together.
func isBlockBoundaryTag(tag string) bool {
	for _, name := range []string{"<br", "</p", "<p", "</div", "<div", "</tr", "</li", "<li", "</h1", "</h2", "</h3", "</blockquote"} {
		if strings.HasPrefix(tag, name) {
			return true
		}
	}
	return false
}

// unescapeBasicEntities decodes only the entities that actually appear in mail
// prose. &amp; is decoded LAST so "&amp;lt;" does not become "<".
func unescapeBasicEntities(s string) string {
	for _, pair := range [][2]string{{"&nbsp;", " "}, {"&lt;", "<"}, {"&gt;", ">"}, {"&quot;", `"`}, {"&#39;", "'"}, {"&apos;", "'"}, {"&amp;", "&"}} {
		s = strings.ReplaceAll(s, pair[0], pair[1])
	}
	return s
}

// collapseWhitespace squeezes the runs of spaces and blank lines HTML source
// indentation leaves behind, preserving single line breaks.
func collapseWhitespace(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if joined := strings.Join(strings.Fields(line), " "); joined != "" {
			out = append(out, joined)
		}
	}
	return strings.Join(out, "\n")
}
