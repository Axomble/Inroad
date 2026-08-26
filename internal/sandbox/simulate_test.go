package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeStore records what the Simulator asked for, so the tests can assert the
// SHAPE of a run — which rows, with which links and timestamps — without a
// database. Failing methods let the error paths be exercised too.
type fakeStore struct {
	sends     []SendRecord
	tracking  []TrackingRecord
	replies   []ReplyRecord
	stops     []stopCall
	suppress  []suppressCall
	enrolled  []EnrollmentRecord
	sendIDs   map[string]uuid.UUID
	failSend  bool
	failReply bool
}

type stopCall struct {
	campaignID, contactID uuid.UUID
	reason                string
	at                    time.Time
}

type suppressCall struct {
	email, reason string
}

func newFakeStore() *fakeStore { return &fakeStore{sendIDs: map[string]uuid.UUID{}} }

func (f *fakeStore) RecordSend(_ context.Context, in SendRecord) (uuid.UUID, error) {
	if f.failSend {
		return uuid.Nil, errors.New("boom")
	}
	f.sends = append(f.sends, in)
	id := uuid.New()
	f.sendIDs[in.MessageID] = id
	return id, nil
}

func (f *fakeStore) RecordTracking(_ context.Context, in TrackingRecord) error {
	f.tracking = append(f.tracking, in)
	return nil
}

func (f *fakeStore) RecordInboundReply(_ context.Context, in ReplyRecord) error {
	if f.failReply {
		return errors.New("boom")
	}
	f.replies = append(f.replies, in)
	return nil
}

func (f *fakeStore) StopEnrollment(_ context.Context, _, campaignID, contactID uuid.UUID, reason string, at time.Time) error {
	f.stops = append(f.stops, stopCall{campaignID, contactID, reason, at})
	return nil
}

func (f *fakeStore) SuppressContact(_ context.Context, _ uuid.UUID, email, reason string) error {
	f.suppress = append(f.suppress, suppressCall{email, reason})
	return nil
}

func (f *fakeStore) EnsureEnrollment(_ context.Context, in EnrollmentRecord) error {
	f.enrolled = append(f.enrolled, in)
	return nil
}

// recordingDeliverer captures what would have gone on the wire.
type recordingDeliverer struct {
	sent []OutboundMessage
	fail bool
}

func (r *recordingDeliverer) Deliver(_ context.Context, m OutboundMessage) error {
	if r.fail {
		return errors.New("smtp down")
	}
	r.sent = append(r.sent, m)
	return nil
}

// newTarget builds a target with n contacts, mirroring what the seeder passes.
func newTarget(n int) Target {
	t := Target{
		WorkspaceID: uuid.New(), CampaignID: uuid.New(), MailboxID: uuid.New(),
		MailboxEmail: "rowan@outbound.inroad.test", MailboxName: "Rowan Ellis",
	}
	for _, p := range BuildPersonas(n) {
		t.Contacts = append(t.Contacts, TargetContact{ContactID: uuid.New(), Persona: p})
	}
	return t
}

func runSim(t *testing.T, n int, opts Options) (*fakeStore, Target, Result) {
	t.Helper()
	store := newFakeStore()
	if opts.Now.IsZero() {
		opts.Now = time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	}
	target := newTarget(n)
	res, err := NewSimulator(store, store, opts).Run(context.Background(), target)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return store, target, res
}

func TestRunWritesTheWholeFunnel(t *testing.T) {
	store, _, res := runSim(t, 120, Options{})

	if res.Contacts != 120 {
		t.Errorf("Contacts = %d, want 120", res.Contacts)
	}
	if len(store.sends) != res.Sends || res.Sends == 0 {
		t.Errorf("Sends = %d, store has %d", res.Sends, len(store.sends))
	}
	if res.Opens == 0 || res.Clicks == 0 || res.Replies == 0 || res.Bounces == 0 {
		t.Errorf("thin run: %+v", res)
	}
	if len(store.replies) != res.Replies {
		t.Errorf("Replies = %d, store recorded %d", res.Replies, len(store.replies))
	}
	// Every send row must be terminal-and-sent with a real Message-ID: the
	// thread's outbound leg only picks up sends with a non-null sent_at.
	for _, s := range store.sends {
		if s.SentAt.IsZero() {
			t.Fatalf("send for %s has no sent_at", s.ToEmail)
		}
		if !strings.HasPrefix(s.MessageID, "<") || !strings.HasSuffix(s.MessageID, ">") {
			t.Errorf("send message id %q is not RFC 5322 bracketed", s.MessageID)
		}
	}
}

// THE central invariant of this harness. The outbound leg of a thread is
// synthesized at read time from `sends` joined to sequence_steps; writing it
// into inbox_messages as well would render every outbound message twice.
func TestOnlyTheInboundLegIsWrittenToTheInbox(t *testing.T) {
	store, _, _ := runSim(t, 150, Options{})
	if len(store.replies) == 0 {
		t.Fatal("no replies produced; the invariant under test was not exercised")
	}
	// ReplyRecord is inbound-only by construction, so the assertion that
	// matters is that the OUTBOUND content never travelled through it: the
	// bodies recorded must be prospect replies, not campaign copy.
	for _, r := range store.replies {
		if strings.Contains(r.BodyText, "{{") {
			t.Errorf("reply body %q carries an unrendered template", r.BodyText)
		}
		for _, step := range campaignSteps {
			if step.BodyText != "" && r.BodyText == step.BodyText {
				t.Errorf("an outbound step body was written to the inbox as inbound: %q", r.BodyText)
			}
		}
	}
}

// The synthesized outbound leg joins on (workspace_id, campaign_id,
// contact_id). A thread missing either link renders the reply with no
// conversation above it.
func TestThreadsCarryTheLinksTheOutboundJoinNeeds(t *testing.T) {
	store, target, _ := runSim(t, 150, Options{})
	for _, r := range store.replies {
		if r.CampaignID != target.CampaignID {
			t.Errorf("thread campaign_id = %v, want %v", r.CampaignID, target.CampaignID)
		}
		if r.ContactID == uuid.Nil {
			t.Error("thread has no contact_id; the outbound leg cannot be joined")
		}
		if r.WorkspaceID != target.WorkspaceID {
			t.Errorf("thread workspace_id = %v, want %v", r.WorkspaceID, target.WorkspaceID)
		}
		if r.MailboxID != target.MailboxID {
			t.Errorf("thread mailbox_id = %v, want %v", r.MailboxID, target.MailboxID)
		}
	}
}

// The thread's root_message_id must be STEP 1's Message-ID, not that of the
// step actually replied to: it is what groups the whole conversation onto one
// thread (the partial unique index is on (workspace, mailbox, root)).
func TestThreadRootIsStepOnesMessageID(t *testing.T) {
	store, target, _ := runSim(t, 150, Options{})

	// Index the step-1 send per contact.
	stepOne := map[uuid.UUID]string{}
	for _, s := range store.sends {
		if s.StepOrder == 1 {
			stepOne[s.ContactID] = s.MessageID
		}
	}
	if len(store.replies) == 0 {
		t.Fatal("no replies produced")
	}
	for _, r := range store.replies {
		want, ok := stepOne[r.ContactID]
		if !ok {
			t.Fatalf("reply for contact %v with no step-1 send", r.ContactID)
		}
		if r.RootMessageID != want {
			t.Errorf("thread root = %q, want step 1's %q", r.RootMessageID, want)
		}
		if r.RootMessageID == "" {
			t.Error("empty root would make every reply its own legacy thread")
		}
	}
	// A reply to a LATER step must still anchor on step 1, which is only
	// meaningfully tested if some contact replied past step 1.
	var repliedLate bool
	for _, e := range store.enrolled {
		if e.CurrentStep > 1 {
			repliedLate = true
			break
		}
	}
	if !repliedLate {
		t.Error("no contact progressed past step 1; multi-step threading untested")
	}
	// Enrollment thread roots must agree with the thread's.
	for _, e := range store.enrolled {
		if e.ThreadRootID != stepOne[e.ContactID] {
			t.Errorf("enrollment thread_root_id = %q, want %q", e.ThreadRootID, stepOne[e.ContactID])
		}
	}
	_ = target
}

// Follow-up steps thread against the root; step 1 IS the root and references
// nothing.
func TestReferencesChainAnchorsOnTheRoot(t *testing.T) {
	store, _, _ := runSim(t, 150, Options{})
	roots := map[uuid.UUID]string{}
	for _, s := range store.sends {
		if s.StepOrder == 1 {
			roots[s.ContactID] = s.MessageID
			if s.ReferencesHeader != "" {
				t.Errorf("step 1 references %q, want empty (it is the root)", s.ReferencesHeader)
			}
		}
	}
	for _, s := range store.sends {
		if s.StepOrder > 1 && s.ReferencesHeader != roots[s.ContactID] {
			t.Errorf("step %d references %q, want the root %q", s.StepOrder, s.ReferencesHeader, roots[s.ContactID])
		}
	}
}

// The inbox orders and windows threads by last_message_at. Seeding replies at
// "now" would pile every thread onto today and make the time scopes useless.
func TestRepliesAreSpreadOverThePast(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	store, _, _ := runSim(t, 200, Options{Now: now, Window: 21 * 24 * time.Hour})

	days := map[string]bool{}
	for _, r := range store.replies {
		if r.OccurredAt.After(now) {
			t.Errorf("reply at %v is in the future", r.OccurredAt)
		}
		days[r.OccurredAt.Format("2006-01-02")] = true
	}
	if len(days) < 3 {
		t.Errorf("replies landed on only %d distinct days: the inbox time scopes need a spread", len(days))
	}
}

// Threads must join reply_labels on a key the workspace actually has, and a
// DEFAULT run has to exercise every label the simulator can produce — an
// inbox demo where three of the five triage buckets are empty is not the
// "believable, fully-populated workspace" this harness exists to make.
func TestDefaultRunCoversEveryReplyLabel(t *testing.T) {
	store, _, res := runSim(t, DefaultContacts, Options{})

	builtin := map[string]bool{
		"positive": true, "negative": true, "neutral": true, "unsubscribe": true,
		"out_of_office": true, "auto_reply": true, "unknown": true,
	}
	seen := map[string]int{}
	for _, r := range store.replies {
		if !builtin[r.ReplyClass] {
			t.Errorf("reply class %q is not a builtin reply label key", r.ReplyClass)
		}
		seen[r.ReplyClass]++
	}
	for _, f := range []ReplyFlavor{ReplyPositive, ReplyQuestion, ReplyNegative, ReplyOutOfOffice, ReplyUnsubscribe} {
		if seen[f.LabelKey()] == 0 {
			t.Errorf("a default run produced no %q thread; distribution was %v", f.LabelKey(), seen)
		}
	}
	// And enough threads overall that the inbox is worth opening.
	if res.Threads < 15 {
		t.Errorf("a default run seeded only %d threads, too thin to demo", res.Threads)
	}
}

// Replies come FROM the prospect TO the mailbox; a reversed pair renders the
// thread as though the operator wrote it.
func TestRepliesAreAddressedFromProspectToMailbox(t *testing.T) {
	store, target, _ := runSim(t, 120, Options{})
	for _, r := range store.replies {
		if r.ToEmail != target.MailboxEmail {
			t.Errorf("reply to_email = %q, want the mailbox %q", r.ToEmail, target.MailboxEmail)
		}
		if r.FromEmail == target.MailboxEmail {
			t.Error("reply appears to come from the sending mailbox")
		}
		if r.FromName == "" || r.BodyHTML == "" {
			t.Errorf("reply from %q is missing display name or HTML body", r.FromEmail)
		}
	}
}

// A reply or bounce stops the sequence and, where the label says so,
// suppresses the address — matching what the real handlers do.
func TestOutcomesStopEnrollmentsAndSuppress(t *testing.T) {
	// Run at the size the product actually seeds: the unsubscribe flavour is
	// the rarest outcome, and asserting it at a smaller population would be
	// asserting something a default run does not in fact guarantee.
	store, _, res := runSim(t, DefaultContacts, Options{})

	var replied, bounced int
	for _, s := range store.stops {
		switch s.reason {
		case "replied":
			replied++
		case "bounced":
			bounced++
		default:
			t.Errorf("unexpected stop reason %q", s.reason)
		}
		if s.at.IsZero() {
			t.Error("stop recorded with no timestamp")
		}
	}
	if replied != res.Replies || bounced != res.Bounces {
		t.Errorf("stops (replied=%d bounced=%d) disagree with result %+v", replied, bounced, res)
	}

	var bounceSup, unsubSup int
	for _, s := range store.suppress {
		switch s.reason {
		case "bounce":
			bounceSup++
		case "unsubscribe":
			unsubSup++
		default:
			t.Errorf("unexpected suppression reason %q", s.reason)
		}
	}
	if bounceSup != res.Bounces {
		t.Errorf("suppressed %d bounces, want %d", bounceSup, res.Bounces)
	}
	if unsubSup == 0 {
		t.Error("no unsubscribe reply suppressed its contact")
	}
	// A bounced contact never generates tracking or a thread.
	for _, s := range store.suppress {
		if s.reason != "bounce" {
			continue
		}
		for _, r := range store.replies {
			if r.FromEmail == s.email {
				t.Errorf("bounced address %q also produced a reply thread", s.email)
			}
		}
	}
}

// Tracking events have to point at their own send and carry a plausible agent,
// or the reporting screens count them as bot opens.
func TestTrackingEventsAttachToTheirSend(t *testing.T) {
	store, target, _ := runSim(t, 120, Options{})
	known := map[uuid.UUID]bool{}
	for _, id := range store.sendIDs {
		known[id] = true
	}
	var opens, clicks int
	for _, e := range store.tracking {
		if !known[e.SendID] {
			t.Errorf("tracking event references unknown send %v", e.SendID)
		}
		if e.CampaignID != target.CampaignID || e.WorkspaceID != target.WorkspaceID {
			t.Error("tracking event is not scoped to the run's campaign/workspace")
		}
		if e.UserAgent == "" {
			t.Error("tracking event has no user agent")
		}
		switch e.Kind {
		case "open":
			opens++
		case "click":
			clicks++
			if e.URL == "" {
				t.Error("click event has no url")
			}
		default:
			t.Errorf("unexpected tracking kind %q", e.Kind)
		}
	}
	if opens == 0 || clicks == 0 {
		t.Errorf("tracking produced opens=%d clicks=%d", opens, clicks)
	}
}

// A half-seeded workspace that reports success is worse than a failure that
// can be re-run, so any store error must abort and propagate.
func TestRunAbortsOnStoreFailure(t *testing.T) {
	for name, mutate := range map[string]func(*fakeStore){
		"send":  func(f *fakeStore) { f.failSend = true },
		"reply": func(f *fakeStore) { f.failReply = true },
	} {
		t.Run(name, func(t *testing.T) {
			store := newFakeStore()
			mutate(store)
			_, err := NewSimulator(store, store, Options{Now: time.Now()}).
				Run(context.Background(), newTarget(150))
			if err == nil {
				t.Fatal("Run succeeded despite a failing store")
			}
			if !strings.Contains(err.Error(), "simulate ") {
				t.Errorf("error %q does not say which contact failed", err)
			}
		})
	}
}

func TestRunPropagatesDelivererFailure(t *testing.T) {
	store := newFakeStore()
	_, err := NewSimulator(store, store, Options{
		Now: time.Now(), Deliverer: &recordingDeliverer{fail: true},
	}).Run(context.Background(), newTarget(5))
	if err == nil {
		t.Fatal("Run succeeded despite a failing deliverer")
	}
	if !strings.Contains(err.Error(), "smtp down") {
		t.Errorf("error %q does not wrap the deliverer's cause", err)
	}
}

// Delivery is optional and additive: recording still happens, and what goes on
// the wire has to match the rows.
func TestDelivererReceivesEveryRecordedSend(t *testing.T) {
	store := newFakeStore()
	rec := &recordingDeliverer{}
	target := newTarget(40)
	res, err := NewSimulator(store, store, Options{
		Now: time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC), Deliverer: rec,
	}).Run(context.Background(), target)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rec.sent) != len(store.sends) || res.Delivered != len(rec.sent) {
		t.Fatalf("delivered %d, recorded %d sends, result says %d", len(rec.sent), len(store.sends), res.Delivered)
	}
	for i, m := range rec.sent {
		if m.MessageID != store.sends[i].MessageID {
			t.Errorf("delivered message id %q != recorded %q", m.MessageID, store.sends[i].MessageID)
		}
		if m.FromEmail != target.MailboxEmail {
			t.Errorf("delivered from %q, want %q", m.FromEmail, target.MailboxEmail)
		}
		if strings.Contains(m.Subject, "{{") || strings.Contains(m.BodyText, "{{") {
			t.Errorf("delivered message carries an unrendered placeholder: %q / %q", m.Subject, m.BodyText)
		}
		if m.Subject == "" {
			t.Error("delivered message has no subject")
		}
	}
}

// Follow-up steps carry no subject of their own; the delivered mail must show
// the "Re: <step 1>" the recipient's client would actually thread on.
func TestFollowUpDeliveriesUseTheReSubject(t *testing.T) {
	store := newFakeStore()
	rec := &recordingDeliverer{}
	if _, err := NewSimulator(store, store, Options{
		Now: time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC), Deliverer: rec,
	}).Run(context.Background(), newTarget(60)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var sawFollowUp bool
	for i, m := range rec.sent {
		if store.sends[i].StepOrder == 1 {
			if strings.HasPrefix(m.Subject, "Re: ") {
				t.Errorf("step 1 subject %q should not be a Re:", m.Subject)
			}
			continue
		}
		sawFollowUp = true
		if !strings.HasPrefix(m.Subject, "Re: ") {
			t.Errorf("step %d subject %q, want a Re: of step 1", store.sends[i].StepOrder, m.Subject)
		}
		if m.References == "" {
			t.Error("follow-up delivered with no References header")
		}
	}
	if !sawFollowUp {
		t.Error("no follow-up steps delivered; the Re: rule went untested")
	}
}

// Re-running the harness must not fabricate a second, divergent history.
func TestRunIsDeterministic(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	target := newTarget(80)

	runOnce := func() *fakeStore {
		store := newFakeStore()
		if _, err := NewSimulator(store, store, Options{Now: now}).Run(context.Background(), target); err != nil {
			t.Fatalf("Run: %v", err)
		}
		return store
	}
	a, b := runOnce(), runOnce()

	if len(a.sends) != len(b.sends) || len(a.replies) != len(b.replies) {
		t.Fatalf("run sizes differ: sends %d/%d replies %d/%d",
			len(a.sends), len(b.sends), len(a.replies), len(b.replies))
	}
	for i := range a.sends {
		if a.sends[i] != b.sends[i] {
			t.Fatalf("send %d differs:\n a=%+v\n b=%+v", i, a.sends[i], b.sends[i])
		}
	}
	for i := range a.replies {
		if a.replies[i] != b.replies[i] {
			t.Fatalf("reply %d differs:\n a=%+v\n b=%+v", i, a.replies[i], b.replies[i])
		}
	}
}

func TestNewSimulatorAppliesDefaults(t *testing.T) {
	s := NewSimulator(newFakeStore(), newFakeStore(), Options{})
	if s.opts.Window != DefaultWindow {
		t.Errorf("Window = %v, want %v", s.opts.Window, DefaultWindow)
	}
	if s.opts.MessageIDDomain != DefaultMessageIDDomain {
		t.Errorf("MessageIDDomain = %q, want %q", s.opts.MessageIDDomain, DefaultMessageIDDomain)
	}
	if s.opts.Now.IsZero() {
		t.Error("Now was left zero, so every event would be dated at the epoch")
	}
}

func TestRunWithNoContactsWritesNothing(t *testing.T) {
	store := newFakeStore()
	res, err := NewSimulator(store, store, Options{Now: time.Now()}).
		Run(context.Background(), Target{WorkspaceID: uuid.New(), CampaignID: uuid.New()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res != (Result{}) {
		t.Errorf("empty target produced %+v, want a zero result", res)
	}
	if len(store.sends) != 0 {
		t.Errorf("empty target wrote %d sends", len(store.sends))
	}
}
