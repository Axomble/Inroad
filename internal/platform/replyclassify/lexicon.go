package replyclassify

import (
	"regexp"
	"strings"
)

// classifyLexicon is Layer 2: a deterministic, offline keyword scan over the
// subject + body. It returns (result, true) only on a CLEAR signal; ambiguous
// text returns (zero, false) so the optional model layer (or "unknown") decides.
//
// Order encodes priority and is deliberate for correctness:
//
//  1. Compliance (unsubscribe / remove me / stop) ALWAYS wins. Treating an
//     opt-out as anything else is a compliance risk, so it short-circuits first.
//  2. Rejection => negative. Checked BEFORE positive so "not interested" (which
//     contains the positive token "interested") resolves to negative.
//  3. Interest => positive, and negation-aware: a positive token preceded within
//     a small word window by a negator (not / n't / never …) does not fire.
//
// Short/ambiguous tokens (e.g. "stop", "no need", "go away") are matched on word
// boundaries so "nonstop" / "stopped by" do not false-positive; unambiguous
// multi-word phrases use substring contains.
func classifyLexicon(in Input) (Result, bool) {
	text := lexiconText(in)
	if text == "" {
		return Result{}, false
	}

	// 1. Compliance / opt-out (highest priority).
	if isUnsubscribeText(text) {
		return Result{Class: ClassUnsubscribe, Confidence: 0.9, Source: SourceLexicon}, true
	}

	// 2. Clear rejection => negative (before positive: "not interested" fix).
	if containsAny(text, negativePhrases) || matchesAny(text, negativeBoundary) {
		return Result{Class: ClassNegative, Confidence: 0.8, Source: SourceLexicon}, true
	}

	// 3. Clear interest => positive (negation-aware).
	if positiveHit(text) {
		return Result{Class: ClassPositive, Confidence: 0.8, Source: SourceLexicon}, true
	}

	return Result{}, false
}

// lexiconText is the normalized subject+body the lexicon layer scans: lower-cased
// and trimmed, subject and body joined by a newline. Shared by classifyLexicon
// and LooksLikeUnsubscribe so both scan identical text.
func lexiconText(in Input) string {
	return strings.ToLower(strings.TrimSpace(in.Subject + "\n" + in.BodyText))
}

// isUnsubscribeText is the boundary-aware compliance/opt-out scan Layer 2 uses,
// factored out so LooksLikeUnsubscribe can reuse the exact same tokens and logic
// (single source of truth — no duplicated keyword list).
func isUnsubscribeText(text string) bool {
	return containsAny(text, unsubscribePhrases) || matchesAny(text, unsubscribeBoundary)
}

// LooksLikeUnsubscribe reports whether the reply carries an explicit opt-out
// signal, running ONLY the compliance-keyword scan (the same boundary-aware
// tokens/logic Layer 2 uses — not a duplicate list). It lets a caller honor a
// compliance request even when an earlier layer already classified the message
// as automated (Precedence: bulk, an OOO subject, …), where the full pipeline
// would otherwise short-circuit before Layer 2 ever ran. Pure and offline.
func (c *Classifier) LooksLikeUnsubscribe(in Input) bool {
	return isUnsubscribeText(lexiconText(in))
}

// unsubscribePhrases are explicit multi-word opt-out requests matched by
// substring; they are unambiguous enough not to need boundary anchoring.
var unsubscribePhrases = []string{
	"unsubscribe",
	"opt out",
	"opt-out",
	"remove me",
	"take me off",
	"do not contact",
	"don't contact",
	"dont contact",
	// Email opt-outs are anchored with "me"/"us" so a benign "I don't email much
	// on weekends" does not read as an opt-out request.
	"do not email me",
	"don't email me",
	"dont email me",
	"do not email us",
	"don't email us",
	"dont email us",
}

// unsubscribeBoundaryTokens are short/ambiguous opt-out tokens that MUST match on
// word boundaries so "nonstop" / "stopped by" are not mistaken for "stop".
var unsubscribeBoundaryTokens = []string{"stop"}

// negativePhrases are clear rejection signals matched by substring.
var negativePhrases = []string{
	"not interested",
	"no thanks",
	"no thank you",
	"not a fit",
	"not the right",
	"not relevant",
	"we already have",
	"we have a solution",
	"already using",
	"not looking",
	"wrong person",
	"wrong contact",
	"leave me alone",
}

// negativeBoundaryTokens are short/ambiguous rejection tokens matched on word
// boundaries.
var negativeBoundaryTokens = []string{"no need", "go away"}

// positiveKeywords are clear buying / interest signals. Kept conservative; nuance
// is left to the optional model layer. Matching is negation-aware (see
// positiveHit): a hit within the negation window does not fire.
var positiveKeywords = []string{
	"interested",
	"sounds good",
	"sounds great",
	"let's chat",
	"lets chat",
	"let's talk",
	"lets talk",
	"happy to chat",
	"happy to talk",
	"set up a call",
	"book a call",
	"schedule a call",
	"schedule a demo",
	"book a demo",
	"send me more",
	"tell me more",
	"would love to",
	"count me in",
	"sign me up",
	"send pricing",
}

// negators are words that flip a following positive token. Any word ending in
// "n't" (isn't / won't / can't / don't / wouldn't / couldn't / …) is caught by
// the suffix check in negatedBefore, so only the non-apostrophe forms need to be
// listed explicitly. "no" is deliberately NOT a negator: it is far more often
// non-negating ("no problem", "no worries") than negating before an interest cue.
var negators = map[string]bool{
	"not":    true,
	"never":  true,
	"cannot": true,
	"dont":   true,
	"wont":   true,
}

// negationWindow is how many preceding words are checked for a negator.
const negationWindow = 3

// unsubscribeBoundary and negativeBoundary are the precompiled word-boundary
// matchers for the short/ambiguous token lists.
var (
	unsubscribeBoundary = compileBoundary(unsubscribeBoundaryTokens)
	negativeBoundary    = compileBoundary(negativeBoundaryTokens)
)

// compileBoundary builds a \b-anchored regexp per token so matching respects word
// boundaries. The input text is already lower-cased before matching.
func compileBoundary(tokens []string) []*regexp.Regexp {
	res := make([]*regexp.Regexp, len(tokens))
	for i, t := range tokens {
		res[i] = regexp.MustCompile(`\b` + regexp.QuoteMeta(t) + `\b`)
	}
	return res
}

func containsAny(text string, keywords []string) bool {
	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

func matchesAny(text string, res []*regexp.Regexp) bool {
	for _, re := range res {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// positiveHit reports whether the text carries a clear interest signal that is
// NOT negated. It scans EVERY occurrence of every positive keyword: an occurrence
// with a negator in the preceding negationWindow words is skipped, but a later
// occurrence outside that window still fires (e.g. "not happy to chat earlier but
// happy to chat now" is positive). Because negationWindow is 3, a keyword whose
// negator sits within three words is suppressed entirely — "not now but interested
// later" does NOT fire (it resolves to unknown), which is the correct
// conservative behavior for the deterministic layer.
func positiveHit(text string) bool {
	for _, kw := range positiveKeywords {
		for start := 0; ; {
			i := strings.Index(text[start:], kw)
			if i < 0 {
				break
			}
			at := start + i
			if !negatedBefore(text[:at]) {
				return true
			}
			start = at + len(kw)
		}
	}
	return false
}

// negatedBefore reports whether any of the last negationWindow whitespace-
// delimited words of prefix is a negator. It scans backward token-by-token from
// the end of prefix instead of splitting the whole string, so it allocates
// nothing and does O(negationWindow) work regardless of prefix length.
func negatedBefore(prefix string) bool {
	end := len(prefix)
	for words := 0; words < negationWindow; words++ {
		for end > 0 && isSpace(prefix[end-1]) {
			end--
		}
		if end == 0 {
			return false
		}
		start := end
		for start > 0 && !isSpace(prefix[start-1]) {
			start--
		}
		w := strings.Trim(prefix[start:end], ".,!?;:'\"()-")
		if negators[w] || strings.HasSuffix(w, "n't") {
			return true
		}
		end = start
	}
	return false
}

// isSpace reports whether b is an ASCII whitespace byte.
func isSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\f', '\v':
		return true
	default:
		return false
	}
}
