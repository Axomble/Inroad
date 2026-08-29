package deadletter

import (
	"bytes"
	"context"
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

// legacyBodyBearingPayload is an inbox:reply_send payload EXACTLY as the pre-fix
// capture path produced it: the operator's reply text in body_text, alongside
// the ids. Every test in this file works from this shape, because a fixture
// without a body cannot prove anything about keeping a body out — the first
// version of this file seeded only body-less rows, so its substring search ran
// over bytes constructed never to contain the thing it looked for and it passed
// with the producer, the capture gate AND the migration all reverted.
func legacyBodyBearingPayload(ws uuid.UUID, threadID string) []byte {
	return []byte(`{"thread_id":"` + threadID + `","body_text":"` + replyBodySentinel +
		`","workspace_id":"` + ws.String() + `","task_id":"inboxreply:x:1700000000"}`)
}

// TestCaptureRedactsALegacyReplySendAndFilesIt is the CONTROL-PLANE half of the
// capture gate, and it exists because the worker-side gate alone is not enough.
//
// internal/platform/queue refuses to hand a terminal inbox:reply_send to the
// recorder, but that gate lives in the WORKER binary. During a rolling deploy an
// old worker is still running against the new control plane, and its terminal
// reply failures arrive here through coreapi — after the one-shot migration has
// already swept the table. Without a gate on this side, that old worker writes a
// fresh body-bearing 'pending' row: readable under campaigns:read, and
// replayable. The Helm chart has no migration hook at all, so the ordering
// cannot even be assumed.
//
// Redact-and-file rather than refuse. Refusing loses the operator's record that
// a send was permanently lost, which is the entire reason this table exists; the
// row is worth keeping and the body is not. 'discarded' rather than 'pending'
// for the same reason the migration flips it: a body-stripped reply left
// replayable would deliver a BLANK message to a real contact.
func TestCaptureRedactsALegacyReplySendAndFilesIt(t *testing.T) {
	ws := uuid.New()
	threadID := uuid.NewString()

	t.Run("the body never reaches the store, and the row is filed", func(t *testing.T) {
		store := newFakeStore()
		svc := NewService(store, &fakeEnqueuer{})

		//nolint:staticcheck // SA1019: naming the deprecated task type is the point —
		// this is the task an old worker is still failing.
		row, err := svc.Capture(context.Background(), Capture{
			WorkspaceID: ws, TaskType: queue.TaskInboxReplySend,
			Payload: legacyBodyBearingPayload(ws, threadID), LastError: "smtp: connection refused",
			AttemptCount: 6,
		})
		if err != nil {
			t.Fatalf("Capture: %v — the record of a permanently lost send is worth keeping", err)
		}
		if len(store.inserted) != 1 {
			t.Fatalf("inserted %d rows, want 1", len(store.inserted))
		}

		stored := store.inserted[0].Payload
		if bytes.Contains(stored, []byte(replyBodySentinel)) {
			t.Errorf("the reply body was written to task_dead_letters: %s", stored)
		}
		if bytes.Contains(stored, []byte("body_text")) {
			t.Errorf("the stored payload still carries a body_text key: %s", stored)
		}
		if row.Status != StatusDiscarded {
			t.Errorf("status = %q, want %q: a body-stripped reply that stayed replayable would "+
				"deliver a blank message to a real contact", row.Status, StatusDiscarded)
		}

		// REDACTED, not emptied. The row is the operator's only record that this
		// send was lost, so it must still name what it always named — and the
		// workspace pin in particular, without which nothing can validate it.
		var kept map[string]string
		if err := json.Unmarshal(stored, &kept); err != nil {
			t.Fatalf("the redacted payload is not a JSON object: %v (%s)", err, stored)
		}
		if kept["thread_id"] != threadID {
			t.Errorf("thread_id = %q, want it preserved (%s)", kept["thread_id"], threadID)
		}
		if kept["workspace_id"] != ws.String() {
			t.Errorf("workspace_id = %q, want it preserved (%s)", kept["workspace_id"], ws)
		}
	})

	// The gate is targeted. Every other task type is captured verbatim and stays
	// actionable — a suppression that quietly filed everything would be a hole in
	// capture rather than a fix for one disclosure.
	t.Run("an ordinary capture is untouched", func(t *testing.T) {
		store := newFakeStore()
		svc := NewService(store, &fakeEnqueuer{})
		payload := payloadFor(t, ws)

		row, err := svc.Capture(context.Background(), Capture{
			WorkspaceID: ws, TaskType: "sequence:advance", Payload: payload,
		})
		if err != nil {
			t.Fatalf("Capture: %v", err)
		}
		if row.Status != StatusPending {
			t.Errorf("status = %q, want %q — an ordinary dead letter is replayable", row.Status, StatusPending)
		}
		if !bytes.Equal(store.inserted[0].Payload, payload) {
			t.Errorf("payload = %s, want the captured bytes verbatim %s", store.inserted[0].Payload, payload)
		}
	})

	// A payload that is not a JSON object cannot be shown to carry no body, so
	// the whole thing goes rather than the one key. Nothing can own such a row
	// anyway (verifyPayloadWorkspace refuses it), so nothing is lost but bytes
	// that might be correspondence.
	t.Run("a non-object legacy payload is dropped entirely", func(t *testing.T) {
		store := newFakeStore()
		svc := NewService(store, &fakeEnqueuer{})

		//nolint:staticcheck // SA1019: the deprecated type is the subject.
		if _, err := svc.Capture(context.Background(), Capture{
			WorkspaceID: ws, TaskType: queue.TaskInboxReplySend,
			Payload: []byte(`"` + replyBodySentinel + `"`),
		}); err != nil {
			t.Fatalf("Capture: %v", err)
		}
		if got := string(store.inserted[0].Payload); got != "null" {
			t.Errorf("payload = %s, want %q — an unparseable legacy payload cannot be proven "+
				"free of correspondence, so none of it is kept", got, "null")
		}
	})
}

// TestReplayRefusesALegacyContentBearingTaskType closes the other half of the
// rolling-deploy window, reported separately by the reviewer: Service.Replay
// re-enqueues row.TaskType VERBATIM with no allowlist, so a body-bearing
// inbox:reply_send row written before the gates existed is re-enqueueable as a
// real send — the drain handler is still registered, so it would deliver.
//
// The refusal is deliberately independent of whether the payload still has a
// body: after the migration the row's body is gone, and replaying THAT sends a
// blank message. Neither outcome is acceptable, so the task type itself is
// refused.
func TestReplayRefusesALegacyContentBearingTaskType(t *testing.T) {
	store := newFakeStore()
	enq := &fakeEnqueuer{}
	svc := NewService(store, enq)
	ws := uuid.New()

	// Seeded straight into the store, bypassing Capture: this is a row a
	// PRE-FIX binary wrote, which is the only way one can exist.
	//nolint:staticcheck // SA1019: the deprecated type is the subject.
	row := store.seed(gen.TaskDeadLetter{
		WorkspaceID: ws, TaskType: queue.TaskInboxReplySend,
		Payload: legacyBodyBearingPayload(ws, uuid.NewString()), Status: StatusPending,
	})

	_, err := svc.Replay(context.Background(), ws, row.ID)
	if err == nil {
		t.Fatal("Replay of an inbox:reply_send succeeded; the drain handler would deliver it")
	}
	if enq.count() != 0 {
		t.Errorf("enqueued %d times, want 0", enq.count())
	}
	// The claim it won on the way in must be given back, or a refusal would
	// leave the row stuck reporting a replay that never happened.
	if store.status(row.ID) != StatusPending {
		t.Errorf("status = %q, want the claim released back to %q", store.status(row.ID), StatusPending)
	}
}

// TestDeadLetterListNeverServesReplyBody is the READ-SIDE half of the fix, and
// the last line of defence.
//
// The disclosure was this endpoint. queue.DeadLetterErrorHandler stored an
// inbox:reply_send payload byte-for-byte, and this list served it verbatim under
// campaigns:read — an OAuth-grantable scope, while inbox:read is deliberately
// NOT one, precisely because reply bodies are correspondence
// (internal/app/auth/scopes.go). So a delegated third-party client could read
// reply text through a scope built to exclude it.
//
// Three gates close it, each proved where it lives:
//   - no NEW row from a current worker can carry a body — capture of
//     inbox:reply_send is refused before it leaves the worker
//     (TestTerminalFailureOfTheLegacyReplySendIsNotCaptured, in
//     internal/platform/queue, which owns that decision);
//   - no new row from a STALE worker can either — Capture redacts and files it
//     (TestCaptureRedactsALegacyReplySendAndFilesIt, above);
//   - no EXISTING row still carries one — the migration strips body_text and
//     flips a pending row to discarded (TestMigrationRedactsLegacyReplyBodies
//     in store_integration_test.go, which runs the real statement).
//
// This test seeds a row that DOES carry the body, straight into the store,
// because that row is reachable in production: a control plane running this
// code against a database whose migration has not run yet (the Helm chart has no
// migration hook) holds exactly it. Whatever is on disk, the bytes a
// campaigns:read principal receives must carry no reply body. The assertion is a
// substring search over the RAW response rather than a check on a named field,
// so reintroducing the text under a different field name — or under a different
// task type — fails here too.
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
	preFix := legacyBodyBearingPayload(ws, "t")
	if !bytes.Contains(preFix, []byte(replyBodySentinel)) || !bytes.Contains(preFix, []byte("body_text")) {
		t.Fatal("the substring search does not match a payload that plainly carries a reply body")
	}

	// 1. An ordinary captured row, so the "no sentinel" assertions cannot pass
	//    merely because the list came back empty.
	ordinary := store.seed(gen.TaskDeadLetter{
		WorkspaceID: ws, TaskType: "sequence:advance",
		Payload: payloadFor(t, ws), Status: StatusPending,
	})

	// 2. A legacy inbox:reply_send row STILL CARRYING ITS BODY, and still
	//    pending: the pre-fix shape, on a database the redaction migration has
	//    not reached. Keyed off the real constant so renaming the task type
	//    cannot leave this fixture quietly describing something that no longer
	//    exists.
	//nolint:staticcheck // SA1019: naming the deprecated task type is the point —
	// this fixture is the legacy row that is still on disk.
	unmigrated := store.seed(gen.TaskDeadLetter{
		WorkspaceID: ws, TaskType: queue.TaskInboxReplySend,
		Payload: legacyBodyBearingPayload(ws, uuid.NewString()), Status: StatusPending,
	})

	// 3. The same row AS THE MIGRATION LEAVES IT: body key removed, formerly
	//    pending moved to discarded. Redacted rather than deleted — the row is
	//    the operator's only record that a send was lost.
	//nolint:staticcheck // SA1019: as above.
	redacted := store.seed(gen.TaskDeadLetter{
		WorkspaceID: ws, TaskType: queue.TaskInboxReplySend,
		Payload: []byte(`{"thread_id":"` + uuid.NewString() + `","workspace_id":"` + ws.String() +
			`","task_id":"inboxreply:x:1700000000"}`),
		Status: StatusDiscarded,
	})

	// 4. The shape that REPLACED it: a pointer to an inbox_pending_replies row.
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
		if len(page.Items) != 4 {
			t.Fatalf("listed %d rows, want the 4 seeded — an empty list would make the "+
				"substring assertion vacuous", len(page.Items))
		}
		assertNoReplyBody(t, w.Body.Bytes())
	})

	t.Run("neither does a single-row read", func(t *testing.T) {
		for _, row := range []gen.TaskDeadLetter{ordinary, unmigrated, redacted, pointer} {
			w := apiKeyRequest(t, h, http.MethodGet, "/"+row.ID.String(), ws, readOnly)
			if w.Code != http.StatusOK {
				t.Fatalf("GET %s: status = %d, want 200: %s", row.TaskType, w.Code, w.Body)
			}
			assertNoReplyBody(t, w.Body.Bytes())
		}
	})

	// The redaction is NOT a blanket "strip anything called body_text": the row
	// still has to name what failed, or triage is impossible.
	t.Run("the unmigrated row still names its thread", func(t *testing.T) {
		w := apiKeyRequest(t, h, http.MethodGet, "/"+unmigrated.ID.String(), ws, readOnly)
		var got deadLetterResponse
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		var payload map[string]string
		if err := json.Unmarshal(got.Payload, &payload); err != nil {
			t.Fatalf("payload is not a JSON object: %v (%s)", err, got.Payload)
		}
		if payload["thread_id"] == "" || payload["workspace_id"] != ws.String() {
			t.Errorf("payload = %s, want the ids preserved", got.Payload)
		}
	})

	// The other half of the migration, as a caller sees it. A redacted row must
	// not be replayable: replaying one would hand the queue a reply with no body
	// and deliver a blank message to a real contact.
	sender := []string{auth.ScopeCampaignsRead, auth.ScopeCampaignsSend}
	t.Run("a redacted legacy row cannot be replayed", func(t *testing.T) {
		w := apiKeyRequest(t, h, http.MethodPost, "/"+redacted.ID.String()+"/replay", ws, sender)
		if w.Code != http.StatusConflict {
			t.Errorf("status = %d, want 409: a body-stripped reply must never be re-enqueued: %s", w.Code, w.Body)
		}
	})

	// And the un-migrated one, which IS still pending, must be refused on the
	// task type rather than reaching the queue — 422, the endpoint's "this row
	// can never be replayed" answer.
	t.Run("an unmigrated legacy row cannot be replayed either", func(t *testing.T) {
		w := apiKeyRequest(t, h, http.MethodPost, "/"+unmigrated.ID.String()+"/replay", ws, sender)
		if w.Code != http.StatusUnprocessableEntity {
			t.Errorf("status = %d, want 422: re-enqueuing it would deliver the operator's "+
				"reply through the drain handler: %s", w.Code, w.Body)
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
