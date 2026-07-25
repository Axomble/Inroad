package replyclassify

import "context"

// ModelClassifier is the OPTIONAL Layer-3 seam. It is injected into a Classifier
// via New — there is no package-global model and no mutex, so the pipeline is
// concurrent-safe by construction and trivially testable. A nil ModelClassifier
// disables Layer 3.
//
// Classify is consulted only for the ambiguous middle that Layers 1-2 could not
// settle, and it must confine itself to the three nuanced sentiment classes:
// positive | negative | neutral. Compliance (unsubscribe) and automation
// (auto_reply / out_of_office) are already decided deterministically upstream and
// are intentionally outside the model's output space; any other class it returns
// is rejected and the reply falls back to "unknown". ok == false also falls back
// to "unknown" so a model miss never hard-errors the pipeline.
type ModelClassifier interface {
	Classify(ctx context.Context, subject, body string) (class string, ok bool)
}

// classifyModel runs Layer 3 when a model is injected. It returns (zero, false)
// when the model declines or returns a class outside positive|negative|neutral,
// so the caller falls back to "unknown".
func (c *Classifier) classifyModel(ctx context.Context, in Input) (Result, bool) {
	class, ok := c.model.Classify(ctx, in.Subject, in.BodyText)
	if !ok {
		return Result{}, false
	}
	switch class {
	case ClassPositive, ClassNegative, ClassNeutral:
		return Result{Class: class, Confidence: 0.7, Source: SourceModel}, true
	default:
		return Result{}, false
	}
}

// IsAutomated reports whether a reply_class represents an automated (non-human)
// reply. This is the single source of truth for the "OOO trap": the stop-on-reply
// check must NOT treat these as a human reply.
func IsAutomated(class string) bool {
	return class == ClassAutoReply || class == ClassOutOfOffice
}
