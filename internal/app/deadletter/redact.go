package deadletter

import (
	"encoding/json"

	"github.com/inroad/inroad/internal/platform/queue"
)

// legacyReplyBodyKey is the one payload key that ever carried correspondence:
// queue.InboxReplySendPayload.BodyText, the operator's free-text reply.
//
// Named as the JSON key rather than derived from the Go type on purpose. This
// package reads a payload that was serialised by a build that may be OLDER than
// this one (that is the whole reason the gate exists), so what matters is the
// bytes on the wire, not a field that a future refactor might rename.
const legacyReplyBodyKey = "body_text"

// redactedPayload is what replaces a legacy payload that cannot be parsed as a
// JSON object. Same value Capture normalises an empty payload to, so the column
// (JSONB NOT NULL) is always satisfied.
var redactedPayload = []byte("null")

// redactLegacyReplyBody removes the operator's reply text from a payload whose
// TASK TYPE is known to have carried it, and returns every other payload
// untouched.
//
// WHY THIS EXISTS AT ALL, given that nothing produces such a payload any more:
// two windows in which a body-bearing row is real.
//
//   - The capture gate that refuses these lives in internal/platform/queue,
//     which is the WORKER binary. During a rolling deploy an old worker is still
//     failing inbox:reply_send tasks and handing them to this control plane
//     through coreapi — after the one-shot migration has already swept the
//     table. Service.Capture runs this so that row is filed without its body.
//   - GET /dead-letters serves whatever is on disk. A control plane running this
//     code against a database whose redaction migration has not been applied —
//     the Helm chart has no migration hook, so the ordering is manual — reads
//     rows that still carry the text. toResponse runs this so the response does
//     not.
//
// SCOPED TO THE ONE DEPRECATED TASK TYPE, deliberately. Stripping body_text from
// every payload on the way out would be a silent band-aid: it would mask a NEW
// payload that carried content instead of failing
// TestTaskPayloadsCarryNoContent, which is the guard that keeps the rule honest.
// This is a drain-window measure and is deleted with queue.TaskInboxReplySend.
//
// The redaction mirrors the migration's `payload - 'body_text'` exactly, so a
// row the migration has already swept and one it has not look identical to a
// client: the ids survive, because the row is the operator's only record that a
// send was permanently lost and it still has to say which thread. A payload that
// is NOT a JSON object cannot be shown to be free of correspondence, so none of
// it is kept.
func redactLegacyReplyBody(taskType string, payload []byte) []byte {
	if !queue.IsLegacyContentBearingTaskType(taskType) {
		return payload
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil || fields == nil {
		return redactedPayload
	}
	if _, carried := fields[legacyReplyBodyKey]; !carried {
		return payload
	}
	delete(fields, legacyReplyBodyKey)
	stripped, err := json.Marshal(fields)
	if err != nil {
		// Unreachable in practice — the map came out of json.Unmarshal, so every
		// value is already valid JSON — but a marshal error must not fall through
		// to returning the payload that still has the body in it.
		return redactedPayload
	}
	return stripped
}
