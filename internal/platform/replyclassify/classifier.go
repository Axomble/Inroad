// Package replyclassify classifies an inbound email reply into a small, stable
// set of classes used by campaign reply automation (the OOO-aware stop check,
// reply-based unsubscribe suppression, and sentiment tagging).
//
// Classification is layered, cheapest-first, and the cheap layers are pure:
//
//	Layer 1 (headers): deterministic, offline. RFC 3834 Auto-Submitted markers,
//	        Precedence bulk/junk/list/auto_reply, vendor auto-responder headers,
//	        null Return-Path, delivery-status/disposition-notification reports,
//	        mailer-daemon/no-reply senders, and the well-known "Automatic reply" /
//	        "Out of Office" subjects.
//	Layer 2 (lexicon): deterministic, offline keyword scan. Compliance words
//	        (unsubscribe / remove me / stop) win first; then clear rejection
//	        (negative); then clear interest (positive, negation-aware).
//	Layer 3 (model): OPTIONAL, injected via New. Only the ambiguous middle reaches
//	        it and it is constrained to positive|negative|neutral. When no model is
//	        injected the middle resolves to "unknown" with zero I/O.
//
// Layers 1 and 2 are pure and deterministic; only Layer 3 has side effects and
// only when a model is injected.
//
// Design notes: the lexicon checks compliance first, then negative before
// positive, and positive matching is negation-aware so "not interested" resolves
// to negative rather than positive; short/ambiguous compliance tokens are matched
// on word boundaries; and the optional Layer-3 model is injected via New rather
// than held in package-global mutable state.
package replyclassify

import "context"

// Reply class enum. These exact strings are the shared contract stored on the
// enrollment and pinned by migration 000014's CHECK constraint.
const (
	ClassPositive    = "positive"
	ClassNegative    = "negative"
	ClassNeutral     = "neutral"
	ClassAutoReply   = "auto_reply"
	ClassOutOfOffice = "out_of_office"
	ClassUnsubscribe = "unsubscribe"
	ClassUnknown     = "unknown"
)

// Source enum: which layer produced the verdict. SourceNone ("") means the
// pipeline reached no conclusion (class unknown).
const (
	SourceHeader  = "header"
	SourceLexicon = "lexicon"
	SourceModel   = "model"
	SourceNone    = ""
)

// Input is the minimal projection of an inbound reply the classifier needs.
// Headers is the raw header map (canonical or lower-cased keys both tolerated);
// Subject and BodyText are best-effort plain text (a snippet is fine).
type Input struct {
	Headers  map[string][]string
	Subject  string
	BodyText string
}

// Result is the classifier verdict. Confidence is in [0,1]. Source names the
// layer that decided (SourceNone for unknown).
type Result struct {
	Class      string
	Source     string
	Confidence float64
}

// Classifier runs the layered pipeline. The optional Layer-3 model is a struct
// field injected via New — there is no package-global model and no mutex, so a
// Classifier is safe for concurrent use as long as the injected model is.
type Classifier struct {
	model ModelClassifier
}

// New builds a Classifier. A nil model disables Layer 3: the ambiguous middle
// resolves to "unknown" with no I/O.
func New(model ModelClassifier) *Classifier {
	return &Classifier{model: model}
}

// Classify runs Layer 1 (headers) → Layer 2 (lexicon) → Layer 3 (model, only if
// injected) → unknown. The first layer to reach a conclusion wins; later layers
// (including the model) are not consulted.
func (c *Classifier) Classify(ctx context.Context, in Input) Result {
	if r, ok := classifyHeaders(in); ok {
		return r
	}
	if r, ok := classifyLexicon(in); ok {
		return r
	}
	if c.model != nil {
		if r, ok := c.classifyModel(ctx, in); ok {
			return r
		}
	}
	return Result{Class: ClassUnknown, Confidence: 0, Source: SourceNone}
}
