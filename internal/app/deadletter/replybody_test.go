package deadletter

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/queue"
)

// replyBodySentinel is a string that appears nowhere else in this package, so
// finding it anywhere in a response is unambiguous.
const replyBodySentinel = "SENTINEL-operator-private-reply-3f9c2a"

// TestDeadLetterListNeverServesReplyBody is the READ-SIDE half of the fix.
//
// The disclosure was this endpoint. queue.DeadLetterErrorHandler stored an
// inbox:reply_send payload byte-for-byte, and this list served it verbatim under
// campaigns:read — an OAuth-grantable scope, while inbox:read is deliberately
// NOT one, precisely because reply bodies are correspondence
// (internal/app/auth/scopes.go). So a delegated third-party client could read
// reply text through a scope built to exclude it.
//
// Two changes close it, each proved where it lives:
//   - no NEW row can carry a body — capture of inbox:reply_send is refused
//     outright (TestTerminalFailureOfTheLegacyReplySendIsNotCaptured, in
//     internal/platform/queue, which owns that decision);
//   - no EXISTING row still carries one — the migration strips body_text and
//     flips a pending row to discarded (TestMigrationRedactsLegacyReplyBodies
//     in store_integration_test.go, which runs the real statement).
//
// What THIS test holds is the surface those two feed: over every row shape the
// system can still hold, the bytes a campaigns:read principal receives carry no
// reply body and no body_text key at all. The assertion is a substring search
// over the RAW response rather than a check on a named field, so reintroducing
// the text under a different field name — or under a different task type —
// fails here too.
//
// The principal must be an API KEY. A session principal implicitly holds every
// scope (auth.Principal.HasScope), so reading as one would prove nothing about
// what campaigns:read can actually reach.
func TestDeadLetterListNeverServesReplyBody(t *testing.T) {
	store := newFakeStore()
	svc := NewService(store, &fakeEnqueuer{})
	h := NewHandler(svc)
	ws := uuid.New()

	// The search has to be able to find the thing it is looking for, or the two
	// assertions below would pass on any response at all.
	preFix := []byte(`{"payload":{"thread_id":"t","body_text":"` + replyBodySentinel + `"}}`)
	if !bytes.Contains(preFix, []byte(replyBodySentinel)) || !bytes.Contains(preFix, []byte("body_text")) {
		t.Fatal("the substring search does not match a payload that plainly carries a reply body")
	}

	// 1. An ordinary captured row, so the "no sentinel" assertions cannot pass
	//    merely because the list came back empty.
	ordinary := store.seed(gen.TaskDeadLetter{
		WorkspaceID: ws, TaskType: "sequence:advance",
		Payload: payloadFor(t, ws), Status: StatusPending,
	})

	// 2. A legacy inbox:reply_send row AS THE MIGRATION LEAVES IT: the body key
	//    removed, and a formerly-pending row moved to discarded. Redacted rather
	//    than deleted — the row is the operator's only record that a send was
	//    lost — and discarded rather than left pending, because a body-stripped
	//    row that stayed replayable would send a BLANK reply to a real contact.
	//    Keyed off the real constant so renaming the task type cannot leave this
	//    fixture quietly describing something that no longer exists.
	//nolint:staticcheck // SA1019: naming the deprecated task type is the point —
	// this fixture is the legacy row the migration had to redact.
	redacted := store.seed(gen.TaskDeadLetter{
		WorkspaceID: ws, TaskType: queue.TaskInboxReplySend,
		Payload: []byte(`{"thread_id":"` + uuid.NewString() + `","workspace_id":"` + ws.String() +
			`","task_id":"inboxreply:x:1700000000"}`),
		Status: StatusDiscarded,
	})

	// 3. The shape that REPLACED it: a pointer to an inbox_pending_replies row.
	pointer := store.seed(gen.TaskDeadLetter{
		WorkspaceID: ws, TaskType: queue.TaskInboxPendingReplySend,
		Payload: []byte(`{"pending_id":"` + uuid.NewString() + `","workspace_id":"` + ws.String() + `"}`),
		Status:  StatusPending,
	})

	readOnly := []string{auth.ScopeCampaignsRead}

	t.Run("the list serves no reply body", func(t *testing.T) {
		w := apiKeyRequest(t, h, http.MethodGet, "/", ws, readOnly)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body)
		}
		var page listResponse
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(page.Items) != 3 {
			t.Fatalf("listed %d rows, want the 3 seeded — an empty list would make the "+
				"substring assertion vacuous", len(page.Items))
		}
		assertNoReplyBody(t, w.Body.Bytes())
	})

	t.Run("neither does a single-row read", func(t *testing.T) {
		for _, row := range []gen.TaskDeadLetter{ordinary, redacted, pointer} {
			w := apiKeyRequest(t, h, http.MethodGet, "/"+row.ID.String(), ws, readOnly)
			if w.Code != http.StatusOK {
				t.Fatalf("GET %s: status = %d, want 200: %s", row.TaskType, w.Code, w.Body)
			}
			assertNoReplyBody(t, w.Body.Bytes())
		}
	})

	// The other half of the migration, as a caller sees it. A redacted row must
	// not be replayable: replaying one would hand the queue a reply with no body
	// and deliver a blank message to a real contact.
	t.Run("a redacted legacy row cannot be replayed", func(t *testing.T) {
		w := apiKeyRequest(t, h, http.MethodPost, "/"+redacted.ID.String()+"/replay", ws,
			[]string{auth.ScopeCampaignsRead, auth.ScopeCampaignsSend})
		if w.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409: a body-stripped reply must never be re-enqueued: %s", w.Code, w.Body)
		}
	})
}

// assertNoReplyBody searches the RAW bytes rather than a decoded field, so the
// check survives a body reintroduced under any other name.
func assertNoReplyBody(t *testing.T, body []byte) {
	t.Helper()
	if bytes.Contains(body, []byte(replyBodySentinel)) {
		t.Errorf("the response carries a reply body:\n%s", body)
	}
	// "body_text" is not itself the secret, but no payload this endpoint can
	// serve has any business carrying that key: the body lives in a row now, and
	// the task names the row.
	if bytes.Contains(body, []byte("body_text")) {
		t.Errorf("the response carries a body_text key; a task payload is a pointer, "+
			"never correspondence:\n%s", body)
	}
}
