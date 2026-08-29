package queue

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// A TASK PAYLOAD IS A POINTER, NEVER CORRESPONDENCE.
//
// This file is the structural guard behind that rule. A payload is not a private
// channel between an enqueuer and its handler: on terminal failure
// DeadLetterErrorHandler stores it byte-for-byte in task_dead_letters, and
// GET /dead-letters serves it verbatim under campaigns:read — which IS
// OAuth-grantable, while inbox:read deliberately is NOT, precisely because reply
// bodies are correspondence (internal/app/auth/scopes.go). So anything a payload
// carries is reachable by a scope that may have been structurally denied it.
//
// The rule that follows: a payload names WHAT to act on, and the worker loads the
// content itself through coreapi. Every exported field must therefore be
// WorkspaceID or carry an explicit, justified entry in payloadFieldAllowlist.

// allTaskPayloads is the ONE list of this package's task payload shapes, shared
// with TestWorkspaceFromPayload (deadletter_test.go) so the two cannot drift —
// a payload added to one list and forgotten in the other is exactly the gap
// these tests exist to close. The exhaustiveness subtest below proves the list
// is complete by parsing the package's own source.
func allTaskPayloads(workspaceID string) map[string]any {
	return map[string]any{
		"AdvancePayload":                 AdvancePayload{EnrollmentID: uuid.New().String(), WorkspaceID: workspaceID},
		"InboxPollPayload":               InboxPollPayload{MailboxID: uuid.New().String(), WorkspaceID: workspaceID},
		"WarmupTickPayload":              WarmupTickPayload{MailboxID: uuid.New().String(), WorkspaceID: workspaceID},
		"WarmupEngagePayload":            WarmupEngagePayload{ReceiptID: uuid.New().String(), WorkspaceID: workspaceID},
		"TestSendPayload":                TestSendPayload{CampaignID: uuid.New().String(), WorkspaceID: workspaceID},
		"InboxReplySendPayload":          InboxReplySendPayload{ThreadID: uuid.New().String(), WorkspaceID: workspaceID},
		"InboxPendingReplySendPayload":   InboxPendingReplySendPayload{PendingID: uuid.New().String(), WorkspaceID: workspaceID},
		"InboxPendingComposeSendPayload": InboxPendingComposeSendPayload{PendingID: uuid.New().String(), WorkspaceID: workspaceID},
		"DeliverabilityEvaluatePayload":  DeliverabilityEvaluatePayload{CampaignID: uuid.New().String(), WorkspaceID: workspaceID},
	}
}

// workspacePinKey is the one key every payload may carry unlisted. It is written
// as the JSON key, not the Go field name, because that is what the capture path
// actually reads (workspaceFromPayload unmarshals `json:"workspace_id"`): a field
// NAMED WorkspaceID whose tag said something else would not pin anything.
const workspacePinKey = "workspace_id"

// payloadFieldAllowlist enumerates every payload key that is NOT the workspace
// pin, keyed "Type.json_key", with the reason it is safe to expose through a
// dead letter. It is deliberately exhaustive rather than pattern-matched on
// "looks like an id": a rule that accepts anything ending in ID would happily
// accept a field that is not one, and the friction of adding a line here — with
// a justification — is the whole point.
//
// Keyed by the WIRE key rather than the Go field name, because the wire is what
// a dead letter stores and GET /dead-letters serves. A field named To whose tag
// says `json:"body_text"` publishes a body; measured by Go name it reads as an
// allowlisted address (TestThePayloadGuardCatchesWhatItMustCatch drives exactly
// that shape).
var payloadFieldAllowlist = map[string]string{
	"AdvancePayload.enrollment_id":              "row id; the worker loads the enrollment through coreapi",
	"InboxPollPayload.mailbox_id":               "row id",
	"WarmupTickPayload.mailbox_id":              "row id",
	"WarmupEngagePayload.receipt_id":            "row id",
	"TestSendPayload.campaign_id":               "row id",
	"TestSendPayload.step_id":                   "row id",
	"TestSendPayload.mailbox_id":                "row id",
	"InboxPendingReplySendPayload.pending_id":   "row id; the body lives in inbox_pending_replies",
	"InboxPendingComposeSendPayload.pending_id": "row id; the body lives in inbox_pending_composes",
	"DeliverabilityEvaluatePayload.campaign_id": "row id",
	"InboxReplySendPayload.thread_id":           "row id",
	"InboxReplySendPayload.task_id":             "server-minted claim key; carries no tenant content",

	// THE ONE NON-ID ENTRY (TestSendPayload.To on the Go side). A test-send
	// recipient is typed by the operator into the test-send form, so it is not a
	// row id — but it is contact-class data: an email address, the same category
	// contacts:read already returns in full. A dead letter served under
	// campaigns:read therefore discloses nothing that a credential able to read
	// the campaign it belongs to could not already read, which is the standard
	// this file holds a payload field to.
	"TestSendPayload.to": "operator-typed recipient address; contact-class data campaigns:read peers already expose",

	// DRAIN-ONLY, AND THE REASON THIS FILE EXISTS (InboxReplySendPayload.BodyText
	// on the Go side). It is the operator's free-text reply — genuine
	// correspondence, and the disclosure this change closes. Nothing produces
	// this payload any more (proven by
	// TestLegacyReplyPayloadIsDeprecatedAndHasNoProducer) and its capture is
	// suppressed at both planes (legacyContentBearingTaskTypes and
	// deadletter.Service.Capture), so the field survives only to let tasks
	// already in Redis finish. It is deleted, with its type, in the release after
	// this one — and this entry with it.
	"InboxReplySendPayload.body_text": "DEPRECATED drain-only; unproduced, capture-suppressed, removed next release",
}

// payloadWireKeys returns every JSON key a payload emits.
//
// The keys, not the Go fields, because the wire is the thing under rule:
// reflect.Type.Field(i) is not recursive, so an embedded struct whose TYPE NAME
// is lowercase reads as one unexported field and is skipped — while json.Marshal
// flattens its exported fields onto the output regardless. That gap is how a
// body_text reached the JSON with every assertion in this file still passing.
// Marshalling asks the same question the disclosure did.
//
// Both a POPULATED and a ZERO value are marshalled, and the keys unioned: with
// `omitempty` (which no payload uses today, and which is exactly the sort of
// thing that gets added later) each would hide keys the other emits.
func payloadWireKeys(t *testing.T, p any) []string {
	t.Helper()
	keys := map[string]struct{}{}
	for _, v := range []any{p, reflect.New(reflect.TypeOf(p)).Elem().Interface()} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal %T: %v", v, err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(b, &fields); err != nil {
			t.Fatalf("a task payload does not marshal to a JSON object (%T -> %s): %v", v, b, err)
		}
		for k := range fields {
			keys[k] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(keys))
}

// unallowlistedFields returns every wire key of one payload that is neither the
// workspace pin nor carries a justified allowlist entry, as "Type.json_key".
//
// Extracted from the subtest below so it can be pointed at a payload shape that
// SHOULD be caught — see TestThePayloadGuardCatchesWhatItMustCatch. A structural
// guard that has only ever been run against compliant input has not been shown
// to reject anything.
func unallowlistedFields(t *testing.T, name string, p any) []string {
	t.Helper()
	var found []string
	for _, key := range payloadWireKeys(t, p) {
		if key == workspacePinKey {
			continue
		}
		full := name + "." + key
		why, allowed := payloadFieldAllowlist[full]
		if !allowed || strings.TrimSpace(why) == "" {
			found = append(found, full)
		}
	}
	return found
}

func TestTaskPayloadsCarryNoContent(t *testing.T) {
	payloads := allTaskPayloads(uuid.New().String())

	t.Run("every emitted key is a workspace pin or an allowlisted pointer", func(t *testing.T) {
		for name, p := range payloads {
			for _, key := range unallowlistedFields(t, name, p) {
				t.Errorf("%s is not in payloadFieldAllowlist (or is allowlisted with no reason).\n"+
					"A task payload is stored verbatim in task_dead_letters and served by "+
					"GET /dead-letters under campaigns:read (an OAuth-grantable scope). If this "+
					"field names a row the worker can load through coreapi, add it to the "+
					"allowlist with that reason. If it carries CONTENT, it does not belong in a "+
					"payload at all — put it in a row and pass the row's id.", key)
			}
		}
	})

	// The map is keyed by hand, and nothing but this checks the key against the
	// value: "FooPayload": BarPayload{} would satisfy the exhaustiveness subtest
	// below while every field assertion above reflected over the WRONG type, and
	// the real FooPayload would never be looked at.
	t.Run("every entry is filed under its own type's name", func(t *testing.T) {
		for name, p := range payloads {
			if got := reflect.TypeOf(p).Name(); got != name {
				t.Errorf("allTaskPayloads[%q] holds a %s; the two must agree or this file "+
					"inspects one type while claiming to cover another", name, got)
			}
		}
	})

	// Without this, the test above only catches a revert of the one field it was
	// written for: a brand-new payload type carrying a BodyText would never be
	// reflected over, and would pass in silence.
	t.Run("every payload type declared in this package is covered", func(t *testing.T) {
		declared := declaredPayloadTypes(t)
		for name := range declared {
			if _, ok := payloads[name]; !ok {
				t.Errorf("%s is declared in this package but absent from allTaskPayloads, so no test "+
					"has ever looked at its fields. Add it there (and to payloadFieldAllowlist if it "+
					"carries anything but a workspace pin).", name)
			}
		}
		for name := range payloads {
			if _, ok := declared[name]; !ok {
				t.Errorf("allTaskPayloads names %s, which this package no longer declares", name)
			}
		}
	})

	// The allowlist must not outlive the keys it excuses, or a stale entry
	// silently pre-authorises a future field emitting the same key.
	t.Run("no allowlist entry names a key that is no longer emitted", func(t *testing.T) {
		for key := range payloadFieldAllowlist {
			typeName, wireKey, ok := strings.Cut(key, ".")
			if !ok {
				t.Errorf("allowlist key %q is not in Type.json_key form", key)
				continue
			}
			p, known := payloads[typeName]
			if !known {
				t.Errorf("allowlist names unknown payload type %q", typeName)
				continue
			}
			if !slices.Contains(payloadWireKeys(t, p), wireKey) {
				t.Errorf("allowlist entry %q names a key %s no longer emits; delete it", key, typeName)
			}
		}
	})
}

// hiddenBody is an unexported struct type carrying an exported field. Embedding
// one promotes BodyText onto the outer struct AND onto its JSON: the marshaller
// flattens an embedded struct's exported fields whether or not the TYPE is
// exported. reflect's Field(i) does not — it reports the embedding as one field
// named "hiddenBody", which is unexported and was therefore skipped.
type hiddenBody struct {
	BodyText string `json:"body_text"`
}

// smugglerPayload is the shape a reviewer got a reply body onto the wire with,
// past the guard, while every assertion still passed. Every OTHER field of it is
// deliberately one the allowlist already excuses under the name it is checked as
// below, so body_text is the only thing left for the guard to find — otherwise
// this would pass on an unrelated complaint.
type smugglerPayload struct {
	hiddenBody
	PendingID   string `json:"pending_id"`
	WorkspaceID string `json:"workspace_id"`
}

// renamedPayload emits body_text out of a field named To — which the allowlist
// excuses, as an operator-typed address. A guard reading Go field names reads
// "TestSendPayload.To", finds its justification, and never looks at what the tag
// actually publishes.
type renamedPayload struct {
	To          string `json:"body_text"`
	WorkspaceID string `json:"workspace_id"`
}

// TestThePayloadGuardCatchesWhatItMustCatch points the guard at payload shapes
// that MUST be rejected.
//
// Without this, TestTaskPayloadsCarryNoContent only ever sees compliant input,
// so a guard that had quietly stopped inspecting anything would pass it — which
// is exactly what happened: reflect.Type.Field(i) is not recursive, so a body
// smuggled in through an embedded unexported-TYPE struct reached the JSON and
// the guard reported nothing.
func TestThePayloadGuardCatchesWhatItMustCatch(t *testing.T) {
	cases := map[string]struct {
		// checkedAs is the allowlist name the shape is measured against, chosen
		// so every field but the smuggled one is already excused.
		checkedAs string
		payload   any
		wantKey   string
	}{
		"a body promoted from an embedded unexported type": {
			checkedAs: "InboxPendingReplySendPayload", payload: smugglerPayload{},
			wantKey: "InboxPendingReplySendPayload.body_text",
		},
		"a body emitted out of an allowlisted field": {
			checkedAs: "TestSendPayload", payload: renamedPayload{},
			wantKey: "TestSendPayload.body_text",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := unallowlistedFields(t, tc.checkedAs, tc.payload)
			if !slices.Contains(got, tc.wantKey) {
				t.Fatalf("the guard reported %v for %T, which puts %q on the wire",
					got, tc.payload, tc.wantKey)
			}
		})
	}
}

// TestLegacyReplyPayloadIsDeprecatedAndHasNoProducer is what makes the one
// content-bearing allowlist entry above a DRAIN rather than a permanent
// exception. Deleting the producer is what makes the drain finite: while nothing
// enqueues an inbox:reply_send, Redis empties on its own and the type can be
// removed next release. Restoring a producer — the faithful revert of this
// change — puts correspondence back into a dead letter, so it fails here.
func TestLegacyReplyPayloadIsDeprecatedAndHasNoProducer(t *testing.T) {
	const typeName = "InboxReplySendPayload"
	files, fset := parsePackageSources(t)

	declared := declaredPayloadTypes(t)
	spec, ok := declared[typeName]
	if !ok {
		t.Fatalf("%s is gone. If this is the follow-up release that removes the drain path, delete "+
			"this test, its allowlist entry, and TaskInboxReplySend together.", typeName)
	}
	if !strings.Contains(spec.doc, "Deprecated:") {
		t.Errorf("%s carries no `Deprecated:` marker. It exists only to drain tasks already in "+
			"Redis; without the marker nothing tells the next author that adding a producer is a "+
			"security regression rather than a feature.", typeName)
	}

	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			lit, isLit := n.(*ast.CompositeLit)
			if !isLit {
				return true
			}
			ident, isIdent := lit.Type.(*ast.Ident)
			if !isIdent || ident.Name != typeName {
				return true
			}
			t.Errorf("%s: %s is constructed here, so something still produces an inbox:reply_send. "+
				"The drain is only finite while nothing enqueues one, and a captured payload of this "+
				"type is the operator's reply text served under campaigns:read.",
				fset.Position(lit.Pos()), typeName)
			return true
		})
	}
}

// parsePackageSources parses this package's non-test sources. Tests are excluded
// deliberately: a test may legitimately construct a deprecated payload (the
// round-trip test does), and it enqueues nothing.
func parsePackageSources(t *testing.T) ([]*ast.File, *token.FileSet) {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		t.Fatal("parsed no package sources; the exhaustiveness guard would pass vacuously")
	}
	return files, fset
}

// payloadDecl is one `type XPayload struct` found in the source, with whichever
// doc comment is attached (a lone type declaration carries it on the GenDecl,
// one inside a `type (...)` block on the TypeSpec).
type payloadDecl struct {
	doc string
}

// declaredPayloadTypes finds every payload struct the package declares, by
// reading the source rather than a hand-kept list — a hand-kept list is exactly
// what a new payload would be missing from.
func declaredPayloadTypes(t *testing.T) map[string]payloadDecl {
	t.Helper()
	files, _ := parsePackageSources(t)
	out := map[string]payloadDecl{}
	for _, file := range files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || !strings.HasSuffix(ts.Name.Name, "Payload") {
					continue
				}
				if _, isStruct := ts.Type.(*ast.StructType); !isStruct {
					continue
				}
				doc := ts.Doc.Text()
				if doc == "" {
					doc = gen.Doc.Text()
				}
				out[ts.Name.Name] = payloadDecl{doc: doc}
			}
		}
	}
	return out
}
