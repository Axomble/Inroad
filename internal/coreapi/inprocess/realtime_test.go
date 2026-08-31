package inprocess

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/inroad/inroad/internal/app/inbox"
	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/realtime"
)

// recordingPublisher captures what would have gone to Redis, so the envelope a
// worker actually produces can be asserted without a broker.
type recordingPublisher struct {
	calls []struct {
		workspaceID uuid.UUID
		envelope    realtime.Envelope
	}
	err error
}

func (p *recordingPublisher) Publish(_ context.Context, workspaceID uuid.UUID, ev realtime.Envelope) (int64, error) {
	if p.err != nil {
		return 0, p.err
	}
	p.calls = append(p.calls, struct {
		workspaceID uuid.UUID
		envelope    realtime.Envelope
	}{workspaceID, ev})
	return int64(len(p.calls)), nil
}

var _ realtime.Publisher = (*recordingPublisher)(nil)

func TestPublishRealtime_SendsTheEnvelopeToTheWorkspacesChannel(t *testing.T) {
	pub := &recordingPublisher{}
	c := client{realtime: pub}
	workspaceID := uuid.New()
	at := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)

	err := c.PublishRealtime(context.Background(), coreapi.RealtimeEventInput{
		WorkspaceID: workspaceID.String(),
		Type:        "inbox.message.created",
		SubjectKind: "thread",
		SubjectID:   "t1",
		ActorID:     "u1",
		OccurredAt:  at,
		Data:        map[string]any{"thread_id": "t1"},
	})
	if err != nil {
		t.Fatalf("PublishRealtime: %v", err)
	}

	if len(pub.calls) != 1 {
		t.Fatalf("published %d events, want 1", len(pub.calls))
	}
	got := pub.calls[0]
	// The workspace is the tenant boundary: an event published to the wrong
	// channel is a cross-tenant leak.
	if got.workspaceID != workspaceID {
		t.Errorf("workspace = %v, want %v", got.workspaceID, workspaceID)
	}
	if got.envelope.Type != "inbox.message.created" {
		t.Errorf("Type = %q", got.envelope.Type)
	}
	if got.envelope.Subject.Kind != "thread" || got.envelope.Subject.ID != "t1" {
		t.Errorf("Subject = %+v", got.envelope.Subject)
	}
	if got.envelope.ActorID != "u1" {
		t.Errorf("ActorID = %q, want u1", got.envelope.ActorID)
	}
	if !got.envelope.At.Equal(at) {
		t.Errorf("At = %v, want %v", got.envelope.At, at)
	}
	// Data is marshalled here rather than in the hub, so the hub writes bytes
	// once per event instead of re-marshalling per connection.
	var data map[string]any
	if err := json.Unmarshal(got.envelope.Data, &data); err != nil {
		t.Fatalf("envelope data is not valid JSON: %v", err)
	}
	if data["thread_id"] != "t1" {
		t.Errorf("data = %#v", data)
	}
}

// A client built without WithRealtime must be a silent no-op, not an error: that
// configuration is valid and means "browsers fall back to polling".
func TestPublishRealtime_WithoutAHubIsANoOp(t *testing.T) {
	c := client{} // no realtime

	err := c.PublishRealtime(context.Background(), coreapi.RealtimeEventInput{
		WorkspaceID: uuid.NewString(),
		Type:        "inbox.message.created",
	})
	if err != nil {
		t.Errorf("PublishRealtime without a hub = %v, want nil", err)
	}
}

func TestPublishRealtime_RejectsAnUnparseableWorkspace(t *testing.T) {
	pub := &recordingPublisher{}
	c := client{realtime: pub}

	if err := c.PublishRealtime(context.Background(), coreapi.RealtimeEventInput{
		WorkspaceID: "not-a-uuid",
		Type:        "inbox.message.created",
	}); err == nil {
		t.Error("err = nil for an unparseable workspace, want an error")
	}
	if len(pub.calls) != 0 {
		t.Errorf("published %d events despite a bad workspace, want 0", len(pub.calls))
	}
}

// Nil Data must not become the JSON literal "null": the field is omitempty on
// the wire, and a client reading `data` would otherwise get null rather than
// nothing.
func TestPublishRealtime_NilDataStaysAbsent(t *testing.T) {
	pub := &recordingPublisher{}
	c := client{realtime: pub}

	if err := c.PublishRealtime(context.Background(), coreapi.RealtimeEventInput{
		WorkspaceID: uuid.NewString(),
		Type:        "campaign.launched",
	}); err != nil {
		t.Fatalf("PublishRealtime: %v", err)
	}
	if got := pub.calls[0].envelope.Data; got != nil {
		t.Errorf("Data = %q, want nil", got)
	}
}

func TestPublishRealtime_PropagatesAPublishFailure(t *testing.T) {
	wantErr := errors.New("redis is down")
	c := client{realtime: &recordingPublisher{err: wantErr}}

	err := c.PublishRealtime(context.Background(), coreapi.RealtimeEventInput{
		WorkspaceID: uuid.NewString(),
		Type:        "inbox.message.created",
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v wrapped", err, wantErr)
	}
}

// --- the inbox emit -------------------------------------------------------

func testThread() inbox.Thread {
	campaignID, contactID := uuid.New(), uuid.New()
	return inbox.Thread{
		ID:          uuid.New(),
		WorkspaceID: uuid.New(),
		MailboxID:   uuid.New(),
		CampaignID:  &campaignID,
		ContactID:   &contactID,
		Subject:     "re: quick question",
		Unread:      true,
	}
}

func TestPublishInboxMessageCreated_CarriesIdsOnly(t *testing.T) {
	pub := &recordingPublisher{}
	c := client{realtime: pub}
	thread := testThread()

	c.publishInboxMessageCreated(context.Background(), thread.WorkspaceID.String(), thread, time.Now())

	if len(pub.calls) != 1 {
		t.Fatalf("published %d events, want 1", len(pub.calls))
	}
	env := pub.calls[0].envelope

	var data map[string]any
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	for _, key := range []string{"thread_id", "mailbox_id", "unread", "campaign_id", "contact_id"} {
		if _, ok := data[key]; !ok {
			t.Errorf("data has no %q", key)
		}
	}

	// THE security assertion. A workspace-wide broadcast must not carry the
	// correspondence itself: the client refetches the thread through the normal
	// authorized endpoint, so the socket cannot become a way around the checks
	// the REST surface applies — and no recipient PII rides along.
	raw := string(env.Data)
	for _, forbidden := range []string{thread.Subject, "from_email", "body_text", "body_html", "subject"} {
		if forbidden != "" && strings.Contains(raw, forbidden) {
			t.Errorf("envelope data contains %q; payloads must be ids and minimal display fields only: %s", forbidden, raw)
		}
	}
}

// Nobody clicked to make an inbound reply arrive, so there is no actor. The
// client's self-echo guard treats an actorless event as "not mine", which is
// what makes a reply appear in the tab of whoever is looking.
func TestPublishInboxMessageCreated_HasNoActor(t *testing.T) {
	pub := &recordingPublisher{}
	c := client{realtime: pub}
	thread := testThread()

	c.publishInboxMessageCreated(context.Background(), thread.WorkspaceID.String(), thread, time.Now())

	if got := pub.calls[0].envelope.ActorID; got != "" {
		t.Errorf("ActorID = %q, want empty for a worker-originated event", got)
	}
}

// An unmatched inbound (no campaign, no contact) is normal and must still
// publish — omitting the optional ids rather than sending empty uuids, which a
// client would try to look up.
func TestPublishInboxMessageCreated_OmitsUnmatchedCampaignAndContact(t *testing.T) {
	pub := &recordingPublisher{}
	c := client{realtime: pub}
	thread := testThread()
	thread.CampaignID, thread.ContactID = nil, nil

	c.publishInboxMessageCreated(context.Background(), thread.WorkspaceID.String(), thread, time.Now())

	var data map[string]any
	if err := json.Unmarshal(pub.calls[0].envelope.Data, &data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if _, ok := data["campaign_id"]; ok {
		t.Error("campaign_id present for an unmatched inbound")
	}
	if _, ok := data["contact_id"]; ok {
		t.Error("contact_id present for an unmatched inbound")
	}
	if _, ok := data["thread_id"]; !ok {
		t.Error("thread_id missing; an unmatched inbound is still a real event")
	}
}

// The load-bearing test of this slice. The message is already committed by the
// time this runs, so a broker outage must not surface as a failure the poller
// would retry — a retry re-reads mail and could re-deliver it.
func TestPublishInboxMessageCreated_SwallowsAPublishFailure(t *testing.T) {
	c := client{realtime: &recordingPublisher{err: errors.New("redis is down")}}
	thread := testThread()

	// No return value to assert: the point is that this neither panics nor gives
	// the caller anything to fail on.
	c.publishInboxMessageCreated(context.Background(), thread.WorkspaceID.String(), thread, time.Now())
}

func TestPublishInboxMessageCreated_WithoutAHubDoesNothing(t *testing.T) {
	c := client{} // no realtime
	thread := testThread()

	c.publishInboxMessageCreated(context.Background(), thread.WorkspaceID.String(), thread, time.Now())
}

// --- send.bounced ---------------------------------------------------------

// THE test for this event. `email` — a recipient address — is in scope one field
// away from the emit, and a socket event is a WORKSPACE-WIDE broadcast reaching
// every connected tab. The address must never ride along (spec §7.3, the same
// reasoning that keeps Final-Recipient out of the bounce log).
func TestPublishSendBounced_NeverCarriesTheRecipientAddress(t *testing.T) {
	pub := &recordingPublisher{}
	c := client{realtime: pub}
	enrollmentID := uuid.NewString()

	c.publishSendBounced(context.Background(), uuid.NewString(), enrollmentID)

	if len(pub.calls) != 1 {
		t.Fatalf("published %d events, want 1", len(pub.calls))
	}
	raw := string(pub.calls[0].envelope.Data)
	for _, forbidden := range []string{"@", "email", "recipient", "to_email"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("envelope data contains %q — a recipient address must never be broadcast: %s", forbidden, raw)
		}
	}
	// It still says WHICH enrollment, which is what a client needs to patch.
	var data map[string]any
	if err := json.Unmarshal(pub.calls[0].envelope.Data, &data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if data["enrollment_id"] != enrollmentID {
		t.Errorf("enrollment_id = %v, want %q", data["enrollment_id"], enrollmentID)
	}
}

// A bounce is something that happened TO the workspace, not something a user
// did, so it carries no actor and every tab treats it as somebody else's.
func TestPublishSendBounced_HasNoActor(t *testing.T) {
	pub := &recordingPublisher{}
	c := client{realtime: pub}

	c.publishSendBounced(context.Background(), uuid.NewString(), uuid.NewString())

	if got := pub.calls[0].envelope.ActorID; got != "" {
		t.Errorf("ActorID = %q, want empty", got)
	}
}

// An unattributable bounce (a DSN matching no enrollment) still publishes — the
// campaign counts move either way — but omits the id rather than sending "",
// which a client would try to look up.
func TestPublishSendBounced_OmitsAnUnmatchedEnrollment(t *testing.T) {
	pub := &recordingPublisher{}
	c := client{realtime: pub}

	c.publishSendBounced(context.Background(), uuid.NewString(), "")

	var data map[string]any
	if err := json.Unmarshal(pub.calls[0].envelope.Data, &data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if _, ok := data["enrollment_id"]; ok {
		t.Errorf("enrollment_id present for an unmatched bounce: %#v", data)
	}
}

func TestPublishSendBounced_WithoutAHubDoesNothing(t *testing.T) {
	c := client{} // no realtime
	c.publishSendBounced(context.Background(), uuid.NewString(), uuid.NewString())
}
