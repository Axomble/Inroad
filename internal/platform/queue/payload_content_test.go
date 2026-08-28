package queue

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
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

// payloadFieldAllowlist enumerates every exported payload field that is NOT
// WorkspaceID, keyed "Type.Field", with the reason it is safe to expose through
// a dead letter. It is deliberately exhaustive rather than pattern-matched on
// "looks like an id": a rule that accepts anything ending in ID would happily
// accept a field that is not one, and the friction of adding a line here — with
// a justification — is the whole point.
var payloadFieldAllowlist = map[string]string{
	"AdvancePayload.EnrollmentID":              "row id; the worker loads the enrollment through coreapi",
	"InboxPollPayload.MailboxID":               "row id",
	"WarmupTickPayload.MailboxID":              "row id",
	"WarmupEngagePayload.ReceiptID":            "row id",
	"TestSendPayload.CampaignID":               "row id",
	"TestSendPayload.StepID":                   "row id",
	"TestSendPayload.MailboxID":                "row id",
	"InboxPendingReplySendPayload.PendingID":   "row id; the body lives in inbox_pending_replies",
	"InboxPendingComposeSendPayload.PendingID": "row id; the body lives in inbox_pending_composes",
	"DeliverabilityEvaluatePayload.CampaignID": "row id",
	"InboxReplySendPayload.ThreadID":           "row id",
	"InboxReplySendPayload.TaskID":             "server-minted claim key; carries no tenant content",

	// THE ONE NON-ID ENTRY. A test-send recipient is typed by the operator into
	// the test-send form, so it is not a row id — but it is contact-class data:
	// an email address, the same category contacts:read already returns in full.
	// A dead letter served under campaigns:read therefore discloses nothing that
	// a credential able to read the campaign it belongs to could not already
	// read, which is the standard this file holds a payload field to.
	"TestSendPayload.To": "operator-typed recipient address; contact-class data campaigns:read peers already expose",

	// DRAIN-ONLY, AND THE REASON THIS FILE EXISTS. BodyText is the operator's
	// free-text reply — genuine correspondence, and the disclosure this change
	// closes. Nothing produces this payload any more (proven by
	// TestLegacyReplyPayloadIsDeprecatedAndHasNoProducer) and its capture is
	// suppressed (legacyContentBearingTaskTypes), so the field survives only to
	// let tasks already in Redis finish. It is deleted, with its type, in the
	// release after this one — and this entry with it.
	"InboxReplySendPayload.BodyText": "DEPRECATED drain-only; unproduced, capture-suppressed, removed next release",
}

func TestTaskPayloadsCarryNoContent(t *testing.T) {
	payloads := allTaskPayloads(uuid.New().String())

	t.Run("every exported field is a workspace pin or an allowlisted pointer", func(t *testing.T) {
		for name, p := range payloads {
			typ := reflect.TypeOf(p)
			for i := range typ.NumField() {
				f := typ.Field(i)
				if !f.IsExported() {
					continue
				}
				if f.Name == "WorkspaceID" {
					continue
				}
				key := name + "." + f.Name
				why, allowed := payloadFieldAllowlist[key]
				if !allowed {
					t.Errorf("%s is not in payloadFieldAllowlist.\n"+
						"A task payload is stored verbatim in task_dead_letters and served by "+
						"GET /dead-letters under campaigns:read (an OAuth-grantable scope). If this "+
						"field names a row the worker can load through coreapi, add it to the "+
						"allowlist with that reason. If it carries CONTENT, it does not belong in a "+
						"payload at all — put it in a row and pass the row's id.", key)
					continue
				}
				if strings.TrimSpace(why) == "" {
					t.Errorf("%s is allowlisted with no justification", key)
				}
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

	// The allowlist must not outlive the fields it excuses, or a stale entry
	// silently pre-authorises a future field of the same name.
	t.Run("no allowlist entry names a field that no longer exists", func(t *testing.T) {
		for key := range payloadFieldAllowlist {
			typeName, fieldName, ok := strings.Cut(key, ".")
			if !ok {
				t.Errorf("allowlist key %q is not in Type.Field form", key)
				continue
			}
			p, known := payloads[typeName]
			if !known {
				t.Errorf("allowlist names unknown payload type %q", typeName)
				continue
			}
			if _, found := reflect.TypeOf(p).FieldByName(fieldName); !found {
				t.Errorf("allowlist entry %q names a field that no longer exists; delete it", key)
			}
		}
	})
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
