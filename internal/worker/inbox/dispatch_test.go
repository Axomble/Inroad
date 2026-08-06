package inbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/mail"
	"github.com/inroad/inroad/internal/platform/replyclassify"
)

// builtinLabels mirrors the seven labels migration 000047 seeds into every
// workspace, flags included. The whole point of those flags is that they
// reproduce the pre-taxonomy switch exactly, and
// TestSeededLabelsReproduceClassBehaviour below is what holds them to it — so
// this table must stay in lockstep with seed_reply_labels().
var builtinLabels = map[string]coreapi.ReplyLabel{
	replyclassify.ClassPositive:    {Key: "positive", StopsEnrollment: true, CapturesDeal: true},
	replyclassify.ClassNegative:    {Key: "negative", StopsEnrollment: true},
	replyclassify.ClassNeutral:     {Key: "neutral", StopsEnrollment: true},
	replyclassify.ClassUnknown:     {Key: "unknown", StopsEnrollment: true},
	replyclassify.ClassUnsubscribe: {Key: "unsubscribe", StopsEnrollment: true, SuppressesContact: true},
	replyclassify.ClassOutOfOffice: {Key: "out_of_office", IsAutomated: true},
	replyclassify.ClassAutoReply:   {Key: "auto_reply", IsAutomated: true},
}

// fakeReplyLabels is a test double for coreapi.ReplyLabelClient. A key absent
// from labels resolves ok=false, which is the "no label claims this key" case
// the poller must degrade to byClass on.
type fakeReplyLabels struct {
	labels map[string]coreapi.ReplyLabel
	err    error
}

func (f *fakeReplyLabels) ResolveReplyLabel(_ context.Context, _, key string) (coreapi.ReplyLabel, bool, error) {
	if f.err != nil {
		return coreapi.ReplyLabel{}, false, f.err
	}
	label, ok := f.labels[key]
	return label, ok, nil
}

type deferCall struct {
	enrollmentID string
	until        time.Time
}

// fakeDeferrer records DeferEnrollment. It is a separate type from stubCore for
// the same reason fakeInboxCapture is: a test must be able to drive a core
// WITHOUT it.
type fakeDeferrer struct{ calls []deferCall }

func (f *fakeDeferrer) DeferEnrollment(_ context.Context, enrollmentID, _ string, until time.Time) error {
	f.calls = append(f.calls, deferCall{enrollmentID, until})
	return nil
}

// coreWithLabels composes the base stub with the label resolver and the
// deferrer, so the value satisfies coreapi.Client, coreapi.ReplyLabelClient and
// (via the promoted DeferEnrollment) the deferral path — mirroring how the real
// inprocess client implements all of them on one concrete type.
type coreWithLabels struct {
	*stubCore
	*fakeReplyLabels
	*fakeDeferrer
}

// The composite must satisfy BOTH seams, or every test below would silently
// exercise the byClass fallback and assert nothing about the taxonomy.
var (
	_ coreapi.Client           = coreWithLabels{}
	_ coreapi.ReplyLabelClient = coreWithLabels{}
)

func labelledCore(labels map[string]coreapi.ReplyLabel) coreWithLabels {
	return coreWithLabels{
		stubCore: &stubCore{
			job:      coreapi.InboxPollJob{UIDValidity: 5, LastSeenUID: 10},
			sendRefs: map[string]coreapi.SendRef{"<root@x>": {SendID: "s1", EnrollmentID: "e1", ContactEmail: "a@b.io"}},
		},
		fakeReplyLabels: &fakeReplyLabels{labels: labels},
		fakeDeferrer:    &fakeDeferrer{},
	}
}

func plainCore() *stubCore {
	return &stubCore{
		job:      coreapi.InboxPollJob{UIDValidity: 5, LastSeenUID: 10},
		sendRefs: map[string]coreapi.SendRef{"<root@x>": {SendID: "s1", EnrollmentID: "e1", ContactEmail: "a@b.io"}},
	}
}

func oneMessage(t *testing.T, raw string) *fakeReader {
	t.Helper()
	return &fakeReader{uidValidity: 5, uidNext: 12, msgs: []mail.InboundMessage{inboundMsg(t, 11, raw)}}
}

// outcome is the observable effect of one dispatch, flattened so the
// label-driven and class-driven paths can be compared for equality.
type outcome struct {
	replied      []string
	repliedClass []string
	unsubscribed []string
	recorded     []string
	capturedFor  []string
	deferrals    int
}

func observed(s *stubCore, d *fakeDeferrer) outcome {
	captured := make([]string, len(s.captured))
	for i, c := range s.captured {
		captured[i] = c.EnrollmentID + ":" + c.ReplyClass
	}
	out := outcome{
		replied: s.replied, repliedClass: s.repliedClass, unsubscribed: s.unsubscribed,
		recorded: s.recorded, capturedFor: captured,
	}
	if d != nil {
		out.deferrals = len(d.calls)
	}
	return out
}

func sameOutcome(a, b outcome) bool {
	return equalStrings(a.replied, b.replied) && equalStrings(a.repliedClass, b.repliedClass) &&
		equalStrings(a.unsubscribed, b.unsubscribed) && equalStrings(a.recorded, b.recorded) &&
		equalStrings(a.capturedFor, b.capturedFor) && a.deferrals == b.deferrals
}

// classFixtures drives one message of each reachable reply class through the
// poller. The subject/body pairs are the same ones the pre-taxonomy tests in
// poll_test.go use, so a change in the classifier fails there too.
var classFixtures = []struct {
	name string
	raw  string
}{
	{"positive", "From: alice@example.com\nTo: bob@example.com\nSubject: Re: Hello\nIn-Reply-To: <root@x>\n\nSounds great, let's chat this week.\n"},
	{"negative", "From: alice@example.com\nTo: bob@example.com\nSubject: Re: Hello\nIn-Reply-To: <root@x>\n\nNot interested, thanks.\n"},
	{"unknown", "From: alice@example.com\nTo: bob@example.com\nSubject: Re: Hello\nIn-Reply-To: <root@x>\n\nThanks for reaching out.\n"},
	{"unsubscribe", "From: alice@example.com\nTo: bob@example.com\nSubject: Re: Hello\nIn-Reply-To: <root@x>\n\nPlease unsubscribe me from this list.\n"},
	{"out_of_office", "From: alice@example.com\nTo: bob@example.com\nSubject: Out of Office\nIn-Reply-To: <root@x>\n\nI am away from my desk.\n"},
	{"auto_reply", "From: bot@example.com\nTo: bob@example.com\nSubject: Re: Hello\nIn-Reply-To: <root@x>\nAuto-Submitted: auto-generated\n\nThis is an automated response.\n"},
	{"ooo_with_opt_out", "From: alice@example.com\nTo: bob@example.com\nSubject: Out of Office\nIn-Reply-To: <root@x>\n\nI am away, but please remove me from your list.\n"},
}

// TestSeededLabelsReproduceClassBehaviour is the migration's contract: for every
// reachable class, dispatching through the SEEDED labels must produce exactly
// the calls the pre-taxonomy switch produced. Both paths run the same fixture
// through the same handler; only the presence of the label resolver differs.
func TestSeededLabelsReproduceClassBehaviour(t *testing.T) {
	for _, f := range classFixtures {
		t.Run(f.name, func(t *testing.T) {
			legacy := plainCore()
			if err := runPoll(t, legacy, oneMessage(t, f.raw)); err != nil {
				t.Fatal(err)
			}
			labelled := labelledCore(builtinLabels)
			if err := runPoll(t, labelled, oneMessage(t, f.raw)); err != nil {
				t.Fatal(err)
			}
			want, got := observed(legacy, nil), observed(labelled.stubCore, labelled.fakeDeferrer)
			if !sameOutcome(want, got) {
				t.Fatalf("label dispatch diverged from class dispatch:\n class: %+v\n label: %+v", want, got)
			}
		})
	}
}

// TestUnresolvableLabelFallsBackToClassBehaviour covers the three ways the
// taxonomy can be unavailable. Each must land on the pre-taxonomy switch, not
// on "no automation": a positive reply still stops the enrollment and still
// captures a deal.
func TestUnresolvableLabelFallsBackToClassBehaviour(t *testing.T) {
	positive := classFixtures[0].raw
	cases := []struct {
		name  string
		build func() (coreapi.Client, *stubCore)
	}{
		{"no resolver capability", func() (coreapi.Client, *stubCore) {
			c := plainCore()
			return c, c
		}},
		{"key claimed by no label", func() (coreapi.Client, *stubCore) {
			c := labelledCore(map[string]coreapi.ReplyLabel{"demo_requested": {Key: "demo_requested", CapturesDeal: true}})
			return c, c.stubCore
		}},
		{"resolver failure", func() (coreapi.Client, *stubCore) {
			c := labelledCore(builtinLabels)
			c.err = errors.New("boom")
			return c, c.stubCore
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			core, stub := tc.build()
			if err := runPoll(t, core, oneMessage(t, positive)); err != nil {
				t.Fatal(err)
			}
			if len(stub.replied) != 1 || stub.repliedClass[0] != replyclassify.ClassPositive {
				t.Fatalf("expected the pre-taxonomy MarkReplied(positive), got ids=%v classes=%v", stub.replied, stub.repliedClass)
			}
			if len(stub.captured) != 1 {
				t.Fatalf("expected the pre-taxonomy CRM capture, got %+v", stub.captured)
			}
		})
	}
}

// TestDispatchActsOnRoleFlags proves each flag drives its own action on a
// CUSTOM label — i.e. the behaviour is genuinely read off the row, not inferred
// from the class name. Every case classifies as "positive" and only the flags
// differ.
func TestDispatchActsOnRoleFlags(t *testing.T) {
	positive := classFixtures[0].raw
	cases := []struct {
		name   string
		label  coreapi.ReplyLabel
		assert func(t *testing.T, s *stubCore, d *fakeDeferrer)
	}{
		{
			name:  "suppresses_contact suppresses and stops",
			label: coreapi.ReplyLabel{Key: "positive", SuppressesContact: true, StopsEnrollment: true},
			assert: func(t *testing.T, s *stubCore, _ *fakeDeferrer) {
				if len(s.unsubscribed) != 1 || s.unsubEmail[0] != "a@b.io" {
					t.Fatalf("expected MarkUnsubscribed, got %v/%v", s.unsubscribed, s.unsubEmail)
				}
				if len(s.replied) != 0 || len(s.recorded) != 0 {
					t.Fatalf("suppression is terminal: replied=%v recorded=%v", s.replied, s.recorded)
				}
			},
		},
		{
			name:  "stops_enrollment alone stops without capturing",
			label: coreapi.ReplyLabel{Key: "positive", StopsEnrollment: true},
			assert: func(t *testing.T, s *stubCore, _ *fakeDeferrer) {
				if len(s.replied) != 1 {
					t.Fatalf("expected MarkReplied, got %v", s.replied)
				}
				if len(s.captured) != 0 {
					t.Fatalf("a label without captures_deal must not capture: %+v", s.captured)
				}
			},
		},
		{
			name:  "captures_deal captures on a non-positive-shaped label",
			label: coreapi.ReplyLabel{Key: "positive", StopsEnrollment: true, CapturesDeal: true},
			assert: func(t *testing.T, s *stubCore, _ *fakeDeferrer) {
				if len(s.captured) != 1 || s.captured[0].EnrollmentID != "e1" {
					t.Fatalf("expected a CRM capture, got %+v", s.captured)
				}
			},
		},
		{
			name:  "no flags at all only tags",
			label: coreapi.ReplyLabel{Key: "positive"},
			assert: func(t *testing.T, s *stubCore, d *fakeDeferrer) {
				if len(s.recorded) != 1 || s.recordedClass[0] != replyclassify.ClassPositive {
					t.Fatalf("expected RecordReplyClass only, got %v/%v", s.recorded, s.recordedClass)
				}
				if len(s.replied) != 0 || len(s.unsubscribed) != 0 || len(d.calls) != 0 {
					t.Fatalf("a flagless label must take no action: replied=%v unsub=%v deferrals=%v", s.replied, s.unsubscribed, d.calls)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			core := labelledCore(map[string]coreapi.ReplyLabel{replyclassify.ClassPositive: tc.label})
			if err := runPoll(t, core, oneMessage(t, positive)); err != nil {
				t.Fatal(err)
			}
			tc.assert(t, core.stubCore, core.fakeDeferrer)
		})
	}
}

// TestDefersEnrollmentOnStatedReturnDate covers the OOO deferral end to end:
// with defers_enrollment ON and a parseable return date, the enrollment is
// rescheduled and stays active (no MarkReplied).
func TestDefersEnrollmentOnStatedReturnDate(t *testing.T) {
	// A far-future absolute year keeps this test stable regardless of when it
	// runs; the +30d cap is asserted separately in returndate_test.go.
	raw := "From: alice@example.com\nTo: bob@example.com\nSubject: Out of Office\n" +
		"In-Reply-To: <root@x>\n\nI am away and will be back on 2099-04-05.\n"
	core := labelledCore(map[string]coreapi.ReplyLabel{
		replyclassify.ClassOutOfOffice: {Key: "out_of_office", IsAutomated: true, DefersEnrollment: true},
	})
	if err := runPoll(t, core, oneMessage(t, raw)); err != nil {
		t.Fatal(err)
	}
	if len(core.calls) != 1 || core.calls[0].enrollmentID != "e1" {
		t.Fatalf("expected one DeferEnrollment(e1), got %+v", core.calls)
	}
	// Capped at +30 days, so the deferral lands within a month of now — never on
	// the literal year 2099.
	until := core.calls[0].until
	if latest := time.Now().UTC().Add(maxDeferral + time.Minute); until.After(latest) {
		t.Fatalf("deferral %v exceeds the +30d cap", until)
	}
	if len(core.replied) != 0 {
		t.Fatalf("a deferral must keep the enrollment active, got replied=%v", core.replied)
	}
	if len(core.recorded) != 1 {
		t.Fatalf("a deferred reply is still tagged, got recorded=%v", core.recorded)
	}
}

// TestUnparseableReturnDateDoesNotDefer is the safety half of the deferral: a
// body with no date we can trust must fall through to today's tag-only
// behaviour rather than guessing one.
func TestUnparseableReturnDateDoesNotDefer(t *testing.T) {
	raw := "From: alice@example.com\nTo: bob@example.com\nSubject: Out of Office\n" +
		"In-Reply-To: <root@x>\n\nI am away for a while. Contact my colleague.\n"
	core := labelledCore(map[string]coreapi.ReplyLabel{
		replyclassify.ClassOutOfOffice: {Key: "out_of_office", IsAutomated: true, DefersEnrollment: true},
	})
	if err := runPoll(t, core, oneMessage(t, raw)); err != nil {
		t.Fatal(err)
	}
	if len(core.calls) != 0 {
		t.Fatalf("an unparseable return date must NOT defer, got %+v", core.calls)
	}
	if len(core.recorded) != 1 || core.recordedClass[0] != replyclassify.ClassOutOfOffice {
		t.Fatalf("expected the tag-only fallback, got %v/%v", core.recorded, core.recordedClass)
	}
	if len(core.replied) != 0 {
		t.Fatalf("tag-only must not stop the enrollment, got %v", core.replied)
	}
}
